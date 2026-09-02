package temporal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
)

type hostTrigger struct {
	uds *udsHTTPClient
}

func newHostTrigger(socketPath string) *hostTrigger {
	return &hostTrigger{uds: newUDSHTTPClient(socketPath)}
}

func (h *hostTrigger) start(ctx context.Context, req startRequest) (StartHandle, error) {
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
	out, err := h.post(ctx, "/v1/workflows/start", body)
	if err != nil {
		return StartHandle{}, err
	}
	return StartHandle{
		WorkflowID: stringField(out, "workflow_id"),
		RunID:      stringField(out, "run_id"),
	}, nil
}

func (h *hostTrigger) signal(ctx context.Context, req signalRequest) error {
	_, err := h.post(ctx, "/v1/workflows/signal", map[string]any{
		"workflow_id": req.WorkflowID,
		"signal_name": req.SignalName,
		"worker_id":   req.WorkerID,
		"args":        req.Args,
	})
	return err
}

func (h *hostTrigger) close() error {
	if h != nil && h.uds != nil {
		h.uds.close()
	}
	return nil
}

func (h *hostTrigger) post(ctx context.Context, path string, body map[string]any) (map[string]any, error) {
	res, err := h.uds.post(ctx, path, body)
	if err != nil {
		if isTriggerTransportUnavailable(err) {
			return nil, fmt.Errorf("%w: host uds %s: %v", ErrTriggerUnavailable, path, err)
		}
		return nil, fmt.Errorf("temporal trigger: host uds %s: %w", path, err)
	}
	if udsUnavailable(res.Status, res.JSON) {
		return nil, fmt.Errorf("%w: host uds %s unavailable", ErrTriggerUnavailable, path)
	}
	if res.Status < 200 || res.Status >= 300 {
		msg := udsErrorMessage(res.JSON, res.Status)
		return nil, fmt.Errorf("temporal trigger: host uds %s failed: %s", path, msg)
	}
	out, err := parseUDSObject(res.JSON)
	if err != nil {
		return nil, fmt.Errorf("temporal trigger: host uds decode %s: %w", path, err)
	}
	return out, nil
}

func isTriggerTransportUnavailable(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		if sys, ok := opErr.Err.(*os.SyscallError); ok {
			switch sys.Err.Error() {
			case "connect: connection refused", "connect: no such file or directory":
				return true
			}
		}
	}
	if errors.Is(err, net.ErrClosed) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "no such file or directory") ||
		strings.Contains(msg, "connection reset")
}

func udsErrorMessage(raw json.RawMessage, status int) string {
	obj, err := parseUDSObject(raw)
	if err == nil {
		if msg, ok := obj["message"]; ok && fmt.Sprint(msg) != "" {
			return fmt.Sprint(msg)
		}
	}
	if len(raw) > 0 {
		return string(raw)
	}
	return fmt.Sprintf("status %d", status)
}

func stringField(obj map[string]any, key string) string {
	if obj == nil {
		return ""
	}
	value, ok := obj[key]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

// PreferHostUDS reports whether hosted trigger should prefer the host-report UDS.
func PreferHostUDS(opts ClientOptions) bool {
	if opts.PreferHostUDS != nil && *opts.PreferHostUDS {
		return true
	}
	if opts.PreferHostUDS != nil && !*opts.PreferHostUDS {
		return false
	}
	if strings.TrimSpace(opts.HostReportSocket) != "" {
		return true
	}
	if strings.TrimSpace(os.Getenv(EnvHostReportSocket)) != "" {
		return true
	}
	return strings.TrimSpace(os.Getenv(EnvEnvironmentID)) != ""
}
