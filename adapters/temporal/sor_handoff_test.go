package temporal

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	common "go.temporal.io/api/common/v1"
	service "go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/grpc"
)

func TestSorHandoffFlushesBeforeAcknowledgement(t *testing.T) {
	h := newSorTaskHandoff()
	h.remember(&service.PollWorkflowTaskQueueResponse{
		TaskToken:         []byte("token"),
		WorkflowExecution: &common.WorkflowExecution{RunId: "run"},
	})
	h.enqueue(ReportSorEventInput{RunID: "run", Type: "timer.started", DedupeKey: "d1"})
	h.enqueue(ReportSorEventInput{RunID: "run", Type: "timer.started", DedupeKey: "d1"}) // dedupe

	var posts atomic.Int32
	h.post = func(_ context.Context, in ReportSorEventInput) error {
		posts.Add(1)
		if in.Type != "timer.started" || in.DedupeKey != "d1" {
			t.Fatalf("unexpected post %#v", in)
		}
		return nil
	}
	acked := false
	err := h.intercept(context.Background(), "",
		&service.RespondWorkflowTaskCompletedRequest{TaskToken: []byte("token")},
		&service.RespondWorkflowTaskCompletedResponse{},
		nil,
		func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
			acked = true
			if posts.Load() != 1 {
				t.Fatalf("acked before flush posts=%d", posts.Load())
			}
			return nil
		},
	)
	if err != nil || !acked || posts.Load() != 1 {
		t.Fatalf("err=%v acked=%v posts=%d", err, acked, posts.Load())
	}
}

func TestSorHandoffBestEffortAllowsAckOnPostFailure(t *testing.T) {
	h := newSorTaskHandoff()
	h.remember(&service.PollWorkflowTaskQueueResponse{
		TaskToken:         []byte("token"),
		WorkflowExecution: &common.WorkflowExecution{RunId: "run"},
	})
	h.enqueue(ReportSorEventInput{RunID: "run", Type: "timer.started", DedupeKey: "d1"})
	h.post = func(context.Context, ReportSorEventInput) error { return errors.New("offline") }
	acked := false
	err := h.intercept(context.Background(), "",
		&service.RespondWorkflowTaskCompletedRequest{TaskToken: []byte("token")},
		&service.RespondWorkflowTaskCompletedResponse{},
		nil,
		func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
			acked = true
			return nil
		},
	)
	if err != nil || !acked {
		t.Fatalf("SoR must not withhold ack: err=%v acked=%v", err, acked)
	}
}

func TestSorHandoffWorkflowHasNoReportSorEventLocalActivities(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	h := newSorTaskHandoff()
	env.SetWorkerOptions(worker.Options{
		Interceptors: []interceptor.WorkerInterceptor{&sorWorkerInterceptor{handoff: h}},
	})
	// Do not register ReportSorEvent: scheduling an LA would fail this test.
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		return workflow.Sleep(ctx, time.Second)
	})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, batch := range h.pending {
		for _, in := range batch {
			kinds[in.Type] = true
		}
	}
	// Test environment does not drive the gRPC complete interceptor, so stamps
	// remain queued. That proves they were enqueued without an LA.
	if !kinds["timer.started"] && !kinds["timer.fired"] && !kinds["workflow.completed"] {
		t.Fatalf("expected SoR stamps queued, got %v", kinds)
	}
}
