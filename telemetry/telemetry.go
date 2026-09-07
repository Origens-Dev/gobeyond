// Package telemetry installs the process-wide OpenTelemetry trace provider
// used by GoBeyond application runtimes.
package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

const (
	tokenHeader          = "x-gobeyond-telemetry-token"
	defaultSampleRatio   = 1.0
	exporterSetupTimeout = 2 * time.Second
	exportBatchTimeout   = time.Second
	shutdownTimeout      = 2 * time.Second
)

type requestIDContextKey struct{}

// InstallFromEnv enables authenticated OTLP/gRPC export when the hosting
// platform injected an endpoint and tenant-bound token. Missing or invalid
// telemetry configuration degrades to the OpenTelemetry no-op provider.
func InstallFromEnv() func() {
	log := slog.Default()
	endpoint := normalizeEndpoint(os.Getenv("GOBEYOND_OTEL_EXPORTER_OTLP_ENDPOINT"))
	token := strings.TrimSpace(os.Getenv("GOBEYOND_OTEL_EXPORTER_OTLP_AUTH_TOKEN"))
	if endpoint == "" || token == "" {
		return func() {}
	}
	ratio := defaultSampleRatio
	if raw := strings.TrimSpace(os.Getenv("GOBEYOND_OTEL_TRACE_SAMPLE_RATIO")); raw != "" {
		if parsed, err := strconv.ParseFloat(raw, 64); err == nil && parsed >= 0 && parsed <= 1 && !math.IsNaN(parsed) {
			ratio = parsed
		}
	}
	setupCtx, cancel := context.WithTimeout(context.Background(), exporterSetupTimeout)
	defer cancel()
	exporter, err := otlptracegrpc.New(
		setupCtx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(),
		otlptracegrpc.WithHeaders(map[string]string{tokenHeader: token}),
	)
	if err != nil {
		log.Warn("OpenTelemetry trace export disabled", "error", err)
		return func() {}
	}
	res, err := resource.New(context.Background(),
		resource.WithTelemetrySDK(),
		resource.WithAttributes(
			semconv.ServiceName(firstNonEmpty(os.Getenv("GOBEYOND_PROJECT_SLUG"), "gobeyond-app")),
			attribute.String("gobeyond.runtime_role", "app"),
			attribute.String("deployment.environment", strings.TrimSpace(os.Getenv("GOBEYOND_PLATFORM_ENV"))),
		),
	)
	if err != nil {
		log.Warn("OpenTelemetry trace export disabled", "error", err)
		_ = exporter.Shutdown(context.Background())
		return func() {}
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))),
		sdktrace.WithSpanProcessor(requestIDProcessor{}),
		sdktrace.WithBatcher(exporter, sdktrace.WithBatchTimeout(exportBatchTimeout)),
	)
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		log.Warn("OpenTelemetry trace export failed", "error", err)
	}))
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := errors.Join(provider.ForceFlush(ctx), provider.Shutdown(ctx)); err != nil {
			log.Warn("OpenTelemetry trace shutdown failed", "error", err)
		}
	}
}

// StartServerRequest extracts upstream W3C context and starts the application
// boundary span. The request ID is propagated through context so every child
// span is normalized onto the platform request trace.
func StartServerRequest(ctx context.Context, header http.Header, requestID, method, path string) (context.Context, trace.Span) {
	ctx = otel.GetTextMapPropagator().Extract(ctx, propagation.HeaderCarrier(header))
	ctx = context.WithValue(ctx, requestIDContextKey{}, requestID)
	return otel.Tracer("github.com/Origens-Dev/gobeyond/runtime").Start(ctx, method+" "+path,
		trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(
			attribute.String("gobeyond.request_id", requestID),
			attribute.String("gobeyond.root_kind", "application"),
			attribute.String("http.request.method", method),
			attribute.String("url.path", path),
		),
	)
}

type requestIDProcessor struct{}

func (requestIDProcessor) OnStart(ctx context.Context, span sdktrace.ReadWriteSpan) {
	if requestID, _ := ctx.Value(requestIDContextKey{}).(string); requestID != "" {
		span.SetAttributes(attribute.String("gobeyond.request_id", requestID))
	}
}

func (requestIDProcessor) OnEnd(sdktrace.ReadOnlySpan)      {}
func (requestIDProcessor) Shutdown(context.Context) error   { return nil }
func (requestIDProcessor) ForceFlush(context.Context) error { return nil }

func normalizeEndpoint(raw string) string {
	value := strings.TrimSpace(raw)
	value = strings.TrimPrefix(value, "http://")
	value = strings.TrimPrefix(value, "https://")
	if value == "" || strings.ContainsAny(value, "/?#") {
		return ""
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
