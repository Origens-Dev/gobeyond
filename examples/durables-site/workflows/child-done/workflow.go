package childdone

import (
	"time"

	gbworkflows "github.com/Origens-Dev/gobeyond/workflows"
	"go.temporal.io/sdk/workflow"
)

func Run(ctx workflow.Context, message string) (string, error) {
	if err := workflow.Sleep(ctx, time.Second); err != nil {
		return "", err
	}
	return "child:" + message, nil
}

var Workflow = gbworkflows.Define(gbworkflows.WorkflowConfig{
	Name: "default.child-done",
}, Run)
