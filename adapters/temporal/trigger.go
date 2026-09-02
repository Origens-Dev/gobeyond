// Package temporal implements GoBeyond Temporal adapters.
//
// Worker lifecycle (Serve) polls one resolved task queue in a worker binary.
// TriggerClient starts or signals workflows from application code (Go actions,
// services, or other server-side callers). Browser bundles must not dial
// Temporal directly; post into a Go handler that uses TriggerClient.
//
// Trigger modes (GOBEYOND_TEMPORAL_MODE):
//
//   - local — dial GOBEYOND_TEMPORAL_ADDRESS and ExecuteWorkflow on
//     {workerId}__{environment} (default environment: local).
//   - preview / hosted — prefer host-report UDS (GOBEYOND_HOST_REPORT_SOCKET)
//     and fall back to GOBEYOND_API_URL when UDS is unavailable.
//
// When GOBEYOND_TEMPORAL_MODE is unset, hosted UDS is preferred when
// GOBEYOND_HOST_REPORT_SOCKET or GOBEYOND_ENVIRONMENT_ID is set; otherwise
// local mode is used.
//
// See docs/guides/workflow-triggers-go.md for author guidance.
package temporal

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	gb "github.com/Origens-Dev/gobeyond"
)

const (
	EnvTemporalMode        = "GOBEYOND_TEMPORAL_MODE"
	EnvTemporalEnvironment = "GOBEYOND_TEMPORAL_ENVIRONMENT"
	EnvHostReportSocket    = "GOBEYOND_HOST_REPORT_SOCKET"
	EnvEnvironmentID       = "GOBEYOND_ENVIRONMENT_ID"
	EnvAPIURL              = "GOBEYOND_API_URL"
	EnvAPIAuthorization    = "GOBEYOND_API_AUTHORIZATION"
	EnvInternalAPIToken    = "GOBEYOND_INTERNAL_API_TOKEN"

	defaultTemporalAddress   = "localhost:7233"
	defaultTemporalNamespace = "default"
)

// ErrTriggerUnavailable means the host UDS trigger path is unavailable and an
// API fallback may be attempted when GOBEYOND_API_URL is configured.
var ErrTriggerUnavailable = errors.New("temporal trigger unavailable")

// Mode selects the trigger transport.
type Mode string

const (
	ModeLocal   Mode = "local"
	ModePreview Mode = "preview"
	ModeHosted  Mode = "hosted"
)

// StartOptions configures a workflow start. TaskQueue is local-only; preview
// and hosted modes derive queue, namespace, and environment on the server.
type StartOptions struct {
	WorkflowName   string
	Args           []any
	WorkflowID     string
	TaskQueue      string
	WorkerID       string
	IdempotencyKey string
}

// SignalOptions configures a workflow signal.
type SignalOptions struct {
	WorkflowID string
	SignalName string
	Args       []any
	WorkerID   string
}

// StartHandle identifies a started workflow execution.
type StartHandle struct {
	WorkflowID string
	RunID      string
}

// TriggerClient starts and signals workflows without exposing Temporal admin
// credentials to web sandboxes.
type TriggerClient struct {
	mode          Mode
	defaultWorker string
	local         *localTrigger
	host          triggerBackend
	api           triggerBackend
}

// ClientOptions configures NewClient / NewClientFromEnv.
type ClientOptions struct {
	Mode             Mode
	Endpoint         string
	Namespace        string
	WorkerID         string
	Environment      string
	APIBaseURL       string
	Authorization    string
	InternalToken    string
	HostReportSocket string
	PreferHostUDS    *bool
	HTTPClient       *http.Client
	LocalDial        func() (localTemporalBridge, error)
	// Host and API inject trigger transports for unit tests.
	Host triggerBackend
	API  triggerBackend
}

type triggerBackend interface {
	start(ctx context.Context, req startRequest) (StartHandle, error)
	signal(ctx context.Context, req signalRequest) error
	close() error
}

type startRequest struct {
	WorkflowName   string
	WorkflowID     string
	WorkerID       string
	Args           []any
	TaskQueue      string
	IdempotencyKey string
}

type signalRequest struct {
	WorkflowID string
	SignalName string
	WorkerID   string
	Args       []any
}

// NewClientFromEnv builds a TriggerClient from process environment variables.
func NewClientFromEnv(opts ClientOptions) (*TriggerClient, error) {
	return NewClient(opts)
}

// NewClient builds a TriggerClient. Connections are established lazily on the
// first Start or Signal call.
func NewClient(opts ClientOptions) (*TriggerClient, error) {
	mode, err := resolveMode(opts)
	if err != nil {
		return nil, err
	}
	client := &TriggerClient{
		mode:          mode,
		defaultWorker: strings.TrimSpace(opts.WorkerID),
	}
	switch mode {
	case ModeLocal:
		client.local = newLocalTrigger(opts)
		return client, nil
	default:
		return newHostedClient(client, opts)
	}
}

func newHostedClient(client *TriggerClient, opts ClientOptions) (*TriggerClient, error) {
	useHost := PreferHostUDS(opts)
	if opts.Host != nil {
		client.host = opts.Host
	} else if useHost {
		socket := strings.TrimSpace(opts.HostReportSocket)
		if socket == "" {
			socket = getenv(EnvHostReportSocket, defaultHostReportSocket)
		}
		client.host = newHostTrigger(socket)
	}
	if opts.API != nil {
		client.api = opts.API
	} else if strings.TrimSpace(opts.APIBaseURL) != "" || strings.TrimSpace(os.Getenv(EnvAPIURL)) != "" {
		api, err := newAPITrigger(opts)
		if err != nil {
			return nil, err
		}
		client.api = api
	}
	if client.host == nil && client.api == nil {
		return nil, fmt.Errorf("temporal trigger: mode=%s requires host UDS, api base URL, or GOBEYOND_API_URL", client.mode)
	}
	return client, nil
}

// Start begins a workflow execution.
func (c *TriggerClient) Start(ctx context.Context, opts StartOptions) (StartHandle, error) {
	if c == nil {
		return StartHandle{}, errors.New("temporal trigger: nil client")
	}
	req := startRequest{
		WorkflowName:   strings.TrimSpace(opts.WorkflowName),
		WorkflowID:     opts.WorkflowID,
		WorkerID:       strings.TrimSpace(opts.WorkerID),
		Args:           opts.Args,
		TaskQueue:      strings.TrimSpace(opts.TaskQueue),
		IdempotencyKey: strings.TrimSpace(opts.IdempotencyKey),
	}
	if req.WorkflowName == "" {
		return StartHandle{}, errors.New("temporal trigger: workflow_name is required")
	}
	if req.WorkerID == "" {
		req.WorkerID = c.defaultWorker
	}
	if req.Args == nil {
		req.Args = []any{}
	}
	switch c.mode {
	case ModeLocal:
		return c.local.start(ctx, req)
	default:
		if req.TaskQueue != "" {
			return StartHandle{}, errors.New("temporal trigger: task_queue must not be supplied in preview/hosted (server-derived)")
		}
		return c.withHostOrAPI(func(backend triggerBackend) (StartHandle, error) {
			return backend.start(ctx, req)
		})
	}
}

// Signal sends a named signal to a running workflow.
func (c *TriggerClient) Signal(ctx context.Context, opts SignalOptions) error {
	if c == nil {
		return errors.New("temporal trigger: nil client")
	}
	req := signalRequest{
		WorkflowID: strings.TrimSpace(opts.WorkflowID),
		SignalName: strings.TrimSpace(opts.SignalName),
		WorkerID:   strings.TrimSpace(opts.WorkerID),
		Args:       opts.Args,
	}
	if req.WorkflowID == "" {
		return errors.New("temporal trigger: workflow_id is required")
	}
	if req.SignalName == "" {
		return errors.New("temporal trigger: signal_name is required")
	}
	if req.WorkerID == "" {
		req.WorkerID = c.defaultWorker
	}
	if req.Args == nil {
		req.Args = []any{}
	}
	switch c.mode {
	case ModeLocal:
		return c.local.signal(ctx, req)
	default:
		return c.withHostOrAPIVoid(func(backend triggerBackend) error {
			return backend.signal(ctx, req)
		})
	}
}

// Wait blocks until a locally started workflow completes.
func Wait(ctx context.Context, client *TriggerClient, handle StartHandle, result any) error {
	if client == nil {
		return errors.New("temporal trigger: nil client")
	}
	if client.mode != ModeLocal || client.local == nil {
		return fmt.Errorf("temporal trigger: Wait is only supported in local mode")
	}
	run, err := client.local.getRun(ctx, handle.WorkflowID, handle.RunID)
	if err != nil {
		return err
	}
	return run.Get(ctx, result)
}

// Close releases trigger resources.
func (c *TriggerClient) Close() error {
	if c == nil {
		return nil
	}
	var first error
	if c.local != nil {
		if err := c.local.close(); err != nil && first == nil {
			first = err
		}
	}
	if c.host != nil {
		if err := c.host.close(); err != nil && first == nil {
			first = err
		}
	}
	if c.api != nil {
		if err := c.api.close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (c *TriggerClient) withHostOrAPI(op func(triggerBackend) (StartHandle, error)) (StartHandle, error) {
	if c.host != nil {
		out, err := op(c.host)
		if err == nil {
			return out, nil
		}
		if errors.Is(err, ErrTriggerUnavailable) && c.api != nil {
			return op(c.api)
		}
		return StartHandle{}, err
	}
	if c.api == nil {
		return StartHandle{}, fmt.Errorf("temporal trigger: mode=%s requires host UDS or GOBEYOND_API_URL", c.mode)
	}
	return op(c.api)
}

func (c *TriggerClient) withHostOrAPIVoid(op func(triggerBackend) error) error {
	if c.host != nil {
		err := op(c.host)
		if err == nil {
			return nil
		}
		if errors.Is(err, ErrTriggerUnavailable) && c.api != nil {
			return op(c.api)
		}
		return err
	}
	if c.api == nil {
		return fmt.Errorf("temporal trigger: mode=%s requires host UDS or GOBEYOND_API_URL", c.mode)
	}
	return op(c.api)
}

func resolveMode(opts ClientOptions) (Mode, error) {
	if opts.Mode != "" {
		return normalizeMode(string(opts.Mode))
	}
	if raw := strings.TrimSpace(os.Getenv(EnvTemporalMode)); raw != "" {
		return normalizeMode(raw)
	}
	if PreferHostUDS(opts) {
		env := strings.TrimSpace(os.Getenv(EnvTemporalEnvironment))
		if env == gb.PreviewEnvironment {
			return ModePreview, nil
		}
		return ModeHosted, nil
	}
	return ModeLocal, nil
}

func normalizeMode(raw string) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(ModeLocal):
		return ModeLocal, nil
	case string(ModePreview):
		return ModePreview, nil
	case string(ModeHosted):
		return ModeHosted, nil
	default:
		return "", fmt.Errorf("temporal trigger: unknown mode %q", raw)
	}
}

func getenv(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
