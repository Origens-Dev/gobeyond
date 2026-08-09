package demo

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
	var first string
	if err := gbworkflows.ExecuteActivityReference(ctx, references.ActivityEcho, message).Get(ctx, &first); err != nil {
		return "", err
	}
	var second string
	if err := gbworkflows.ExecuteActivityReference(ctx, references.ActivityEcho, message+" again").Get(ctx, &second); err != nil {
		return "", err
	}
	return first + " | " + second, nil
}

var Workflow = gbworkflows.Define(gbworkflows.WorkflowConfig{
	Name: "default.demo",
}, Run)
