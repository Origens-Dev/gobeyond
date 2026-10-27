package telemetry

import (
	"context"
	"crypto/hmac"
	"encoding/base64"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type signedTraceReceiverFixture struct {
	collectortrace.UnimplementedTraceServiceServer
	now    atomic.Int64
	mu     sync.Mutex
	epochs []string
}

func (s *signedTraceReceiverFixture) Export(ctx context.Context, _ *collectortrace.ExportTraceServiceRequest) (*collectortrace.ExportTraceServiceResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	tokens := md.Get(tokenHeader)
	if len(tokens) != 1 {
		return nil, status.Error(codes.Unauthenticated, "expected one auth credential")
	}
	parts := strings.Split(tokens[0], ".")
	if len(parts) != 2 {
		return nil, status.Error(codes.Unauthenticated, "bad token")
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || !hmac.Equal([]byte(parts[1]), []byte(fixtureMetricSignature(body))) {
		return nil, status.Error(codes.Unauthenticated, "bad fixture signature")
	}
	var claims struct {
		Producer    MetricGrant `json:"metric_producer"`
		Expires     int64       `json:"exp"`
		Org         string      `json:"organization_id"`
		Project     string      `json:"project_id"`
		Environment string      `json:"environment_id"`
	}
	if err = json.Unmarshal(body, &claims); err != nil || s.now.Load() >= claims.Expires || s.now.Load() >= claims.Producer.RetryUntil {
		return nil, status.Error(codes.Unauthenticated, "expired trace authorization")
	}
	if claims.Org != "fixture-org" || claims.Project != "fixture-project" || claims.Environment != "fixture-env" {
		return nil, status.Error(codes.PermissionDenied, "signed tenant changed")
	}
	s.mu.Lock()
	s.epochs = append(s.epochs, claims.Producer.ProducerEpoch)
	s.mu.Unlock()
	return &collectortrace.ExportTraceServiceResponse{}, nil
}
func TestActualTraceRPCRefreshesAfter24HoursAndOldQueueRenewCannotDowngradeAuth(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", tokenHeader+"=untrusted-static-token")
	fixture := newGrantFixture()
	connection := &grantConnectionFixture{fixture: fixture}
	grants, err := NewGRPCMetricGrantProvider(connection, "initial-runtime-token")
	if err != nil {
		t.Fatal(err)
	}
	authorization := &refreshableTelemetryCredentials{token: "initial-runtime-token"}
	grants.tokenUpdated = authorization.update
	grants.now = func() time.Time { return fixture.now }
	first, err := grants.Acquire(context.Background(), strings.Repeat("a", 32))
	if err != nil {
		t.Fatal(err)
	}
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	receiver := new(signedTraceReceiverFixture)
	receiver.now.Store(fixture.now.Unix())
	collectortrace.RegisterTraceServiceServer(server, receiver)
	go server.Serve(listener)
	defer server.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	exporter, err := otlptracegrpc.New(ctx, otlptracegrpc.WithEndpoint("127.0.0.1:4317"), otlptracegrpc.WithInsecure(), otlptracegrpc.WithHeaders(map[string]string{}), otlptracegrpc.WithTimeout(time.Second), otlptracegrpc.WithRetry(otlptracegrpc.RetryConfig{Enabled: false}), otlptracegrpc.WithDialOption(grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }), grpc.WithPerRPCCredentials(authorization)))
	if err != nil {
		t.Fatal(err)
	}
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer provider.Shutdown(context.Background())
	emit := func(name string) {
		_, span := provider.Tracer("fixture.actual-trace").Start(context.Background(), name)
		span.End()
	}
	emit("before-renewal")
	fixture.now = fixture.now.Add(24*time.Hour - 2*time.Minute)
	renewed, err := grants.Renew(context.Background(), first.ProducerEpoch)
	if err != nil {
		t.Fatal(err)
	}
	if !renewed.sameIdentity(first) || renewed.Token == first.Token {
		t.Fatal("renewal did not preserve fixed identity and refresh auth")
	}
	fixture.now = fixture.now.Add(3 * time.Minute)
	receiver.now.Store(fixture.now.Unix())
	emit("after-original-24h-expiry")
	second, err := grants.Acquire(context.Background(), strings.Repeat("b", 32))
	if err != nil {
		t.Fatal(err)
	}
	// An unresolved old RPC refreshes its own epoch during grace, after the
	// current producer has reset. Its token must never become trace authority.
	if _, err = grants.Renew(context.Background(), first.ProducerEpoch); err != nil {
		t.Fatal(err)
	}
	emit("after-old-queued-epoch-renewal")
	receiver.mu.Lock()
	defer receiver.mu.Unlock()
	if len(receiver.epochs) != 3 || receiver.epochs[0] != first.ProducerEpoch || receiver.epochs[1] != first.ProducerEpoch || receiver.epochs[2] != second.ProducerEpoch {
		t.Fatalf("actual trace RPC auth epochs=%v", receiver.epochs)
	}
}
func TestRefreshableTraceCredentialsKeepLegacyTokenWhenMetricsDisabled(t *testing.T) {
	auth := &refreshableTelemetryCredentials{token: "legacy-runtime-token"}
	headers, err := auth.GetRequestMetadata(context.Background())
	if err != nil || len(headers) != 1 || headers[tokenHeader] != "legacy-runtime-token" || auth.RequireTransportSecurity() {
		t.Fatalf("legacy credentials=%v err=%v", headers, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = auth.GetRequestMetadata(ctx); err == nil {
		t.Fatal("canceled credential callback ignored deadline")
	}
}

func TestSharedCredentialsRequireOriginalRuntimeNamespace(t *testing.T) {
	old := activeTelemetryCredentials.Load()
	defer activeTelemetryCredentials.Store(old)
	c := &refreshableTelemetryCredentials{token: "current", initialToken: "original", endpoint: "localhost:4317"}
	activeTelemetryCredentials.Store(c)
	if got := CurrentTelemetryAuthToken("http://localhost:4317", "original"); got != "current" {
		t.Fatal(got)
	}
	for _, v := range []struct{ endpoint, token string }{{"localhost:9999", "original"}, {"localhost:4317", "another-runtime"}} {
		if got := CurrentTelemetryAuthToken(v.endpoint, v.token); got != v.token {
			t.Fatalf("credential crossed namespace: %s", got)
		}
	}
}
