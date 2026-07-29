package demo

import (
	"context"
	"fmt"

	gb "github.com/Origens-Dev/gobeyond"
)

// EchoConfig is the compiler-visible task metadata for demo.echo.
var EchoConfig = gb.TaskConfig{
	Name: "demo.echo",
}

// Echo is a standalone durable task (Temporal activity in the Temporal adapter).
func Echo(ctx context.Context, message string) (string, error) {
	if message == "" {
		return "", fmt.Errorf("message is required")
	}
	return "echo: " + message, nil
}
