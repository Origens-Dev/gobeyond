package durables

import (
	"context"
	"fmt"
	"time"

	gb "github.com/Origens-Dev/gobeyond"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// EchoConfig is the compiler-visible task metadata for the default worker echo task.
var EchoConfig = gb.TaskConfig{
	Name: "default.echo",
}

// EchoOnceConfig is a thin one-activity workflow used to trigger Echo.
var EchoOnceConfig = gb.WorkflowConfig{
	Name: "default.echo-once",
}

// DemoConfig is a richer demo workflow (Echo twice).
var DemoConfig = gb.WorkflowConfig{
	Name: "default.demo",
}

// SoftSleepConfig sleeps ≥ MinDuration so EventBridge early-wake canaries can
// prove timer Dynamo-first wake (ADR 010).
var SoftSleepConfig = gb.WorkflowConfig{
	Name: "default.soft-sleep",
}

// LongRetryConfig fails an activity once with a ≥2m initial retry interval.
var LongRetryConfig = gb.WorkflowConfig{
	Name: "default.long-retry",
}

// ParentChildConfig starts a short child; child terminal should wake parent tip.
var ParentChildConfig = gb.WorkflowConfig{
	Name: "default.parent-child",
}

// ChildDoneConfig is the child workflow for ParentChild.
var ChildDoneConfig = gb.WorkflowConfig{
	Name: "default.child-done",
}

// FlakyOnceConfig is the activity used by LongRetry.
var FlakyOnceConfig = gb.TaskConfig{
	Name: "default.flaky-once",
}

// Echo is a standalone durable task (Temporal activity).
func Echo(_ context.Context, message string) (string, error) {
	if message == "" {
		return "", fmt.Errorf("message is required")
	}
	return "echo: " + message, nil
}

// EchoOnce runs Echo once.
func EchoOnce(ctx workflow.Context, message string) (string, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Second,
	})
	var result string
	err := workflow.ExecuteActivity(ctx, EchoConfig.Name, message).Get(ctx, &result)
	return result, err
}

// Demo runs Echo twice and joins the results.
func Demo(ctx workflow.Context, message string) (string, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Second,
	})
	var first string
	if err := workflow.ExecuteActivity(ctx, EchoConfig.Name, message).Get(ctx, &first); err != nil {
		return "", err
	}
	var second string
	if err := workflow.ExecuteActivity(ctx, EchoConfig.Name, message+" again").Get(ctx, &second); err != nil {
		return "", err
	}
	return first + " | " + second, nil
}

// SoftSleep sleeps for durationSec (default 3m when < 2m) then returns.
func SoftSleep(ctx workflow.Context, durationSec int) (string, error) {
	d := time.Duration(durationSec) * time.Second
	if d < 2*time.Minute {
		d = 3 * time.Minute
	}
	if err := workflow.Sleep(ctx, d); err != nil {
		return "", err
	}
	return fmt.Sprintf("slept %s", d), nil
}

// FlakyOnce fails on attempt 1, succeeds afterward (activity retry wake canary).
func FlakyOnce(ctx context.Context) (string, error) {
	info := activity.GetInfo(ctx)
	if info.Attempt <= 1 {
		return "", temporal.NewApplicationError("intentional flaky failure", "FlakyOnce")
	}
	return fmt.Sprintf("ok attempt=%d", info.Attempt), nil
}

// LongRetry runs FlakyOnce with a long InitialInterval so ingest schedules EB.
func LongRetry(ctx workflow.Context) (string, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    2 * time.Minute,
			BackoffCoefficient: 1.0,
			MaximumInterval:    2 * time.Minute,
			MaximumAttempts:    3,
		},
	})
	var out string
	err := workflow.ExecuteActivity(ctx, FlakyOnceConfig.Name).Get(ctx, &out)
	return out, err
}

// ChildDone finishes quickly so the parent can be woken on terminal.
func ChildDone(ctx workflow.Context, message string) (string, error) {
	_ = workflow.Sleep(ctx, time.Second)
	return "child:" + message, nil
}

// ParentChild starts ChildDone and awaits it (child→parent wake canary).
func ParentChild(ctx workflow.Context, message string) (string, error) {
	ctx = workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
		WorkflowID: "child-of-" + workflow.GetInfo(ctx).WorkflowExecution.ID,
	})
	var out string
	err := workflow.ExecuteChildWorkflow(ctx, ChildDoneConfig.Name, message).Get(ctx, &out)
	return out, err
}

// Register attaches this worker's workflows and activities to a Temporal worker.
func Register(w worker.Worker) {
	w.RegisterActivityWithOptions(Echo, activity.RegisterOptions{
		Name: EchoConfig.Name,
	})
	w.RegisterActivityWithOptions(FlakyOnce, activity.RegisterOptions{
		Name: FlakyOnceConfig.Name,
	})
	w.RegisterWorkflowWithOptions(EchoOnce, workflow.RegisterOptions{
		Name: EchoOnceConfig.Name,
	})
	w.RegisterWorkflowWithOptions(Demo, workflow.RegisterOptions{
		Name: DemoConfig.Name,
	})
	w.RegisterWorkflowWithOptions(SoftSleep, workflow.RegisterOptions{
		Name: SoftSleepConfig.Name,
	})
	w.RegisterWorkflowWithOptions(LongRetry, workflow.RegisterOptions{
		Name: LongRetryConfig.Name,
	})
	w.RegisterWorkflowWithOptions(ParentChild, workflow.RegisterOptions{
		Name: ParentChildConfig.Name,
	})
	w.RegisterWorkflowWithOptions(ChildDone, workflow.RegisterOptions{
		Name: ChildDoneConfig.Name,
	})
}
