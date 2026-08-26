package temporal

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

func TestShouldReportTimer(t *testing.T) {
	if shouldReportTimer(0) || shouldReportTimer(-time.Second) {
		t.Fatal("non-positive duration must skip SoR")
	}
	if !shouldReportTimer(time.Millisecond) {
		t.Fatal("positive duration must report")
	}
}

func TestFireAtFromDuration(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	d := 5 * time.Minute
	got := now.Add(d).UTC().Format(time.RFC3339)
	want := "2026-07-31T12:05:00Z"
	if got != want {
		t.Fatalf("fire_at=%q want %q", got, want)
	}
}

func shouldReportTimer(d time.Duration) bool {
	return d > 0
}

func TestStampFromRetryPolicyDefaults(t *testing.T) {
	got := stampFromRetryPolicy(nil)
	if got.InitialIntervalMS != 1000 || got.BackoffCoefficient != 2.0 || got.MaximumAttempts != 0 {
		t.Fatalf("defaults=%+v", got)
	}
	got = stampFromRetryPolicy(&temporal.RetryPolicy{
		InitialInterval:    2 * time.Minute,
		BackoffCoefficient: 1.5,
		MaximumInterval:    10 * time.Minute,
		MaximumAttempts:    5,
	})
	if got.InitialIntervalMS != 120000 || got.MaximumAttempts != 5 {
		t.Fatalf("custom=%+v", got)
	}
}

func TestIsNonRetryableActivityErr(t *testing.T) {
	if isNonRetryableActivityErr(temporal.NewApplicationError("x", "t")) {
		t.Fatal("retryable ApplicationError must not mark non_retryable")
	}
	if !isNonRetryableActivityErr(temporal.NewNonRetryableApplicationError("x", "t", nil)) {
		t.Fatal("NonRetryableApplicationError must mark non_retryable")
	}
}

func TestWorkflowInterceptorReportsStartAndTerminalEvents(t *testing.T) {
	testSorWorkflowEvents(t, func(workflow.Context) error { return nil }, "workflow.completed", "COMPLETED")
}

func TestWorkflowInterceptorReportsFailedTerminalEvent(t *testing.T) {
	testSorWorkflowEvents(t, func(workflow.Context) error { return errors.New("boom") }, "workflow.failed", "FAILED")
}

func testSorWorkflowEvents(t *testing.T, workflowFn interface{}, terminalType, terminalStatus string) {
	t.Helper()
	t.Setenv(envHostReportSocket, t.TempDir()+"/missing.sock")
	t.Setenv(envEnvironmentID, "env-1")
	t.Setenv(envWorkerID, "worker-1")
	events := make(chan ReportSorEventInput, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var event ReportSorEventInput
		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		events <- event
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	t.Setenv(envAPIURL, server.URL)

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.SetWorkerOptions(worker.Options{Interceptors: []interceptor.WorkerInterceptor{&sorWorkerInterceptor{}}})
	env.RegisterActivity(ReportSorEvent)
	env.ExecuteWorkflow(workflowFn)
	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if terminalErr := env.GetWorkflowError(); terminalType == "workflow.completed" && terminalErr != nil {
		t.Fatalf("workflow error = %v", terminalErr)
	}

	got := make(map[string]ReportSorEventInput, 2)
	for len(got) < 2 {
		select {
		case event := <-events:
			got[event.Type] = event
		default:
			t.Fatalf("reported events = %#v", got)
		}
	}
	started, ok := got["workflow.started"]
	if !ok || started.Status != "RUNNING" || started.RunID == "" {
		t.Fatalf("workflow.started = %#v", started)
	}
	terminal, ok := got[terminalType]
	if !ok || terminal.Status != terminalStatus || terminal.RunID != started.RunID {
		t.Fatalf("%s = %#v, started = %#v", terminalType, terminal, started)
	}
}
