package cache

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// RemoteInvalidationOptions describes an application-owned invalidation event.
// Applications should use their webhook provider's delivery ID as the
// idempotency key. Tags and paths are both optional; the platform always
// advances the deployment generation for correctness.
type RemoteInvalidationOptions struct {
	EnvironmentID  string
	IdempotencyKey string
	Hosts          []string
	Tags           []string
	Paths          []string
	APIURL         string
	Token          string
}

type RemoteInvalidationResult struct {
	Status     string `json:"status"`
	WorkflowID string `json:"workflow_id,omitempty"`
	RunID      string `json:"run_id,omitempty"`
}

// InvalidateRemote starts the platform's durable invalidation workflow. It is
// intended for an application webhook handler after that handler has verified
// the source webhook signature. The call is asynchronous: accepted means the
// invalidation was durably handed to Temporal, not that every cache layer has
// already observed it.
func InvalidateRemote(ctx context.Context, options RemoteInvalidationOptions) (RemoteInvalidationResult, error) {
	options.EnvironmentID = strings.TrimSpace(options.EnvironmentID)
	options.IdempotencyKey = strings.TrimSpace(options.IdempotencyKey)
	if options.EnvironmentID == "" || options.IdempotencyKey == "" {
		return RemoteInvalidationResult{}, fmt.Errorf("cache: remote invalidation requires environment id and idempotency key")
	}
	base := strings.TrimRight(strings.TrimSpace(options.APIURL), "/")
	if base == "" {
		base = strings.TrimRight(strings.TrimSpace(os.Getenv("GOBEYOND_API_URL")), "/")
	}
	token := strings.TrimSpace(options.Token)
	if token == "" {
		token = strings.TrimSpace(os.Getenv("GOBEYOND_INTERNAL_API_TOKEN"))
	}
	if base == "" || token == "" {
		return RemoteInvalidationResult{}, fmt.Errorf("cache: remote invalidation platform credentials are not configured")
	}
	payload, err := json.Marshal(struct {
		EnvironmentID  string   `json:"environment_id"`
		IdempotencyKey string   `json:"idempotency_key"`
		Hosts          []string `json:"hosts,omitempty"`
		Tags           []string `json:"tags,omitempty"`
		Paths          []string `json:"paths,omitempty"`
	}{
		EnvironmentID:  options.EnvironmentID,
		IdempotencyKey: options.IdempotencyKey,
		Hosts:          options.Hosts,
		Tags:           options.Tags,
		Paths:          options.Paths,
	})
	if err != nil {
		return RemoteInvalidationResult{}, fmt.Errorf("cache: encode remote invalidation: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/internal/cache/invalidate", bytes.NewReader(payload))
	if err != nil {
		return RemoteInvalidationResult{}, fmt.Errorf("cache: create remote invalidation request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GoBeyond-Internal-Token", token)
	req.Header.Set("X-GoBeyond-Environment-ID", options.EnvironmentID)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return RemoteInvalidationResult{}, fmt.Errorf("cache: send remote invalidation: %w", err)
	}
	defer res.Body.Close()
	var result RemoteInvalidationResult
	if err := json.NewDecoder(res.Body).Decode(&result); err != nil {
		return RemoteInvalidationResult{}, fmt.Errorf("cache: decode remote invalidation response (http %d): %w", res.StatusCode, err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return RemoteInvalidationResult{}, fmt.Errorf("cache: remote invalidation returned http %d", res.StatusCode)
	}
	return result, nil
}
