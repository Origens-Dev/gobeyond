package telemetry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sync"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	collectormetric "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

var (
	ErrMetricBufferFull        = errors.New("metric retry buffer is full")
	ErrMetricSequenceExhausted = errors.New("metric epoch point sequence exhausted; fresh producer instance required")
	ErrMetricEpochExpired      = errors.New("metric retry epoch expired; queued data remains unresolved")
)

// MetricsExporterConfig is explicit: these limits bound retained serialized
// RPC bytes, not decode allocations or the SDK's instrument aggregation state.
type MetricsExporterConfig struct {
	Grants           MetricGrantProvider
	Client           collectormetric.MetricsServiceClient
	MaxRetainedBytes uint64
	MaxRPCBytes      uint64
	MaxPendingRPCs   int
	RPCTimeout       time.Duration
	HistogramBounds  []float64
}

type queuedMetricRPC struct {
	body  []byte
	grant MetricGrant
}

// MetricsExporter owns immutable point IDs and whole-RPC retry bodies. Export
// returning an error never discards an admitted queue entry. Buffer exhaustion
// rejects the new collection explicitly and does not evict older retry work.
type MetricsExporter struct {
	config          MetricsExporterConfig
	exportGate      chan struct{}
	mu              sync.Mutex
	queue           []queuedMetricRPC
	retained        uint64
	closed          bool
	instanceID      string
	resetInstanceID string
	current         MetricGrant
	nextSequence    uint64
	now             func() time.Time
}

var _ sdkmetric.Exporter = (*MetricsExporter)(nil)

func NewMetricsExporter(ctx context.Context, config MetricsExporterConfig) (*MetricsExporter, error) {
	if !metricInterfacePresent(config.Grants) || !metricInterfacePresent(config.Client) || config.MaxRetainedBytes == 0 || config.MaxRPCBytes == 0 || config.MaxRPCBytes > 256<<10 || config.MaxRPCBytes > config.MaxRetainedBytes || config.MaxPendingRPCs <= 0 || config.RPCTimeout <= 0 || config.RPCTimeout > 5*time.Second || len(config.HistogramBounds) > 64 {
		return nil, errors.New("explicit bounded metric exporter configuration is required")
	}
	for i, b := range config.HistogramBounds {
		if math.IsNaN(b) || math.IsInf(b, 0) || (i > 0 && b <= config.HistogramBounds[i-1]) {
			return nil, errors.New("metric histogram bounds must be finite and ascending")
		}
	}
	config.HistogramBounds = append([]float64{}, config.HistogramBounds...)
	instance, err := metricRandomID()
	if err != nil {
		return nil, err
	}
	e := &MetricsExporter{config: config, instanceID: instance, now: time.Now, exportGate: make(chan struct{}, 1)}
	e.exportGate <- struct{}{}
	grant, err := config.Grants.Acquire(ctx, instance)
	if err != nil {
		return nil, err
	}
	if err = grant.validate(e.now()); err != nil {
		return nil, err
	}
	if e.now().Unix() >= grant.EpochNotAfter {
		return nil, errors.New("new metric grant epoch already expired")
	}
	e.current = grant
	return e, nil
}
func metricRandomID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}
func (e *MetricsExporter) Temporality(sdkmetric.InstrumentKind) metricdata.Temporality {
	return metricdata.DeltaTemporality
}
func (e *MetricsExporter) Aggregation(kind sdkmetric.InstrumentKind) sdkmetric.Aggregation {
	if kind == sdkmetric.InstrumentKindHistogram {
		return sdkmetric.AggregationExplicitBucketHistogram{Boundaries: append([]float64{}, e.config.HistogramBounds...), NoMinMax: true}
	}
	return sdkmetric.DefaultAggregationSelector(kind)
}
func (e *MetricsExporter) Export(ctx context.Context, data *metricdata.ResourceMetrics) error {
	if err := e.lockExport(ctx); err != nil {
		return err
	}
	defer e.unlockExport()
	e.mu.Lock()
	closed := e.closed
	e.mu.Unlock()
	if closed {
		return sdkmetric.ErrExporterShutdown
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Empty periodic collections also refresh auth. An idle producer must
	// remain able to acquire its next epoch without a process restart.
	if metricTokenNeedsRenewal(e.current.Token, e.now()) {
		grant, err := e.config.Grants.Renew(ctx, e.current.ProducerEpoch)
		if err != nil {
			return err
		}
		if err = grant.validate(e.now()); err != nil {
			return err
		}
		if !grant.sameIdentity(e.current) {
			return errors.New("metric renewal changed fixed producer identity or deadlines")
		}
		e.current = grant
	}
	if e.now().Unix() >= e.current.EpochNotAfter {
		// Each SDK delta collection is independent. Rotate at this collection
		// boundary; pending old RPCs keep their own epoch and absolute deadlines.
		if e.resetInstanceID == "" {
			var err error
			e.resetInstanceID, err = metricRandomID()
			if err != nil {
				return err
			}
		}
		next, err := e.config.Grants.Acquire(ctx, e.resetInstanceID)
		if err != nil {
			return err
		}
		if err = next.validate(e.now()); err != nil {
			return err
		}
		if next.ProducerEpoch == e.current.ProducerEpoch || e.now().Unix() >= next.EpochNotAfter {
			return errors.New("metric reset did not acquire a fresh active epoch")
		}
		e.current = next
		e.nextSequence = 0
		e.instanceID = e.resetInstanceID
		e.resetInstanceID = ""
	}
	request, err := metricResourceRequest(data)
	if err != nil {
		return err
	}
	if err := metricRequestWithinEpoch(request, e.current); err != nil {
		return err
	}
	if err := e.assignPointSequences(request); err != nil {
		return err
	}
	bodies, err := metricRPCBodies(request, e.config.MaxRPCBytes)
	if err != nil {
		return err
	}
	var added uint64
	for _, body := range bodies {
		if uint64(len(body)) > e.config.MaxRetainedBytes-added {
			return ErrMetricBufferFull
		}
		added += uint64(len(body))
	}
	e.mu.Lock()
	if added > e.config.MaxRetainedBytes-e.retained || len(bodies) > e.config.MaxPendingRPCs-len(e.queue) {
		e.mu.Unlock()
		return ErrMetricBufferFull
	}
	for _, body := range bodies {
		e.queue = append(e.queue, queuedMetricRPC{body: body, grant: e.current})
	}
	e.retained += added
	e.mu.Unlock()
	return e.flush(ctx)
}
func (e *MetricsExporter) flush(ctx context.Context) error {
	// The total flush honors caller deadline and a finite exporter ceiling.
	ctx, cancel := context.WithTimeout(ctx, e.config.RPCTimeout)
	defer cancel()
	delay := 25 * time.Millisecond
	for {
		e.mu.Lock()
		if len(e.queue) == 0 {
			e.mu.Unlock()
			return nil
		}
		batch := e.queue[0]
		e.mu.Unlock()
		if e.now().Unix() >= batch.grant.RetryUntil {
			return ErrMetricEpochExpired
		}
		// Refresh with ample margin. Renew updates only auth, never the immutable
		// resource/producer/epoch/deadline tuple bound to a serialized RPC.
		if metricTokenNeedsRenewal(batch.grant.Token, e.now()) {
			grant, err := e.config.Grants.Renew(ctx, batch.grant.ProducerEpoch)
			if err != nil {
				return err
			}
			if err = grant.validate(e.now()); err != nil {
				return err
			}
			if !grant.sameIdentity(batch.grant) {
				return errors.New("metric renewal changed fixed producer identity or deadlines")
			}
			batch.grant = grant
			e.mu.Lock()
			e.queue[0].grant = grant
			e.mu.Unlock()
			if e.current.ProducerEpoch == grant.ProducerEpoch {
				e.current = grant
			}
		}
		request := new(collectormetric.ExportMetricsServiceRequest)
		if err := proto.Unmarshal(batch.body, request); err != nil {
			return fmt.Errorf("decode retained metric RPC: %w", err)
		}
		response, err := e.config.Client.Export(metricAuthContext(ctx, batch.grant.Token), request)
		if err == nil {
			if response == nil || response.PartialSuccess != nil {
				return errors.New("metric receiver returned unsupported partial or missing success")
			}
			e.mu.Lock()
			e.retained -= uint64(len(e.queue[0].body))
			e.queue[0] = queuedMetricRPC{}
			e.queue = e.queue[1:]
			e.mu.Unlock()
			delay = 25 * time.Millisecond
			continue
		}
		switch status.Code(err) {
		case codes.Unavailable, codes.DeadlineExceeded, codes.Aborted, codes.ResourceExhausted:
		default:
			return err
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if delay < time.Second {
			delay *= 2
			if delay > time.Second {
				delay = time.Second
			}
		}
	}
}
func (e *MetricsExporter) ForceFlush(ctx context.Context) error {
	if err := e.lockExport(ctx); err != nil {
		return err
	}
	defer e.unlockExport()
	return e.flush(ctx)
}
func (e *MetricsExporter) Shutdown(ctx context.Context) error {
	if err := e.lockExport(ctx); err != nil {
		return err
	}
	defer e.unlockExport()
	e.mu.Lock()
	e.closed = true
	e.mu.Unlock()
	return e.flush(ctx)
}

// PendingMetrics reports retained retry work for shutdown/error monitoring.
func (e *MetricsExporter) PendingMetrics() (rpcs int, bytes uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.queue), e.retained
}

func metricInterfacePresent(value any) bool {
	if value == nil {
		return false
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return !v.IsNil()
	default:
		return true
	}
}

func (e *MetricsExporter) lockExport(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-e.exportGate:
		return nil
	}
}
func (e *MetricsExporter) unlockExport() { e.exportGate <- struct{}{} }
