package telemetry

import (
	"context"
	"sync"
	"sync/atomic"
)

// refreshableTelemetryCredentials supplies exactly one current auth header.
// No static auth header is installed alongside these per-RPC credentials.
type refreshableTelemetryCredentials struct {
	mu           sync.RWMutex
	token        string
	endpoint     string
	initialToken string
}

func (c *refreshableTelemetryCredentials) update(token string) {
	c.mu.Lock()
	c.token = token
	c.mu.Unlock()
}
func (c *refreshableTelemetryCredentials) GetRequestMetadata(ctx context.Context, _ ...string) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.RLock()
	token := c.token
	c.mu.RUnlock()
	return map[string]string{tokenHeader: token}, nil
}

// Existing platform OTLP is an authenticated host-local insecure gRPC endpoint.
func (*refreshableTelemetryCredentials) RequireTransportSecurity() bool { return false }

var activeTelemetryCredentials atomic.Pointer[refreshableTelemetryCredentials]

// CurrentTelemetryAuthToken returns the current active producer credential for
// another process-local telemetry exporter. Old queued epochs never replace it.
// If metrics renewal is disabled or installation has not run, fallback is kept.
func CurrentTelemetryAuthToken(endpoint, fallback string) string {
	c := activeTelemetryCredentials.Load()
	if c == nil || normalizeEndpoint(endpoint) != c.endpoint || fallback != c.initialToken {
		return fallback
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.token == "" {
		return fallback
	}
	return c.token
}
