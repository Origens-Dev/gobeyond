package telemetry

import (
	"context"
	"net"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	collectormetric "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/structpb"
)

type installationGrantAPI interface{}
type installationServer struct {
	collectormetric.UnimplementedMetricsServiceServer
	fixture  *metricGrantFixture
	mu       sync.Mutex
	acquires int
	exports  int
}

func (s *installationServer) Export(context.Context, *collectormetric.ExportMetricsServiceRequest) (*collectormetric.ExportMetricsServiceResponse, error) {
	s.mu.Lock()
	s.exports++
	s.mu.Unlock()
	return &collectormetric.ExportMetricsServiceResponse{}, nil
}
func installationAcquire(server any, ctx context.Context, decode func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
	var request structpb.Struct
	if err := decode(&request); err != nil {
		return nil, err
	}
	s := server.(*installationServer)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acquires++
	g, err := s.fixture.Acquire(ctx, request.AsMap()["producer_instance_id"].(string))
	if err != nil {
		return nil, err
	}
	return structpb.NewStruct(map[string]any{"token": g.Token, "resource_id": g.ResourceID, "producer_id": g.ProducerID, "producer_epoch": g.ProducerEpoch, "epoch_started_at": float64(g.EpochStartedAt), "epoch_not_after": float64(g.EpochNotAfter), "retry_until": float64(g.RetryUntil)})
}
func TestRepeatedRuntimeInstallationDoesNotAcquireOrCollectTwice(t *testing.T) {
	oldAuth := activeTelemetryCredentials.Load()
	oldMeter, oldTracer := otel.GetMeterProvider(), otel.GetTracerProvider()
	installOnce = sync.Once{}
	shutdownOnce = sync.Once{}
	shutdown = func() {}
	t.Cleanup(func() {
		installOnce = sync.Once{}
		shutdownOnce = sync.Once{}
		shutdown = func() {}
		activeTelemetryCredentials.Store(oldAuth)
		otel.SetMeterProvider(oldMeter)
		otel.SetTracerProvider(oldTracer)
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	sink := &installationServer{fixture: newGrantFixture()}
	server.RegisterService(&grpc.ServiceDesc{ServiceName: "gobeyond.telemetry.v1.MetricProducerGrants", HandlerType: (*installationGrantAPI)(nil), Methods: []grpc.MethodDesc{{MethodName: "Acquire", Handler: installationAcquire}}}, sink)
	collectormetric.RegisterMetricsServiceServer(server, sink)
	go server.Serve(listener)
	defer server.Stop()
	for key, value := range map[string]string{
		"GOBEYOND_OTEL_EXPORTER_OTLP_ENDPOINT": listener.Addr().String(), "GOBEYOND_OTEL_EXPORTER_OTLP_AUTH_TOKEN": "original-runtime-token",
		"GOBEYOND_OTEL_METRICS_ENABLED": "1", "GOBEYOND_OTEL_METRICS_INTERVAL_MS": "60000", "GOBEYOND_OTEL_METRICS_RPC_TIMEOUT_MS": "5000", "GOBEYOND_OTEL_METRICS_MAX_PENDING_RPCS": "32", "GOBEYOND_OTEL_METRICS_MAX_RETAINED_BYTES": "8388608", "GOBEYOND_OTEL_METRICS_MAX_RPC_BYTES": "262144", "GOBEYOND_OTEL_METRICS_HISTOGRAM_BOUNDS": ".1,1,10",
	} {
		t.Setenv(key, value)
	}
	closeFirst := InstallFromEnv()
	first := otel.GetMeterProvider()
	closeSecond := InstallFromEnv()
	if first != otel.GetMeterProvider() {
		t.Fatal("repeat installation replaced meter provider")
	}
	provider, ok := first.(*sdkmetric.MeterProvider)
	if !ok {
		t.Fatal("metric installation failed")
	}
	counter, err := provider.Meter("fixture.idempotent-install").Int64Counter("requests")
	if err != nil {
		t.Fatal(err)
	}
	counter.Add(context.Background(), 1)
	if err := provider.ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}
	sink.mu.Lock()
	acquires, exports := sink.acquires, sink.exports
	sink.mu.Unlock()
	if acquires != 1 || exports != 1 {
		t.Fatalf("duplicate installation acquire=%d exports=%d", acquires, exports)
	}
	closeFirst()
	closeSecond()
}
