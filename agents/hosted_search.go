package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// HostedWebSearchPath is the slot-private host-report endpoint used by
// customer tools. The host owns Google credentials and tenant binding.
const (
	HostedWebSearchPath       = "/v1/web-search"
	defaultHostedSearchSocket = "/run/gobeyond/host/host-report.sock"
)

// HostedWebSearchRequest is the provider-neutral Google grounding request.
// NetworkID is context/attribution only; the host-report socket identity is
// authoritative for the tenant.
type HostedWebSearchRequest struct {
	NetworkID  string `json:"network_id,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	Query      string `json:"query"`
}

// HostedWebSearchResponse is deliberately explicit about grounding state so
// a failed provider call cannot be mistaken for a successful search.
type HostedWebSearchResponse struct {
	Answer      string   `json:"answer,omitempty"`
	Sources     []string `json:"sources,omitempty"`
	Searched    bool     `json:"searched"`
	Stub        bool     `json:"stub"`
	ReasonCode  string   `json:"reason_code,omitempty"`
	SearchModel string   `json:"search_model,omitempty"`
	Provider    string   `json:"provider,omitempty"`
}

// HostedWebSearch calls the slot-private host grounding endpoint. It returns
// transport errors to the caller so application-level search code can convert
// them into its own fail-closed result shape.
func HostedWebSearch(ctx context.Context, request HostedWebSearchRequest) (HostedWebSearchResponse, error) {
	var response HostedWebSearchResponse
	if ctx == nil {
		return response, errors.New("hosted web search context is required")
	}
	socketPath := strings.TrimSpace(os.Getenv(EnvHostReportSocket))
	if socketPath == "" {
		socketPath = defaultHostedSearchSocket
	}
	body, err := json.Marshal(request)
	if err != nil {
		return response, fmt.Errorf("hosted web search encode: %w", err)
	}
	transport := &http.Transport{
		DisableCompression: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", socketPath)
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://gobeyond"+HostedWebSearchPath, bytes.NewReader(body))
	if err != nil {
		return response, fmt.Errorf("hosted web search request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return response, fmt.Errorf("hosted web search transport: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode < http.StatusOK || res.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
		return response, fmt.Errorf("hosted web search returned status %d", res.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&response); err != nil {
		return response, fmt.Errorf("hosted web search decode: %w", err)
	}
	return response, nil
}
