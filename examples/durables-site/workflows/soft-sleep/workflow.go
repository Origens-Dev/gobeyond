package softsleep

import (
	"fmt"
	"time"

	gbworkflows "github.com/Origens-Dev/gobeyond/workflows"
	"go.temporal.io/sdk/workflow"
)

// Run sleeps for durationSec (default 3m when < 2m) so EventBridge early-wake
// canaries can prove timer Dynamo-first wake (ADR 010).
func Run(ctx workflow.Context, durationSec int) (string, error) {
	duration := time.Duration(durationSec) * time.Second
	if duration < 2*time.Minute {
		duration = 3 * time.Minute
	}
	if err := workflow.Sleep(ctx, duration); err != nil {
		return "", err
	}
	return fmt.Sprintf("slept %s", duration), nil
}

var Workflow = gbworkflows.Define(gbworkflows.WorkflowConfig{
	Name: "default.soft-sleep",
}, Run)
