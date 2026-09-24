package temporal

import (
	"context"
	"errors"
	"fmt"
	common "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	service "go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/grpc"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestRecoveryHandoffRetriesBeforeAcknowledgement(t *testing.T) {
	var pending atomic.Int64
	h := newRecoveryTaskHandoff(&pending)
	h.remember(&service.PollWorkflowTaskQueueResponse{TaskToken: []byte("token"), WorkflowExecution: &common.WorkflowExecution{RunId: "run"}})
	h.enqueue(ReportSorEventInput{RunID: "run", Type: "run"})
	tries := 0
	h.post = func(context.Context, ReportSorEventInput) error {
		tries++
		if tries < 2 {
			return errors.New("offline")
		}
		return nil
	}
	called := false
	err := h.intercept(context.Background(), "", &service.RespondWorkflowTaskCompletedRequest{TaskToken: []byte("token")}, &service.RespondWorkflowTaskCompletedResponse{}, nil, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
		called = true
		if pending.Load() != 0 || tries != 2 {
			t.Fatal("acknowledged without coverage")
		}
		return nil
	})
	if err != nil || !called {
		t.Fatal(err, called)
	}
}
func TestRecoveryHandoffFailureKeepsSleepBlocked(t *testing.T) {
	var pending atomic.Int64
	h := newRecoveryTaskHandoff(&pending)
	h.remember(&service.PollWorkflowTaskQueueResponse{TaskToken: []byte("token"), WorkflowExecution: &common.WorkflowExecution{RunId: "run"}})
	h.enqueue(ReportSorEventInput{RunID: "run"})
	h.post = func(context.Context, ReportSorEventInput) error { return errors.New("offline") }
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := h.intercept(ctx, "", &service.RespondWorkflowTaskCompletedRequest{TaskToken: []byte("token")}, &service.RespondWorkflowTaskCompletedResponse{}, nil, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
		t.Fatal("unsafe acknowledgement")
		return nil
	})
	if !errors.Is(err, context.DeadlineExceeded) || pending.Load() != 1 {
		t.Fatal(err, pending.Load())
	}
}
func TestRecoveryHandoffWorkflowHasNoRegistrationActivities(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	h := newRecoveryTaskHandoff(nil)
	env.SetWorkerOptions(worker.Options{Interceptors: []interceptor.WorkerInterceptor{&recoveryWorkerInterceptor{enabled: true, handoff: h}}})
	// No registration activity is registered: scheduling one would fail this test.
	env.ExecuteWorkflow(func(ctx workflow.Context) error { return workflow.Sleep(ctx, time.Second) })
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, batch := range h.pending {
		for _, in := range batch {
			kinds[in.Type] = true
		}
	}
	if !kinds["run"] || !kinds["timer.started"] {
		t.Fatal(kinds)
	}
}

func TestRecoveryHandoffRealTemporal(t *testing.T) {
	address := os.Getenv("GOBEYOND_TEMPORAL_INTEGRATION_ADDRESS")
	if address == "" {
		t.Skip("requires isolated local Temporal")
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil || (host != "127.0.0.1" && host != "localhost") {
		t.Fatal("loopback required")
	}
	var pending atomic.Int64
	h := newRecoveryTaskHandoff(&pending)
	var attempts atomic.Int32
	h.post = func(context.Context, ReportSorEventInput) error {
		if attempts.Add(1) == 1 {
			return errors.New("transient")
		}
		return nil
	}
	c, err := client.Dial(client.Options{HostPort: address, Namespace: "default", ConnectionOptions: client.ConnectionOptions{DialOptions: []grpc.DialOption{grpc.WithChainUnaryInterceptor(h.intercept)}}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	queue := fmt.Sprintf("handoff-%d", time.Now().UnixNano())
	fn := func(ctx workflow.Context) error {
		ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Second})
		if err := workflow.Sleep(ctx, time.Second); err != nil {
			return err
		}
		return workflow.ExecuteActivity(ctx, "business").Get(ctx, nil)
	}
	w := worker.New(c, queue, worker.Options{Interceptors: []interceptor.WorkerInterceptor{&recoveryWorkerInterceptor{enabled: true, handoff: h}}})
	w.RegisterWorkflowWithOptions(fn, workflow.RegisterOptions{Name: "handoff"})
	w.RegisterActivityWithOptions(func(context.Context) error { return nil }, activity.RegisterOptions{Name: "business"})
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: queue, TaskQueue: queue}, "handoff")
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Get(ctx, nil); err != nil {
		t.Fatal(err)
	}
	history := &historypb.History{}
	it := c.GetWorkflowHistory(ctx, queue, run.GetRunID(), false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	for it.HasNext() {
		e, err := it.Next()
		if err != nil {
			t.Fatal(err)
		}
		history.Events = append(history.Events, e)
		if a := e.GetMarkerRecordedEventAttributes(); a != nil && a.MarkerName == "LocalActivity" {
			t.Fatal("unexpected local activity marker")
		}
	}
	if pending.Load() != 0 || attempts.Load() < 4 {
		t.Fatal(pending.Load(), attempts.Load())
	}
	replayer, err := worker.NewWorkflowReplayerWithOptions(worker.WorkflowReplayerOptions{Interceptors: []interceptor.WorkerInterceptor{&recoveryWorkerInterceptor{enabled: true, handoff: newRecoveryTaskHandoff(nil)}}})
	if err != nil {
		t.Fatal(err)
	}
	replayer.RegisterWorkflowWithOptions(fn, workflow.RegisterOptions{Name: "handoff"})
	if err := replayer.ReplayWorkflowHistory(nil, history); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryHandoffReplayDeduplicatesPending(t *testing.T) {
	var pending atomic.Int64
	h := newRecoveryTaskHandoff(&pending)
	in := ReportSorEventInput{RunID: "run", Type: "checkpoint", DedupeKey: "checkpoint:1", Payload: map[string]string{"activity_queue": "worker"}}
	for i := 0; i < 100; i++ {
		h.enqueue(in)
	}
	if pending.Load() != 1 || len(h.pending["run"]) != 1 {
		t.Fatal("replay grew pending registrations")
	}
}
