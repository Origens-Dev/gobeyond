package temporal

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

func TestRecoveryRegistrationRetriesBeforeBusinessCode(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.SetWorkerOptions(worker.Options{Interceptors: []interceptor.WorkerInterceptor{&recoveryWorkerInterceptor{enabled: true}}})
	var tries atomic.Int32
	var ran atomic.Bool
	env.RegisterActivityWithOptions(func(_ context.Context, in ReportSorEventInput) error {
		if in.Type != "run" {
			t.Error("registration retry recursively registered a timer")
		}
		if ran.Load() {
			t.Error("business code ran before registration")
		}
		if tries.Add(1) < 3 {
			return errors.New("persistence unavailable")
		}
		return nil
	}, activity.RegisterOptions{Name: recoveryRegisterName})
	env.ExecuteWorkflow(func(workflow.Context) error { ran.Store(true); return nil })
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if !ran.Load() || tries.Load() != 3 {
		t.Fatal("registration did not fence business code", tries.Load())
	}
}
func TestRecoveryRegistrationCancellationDoesNotSpin(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.SetWorkerOptions(worker.Options{Interceptors: []interceptor.WorkerInterceptor{&recoveryWorkerInterceptor{enabled: true}}})
	var ran atomic.Bool
	env.RegisterActivityWithOptions(func(context.Context, ReportSorEventInput) error { return errors.New("unavailable") }, activity.RegisterOptions{Name: recoveryRegisterName})
	env.RegisterDelayedCallback(func() { env.CancelWorkflow() }, 2*time.Second)
	env.ExecuteWorkflow(func(workflow.Context) error { ran.Store(true); return nil })
	if env.GetWorkflowError() == nil || ran.Load() {
		t.Fatal("cancellation executed protected code")
	}
}
func TestRecoveryMissingTransportFailsClosed(t *testing.T) {
	t.Setenv(envEnvironmentID, "e")
	t.Setenv(envWorkerID, "worker")
	t.Setenv(envHostReportSocket, t.TempDir()+"/missing.sock")
	if err := postRecoveryRegistration(context.Background(), ReportSorEventInput{WorkflowID: "w", RunID: "r"}); err == nil {
		t.Fatal("missing transport acknowledged")
	}
}

func TestRecoveryTomorrowTimerRegistersBeforeScheduling(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	var uncovered atomic.Int64
	env.SetWorkerOptions(worker.Options{Interceptors: []interceptor.WorkerInterceptor{&recoveryWorkerInterceptor{enabled: true, uncovered: &uncovered}}})
	var deadline time.Time
	var registered atomic.Bool
	env.RegisterActivityWithOptions(func(_ context.Context, in ReportSorEventInput) error {
		if in.Type != "timer.finished" && uncovered.Load() < 1 {
			t.Error("pending registration is sleep eligible")
		}
		if in.Type == "timer.started" {
			var err error
			deadline, err = time.Parse(time.RFC3339Nano, in.Payload["deadline"])
			if err != nil {
				t.Error(err)
			}
			registered.Store(true)
		}
		return nil
	}, activity.RegisterOptions{Name: recoveryRegisterName})
	var start time.Time
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		start = workflow.Now(ctx)
		timer := workflow.NewTimer(ctx, 24*time.Hour)
		if !registered.Load() {
			return errors.New("timer scheduled before durable registration")
		}
		return timer.Get(ctx, nil)
	})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if !deadline.Equal(start.Add(24 * time.Hour)) {
		t.Fatal("incorrect future wake", deadline, start)
	}
}

func TestRecoveryExternalSignalAcknowledgementUsesItsOwnCoroutine(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.SetWorkerOptions(worker.Options{Interceptors: []interceptor.WorkerInterceptor{&recoveryWorkerInterceptor{enabled: true}}})
	var pending, accepted atomic.Int32
	env.RegisterActivityWithOptions(func(_ context.Context, in ReportSorEventInput) error {
		switch in.Type {
		case "target.pending":
			pending.Add(1)
		case "target.accepted":
			accepted.Add(1)
		}
		return nil
	}, activity.RegisterOptions{Name: recoveryRegisterName})
	env.OnSignalExternalWorkflow("default-test-namespace", "target", "", "message", nil).Return(nil).Once()
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		return workflow.SignalExternalWorkflow(ctx, "target", "", "message", nil).Get(ctx, nil)
	})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if pending.Load() != 1 || accepted.Load() != 1 {
		t.Fatal("signal lacked both sides of recovery handoff", pending.Load(), accepted.Load())
	}
	env.AssertExpectations(t)
}

func TestRecoveryActivityQueueIsDurableBeforeScheduling(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.SetWorkerOptions(worker.Options{Interceptors: []interceptor.WorkerInterceptor{&recoveryWorkerInterceptor{enabled: true}}})
	var registered atomic.Bool
	env.RegisterActivityWithOptions(func(_ context.Context, in ReportSorEventInput) error {
		if in.Type == "checkpoint" {
			if in.Payload["activity_queue"] != "sibling__production" {
				t.Error("activity queue not captured", in.Payload)
			}
			registered.Store(true)
		}
		return nil
	}, activity.RegisterOptions{Name: recoveryRegisterName})
	env.RegisterActivityWithOptions(func(context.Context) error {
		if !registered.Load() {
			return errors.New("activity ran before queue registration")
		}
		return nil
	}, activity.RegisterOptions{Name: "business"})
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{TaskQueue: "sibling__production", StartToCloseTimeout: time.Second})
		return workflow.ExecuteActivity(ctx, "business").Get(ctx, nil)
	})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
}
