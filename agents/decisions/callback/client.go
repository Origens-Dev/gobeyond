package callback

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Origens-Dev/gobeyond/agents/connections"
	decisionv1 "github.com/Origens-Dev/gobeyond/agents/decisioncontract/v1"
)

const maxMatchResponseBytes = 64 << 10

// MatchClient is the host-side signed POST for product-owned match. It calls
// connections.SignDelivery directly. There is no second HMAC, timestamp, or
// nonce scheme.
//
// Event-ID is derived from session id, run id, and attempt count, so a retry
// of the same request reuses the same Event-ID and is safe to answer again.
// A session can have more than one run (resume after a terminal result);
// run id is minted once per execution and does not change on retry.
type MatchClient struct {
	BaseURL    string
	Key        []byte
	Path       string
	HTTPClient *http.Client
	Now        func() time.Time
}

// MatchEventID is the stable connections delivery Event-ID for one match
// attempt. It includes the run ID because httpruntime.Runtime.createRun
// appends a new execution to an existing session after a terminal result
// (POST .../sessions/{sessionID}/resume). WorkflowID is likewise
// sessionID/runID. Run ID is minted once per execution and does not change
// when the same attempt is retried, so retries keep this value.
func MatchEventID(sessionID, runID string, attemptCount uint64) string {
	return fmt.Sprintf("match/%s/%s/%d", sessionID, runID, attemptCount)
}

// Match signs the request with connections.SignDelivery, POSTs it, and
// ValidateFor the frozen definition before returning. A clarify prompt ID
// that is not in the manifest fails closed with prompt_unavailable; the
// caller must not emit say or advance the session on that error.
func (client *MatchClient) Match(ctx context.Context, definition decisionv1.Definition, request MatchRequest) (MatchResponse, error) {
	if client == nil {
		return MatchResponse{}, fmt.Errorf("match client is required")
	}
	if err := request.Validate(); err != nil {
		return MatchResponse{}, err
	}
	body, err := json.Marshal(request)
	if err != nil {
		return MatchResponse{}, fmt.Errorf("encode match request: %w", err)
	}
	eventID := MatchEventID(request.SessionID, request.RunID, request.AttemptCount)
	now := time.Now().UTC()
	if client.Now != nil {
		now = client.Now().UTC()
	}
	header := make(http.Header)
	if err := connections.SignDelivery(header, client.Key, eventID, body, now); err != nil {
		return MatchResponse{}, fmt.Errorf("sign match delivery: %w", err)
	}
	path := client.Path
	if strings.TrimSpace(path) == "" {
		path = PathMatch
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	url := strings.TrimRight(client.BaseURL, "/") + path
	httpRequest, err := http.NewRequestWithContext(ctx, Method, url, bytes.NewReader(body))
	if err != nil {
		return MatchResponse{}, fmt.Errorf("match request: %w", err)
	}
	httpRequest.Header = header
	httpRequest.Header.Set("Content-Type", "application/json")
	httpClient := client.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout: 10 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return fmt.Errorf("match callback redirect refused")
			},
		}
	}
	httpResponse, err := httpClient.Do(httpRequest)
	if err != nil {
		return MatchResponse{}, fmt.Errorf("match callback: %w", err)
	}
	defer httpResponse.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(httpResponse.Body, maxMatchResponseBytes+1))
	if err != nil {
		return MatchResponse{}, fmt.Errorf("read match callback: %w", err)
	}
	if len(raw) > maxMatchResponseBytes {
		return MatchResponse{}, fmt.Errorf("match callback exceeded %d bytes", maxMatchResponseBytes)
	}
	if httpResponse.StatusCode != http.StatusOK {
		return MatchResponse{}, fmt.Errorf("match callback status %d", httpResponse.StatusCode)
	}
	response, err := DecodeMatchResponse(raw)
	if err != nil {
		return MatchResponse{}, err
	}
	if err := response.ValidateFor(definition); err != nil {
		return MatchResponse{}, err
	}
	return response, nil
}
