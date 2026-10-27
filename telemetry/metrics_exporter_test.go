package telemetry

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/instrumentation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	collectormetric "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type metricGrantFixture struct {
	now         time.Time
	ids         []string
	epochs      map[string]MetricGrant
	failAcquire bool
	badRenew    bool
}

func newGrantFixture() *metricGrantFixture {
	return &metricGrantFixture{now: time.Now().Truncate(time.Second), epochs: make(map[string]MetricGrant)}
}
func (f *metricGrantFixture) Acquire(_ context.Context, id string) (MetricGrant, error) {
	f.ids = append(f.ids, id)
	if f.failAcquire {
		return MetricGrant{}, errors.New("ambiguous acquire")
	}
	if g, ok := f.epochs[id]; ok {
		return g, nil
	}
	g := MetricGrant{ResourceID: "signed-resource", ProducerID: "signed-producer", ProducerEpoch: id, EpochStartedAt: f.now.Unix(), EpochNotAfter: f.now.Add(24 * time.Hour).Unix(), RetryUntil: f.now.Add(48 * time.Hour).Unix()}
	g.Token = fixtureMetricToken(g, f.now.Add(24*time.Hour))
	f.epochs[id] = g
	return g, nil
}
func (f *metricGrantFixture) Renew(_ context.Context, epoch string) (MetricGrant, error) {
	for _, g := range f.epochs {
		if g.ProducerEpoch == epoch {
			if f.badRenew {
				g.RetryUntil++
			}
			g.Token = fixtureMetricToken(g, f.now.Add(24*time.Hour))
			return g, nil
		}
	}
	return MetricGrant{}, errors.New("unknown epoch")
}
func fixtureMetricToken(g MetricGrant, expires time.Time) string {
	g.Token = ""
	body, _ := json.Marshal(map[string]any{"metric_producer": g, "exp": expires.Unix(), "organization_id": "fixture-org", "project_id": "fixture-project", "environment_id": "fixture-env"})
	return base64.RawURLEncoding.EncodeToString(body) + "." + fixtureMetricSignature(body)
}

type metricClientFixture struct {
	mu       sync.Mutex
	requests []*collectormetric.ExportMetricsServiceRequest
	tokens   []string
	err      error
	failures int
	partial  bool
	entered  chan struct{}
	release  chan struct{}
}

func (f *metricClientFixture) Export(ctx context.Context, r *collectormetric.ExportMetricsServiceRequest, _ ...grpc.CallOption) (*collectormetric.ExportMetricsServiceResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, proto.Clone(r).(*collectormetric.ExportMetricsServiceRequest))
	md, _ := metadata.FromOutgoingContext(ctx)
	tokens := md.Get(tokenHeader)
	if len(tokens) > 0 {
		f.tokens = append(f.tokens, tokens[len(tokens)-1])
	}
	if f.entered != nil {
		close(f.entered)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-f.release:
		}
	}
	if f.failures > 0 {
		f.failures--
		return nil, status.Error(codes.Unavailable, "ambiguous remote success")
	}
	if f.err != nil {
		return nil, f.err
	}
	if f.partial {
		return &collectormetric.ExportMetricsServiceResponse{PartialSuccess: &collectormetric.ExportMetricsPartialSuccess{}}, nil
	}
	return &collectormetric.ExportMetricsServiceResponse{}, nil
}
func exporterFixture(t *testing.T) (*MetricsExporter, *metricGrantFixture, *metricClientFixture) {
	t.Helper()
	grants := newGrantFixture()
	client := new(metricClientFixture)
	e, err := NewMetricsExporter(context.Background(), MetricsExporterConfig{Grants: grants, Client: client, MaxRetainedBytes: 1 << 20, MaxRPCBytes: 256 << 10, MaxPendingRPCs: 32, RPCTimeout: 100 * time.Millisecond, HistogramBounds: []float64{.1, 1, 10}})
	if err != nil {
		t.Fatal(err)
	}
	e.now = func() time.Time { return grants.now }
	return e, grants, client
}
func metricDataFixture(now time.Time) *metricdata.ResourceMetrics {
	attrs := attribute.NewSet(attribute.String("route", "/test"))
	return &metricdata.ResourceMetrics{ScopeMetrics: []metricdata.ScopeMetrics{{Scope: instrumentation.Scope{Name: "fixture.sdk"}, Metrics: []metricdata.Metrics{
		{Name: "fixture.counter", Unit: "{request}", Data: metricdata.Sum[int64]{Temporality: metricdata.DeltaTemporality, IsMonotonic: true, DataPoints: []metricdata.DataPoint[int64]{{StartTime: now.Add(-time.Second), Time: now, Value: 9007199254740993, Attributes: attrs}}}},
		{Name: "fixture.gauge", Unit: "1", Data: metricdata.Gauge[float64]{DataPoints: []metricdata.DataPoint[float64]{{Time: now, Value: 0, Attributes: attrs}}}},
		{Name: "fixture.histogram", Unit: "s", Data: metricdata.Histogram[float64]{Temporality: metricdata.DeltaTemporality, DataPoints: []metricdata.HistogramDataPoint[float64]{{StartTime: now.Add(-time.Second), Time: now, Count: 9007199254740993, Bounds: []float64{.1, 1}, BucketCounts: []uint64{1, 9007199254740992, 0}, Sum: 42, Attributes: attrs}}}},
	}}}}
}
func TestMetricExporterRetriesImmutableWholeRPCAndExactTypedValues(t *testing.T) {
	e, g, client := exporterFixture(t)
	client.failures = 1
	data := metricDataFixture(g.now)
	if err := e.Export(context.Background(), data); err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 2 || !proto.Equal(client.requests[0], client.requests[1]) || client.tokens[0] != client.tokens[1] {
		t.Fatal("retry changed RPC point identity/body or epoch token")
	}
	// Chunking preserves order, with one point per ScopeMetrics unit.
	scopes := client.requests[1].ResourceMetrics[0].ScopeMetrics
	if scopes[0].Metrics[0].GetSum().DataPoints[0].GetAsInt() != 9007199254740993 {
		t.Fatal("counter rounded above 2^53")
	}
	hist := scopes[2].Metrics[0].GetHistogram().DataPoints[0]
	if hist.Count != 9007199254740993 || hist.BucketCounts[1] != 9007199254740992 || hist.Sum == nil || *hist.Sum != 42 {
		t.Fatal("histogram lost typed population or sum")
	}
	ids := map[string]bool{}
	for _, scope := range scopes {
		m := scope.Metrics[0]
		var id string
		switch {
		case m.GetSum() != nil:
			for _, a := range m.GetSum().DataPoints[0].Attributes {
				if a.Key == MetricPointIDAttribute {
					id = a.Value.GetStringValue()
				}
			}
		case m.GetGauge() != nil:
			for _, a := range m.GetGauge().DataPoints[0].Attributes {
				if a.Key == MetricPointIDAttribute {
					id = a.Value.GetStringValue()
				}
			}
		default:
			for _, a := range m.GetHistogram().DataPoints[0].Attributes {
				if a.Key == MetricPointIDAttribute {
					id = a.Value.GetStringValue()
				}
			}
		}
		if len(id) != 20 || id[:4] != "seq:" || ids[id] {
			t.Fatal("point IDs missing or reused")
		}
		ids[id] = true
	}
	if rpcs, bytes := e.PendingMetrics(); rpcs != 0 || bytes != 0 {
		t.Fatalf("pending %d/%d after ACK", rpcs, bytes)
	}
}
func TestMetricExporterRetainsFailedRPCAndRejectsOverflowWithoutEviction(t *testing.T) {
	e, g, client := exporterFixture(t)
	e.config.MaxPendingRPCs = 1
	client.err = status.Error(codes.InvalidArgument, "fixture permanent reject")
	if err := e.Export(context.Background(), metricDataFixture(g.now)); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("error=%v", err)
	}
	before := append([]byte(nil), e.queue[0].body...)
	if err := e.Export(context.Background(), metricDataFixture(g.now.Add(time.Second))); !errors.Is(err, ErrMetricBufferFull) {
		t.Fatalf("overflow=%v", err)
	}
	if !reflect.DeepEqual(before, e.queue[0].body) {
		t.Fatal("buffer evicted or changed unresolved RPC")
	}
	client.err = nil
	if err := e.ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(client.requests[0], client.requests[1]) {
		t.Fatal("later retry regenerated point identity")
	}
}
func TestMetricExporterCannotTreatPartialSuccessAsACK(t *testing.T) {
	e, g, client := exporterFixture(t)
	client.partial = true
	if err := e.Export(context.Background(), metricDataFixture(g.now)); err == nil {
		t.Fatal("partial success ACKed")
	}
	if n, _ := e.PendingMetrics(); n != 1 {
		t.Fatal("partial success discarded RPC")
	}
}
func TestMetricExporterRenewalCannotSlideEpochBounds(t *testing.T) {
	e, g, client := exporterFixture(t)
	e.current.Token = fixtureMetricToken(e.current, g.now.Add(time.Minute))
	g.badRenew = true
	if err := e.Export(context.Background(), metricDataFixture(g.now)); err == nil {
		t.Fatal("sliding retry deadline accepted")
	}
	if len(client.requests) != 0 {
		t.Fatal("sent altered epoch")
	}
}
func TestMetricExporterEpochResetKeepsOldQueuedIdentityAndAcquireRetryKey(t *testing.T) {
	e, g, client := exporterFixture(t)
	client.err = status.Error(codes.InvalidArgument, "retain old RPC")
	if err := e.Export(context.Background(), metricDataFixture(g.now)); err == nil {
		t.Fatal("expected retained RPC")
	}
	old := e.queue[0].grant
	g.now = g.now.Add(24*time.Hour + time.Second)
	g.failAcquire = true
	if err := e.Export(context.Background(), metricDataFixture(g.now)); err == nil {
		t.Fatal("expected acquire failure")
	}
	attempt := g.ids[len(g.ids)-1]
	g.failAcquire = false
	client.err = nil
	if err := e.Export(context.Background(), metricDataFixture(g.now)); err != nil {
		t.Fatal(err)
	}
	if g.ids[len(g.ids)-1] != attempt || attempt == old.ProducerEpoch {
		t.Fatal("reset acquire retry identity changed or reused old epoch")
	}
	if len(client.tokens) < 3 {
		t.Fatal("missing retry/new RPC")
	}
	var body struct {
		Producer MetricGrant `json:"metric_producer"`
	}
	raw, _ := base64.RawURLEncoding.DecodeString(strings.Split(client.tokens[1], ".")[0])
	_ = json.Unmarshal(raw, &body)
	if body.Producer.ProducerEpoch != old.ProducerEpoch || body.Producer.RetryUntil != old.RetryUntil {
		t.Fatal("old RPC attached to new epoch or deadlines")
	}
}
func TestMetricExporterRejectsUnsupportedCollectionsBeforeQueue(t *testing.T) {
	for name, mutate := range map[string]func(*metricdata.ResourceMetrics){
		"cumulative": func(d *metricdata.ResourceMetrics) {
			x := d.ScopeMetrics[0].Metrics[0].Data.(metricdata.Sum[int64])
			x.Temporality = metricdata.CumulativeTemporality
			d.ScopeMetrics[0].Metrics[0].Data = x
		},
		"nonmonotonic": func(d *metricdata.ResourceMetrics) {
			x := d.ScopeMetrics[0].Metrics[0].Data.(metricdata.Sum[int64])
			x.IsMonotonic = false
			d.ScopeMetrics[0].Metrics[0].Data = x
		},
		"nonfinite": func(d *metricdata.ResourceMetrics) {
			x := d.ScopeMetrics[0].Metrics[1].Data.(metricdata.Gauge[float64])
			x.DataPoints[0].Value = math.Inf(1)
			d.ScopeMetrics[0].Metrics[1].Data = x
		},
		"count overflow": func(d *metricdata.ResourceMetrics) {
			x := d.ScopeMetrics[0].Metrics[2].Data.(metricdata.Histogram[float64])
			x.DataPoints[0].BucketCounts = []uint64{math.MaxUint64, 1, 0}
			d.ScopeMetrics[0].Metrics[2].Data = x
		},
		"reserved ID": func(d *metricdata.ResourceMetrics) {
			x := d.ScopeMetrics[0].Metrics[0].Data.(metricdata.Sum[int64])
			x.DataPoints[0].Attributes = attribute.NewSet(attribute.String(MetricPointIDAttribute, "forged"))
			d.ScopeMetrics[0].Metrics[0].Data = x
		},
	} {
		t.Run(name, func(t *testing.T) {
			e, g, c := exporterFixture(t)
			data := metricDataFixture(g.now)
			mutate(data)
			if err := e.Export(context.Background(), data); err == nil {
				t.Fatal("unsupported data accepted")
			}
			if n, _ := e.PendingMetrics(); n != 0 || len(c.requests) != 0 {
				t.Fatal("invalid collection queued or exported")
			}
		})
	}
}
func TestMetricExporterSchemaSelectorsAndShutdown(t *testing.T) {
	e, g, _ := exporterFixture(t)
	if e.Temporality(sdkmetric.InstrumentKindCounter) != metricdata.DeltaTemporality {
		t.Fatal("counter not delta")
	}
	a := e.Aggregation(sdkmetric.InstrumentKindHistogram).(sdkmetric.AggregationExplicitBucketHistogram)
	if !a.NoMinMax || !reflect.DeepEqual(a.Boundaries, e.config.HistogramBounds) {
		t.Fatal("unexpected histogram layout")
	}
	if err := e.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := e.Export(context.Background(), metricDataFixture(g.now)); !errors.Is(err, sdkmetric.ErrExporterShutdown) {
		t.Fatalf("after shutdown=%v", err)
	}
}

func TestMetricSDKProducesDeltaCountersInstantGaugesAndExplicitHistogram(t *testing.T) {
	e, _, client := exporterFixture(t)
	reader := sdkmetric.NewManualReader(sdkmetric.WithTemporalitySelector(e.Temporality), sdkmetric.WithAggregationSelector(e.Aggregation))
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer provider.Shutdown(context.Background())
	meter := provider.Meter("fixture.actual-sdk")
	counter, err := meter.Int64Counter("fixture.counter")
	if err != nil {
		t.Fatal(err)
	}
	gauge, err := meter.Float64Gauge("fixture.gauge")
	if err != nil {
		t.Fatal(err)
	}
	hist, err := meter.Float64Histogram("fixture.histogram")
	if err != nil {
		t.Fatal(err)
	}
	counter.Add(context.Background(), 9007199254740993)
	gauge.Record(context.Background(), 0)
	hist.Record(context.Background(), .2)
	var first metricdata.ResourceMetrics
	if err = reader.Collect(context.Background(), &first); err != nil {
		t.Fatal(err)
	}
	if err = e.Export(context.Background(), &first); err != nil {
		t.Fatal(err)
	}
	counter.Add(context.Background(), 2)
	gauge.Record(context.Background(), 7)
	hist.Record(context.Background(), 2)
	var second metricdata.ResourceMetrics
	if err = reader.Collect(context.Background(), &second); err != nil {
		t.Fatal(err)
	}
	if err = e.Export(context.Background(), &second); err != nil {
		t.Fatal(err)
	}
	for index, want := range []int64{9007199254740993, 2} {
		foundCounter, foundGauge, foundHistogram := false, false, false
		for _, scope := range client.requests[index].ResourceMetrics[0].ScopeMetrics {
			m := scope.Metrics[0]
			if sum := m.GetSum(); sum != nil {
				foundCounter = true
				if sum.DataPoints[0].GetAsInt() != want {
					t.Fatalf("collection%d counter=%d", index, sum.DataPoints[0].GetAsInt())
				}
			}
			if gauge := m.GetGauge(); gauge != nil {
				foundGauge = true
				value := float64(0)
				if index == 1 {
					value = 7
				}
				if gauge.DataPoints[0].GetAsDouble() != value {
					t.Fatal("gauge missing observed zero or latest sample")
				}
			}
			if hist := m.GetHistogram(); hist != nil {
				foundHistogram = true
				p := hist.DataPoints[0]
				if p.Count != 1 || !reflect.DeepEqual(p.ExplicitBounds, []float64{.1, 1, 10}) {
					t.Fatalf("histogram not explicit delta: %+v", p)
				}
			}
		}
		if !foundCounter || !foundGauge || !foundHistogram {
			t.Fatal("actual SDK collection missing supported kinds")
		}
	}
}
func TestMetricRPCChunkingIsBoundedWithoutLosingOrReidentifyingPoints(t *testing.T) {
	e, g, client := exporterFixture(t)
	e.config.MaxRPCBytes = 350
	if err := e.Export(context.Background(), metricDataFixture(g.now)); err != nil {
		t.Fatal(err)
	}
	if len(client.requests) < 2 {
		t.Fatal("fixture did not exercise splitting")
	}
	count := 0
	for _, r := range client.requests {
		if proto.Size(r) > 350 {
			t.Fatal("oversized RPC emitted")
		}
		for _, resource := range r.ResourceMetrics {
			for _, scope := range resource.ScopeMetrics {
				count += len(scope.Metrics)
			}
		}
	}
	if count != 3 {
		t.Fatalf("split point count=%d", count)
	}
}
func TestMetricExporterRetainsDeadlineFailureAndExpiredWork(t *testing.T) {
	e, g, client := exporterFixture(t)
	client.err = status.Error(codes.Unavailable, "offline")
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := e.Export(ctx, metricDataFixture(g.now)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline=%v", err)
	}
	if n, _ := e.PendingMetrics(); n != 1 {
		t.Fatal("deadline discarded retry body")
	}
	g.now = g.now.Add(48 * time.Hour)
	if err := e.ForceFlush(context.Background()); !errors.Is(err, ErrMetricEpochExpired) {
		t.Fatalf("expired=%v", err)
	}
	if n, _ := e.PendingMetrics(); n != 1 {
		t.Fatal("expiry silently discarded unresolved work")
	}
}

func TestMetricExporterIdleCollectionRenewsBeforeNextEpoch(t *testing.T) {
	e, g, _ := exporterFixture(t)
	initial := e.current
	g.now = g.now.Add(24*time.Hour - time.Minute)
	if err := e.Export(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if e.current.Token == initial.Token || !e.current.sameIdentity(initial) {
		t.Fatal("idle renewal changed identity or failed to refresh authorization")
	}
	g.now = g.now.Add(2 * time.Minute)
	if err := e.Export(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if e.current.ProducerEpoch == initial.ProducerEpoch {
		t.Fatal("idle producer did not reset expired epoch")
	}
}
func TestMetricExporterConcurrentFlushHonorsCallerDeadline(t *testing.T) {
	e, g, c := exporterFixture(t)
	e.config.RPCTimeout = time.Second
	c.entered = make(chan struct{})
	c.release = make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- e.Export(context.Background(), metricDataFixture(g.now)) }()
	<-c.entered
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	started := time.Now()
	err := e.ForceFlush(ctx)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 250*time.Millisecond {
		t.Fatalf("flush blocked behind another RPC: %v", err)
	}
	close(c.release)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}
func TestMetricSDKPreservesObservedZeroAndDoesNotInventMissingGauge(t *testing.T) {
	e, _, c := exporterFixture(t)
	reader := sdkmetric.NewManualReader(sdkmetric.WithTemporalitySelector(e.Temporality), sdkmetric.WithAggregationSelector(e.Aggregation))
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer provider.Shutdown(context.Background())
	gauge, _ := provider.Meter("fixture.sparse").Int64Gauge("fixture.gauge")
	gauge.Record(context.Background(), 0)
	var first metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &first); err != nil {
		t.Fatal(err)
	}
	if err := e.Export(context.Background(), &first); err != nil {
		t.Fatal(err)
	}
	var second metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &second); err != nil {
		t.Fatal(err)
	}
	if err := e.Export(context.Background(), &second); err != nil {
		t.Fatal(err)
	}
	if len(c.requests) != 1 {
		t.Fatal("missing gauge observation became a fabricated data point")
	}
	point := c.requests[0].ResourceMetrics[0].ScopeMetrics[0].Metrics[0].GetGauge().DataPoints[0]
	if point.GetAsInt() != 0 {
		t.Fatal("observed zero lost")
	}
}

func TestMetricExporterRejectsTypedNilDependencies(t *testing.T) {
	var grants *metricGrantFixture
	client := new(metricClientFixture)
	_, err := NewMetricsExporter(context.Background(), MetricsExporterConfig{Grants: grants, Client: client, MaxRetainedBytes: 1024, MaxRPCBytes: 1024, MaxPendingRPCs: 1, RPCTimeout: time.Second})
	if err == nil {
		t.Fatal("typed-nil grants accepted")
	}
	var connection *grantConnectionFixture
	if _, err = NewGRPCMetricGrantProvider(connection, "runtime-token"); err == nil {
		t.Fatal("typed-nil grant transport accepted")
	}
}
func TestMetricExporterRejectsUnsupportedScopeDimensions(t *testing.T) {
	e, g, c := exporterFixture(t)
	data := metricDataFixture(g.now)
	data.ScopeMetrics[0].Scope.Attributes = attribute.NewSet(attribute.String("scope.dimension", "different"))
	if err := e.Export(context.Background(), data); err == nil {
		t.Fatal("scope attributes silently omitted")
	}
	if n, _ := e.PendingMetrics(); n != 0 || len(c.requests) != 0 {
		t.Fatal("unsupported scope queued or exported")
	}
}

func TestActualMetricSDK24HourEpochBoundaryKeepsWholeConsecutiveDeltas(t *testing.T) {
	e, g, c := exporterFixture(t)
	e.now = time.Now
	// Full24h signed lifetime, restored near its end. Crossing the real SDK
	// clock boundary takes two seconds rather than sleeping for a whole day.
	old := e.current
	old.EpochStartedAt = g.now.Add(-24*time.Hour + 2*time.Second).Unix()
	old.EpochNotAfter = old.EpochStartedAt + 86400
	old.RetryUntil = old.EpochNotAfter + 86400
	old.Token = fixtureMetricToken(old, g.now.Add(time.Hour))
	e.current = old
	g.epochs[e.instanceID] = old
	reader := sdkmetric.NewManualReader(sdkmetric.WithTemporalitySelector(e.Temporality), sdkmetric.WithAggregationSelector(e.Aggregation))
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	defer provider.Shutdown(context.Background())
	counter, _ := provider.Meter("fixture.boundary").Int64Counter("fixture.counter")
	counter.Add(context.Background(), 5)
	var first metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &first); err != nil {
		t.Fatal(err)
	}
	if err := e.Export(context.Background(), &first); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Until(time.Unix(old.EpochNotAfter, 0)) + time.Millisecond)
	counter.Add(context.Background(), 7)
	var second metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &second); err != nil {
		t.Fatal(err)
	}
	g.now = time.Now().Truncate(time.Second)
	if err := e.Export(context.Background(), &second); err != nil {
		t.Fatal(err)
	}
	if len(c.requests) != 2 {
		t.Fatalf("unexpected RPC count %d", len(c.requests))
	}
	a := c.requests[0].ResourceMetrics[0].ScopeMetrics[0].Metrics[0].GetSum().DataPoints[0]
	b := c.requests[1].ResourceMetrics[0].ScopeMetrics[0].Metrics[0].GetSum().DataPoints[0]
	if a.GetAsInt() != 5 || b.GetAsInt() != 7 || a.GetAsInt()+b.GetAsInt() != 12 || a.TimeUnixNano != b.StartTimeUnixNano {
		t.Fatalf("boundary lost or repeated a delta: old=%+v new=%+v", a, b)
	}
	if a.TimeUnixNano > uint64(old.EpochNotAfter)*uint64(time.Second) || b.TimeUnixNano <= uint64(old.EpochNotAfter)*uint64(time.Second) {
		t.Fatal("fixture did not cross actual signed boundary")
	}
	if e.current.ProducerEpoch == old.ProducerEpoch || b.TimeUnixNano > uint64(e.current.EpochNotAfter)*uint64(time.Second) {
		t.Fatal("new delta has wrong epoch")
	}
	// A new epoch's first closed delta may start before capability mint. This
	// is grant metadata, not an event-time lower bound or permission to split.
	if b.StartTimeUnixNano >= uint64(e.current.EpochStartedAt)*uint64(time.Second) {
		t.Fatal("fixture did not exercise collection-before-mint")
	}
	id := func(attrs []*commonpb.KeyValue) string {
		for _, attr := range attrs {
			if attr.Key == MetricPointIDAttribute {
				return attr.Value.GetStringValue()
			}
		}
		return ""
	}
	if id(a.Attributes) != "seq:0000000000000001" || id(b.Attributes) != "seq:0000000000000001" || e.current.ProducerEpoch == old.ProducerEpoch {
		t.Fatal("boundary reused point identity")
	}
}
func TestMetricExporterRejectsFutureEndBeyondSignedEpochBeforeQueue(t *testing.T) {
	e, g, c := exporterFixture(t)
	data := metricDataFixture(g.now)
	sum := data.ScopeMetrics[0].Metrics[0].Data.(metricdata.Sum[int64])
	sum.DataPoints[0].Time = time.Unix(e.current.EpochNotAfter+1, 0)
	data.ScopeMetrics[0].Metrics[0].Data = sum
	if err := e.Export(context.Background(), data); err == nil {
		t.Fatal("point ending beyond epoch admitted")
	}
	if n, _ := e.PendingMetrics(); n != 0 || len(c.requests) != 0 {
		t.Fatal("invalid event window queued or sent")
	}
}

func fixtureMetricSignature(body []byte) string {
	mac := hmac.New(sha256.New, []byte("synthetic-sdk-fixture-signing-key"))
	_, _ = mac.Write(body)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func TestMetricSDKCollectionOver63PointsSplitsWithoutChangingValuesEpochOrIDs(t *testing.T) {
	e, g, c := exporterFixture(t)
	var points []metricdata.DataPoint[int64]
	for index := 0; index < 100; index++ {
		points = append(points, metricdata.DataPoint[int64]{StartTime: g.now.Add(-time.Second), Time: g.now, Value: int64(index + 1), Attributes: attribute.NewSet(attribute.Int("fixture.series", index))})
	}
	data := &metricdata.ResourceMetrics{ScopeMetrics: []metricdata.ScopeMetrics{{Scope: instrumentation.Scope{Name: "fixture.large-collection"}, Metrics: []metricdata.Metrics{{Name: "fixture.counter", Data: metricdata.Sum[int64]{Temporality: metricdata.DeltaTemporality, IsMonotonic: true, DataPoints: points}}}}}}
	if err := e.Export(context.Background(), data); err != nil {
		t.Fatal(err)
	}
	if len(c.requests) != 2 {
		t.Fatalf("request count=%d", len(c.requests))
	}
	ids := map[string]bool{}
	var total int64
	count := 0
	for index, request := range c.requests {
		if proto.Size(request) > int(e.config.MaxRPCBytes) || len(request.ResourceMetrics[0].ScopeMetrics) > 63 || c.tokens[index] != e.current.Token {
			t.Fatal("split violated bytes/point/epoch bound")
		}
		for _, scope := range request.ResourceMetrics[0].ScopeMetrics {
			point := scope.Metrics[0].GetSum().DataPoints[0]
			count++
			total += point.GetAsInt()
			var id string
			for _, a := range point.Attributes {
				if a.Key == MetricPointIDAttribute {
					id = a.Value.GetStringValue()
				}
			}
			if len(id) != 20 || id[:4] != "seq:" || ids[id] {
				t.Fatal("split lost/reused point identity")
			}
			ids[id] = true
		}
	}
	if count != 100 || total != 5050 {
		t.Fatalf("count=%d total=%d", count, total)
	}
}

func TestMetricDenseSequenceHardCapDoesNotWrapOrPartiallyAssign(t *testing.T) {
	e, _, c := exporterFixture(t)
	e.nextSequence = MetricMaxPointsPerEpoch - 1
	if err := e.Export(context.Background(), metricDataFixture(time.Now())); !errors.Is(err, ErrMetricSequenceExhausted) {
		t.Fatalf("err=%v", err)
	}
	if e.nextSequence != MetricMaxPointsPerEpoch-1 || len(c.requests) != 0 {
		t.Fatal("cap partially assigned or sent collection")
	}
	e.nextSequence = MetricMaxPointsPerEpoch + 1
	if err := e.Export(context.Background(), metricDataFixture(time.Now())); !errors.Is(err, ErrMetricSequenceExhausted) {
		t.Fatalf("overflow err=%v", err)
	}
}
