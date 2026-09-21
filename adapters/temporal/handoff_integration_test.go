package temporal

import (
	"context"
	"fmt"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// Run against an isolated local Temporal dev server, never a hosted namespace.
func TestWorkerHandoffFinishesAdmittedActivity(t *testing.T) {
	address := os.Getenv("GOBEYOND_TEMPORAL_INTEGRATION_ADDRESS")
	if address == "" {
		t.Skip("requires local Temporal dev server")
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil || (host != "127.0.0.1" && host != "localhost" && host != "::1") {
		t.Fatal("integration test requires a loopback dev server")
	}
	t.Setenv("GOBEYOND_SHUTDOWN_GRACE", "5s")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := client.Dial(client.Options{HostPort: address, Namespace: "default"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	queue := fmt.Sprintf("handoff-%d", time.Now().UnixNano())
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	canceled := make(chan struct{}, 2)
	var attempts atomic.Int32
	register := func(w worker.Worker) {
		w.RegisterWorkflowWithOptions(func(ctx workflow.Context) (string, error) {
			ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: 10 * time.Second})
			var result string
			err := workflow.ExecuteActivity(ctx, "hold-build").Get(ctx, &result)
			return result, err
		}, workflow.RegisterOptions{Name: "build"})
		w.RegisterActivityWithOptions(func(ctx context.Context) (string, error) {
			attempts.Add(1)
			started <- struct{}{}
			select {
			case <-release:
				return "published", nil
			case <-ctx.Done():
				canceled <- struct{}{}
				return "", ctx.Err()
			}
		}, activity.RegisterOptions{Name: "hold-build"})
	}
	options := Options{Address: address, Namespace: "default", TaskQueue: queue, Register: register}
	oldCtx, stopOld := context.WithCancel(ctx)
	defer stopOld()
	oldDone := make(chan error, 1)
	go func() { oldDone <- Serve(oldCtx, options) }()
	run, err := c.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: queue, TaskQueue: queue}, "build")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("activity did not start")
	}
	newCtx, stopNew := context.WithCancel(ctx)
	defer stopNew()
	newDone := make(chan error, 1)
	go func() { newDone <- Serve(newCtx, options) }()
	defer func() {
		stopNew()
		select {
		case <-newDone:
		case <-time.After(6 * time.Second):
			t.Error("replacement failed to stop")
		}
	}()
	stopOld()
	select {
	case <-canceled:
		t.Fatal("cutover canceled the admitted build")
	case <-oldDone:
		t.Fatal("previous worker exited before its activity finished")
	case <-time.After(150 * time.Millisecond):
	}
	unblock()
	var result string
	if err := run.Get(ctx, &result); err != nil {
		t.Fatal(err)
	}
	if result != "published" || attempts.Load() != 1 {
		t.Fatalf("result=%q attempts=%d", result, attempts.Load())
	}
	select {
	case err := <-oldDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("previous worker did not drain")
	}
}
