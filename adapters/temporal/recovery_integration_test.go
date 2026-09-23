package temporal

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

func TestRecoveryRegistrationSurvivesWorkerReplacementAndReplay(t *testing.T) {
	address := os.Getenv("GOBEYOND_TEMPORAL_INTEGRATION_ADDRESS")
	if address == "" {
		t.Skip("requires isolated local Temporal dev server")
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil || (host != "localhost" && host != "127.0.0.1" && host != "::1") {
		t.Fatal("loopback server required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	c, err := client.Dial(client.Options{HostPort: address, Namespace: "default"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	queue := fmt.Sprintf("recovery-proof-%d", time.Now().UnixNano())
	var attempts, business atomic.Int32
	started := make(chan struct{}, 1)
	fn := func(ctx workflow.Context) error {
		ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 5 * time.Second})
		return workflow.ExecuteActivity(ctx, "business").Get(ctx, nil)
	}
	start := func(enabled bool) (worker.Worker, *recoveryTuner) {
		tuner, err := newRecoveryTuner(100)
		if err != nil {
			t.Fatal(err)
		}
		w := worker.New(c, queue, worker.Options{Tuner: tuner, WorkerStopTimeout: time.Second, Interceptors: []interceptor.WorkerInterceptor{&recoveryWorkerInterceptor{enabled: enabled}}})
		w.RegisterWorkflowWithOptions(fn, workflow.RegisterOptions{Name: "recovery-proof"})
		w.RegisterActivityWithOptions(func(_ context.Context, in ReportSorEventInput) error {
			if in.Type == "checkpoint" {
				return nil
			}
			if in.Type != "run" {
				return errors.New("recursive registration")
			}
			if attempts.Add(1) == 1 {
				started <- struct{}{}
			}
			if attempts.Load() < 5 {
				return errors.New("registration unavailable")
			}
			return nil
		}, activity.RegisterOptions{Name: recoveryRegisterName})
		w.RegisterActivityWithOptions(func(context.Context) error { business.Add(1); return nil }, activity.RegisterOptions{Name: "business"})
		if err := w.Start(); err != nil {
			t.Fatal(err)
		}
		return w, tuner
	}
	old, slots := start(true)
	defer old.Stop()
	run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: queue, TaskQueue: queue, WorkflowTaskTimeout: 3 * time.Second}, "recovery-proof")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("registration did not start")
	}
	slots.closeIntake()
	old.Stop()
	if abandoned, safe := slots.accountedAfterStop(true); !safe {
		t.Fatal("unaccounted work after SDK stop")
	} else {
		t.Logf("abandoned workflow tasks retained under independent recovery coverage: %d", abandoned)
	}
	// A replacement with the same enrollment resumes the interrupted first
	// workflow task. Replay below separately verifies persisted enrollment.
	replacement, replacementSlots := start(true)
	defer replacement.Stop()
	if err := run.Get(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if attempts.Load() < 5 || business.Load() != 1 {
		t.Fatalf("attempts=%d business=%d", attempts.Load(), business.Load())
	}
	replacementSlots.closeIntake()
	replacement.Stop()
	if !replacementSlots.drained() {
		t.Fatal("replacement did not drain")
	}
	history := &historypb.History{}
	it := c.GetWorkflowHistory(ctx, queue, run.GetRunID(), false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	for it.HasNext() {
		event, err := it.Next()
		if err != nil {
			t.Fatal(err)
		}
		history.Events = append(history.Events, event)
	}
	replayer, err := worker.NewWorkflowReplayerWithOptions(worker.WorkflowReplayerOptions{Interceptors: []interceptor.WorkerInterceptor{&recoveryWorkerInterceptor{enabled: false}}})
	if err != nil {
		t.Fatal(err)
	}
	replayer.RegisterWorkflowWithOptions(fn, workflow.RegisterOptions{Name: "recovery-proof"})
	if err := replayer.ReplayWorkflowHistory(nil, history); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryLegacyHistoryReplay(t *testing.T) {
	address := os.Getenv("GOBEYOND_TEMPORAL_INTEGRATION_ADDRESS")
	if address == "" {
		t.Skip("requires isolated local Temporal dev server")
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil || (host != "localhost" && host != "127.0.0.1" && host != "::1") {
		t.Fatal("loopback server required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := client.Dial(client.Options{HostPort: address, Namespace: "default"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	queue := fmt.Sprintf("recovery-legacy-%d", time.Now().UnixNano())
	fn := func(ctx workflow.Context) error { return workflow.Sleep(ctx, 10*time.Millisecond) }
	w := worker.New(c, queue, worker.Options{})
	w.RegisterWorkflowWithOptions(fn, workflow.RegisterOptions{Name: "legacy-recovery-proof"})
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	defer w.Stop()
	run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: queue, TaskQueue: queue}, "legacy-recovery-proof")
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Get(ctx, nil); err != nil {
		t.Fatal(err)
	}
	history := &historypb.History{}
	it := c.GetWorkflowHistory(ctx, queue, run.GetRunID(), false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	for it.HasNext() {
		event, err := it.Next()
		if err != nil {
			t.Fatal(err)
		}
		history.Events = append(history.Events, event)
	}
	replayer, err := worker.NewWorkflowReplayerWithOptions(worker.WorkflowReplayerOptions{Interceptors: []interceptor.WorkerInterceptor{&recoveryWorkerInterceptor{enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	replayer.RegisterWorkflowWithOptions(fn, workflow.RegisterOptions{Name: "legacy-recovery-proof"})
	if err := replayer.ReplayWorkflowHistory(nil, history); err != nil {
		t.Fatal(err)
	}
}
