package temporal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	gb "github.com/Origens-Dev/gobeyond"
	"go.temporal.io/sdk/client"
)

type fakeBackend struct {
	startFn  func(context.Context, startRequest) (StartHandle, error)
	signalFn func(context.Context, signalRequest) error
	calls    []string
}

func (f *fakeBackend) start(ctx context.Context, req startRequest) (StartHandle, error) {
	f.calls = append(f.calls, "start")
	if f.startFn != nil {
		return f.startFn(ctx, req)
	}
	return StartHandle{WorkflowID: "wf-1", RunID: "run-1"}, nil
}

func (f *fakeBackend) signal(ctx context.Context, req signalRequest) error {
	f.calls = append(f.calls, "signal")
	if f.signalFn != nil {
		return f.signalFn(ctx, req)
	}
	return nil
}

func (f *fakeBackend) close() error { return nil }

type fakeLocalBridge struct {
	started []startRequest
	signals []signalRequest
}

func (f *fakeLocalBridge) start(ctx context.Context, workflowType, taskQueue, workflowID string, args []any) (StartHandle, error) {
	f.started = append(f.started, startRequest{
		WorkflowName: workflowType,
		TaskQueue:    taskQueue,
		WorkflowID:   workflowID,
		Args:         args,
	})
	return StartHandle{WorkflowID: workflowID, RunID: "run-local"}, nil
}

func (f *fakeLocalBridge) signal(ctx context.Context, workflowID, signalName string, args []any) error {
	f.signals = append(f.signals, signalRequest{
		WorkflowID: workflowID,
		SignalName: signalName,
		Args:       args,
	})
	return nil
}

func (f *fakeLocalBridge) getRun(ctx context.Context, workflowID, runID string) (client.WorkflowRun, error) {
	return nil, errors.New("not implemented")
}

func (f *fakeLocalBridge) close() error { return nil }

func withEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for key, value := range kv {
		t.Setenv(key, value)
	}
	for _, key := range []string{
		EnvTemporalMode,
		EnvHostReportSocket,
		EnvEnvironmentID,
		EnvAPIURL,
		EnvTemporalEnvironment,
	} {
		if _, ok := kv[key]; !ok {
			t.Setenv(key, "")
		}
	}
}

func TestResolveModeDefaultsLocal(t *testing.T) {
	withEnv(t, map[string]string{})
	mode, err := resolveMode(ClientOptions{})
	if err != nil || mode != ModeLocal {
		t.Fatalf("mode=%q err=%v", mode, err)
	}
}

func TestResolveModePrefersHostedWhenEnvironmentIDSet(t *testing.T) {
	withEnv(t, map[string]string{EnvEnvironmentID: "env-123"})
	mode, err := resolveMode(ClientOptions{})
	if err != nil || mode != ModeHosted {
		t.Fatalf("mode=%q err=%v", mode, err)
	}
}

func TestResolveModePrefersPreviewWhenEnvironmentIDAndPreviewEnv(t *testing.T) {
	withEnv(t, map[string]string{
		EnvEnvironmentID:       "env-123",
		EnvTemporalEnvironment: gb.PreviewEnvironment,
	})
	mode, err := resolveMode(ClientOptions{})
	if err != nil || mode != ModePreview {
		t.Fatalf("mode=%q err=%v", mode, err)
	}
}

func TestPreferHostUDS(t *testing.T) {
	withEnv(t, map[string]string{})
	if PreferHostUDS(ClientOptions{}) {
		t.Fatal("expected false without signals")
	}
	withEnv(t, map[string]string{EnvHostReportSocket: "/tmp/s.sock"})
	if !PreferHostUDS(ClientOptions{}) {
		t.Fatal("expected true with socket env")
	}
}

func TestHostedStartFallsBackToAPI(t *testing.T) {
	host := &fakeBackend{
		startFn: func(ctx context.Context, req startRequest) (StartHandle, error) {
			return StartHandle{}, fmt.Errorf("%w: simulated", ErrTriggerUnavailable)
		},
	}
	api := &fakeBackend{
		startFn: func(ctx context.Context, req startRequest) (StartHandle, error) {
			if req.WorkflowName != "orders.checkout" || req.WorkerID != "orders" {
				t.Fatalf("unexpected req: %+v", req)
			}
			return StartHandle{WorkflowID: "api-wf"}, nil
		},
	}
	client, err := NewClient(ClientOptions{
		Mode: ModeHosted,
		Host: host,
		API:  api,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	handle, err := client.Start(context.Background(), StartOptions{
		WorkflowName: "orders.checkout",
		WorkerID:     "orders",
		Args:         []any{"x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if handle.WorkflowID != "api-wf" {
		t.Fatalf("handle=%+v", handle)
	}
}

func TestHostedRejectsTaskQueue(t *testing.T) {
	client, err := NewClient(ClientOptions{
		Mode: ModeHosted,
		Host: &fakeBackend{},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Start(context.Background(), StartOptions{
		WorkflowName: "demo",
		TaskQueue:    "default__local",
	})
	if err == nil || !strings.Contains(err.Error(), "task_queue") {
		t.Fatalf("err=%v", err)
	}
}

func TestLocalStartUsesTaskQueueName(t *testing.T) {
	bridge := &fakeLocalBridge{}
	client, err := NewClient(ClientOptions{
		Mode: ModeLocal,
		LocalDial: func() (localTemporalBridge, error) {
			return bridge, nil
		},
		WorkerID:    "default",
		Environment: gb.LocalEnvironment,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	_, err = client.Start(context.Background(), StartOptions{
		WorkflowName: "default.demo",
		Args:         []any{"hello"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bridge.started) != 1 {
		t.Fatalf("started=%+v", bridge.started)
	}
	wantQueue, _ := gb.TaskQueueName(gb.DefaultTaskQueueID, gb.LocalEnvironment)
	if bridge.started[0].TaskQueue != wantQueue {
		t.Fatalf("queue=%q want %q", bridge.started[0].TaskQueue, wantQueue)
	}
	if bridge.started[0].WorkflowID == "" {
		t.Fatal("expected minted workflow id")
	}
}

func TestLocalSignalRequiresWorkflowID(t *testing.T) {
	client, err := NewClient(ClientOptions{
		Mode: ModeLocal,
		LocalDial: func() (localTemporalBridge, error) {
			return &fakeLocalBridge{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = client.Signal(context.Background(), SignalOptions{SignalName: "tick"})
	if err == nil || !strings.Contains(err.Error(), "workflow_id") {
		t.Fatalf("err=%v", err)
	}
}

func TestHostTriggerStartUnavailable503(t *testing.T) {
	host := &hostTrigger{
		uds: &udsHTTPClient{
			client: &http.Client{
				Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: http.StatusServiceUnavailable,
						Body:       io.NopCloser(strings.NewReader(`{"fallback":"api"}`)),
						Header:     make(http.Header),
					}, nil
				}),
			},
		},
	}
	_, err := host.start(context.Background(), startRequest{WorkflowName: "demo"})
	if err == nil || !errors.Is(err, ErrTriggerUnavailable) {
		t.Fatalf("err=%v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestAPITriggerStart(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/workflows/start" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test" {
			t.Fatalf("auth=%q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("x-gobeyond-internal-token") != "secret" {
			t.Fatalf("token=%q", r.Header.Get("x-gobeyond-internal-token"))
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"workflow_id": "wf-api",
			"run_id":      "run-api",
		})
	}))
	t.Cleanup(srv.Close)

	api, err := newAPITrigger(ClientOptions{
		APIBaseURL:      srv.URL,
		Authorization:   "Bearer test",
		InternalToken:   "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	handle, err := api.start(context.Background(), startRequest{
		WorkflowName: "default.demo",
		WorkerID:     "default",
		Args:         []any{1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if handle.WorkflowID != "wf-api" {
		t.Fatalf("handle=%+v", handle)
	}
	if got["environment_id"] != nil || got["namespace"] != nil || got["task_queue"] != nil {
		t.Fatalf("must not send server-derived fields: %+v", got)
	}
}

func TestUDSUnavailableDetection(t *testing.T) {
	if !udsUnavailable(http.StatusServiceUnavailable, json.RawMessage(`{"fallback":"api"}`)) {
		t.Fatal("503 with fallback=api should be unavailable")
	}
	if udsUnavailable(http.StatusOK, json.RawMessage(`{}`)) {
		t.Fatal("200 should not be unavailable")
	}
}

func TestIsTriggerTransportUnavailable(t *testing.T) {
	if !isTriggerTransportUnavailable(&net.OpError{Err: &os.SyscallError{Err: errors.New("connect: connection refused")}}) {
		t.Fatal("expected connection refused")
	}
}
