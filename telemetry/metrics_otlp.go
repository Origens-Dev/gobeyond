package telemetry

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	collectormetric "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/proto"
	"strings"
)

func metricTokenNeedsRenewal(token string, now time.Time) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return true
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return true
	}
	var c struct {
		ExpiresAt int64 `json:"exp"`
	}
	if json.Unmarshal(body, &c) != nil {
		return true
	}
	return c.ExpiresAt <= now.Add(5*time.Minute).Unix()
}
func metricResourceRequest(data *metricdata.ResourceMetrics) (*collectormetric.ExportMetricsServiceRequest, error) {
	out := new(collectormetric.ExportMetricsServiceRequest)
	if data == nil || len(data.ScopeMetrics) == 0 {
		return out, nil
	}
	resource := &metricspb.ResourceMetrics{Resource: &resourcepb.Resource{}}
	if data.Resource != nil {
		attrs, err := metricOTLPAttributes(data.Resource.Attributes(), false)
		if err != nil {
			return nil, err
		}
		resource.Resource.Attributes = attrs
		resource.SchemaUrl = data.Resource.SchemaURL()
	}
	for _, scope := range data.ScopeMetrics {
		if scope.Scope.Attributes.Len() != 0 {
			return nil, errors.New("metric instrumentation scope attributes are unsupported by the current contract")
		}
		sm := &metricspb.ScopeMetrics{Scope: &commonpb.InstrumentationScope{Name: scope.Scope.Name, Version: scope.Scope.Version}, SchemaUrl: scope.Scope.SchemaURL}
		for _, metric := range scope.Metrics {
			m := &metricspb.Metric{Name: metric.Name, Description: metric.Description, Unit: metric.Unit}
			switch d := metric.Data.(type) {
			case metricdata.Sum[int64]:
				if d.Temporality != metricdata.DeltaTemporality || !d.IsMonotonic {
					return nil, errors.New("only monotonic delta counters are supported")
				}
				points, err := metricOTLPNumberPoints(d.DataPoints, true)
				if err != nil {
					return nil, err
				}
				m.Data = &metricspb.Metric_Sum{Sum: &metricspb.Sum{IsMonotonic: true, AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA, DataPoints: points}}
			case metricdata.Sum[float64]:
				if d.Temporality != metricdata.DeltaTemporality || !d.IsMonotonic {
					return nil, errors.New("only monotonic delta counters are supported")
				}
				points, err := metricOTLPNumberPoints(d.DataPoints, true)
				if err != nil {
					return nil, err
				}
				m.Data = &metricspb.Metric_Sum{Sum: &metricspb.Sum{IsMonotonic: true, AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA, DataPoints: points}}
			case metricdata.Gauge[int64]:
				points, err := metricOTLPNumberPoints(d.DataPoints, false)
				if err != nil {
					return nil, err
				}
				m.Data = &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: points}}
			case metricdata.Gauge[float64]:
				points, err := metricOTLPNumberPoints(d.DataPoints, false)
				if err != nil {
					return nil, err
				}
				m.Data = &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: points}}
			case metricdata.Histogram[int64]:
				if d.Temporality != metricdata.DeltaTemporality {
					return nil, errors.New("histogram must use delta temporality")
				}
				points, err := metricOTLPHistogramPoints(d.DataPoints)
				if err != nil {
					return nil, err
				}
				m.Data = &metricspb.Metric_Histogram{Histogram: &metricspb.Histogram{AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA, DataPoints: points}}
			case metricdata.Histogram[float64]:
				if d.Temporality != metricdata.DeltaTemporality {
					return nil, errors.New("histogram must use delta temporality")
				}
				points, err := metricOTLPHistogramPoints(d.DataPoints)
				if err != nil {
					return nil, err
				}
				m.Data = &metricspb.Metric_Histogram{Histogram: &metricspb.Histogram{AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_DELTA, DataPoints: points}}
			default:
				return nil, errors.New("unsupported metric aggregation; explicit delta histograms, counters and gauges only")
			}
			if m.Name == "" {
				return nil, errors.New("metric name is required")
			}
			sm.Metrics = append(sm.Metrics, m)
		}
		if len(sm.Metrics) > 0 {
			resource.ScopeMetrics = append(resource.ScopeMetrics, sm)
		}
	}
	if len(resource.ScopeMetrics) > 0 {
		out.ResourceMetrics = []*metricspb.ResourceMetrics{resource}
	}
	return out, nil
}
func metricOTLPNumberPoints[N int64 | float64](input []metricdata.DataPoint[N], delta bool) ([]*metricspb.NumberDataPoint, error) {
	out := make([]*metricspb.NumberDataPoint, 0, len(input))
	for _, p := range input {
		attrs, err := metricOTLPAttributes(p.Attributes.ToSlice(), true)
		if err != nil {
			return nil, err
		}
		if p.Time.IsZero() || p.Time.UnixNano() <= 0 {
			return nil, errors.New("metric observation timestamp is required")
		}
		point := &metricspb.NumberDataPoint{Attributes: attrs, TimeUnixNano: uint64(p.Time.UnixNano())}
		if !p.StartTime.IsZero() {
			if p.StartTime.UnixNano() <= 0 {
				return nil, errors.New("invalid metric start timestamp")
			}
			point.StartTimeUnixNano = uint64(p.StartTime.UnixNano())
		}
		if delta && (point.StartTimeUnixNano == 0 || point.StartTimeUnixNano >= point.TimeUnixNano) {
			return nil, errors.New("delta counter interval must have positive duration")
		}
		switch value := any(p.Value).(type) {
		case int64:
			if delta && value < 0 {
				return nil, errors.New("monotonic delta counter cannot be negative")
			}
			point.Value = &metricspb.NumberDataPoint_AsInt{AsInt: value}
		case float64:
			if math.IsNaN(value) || math.IsInf(value, 0) || (delta && value < 0) {
				return nil, errors.New("metric value must be finite and monotonic counters nonnegative")
			}
			point.Value = &metricspb.NumberDataPoint_AsDouble{AsDouble: value}
		}
		out = append(out, point)
	}
	return out, nil
}
func metricOTLPHistogramPoints[N int64 | float64](input []metricdata.HistogramDataPoint[N]) ([]*metricspb.HistogramDataPoint, error) {
	out := make([]*metricspb.HistogramDataPoint, 0, len(input))
	for _, p := range input {
		if p.StartTime.IsZero() || p.StartTime.UnixNano() <= 0 || !p.Time.After(p.StartTime) {
			return nil, errors.New("delta histogram interval must have positive duration")
		}
		if len(p.Bounds) > 64 || len(p.BucketCounts) != len(p.Bounds)+1 {
			return nil, errors.New("invalid explicit histogram layout")
		}
		for i, b := range p.Bounds {
			if math.IsNaN(b) || math.IsInf(b, 0) || (i > 0 && b <= p.Bounds[i-1]) {
				return nil, errors.New("invalid explicit histogram boundary")
			}
		}
		var count uint64
		for _, n := range p.BucketCounts {
			if math.MaxUint64-count < n {
				return nil, errors.New("histogram count overflows uint64")
			}
			count += n
		}
		if count != p.Count {
			return nil, errors.New("histogram count differs from bucket population")
		}
		sum := float64(p.Sum)
		if math.IsNaN(sum) || math.IsInf(sum, 0) || (p.Count == 0 && sum != 0) {
			return nil, errors.New("invalid histogram sum")
		}
		attrs, err := metricOTLPAttributes(p.Attributes.ToSlice(), true)
		if err != nil {
			return nil, err
		}
		out = append(out, &metricspb.HistogramDataPoint{Attributes: attrs, StartTimeUnixNano: uint64(p.StartTime.UnixNano()), TimeUnixNano: uint64(p.Time.UnixNano()), Count: p.Count, Sum: &sum, ExplicitBounds: append([]float64{}, p.Bounds...), BucketCounts: append([]uint64{}, p.BucketCounts...)})
	}
	return out, nil
}
func metricOTLPAttributes(input []attribute.KeyValue, point bool) ([]*commonpb.KeyValue, error) {
	out := make([]*commonpb.KeyValue, 0, len(input)+1)
	for _, a := range input {
		if string(a.Key) == MetricPointIDAttribute {
			return nil, errors.New("reserved metric point identity cannot be supplied by user attributes")
		}
		v := new(commonpb.AnyValue)
		switch a.Value.Type() {
		case attribute.STRING:
			v.Value = &commonpb.AnyValue_StringValue{StringValue: a.Value.AsString()}
		case attribute.INT64:
			v.Value = &commonpb.AnyValue_IntValue{IntValue: a.Value.AsInt64()}
		case attribute.BOOL:
			v.Value = &commonpb.AnyValue_BoolValue{BoolValue: a.Value.AsBool()}
		case attribute.FLOAT64:
			n := a.Value.AsFloat64()
			if math.IsNaN(n) || math.IsInf(n, 0) {
				return nil, errors.New("metric attribute must be finite")
			}
			v.Value = &commonpb.AnyValue_DoubleValue{DoubleValue: n}
		default:
			return nil, errors.New("metric attributes must be scalar")
		}
		out = append(out, &commonpb.KeyValue{Key: string(a.Key), Value: v})
	}
	if point {
		id, err := metricRandomID()
		if err != nil {
			return nil, err
		}
		out = append(out, &commonpb.KeyValue{Key: MetricPointIDAttribute, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: id}}})
	}
	return out, nil
}

// metricRPCBodies splits only between already identified points. Each wire
// request is bounded and independently retryable; no point ID is regenerated.
// Current contract-v2 permits at most 4KiB canonical points. Its bounded
// normalized spool envelope is at most64 bytes: 63*(4096+64)=262080<256KiB.
// This additional cap does not excuse invalid/oversized individual points.
const maxMetricRPCPoints = 63

func metricRPCBodies(request *collectormetric.ExportMetricsServiceRequest, limit uint64) ([][]byte, error) {
	var bodies [][]byte
	for _, resource := range request.ResourceMetrics {
		base := &metricspb.ResourceMetrics{Resource: resource.Resource, SchemaUrl: resource.SchemaUrl}
		chunk := &collectormetric.ExportMetricsServiceRequest{ResourceMetrics: []*metricspb.ResourceMetrics{base}}
		flush := func() error {
			if len(base.ScopeMetrics) == 0 {
				return nil
			}
			body, err := proto.MarshalOptions{Deterministic: true}.Marshal(chunk)
			if err != nil {
				return err
			}
			bodies = append(bodies, body)
			base.ScopeMetrics = nil
			return nil
		}
		for _, scope := range resource.ScopeMetrics {
			for _, metric := range scope.Metrics {
				var points int
				switch d := metric.Data.(type) {
				case *metricspb.Metric_Sum:
					points = len(d.Sum.DataPoints)
				case *metricspb.Metric_Gauge:
					points = len(d.Gauge.DataPoints)
				case *metricspb.Metric_Histogram:
					points = len(d.Histogram.DataPoints)
				default:
					return nil, errors.New("unsupported metric request")
				}
				for index := 0; index < points; index++ {
					unit := &metricspb.Metric{Name: metric.Name, Description: metric.Description, Unit: metric.Unit}
					switch d := metric.Data.(type) {
					case *metricspb.Metric_Sum:
						unit.Data = &metricspb.Metric_Sum{Sum: &metricspb.Sum{AggregationTemporality: d.Sum.AggregationTemporality, IsMonotonic: d.Sum.IsMonotonic, DataPoints: []*metricspb.NumberDataPoint{d.Sum.DataPoints[index]}}}
					case *metricspb.Metric_Gauge:
						unit.Data = &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: []*metricspb.NumberDataPoint{d.Gauge.DataPoints[index]}}}
					case *metricspb.Metric_Histogram:
						unit.Data = &metricspb.Metric_Histogram{Histogram: &metricspb.Histogram{AggregationTemporality: d.Histogram.AggregationTemporality, DataPoints: []*metricspb.HistogramDataPoint{d.Histogram.DataPoints[index]}}}
					}
					part := &metricspb.ScopeMetrics{Scope: scope.Scope, SchemaUrl: scope.SchemaUrl, Metrics: []*metricspb.Metric{unit}}
					base.ScopeMetrics = append(base.ScopeMetrics, part)
					if uint64(proto.Size(chunk)) > limit || len(base.ScopeMetrics) > maxMetricRPCPoints {
						base.ScopeMetrics = base.ScopeMetrics[:len(base.ScopeMetrics)-1]
						if err := flush(); err != nil {
							return nil, err
						}
						base.ScopeMetrics = append(base.ScopeMetrics, part)
						if uint64(proto.Size(chunk)) > limit {
							return nil, errors.New("one metric point exceeds configured RPC payload limit")
						}
					}
				}
			}
		}
		if err := flush(); err != nil {
			return nil, err
		}
	}
	return bodies, nil
}

func metricRequestWithinEpoch(request *collectormetric.ExportMetricsServiceRequest, grant MetricGrant) error {
	limit := uint64(grant.EpochNotAfter) * uint64(time.Second)
	check := func(end uint64) error {
		if end > limit {
			return errors.New("new metric point ends after its signed producer epoch")
		}
		return nil
	}
	for _, resource := range request.ResourceMetrics {
		for _, scope := range resource.ScopeMetrics {
			for _, metric := range scope.Metrics {
				switch data := metric.Data.(type) {
				case *metricspb.Metric_Sum:
					for _, point := range data.Sum.DataPoints {
						if err := check(point.TimeUnixNano); err != nil {
							return err
						}
					}
				case *metricspb.Metric_Gauge:
					for _, point := range data.Gauge.DataPoints {
						if err := check(point.TimeUnixNano); err != nil {
							return err
						}
					}
				case *metricspb.Metric_Histogram:
					for _, point := range data.Histogram.DataPoints {
						if err := check(point.TimeUnixNano); err != nil {
							return err
						}
					}
				default:
					return errors.New("unsupported metric epoch validation")
				}
			}
		}
	}
	return nil
}

// MetricPointTokenFormat is the bounded epoch-wide identity directory format.
const MetricPointTokenFormat = "dense-sequence/v1"
const MetricMaxPointsPerEpoch uint64 = 1 << 24

func (e *MetricsExporter) assignPointSequences(request *collectormetric.ExportMetricsServiceRequest) error {
	var attributes []*[]*commonpb.KeyValue
	for _, r := range request.ResourceMetrics {
		for _, scope := range r.ScopeMetrics {
			for _, m := range scope.Metrics {
				switch d := m.Data.(type) {
				case *metricspb.Metric_Sum:
					for _, p := range d.Sum.DataPoints {
						attributes = append(attributes, &p.Attributes)
					}
				case *metricspb.Metric_Gauge:
					for _, p := range d.Gauge.DataPoints {
						attributes = append(attributes, &p.Attributes)
					}
				case *metricspb.Metric_Histogram:
					for _, p := range d.Histogram.DataPoints {
						attributes = append(attributes, &p.Attributes)
					}
				}
			}
		}
	}
	if e.nextSequence > MetricMaxPointsPerEpoch || uint64(len(attributes)) > MetricMaxPointsPerEpoch-e.nextSequence {
		return ErrMetricSequenceExhausted
	}
	for _, attrs := range attributes {
		e.nextSequence++
		for _, a := range *attrs {
			if a.Key == MetricPointIDAttribute {
				a.Value = &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: fmt.Sprintf("seq:%016x", e.nextSequence)}}
			}
		}
	}
	return nil
}
