package durables

import (
	"context"
	"fmt"
	"time"

	gb "github.com/Origens-Dev/gobeyond"
	"go.temporal.io/sdk/activity"
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

// Register attaches this worker's workflows and activities to a Temporal worker.
func Register(w worker.Worker) {
	w.RegisterActivityWithOptions(Echo, activity.RegisterOptions{
		Name: EchoConfig.Name,
	})
	w.RegisterWorkflowWithOptions(EchoOnce, workflow.RegisterOptions{
		Name: EchoOnceConfig.Name,
	})
	w.RegisterWorkflowWithOptions(Demo, workflow.RegisterOptions{
		Name: DemoConfig.Name,
	})
}
