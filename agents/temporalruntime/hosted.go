package temporalruntime

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

const (
	hostAgentExecutePath   = "/v1/agents/execute"
	hostWorkflowSignalPath = "/v1/workflows/signal"
	hostWorkflowCancelPath = "/v1/workflows/cancel"
)

type hostedAgentClient struct {
	client    *http.Client
	transport *http.Transport
}

type hostedAgentExecuteRequest struct {
	AgentID   string `json:"agent_id"`
	Kind      string `json:"kind"`
	SessionID string `json:"session_id"`
	RunID     string `json:"run_id"`
	WorkerID  string `json:"worker_id,omitempty"`
	Args      any    `json:"args"`
}

type hostedAgentExecuteResponse struct {
	Result json.RawMessage `json:"result"`
}

// NewLazyFromEnv chooses the site-bound host broker in a hosted slot and the
// lazy plaintext client in local development. Hosted web sandboxes never need
// Temporal API keys or mTLS leaf material.
func NewLazyFromEnv(ctx context.Context) (*Dispatcher, error) {
	if strings.TrimSpace(os.Getenv(EnvHostedRuntime)) == "1" {
		return newHostedFromEnv(ctx)
	}
	return NewLazyLocalFromEnv(ctx)
}

func newHostedFromEnv(ctx context.Context) (*Dispatcher, error) {
	if ctx == nil {
		return nil, errors.New("agent Temporal dispatcher context is required")
	}
	environment, err := normalizeEnvironment(os.Getenv(EnvEnvironment))
	if err != nil {
		return nil, err
	}
	socketPath := strings.TrimSpace(os.Getenv(EnvHostReportSocket))
	if socketPath == "" {
		return nil, fmt.Errorf("agent hosted dispatcher: %s is required", EnvHostReportSocket)
	}
	transport := &http.Transport{
		DisableCompression: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", socketPath)
		},
	}
	return &Dispatcher{
		environment: environment,
		hosted: &hostedAgentClient{
			client:    &http.Client{Transport: transport},
			transport: transport,
		},
	}, nil
}

func (client *hostedAgentClient) execute(ctx context.Context, request hostedAgentExecuteRequest) (json.RawMessage, error) {
	var response hostedAgentExecuteResponse
	if err := client.post(ctx, hostAgentExecutePath, request, &response); err != nil {
		return nil, err
	}
	if len(response.Result) == 0 || !json.Valid(response.Result) {
		return nil, errors.New("agent hosted dispatcher returned invalid JSON result")
	}
	return response.Result, nil
}

func (client *hostedAgentClient) signal(ctx context.Context, workflowID, signalName string, argument any) error {
	return client.post(ctx, hostWorkflowSignalPath, map[string]any{
		"workflow_id": workflowID,
		"signal_name": signalName,
		"args":        []any{argument},
	}, nil)
}

func (client *hostedAgentClient) cancel(ctx context.Context, workflowID string) error {
	return client.post(ctx, hostWorkflowCancelPath, map[string]string{"workflow_id": workflowID}, nil)
}

func (client *hostedAgentClient) post(ctx context.Context, path string, input, output any) error {
	if client == nil || client.client == nil {
		return ErrClosed
	}
	body, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("agent hosted dispatcher encode %s: %w", path, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://gobeyond"+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("agent hosted dispatcher request %s: %w", path, err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.client.Do(request)
	if err != nil {
		return fmt.Errorf("agent hosted dispatcher %s: %w", path, err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return fmt.Errorf("agent hosted dispatcher %s returned status %d", path, response.StatusCode)
	}
	if output == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(output); err != nil {
		return fmt.Errorf("agent hosted dispatcher decode %s: %w", path, err)
	}
	return nil
}

func (client *hostedAgentClient) close() {
	if client != nil && client.transport != nil {
		client.transport.CloseIdleConnections()
	}
}
