package echo

import (
	"context"
	"fmt"

	gbworkflows "github.com/Origens-Dev/gobeyond/workflows"
)

func Run(_ context.Context, message string) (string, error) {
	if message == "" {
		return "", fmt.Errorf("message is required")
	}
	return "echo: " + message, nil
}

var Activity = gbworkflows.DefineActivity(gbworkflows.ActivityConfig{
	Name: "default.echo",
}, Run)
