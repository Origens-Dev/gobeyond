// Package temporal implements the process lifecycle for a GoBeyond worker
// binary that polls one Temporal task queue (ADR 006 / ADR 007).
//
// Environment:
//
//   - GOBEYOND_TEMPORAL_ADDRESS — host:port (default localhost:7233)
//   - GOBEYOND_TEMPORAL_NAMESPACE — Temporal namespace (default default)
//   - GOBEYOND_TEMPORAL_TASK_QUEUE — required resolved queue {workerId}__{environment}
//   - GOBEYOND_TEMPORAL_API_KEY — optional Temporal Cloud API key
//
// Hosted/preview clients must not put Temporal admin keys in web sandboxes;
// this adapter is for worker bundles only.
package temporal

import (
	"context"
	"crypto/tls"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
)

const (
	EnvAddress   = "GOBEYOND_TEMPORAL_ADDRESS"
	EnvNamespace = "GOBEYOND_TEMPORAL_NAMESPACE"
	EnvTaskQueue = "GOBEYOND_TEMPORAL_TASK_QUEUE"
	EnvAPIKey    = "GOBEYOND_TEMPORAL_API_KEY"
)

// RegisterFunc configures workflows and activities on a Temporal worker.
type RegisterFunc func(w worker.Worker)

// Options configures the Temporal worker process.
type Options struct {
	Address   string
	Namespace string
	TaskQueue string
	APIKey    string
	Register  RegisterFunc
}

// Serve dials Temporal, runs one worker for Options.TaskQueue, and blocks
// until SIGINT/SIGTERM or ctx cancellation.
func Serve(ctx context.Context, options Options) error {
	if options.Register == nil {
		return fmt.Errorf("temporal adapter: Register is required")
	}
	options = optionsFromEnv(options)
	if options.TaskQueue == "" {
		return fmt.Errorf("temporal adapter: %s is required", EnvTaskQueue)
	}
	if options.Address == "" {
		options.Address = "localhost:7233"
	}
	if options.Namespace == "" {
		options.Namespace = "default"
	}

	clientOptions := client.Options{
		HostPort:  options.Address,
		Namespace: options.Namespace,
	}
	if options.APIKey != "" {
		clientOptions.Credentials = client.NewAPIKeyStaticCredentials(options.APIKey)
		clientOptions.ConnectionOptions = client.ConnectionOptions{
			TLS: &tls.Config{MinVersion: tls.VersionTLS12},
		}
	}

	c, err := client.Dial(clientOptions)
	if err != nil {
		return fmt.Errorf("temporal adapter: dial: %w", err)
	}
	defer c.Close()

	w := worker.New(c, options.TaskQueue, worker.Options{})
	options.Register(w)

	runCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		errCh <- w.Run(worker.InterruptCh())
	}()

	select {
	case <-runCtx.Done():
		w.Stop()
		err := <-errCh
		if err == nil || isInterrupt(err) {
			return nil
		}
		return err
	case err := <-errCh:
		if err == nil || isInterrupt(err) {
			return nil
		}
		return err
	}
}

func optionsFromEnv(options Options) Options {
	if options.Address == "" {
		options.Address = strings.TrimSpace(os.Getenv(EnvAddress))
	}
	if options.Namespace == "" {
		options.Namespace = strings.TrimSpace(os.Getenv(EnvNamespace))
	}
	if options.TaskQueue == "" {
		options.TaskQueue = strings.TrimSpace(os.Getenv(EnvTaskQueue))
	}
	if options.APIKey == "" {
		options.APIKey = strings.TrimSpace(os.Getenv(EnvAPIKey))
	}
	return options
}

func isInterrupt(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "interrupt") || strings.Contains(err.Error(), "canceled"))
}
