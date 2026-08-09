package flakyonce

import (
	"context"
	"fmt"

	gbworkflows "github.com/Origens-Dev/gobeyond/workflows"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
)

func Run(ctx context.Context) (string, error) {
	info := activity.GetInfo(ctx)
	if info.Attempt <= 1 {
		return "", temporal.NewApplicationError("intentional flaky failure", "FlakyOnce")
	}
	return fmt.Sprintf("ok attempt=%d", info.Attempt), nil
}

var Activity = gbworkflows.DefineActivity(gbworkflows.ActivityConfig{
	Name: "default.flaky-once",
}, Run)
