package temporal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

type apiTrigger struct {
	baseURL       string
	authorization string
	internalToken string
	client        *http.Client
}

func newAPITrigger(opts ClientOptions) (*apiTrigger, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(opts.APIBaseURL), "/")
	if baseURL == "" {
		baseURL = strings.TrimRight(strings.TrimSpace(getenv(EnvAPIURL, "")), "/")
	}
	if baseURL == "" {
		return nil, fmt.Errorf("temporal trigger: api base URL is required")
	}
	auth := strings.TrimSpace(opts.Authorization)
	if auth == "" {
		auth = strings.TrimSpace(os.Getenv(EnvAPIAuthorization))
	}
	token := strings.TrimSpace(opts.InternalToken)
	if token == "" {
		token = strings.TrimSpace(os.Getenv(EnvInternalAPIToken))
	}
	return &apiTrigger{
		baseURL:       baseURL,
		authorization: auth,
		internalToken: token,
		client:        opts.HTTPClient,
	}, nil
}

func (a *apiTrigger) httpClient() *http.Client {
	if a != nil && a.client != nil {
		return a.client
	}
	return http.DefaultClient
}

func (a *apiTrigger) start(ctx context.Context, req startRequest) (StartHandle, error) {
	body := map[string]any{
		"workflow_name": req.WorkflowName,
		"worker_id":     req.WorkerID,
		"args":          req.Args,
	}
	if id := strings.TrimSpace(req.WorkflowID); id != "" {
		body["workflow_id"] = id
	}
	if key := strings.TrimSpace(req.IdempotencyKey); key != "" {
		body["idempotency_key"] = key
	}
	out, err := a.post(ctx, "/internal/workflows/start", body)
	if err != nil {
		return StartHandle{}, err
	}
	return StartHandle{
		WorkflowID: stringField(out, "workflow_id"),
		RunID:      stringField(out, "run_id"),
	}, nil
}

func (a *apiTrigger) signal(ctx context.Context, req signalRequest) error {
	_, err := a.post(ctx, "/internal/workflows/signal", map[string]any{
		"workflow_id": req.WorkflowID,
		"signal_name": req.SignalName,
		"worker_id":   req.WorkerID,
		"args":        req.Args,
	})
	return err
}

func (a *apiTrigger) close() error {
	return nil
}

func (a *apiTrigger) post(ctx context.Context, path string, body map[string]any) (map[string]any, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("temporal trigger: encode %s: %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("temporal trigger: request %s: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if a.authorization != "" {
		req.Header.Set("Authorization", a.authorization)
	}
	if a.internalToken != "" {
		req.Header.Set("x-gobeyond-internal-token", a.internalToken)
	}
	res, err := a.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("temporal trigger: api %s: %w", path, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("temporal trigger: read %s: %w", path, err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		msg := string(raw)
		var parsed map[string]any
		if json.Unmarshal(raw, &parsed) == nil {
			if m, ok := parsed["message"]; ok && fmt.Sprint(m) != "" {
				msg = fmt.Sprint(m)
			}
		}
		if msg == "" {
			msg = res.Status
		}
		return nil, fmt.Errorf("temporal trigger: api %s failed (%d): %s", path, res.StatusCode, msg)
	}
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("temporal trigger: decode %s: %w", path, err)
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}
