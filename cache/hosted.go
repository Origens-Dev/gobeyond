package cache

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// HostedInvalidationOptions addresses only the admitted environment. The host
// supplies identity; applications cannot select hosts or another environment.
type HostedInvalidationOptions struct {
	IdempotencyKey string `json:"idempotency_key"`
}

// InvalidateHosted durably submits an invalidation through the slot capability.
// Accepted means queued, not propagated. It never uses platform credentials.
func InvalidateHosted(ctx context.Context, options HostedInvalidationOptions) (RemoteInvalidationResult, error) {
	if isSharedComputation(ctx) {
		return RemoteInvalidationResult{}, ErrSharedComputation
	}
	key := strings.TrimSpace(options.IdempotencyKey)
	if key == "" || len(key) > 200 {
		return RemoteInvalidationResult{}, fmt.Errorf("cache: invalid idempotency key")
	}
	socket := strings.TrimSpace(os.Getenv("GOBEYOND_HOST_REPORT_SOCKET"))
	if socket == "" {
		return RemoteInvalidationResult{}, fmt.Errorf("cache: hosted invalidation capability unavailable")
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	raw, _ := json.Marshal(HostedInvalidationOptions{IdempotencyKey: key})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://host/v1/cache/invalidate", bytes.NewReader(raw))
	if err != nil {
		return RemoteInvalidationResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return RemoteInvalidationResult{}, fmt.Errorf("cache: host invalidation request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return RemoteInvalidationResult{}, fmt.Errorf("cache: host invalidation returned http %d", resp.StatusCode)
	}
	var result RemoteInvalidationResult
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&result); err != nil {
		return result, fmt.Errorf("cache: invalid acceptance response")
	}
	if result.Status != "accepted" && result.Status != "started" && result.Status != "already_started" && result.Status != "existing" {
		return result, fmt.Errorf("cache: invalid acceptance status")
	}
	return result, nil
}
