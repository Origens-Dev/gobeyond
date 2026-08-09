package longretry

import (
	"time"

	references "github.com/Origens-Dev/gobeyond/examples/durables-site/generated/workflows/references"
	gbworkflows "github.com/Origens-Dev/gobeyond/workflows"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

func Run(ctx workflow.Context) (string, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    2 * time.Minute,
			BackoffCoefficient: 1.0,
			MaximumInterval:    2 * time.Minute,
			MaximumAttempts:    3,
		},
	})
	var output string
	err := gbworkflows.ExecuteActivityReference(ctx, references.ActivityFlakyOnce).Get(ctx, &output)
	return output, err
}

var Workflow = gbworkflows.Define(gbworkflows.WorkflowConfig{
	Name: "default.long-retry",
}, Run)
