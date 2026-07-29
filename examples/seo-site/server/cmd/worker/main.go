package main

import (
	"context"
	"log"
	"os"

	gbtemporal "github.com/Origens-Dev/gobeyond/adapters/temporal"
	demo "github.com/Origens-Dev/gobeyond/examples/seo-site/internal/gobeyondgen/workers/w_demo_ea1cb325"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/worker"
)

func main() {
	if err := gbtemporal.Serve(context.Background(), gbtemporal.Options{
		Register: register,
	}); err != nil {
		log.Fatal(err)
	}
}

func register(w worker.Worker) {
	w.RegisterActivityWithOptions(demo.Echo, activity.RegisterOptions{
		Name: demo.EchoConfig.Name,
	})
}

func init() {
	if os.Getenv("GOBEYOND_TEMPORAL_TASK_QUEUE") == "" {
		os.Setenv("GOBEYOND_TEMPORAL_TASK_QUEUE", "demo__local")
	}
	if os.Getenv("GOBEYOND_TEMPORAL_NAMESPACE") == "" {
		os.Setenv("GOBEYOND_TEMPORAL_NAMESPACE", "default")
	}
}
