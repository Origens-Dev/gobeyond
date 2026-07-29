package durables

import (
	"context"
	"fmt"

	gb "github.com/Origens-Dev/gobeyond"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/worker"
)

// EchoConfig is the compiler-visible task metadata for the default worker echo task.
var EchoConfig = gb.TaskConfig{
	Name: "default.echo",
}

// Echo is a standalone durable task (Temporal activity in the Temporal adapter).
func Echo(ctx context.Context, message string) (string, error) {
	if message == "" {
		return "", fmt.Errorf("message is required")
	}
	return "echo: " + message, nil
}

// Register attaches this worker's workflows and activities to a Temporal worker.
func Register(w worker.Worker) {
	w.RegisterActivityWithOptions(Echo, activity.RegisterOptions{
		Name: EchoConfig.Name,
	})
}
