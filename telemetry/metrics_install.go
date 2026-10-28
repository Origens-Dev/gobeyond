package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	collectormetric "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func installMetricsFromEnv(endpoint, token string, res *resource.Resource, log *slog.Logger, tokenUpdated func(string)) func() {
	if os.Getenv("GOBEYOND_OTEL_METRICS_ENABLED") != "1" {
		return func() {}
	}
	disabled := func(err error) func() {
		log.Warn("OpenTelemetry metric export unavailable", "error", err)
		otel.Handle(err)
		return func() {}
	}
	positive := func(name string) (uint64, error) {
		raw := strings.TrimSpace(os.Getenv(name))
		n, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || n == 0 {
			return 0, errors.New("explicit positive metric exporter setting required: " + name)
		}
		return n, nil
	}
	intervalMS, err := positive("GOBEYOND_OTEL_METRICS_INTERVAL_MS")
	if err != nil {
		return disabled(err)
	}
	timeoutMS, err := positive("GOBEYOND_OTEL_METRICS_RPC_TIMEOUT_MS")
	if err != nil {
		return disabled(err)
	}
	pending, err := positive("GOBEYOND_OTEL_METRICS_MAX_PENDING_RPCS")
	if err != nil {
		return disabled(err)
	}
	retained, err := positive("GOBEYOND_OTEL_METRICS_MAX_RETAINED_BYTES")
	if err != nil {
		return disabled(err)
	}
	rpcBytes, err := positive("GOBEYOND_OTEL_METRICS_MAX_RPC_BYTES")
	if err != nil {
		return disabled(err)
	}
	if intervalMS > 60000 || timeoutMS > 5000 || pending > 1024 {
		return disabled(errors.New("metric exporter interval, timeout or pending RPC setting exceeds bound"))
	}
	rawBounds, ok := os.LookupEnv("GOBEYOND_OTEL_METRICS_HISTOGRAM_BOUNDS")
	if !ok {
		return disabled(errors.New("explicit histogram bounds are required"))
	}
	var bounds []float64
	if strings.TrimSpace(rawBounds) != "" {
		for _, raw := range strings.Split(rawBounds, ",") {
			n, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
			if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
				return disabled(errors.New("invalid metric histogram bounds"))
			}
			bounds = append(bounds, n)
		}
	}
	client, err := grpc.NewClient(endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return disabled(err)
	}
	grants, err := NewGRPCMetricGrantProvider(client, token)
	if err != nil {
		_ = client.Close()
		return disabled(err)
	}
	grants.tokenUpdated = tokenUpdated
	ctx, cancel := context.WithTimeout(context.Background(), exporterSetupTimeout)
	defer cancel()
	exporter, err := NewMetricsExporter(ctx, MetricsExporterConfig{Grants: grants, Client: collectormetric.NewMetricsServiceClient(client), MaxRetainedBytes: retained, MaxRPCBytes: rpcBytes, MaxPendingRPCs: int(pending), RPCTimeout: time.Duration(timeoutMS) * time.Millisecond, HistogramBounds: bounds})
	if err != nil {
		_ = client.Close()
		return disabled(err)
	}
	reader := sdkmetric.NewPeriodicReader(exporter, sdkmetric.WithInterval(time.Duration(intervalMS)*time.Millisecond), sdkmetric.WithTimeout(time.Duration(timeoutMS)*time.Millisecond))
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithResource(res), sdkmetric.WithReader(reader))
	otel.SetMeterProvider(provider)
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := errors.Join(provider.ForceFlush(ctx), provider.Shutdown(ctx), client.Close()); err != nil {
			log.Warn("OpenTelemetry metric shutdown incomplete", "error", err)
		}
	}
}
