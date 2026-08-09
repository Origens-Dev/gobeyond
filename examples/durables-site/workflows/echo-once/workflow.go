package echoonce

import (
	"time"

	references "github.com/Origens-Dev/gobeyond/examples/durables-site/generated/workflows/references"
	gbworkflows "github.com/Origens-Dev/gobeyond/workflows"
	"go.temporal.io/sdk/workflow"
)

func Run(ctx workflow.Context, message string) (string, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Second,
	})
	var result string
	err := gbworkflows.ExecuteActivityReference(ctx, references.ActivityEcho, message).Get(ctx, &result)
	return result, err
}

var Workflow = gbworkflows.Define(gbworkflows.WorkflowConfig{
	Name: "default.echo-once",
}, Run)
