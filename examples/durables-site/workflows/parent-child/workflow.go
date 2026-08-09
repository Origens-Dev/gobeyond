package parentchild

import (
	references "github.com/Origens-Dev/gobeyond/examples/durables-site/generated/workflows/references"
	gbworkflows "github.com/Origens-Dev/gobeyond/workflows"
	"go.temporal.io/sdk/workflow"
)

func Run(ctx workflow.Context, message string) (string, error) {
	ctx = workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
		WorkflowID: "child-of-" + workflow.GetInfo(ctx).WorkflowExecution.ID,
	})
	var output string
	err := gbworkflows.ExecuteSubworkflow(ctx, references.WorkflowChildDone, message).Get(ctx, &output)
	return output, err
}

var Workflow = gbworkflows.Define(gbworkflows.WorkflowConfig{
	Name: "default.parent-child",
}, Run)
