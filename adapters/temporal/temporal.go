// Package temporal implements the process lifecycle for a GoBeyond worker
// binary that polls one Temporal task queue (ADR 006 / ADR 007).
//
// Environment:
//
//   - GOBEYOND_TEMPORAL_ADDRESS — host:port (default localhost:7233)
//   - GOBEYOND_TEMPORAL_NAMESPACE — Temporal namespace (default default)
//   - GOBEYOND_TEMPORAL_TASK_QUEUE — required resolved queue {workerId}__{environment}
//   - GOBEYOND_TEMPORAL_API_KEY — optional Temporal Cloud API key (mutually exclusive with mTLS)
//   - GOBEYOND_TEMPORAL_TLS_CERT — optional client certificate PEM (requires TLS_KEY)
//   - GOBEYOND_TEMPORAL_TLS_KEY — optional client private key PEM (requires TLS_CERT)
//
// Auth modes: local plaintext (neither API key nor mTLS), API-key TLS, or
// mTLS via X509KeyPair. Setting only one of TLS_CERT/TLS_KEY is an error;
// setting both API key and mTLS is an error.
//
// Hosted/preview clients must not put Temporal admin keys or leaf PEMs in web
// sandboxes; this adapter is for worker bundles only.
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
	EnvTLSCert   = "GOBEYOND_TEMPORAL_TLS_CERT"
	EnvTLSKey    = "GOBEYOND_TEMPORAL_TLS_KEY"
)

// RegisterFunc configures workflows and activities on a Temporal worker.
type RegisterFunc func(w worker.Worker)

// Options configures the Temporal worker process.
type Options struct {
	Address   string
	Namespace string
	TaskQueue string
	APIKey    string
	// TLSCert and TLSKey are PEM-encoded client certificate material for mTLS.
	TLSCert  string
	TLSKey   string
	Register RegisterFunc
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

	clientOptions, err := clientOptions(options)
	if err != nil {
		return err
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

func clientOptions(options Options) (client.Options, error) {
	out := client.Options{
		HostPort:  options.Address,
		Namespace: options.Namespace,
	}
	tlsConfig, err := dialTLSConfig(options)
	if err != nil {
		return client.Options{}, err
	}
	if options.APIKey != "" {
		out.Credentials = client.NewAPIKeyStaticCredentials(options.APIKey)
	}
	if tlsConfig != nil {
		out.ConnectionOptions = client.ConnectionOptions{
			TLS: tlsConfig,
		}
	}
	return out, nil
}

// dialTLSConfig returns a TLS config for API-key or mTLS dial, or nil for
// local plaintext. Mutual exclusion and half-set PEM pairs are rejected here.
func dialTLSConfig(options Options) (*tls.Config, error) {
	hasCert := options.TLSCert != ""
	hasKey := options.TLSKey != ""
	hasAPIKey := options.APIKey != ""

	if hasCert != hasKey {
		return nil, fmt.Errorf("temporal adapter: %s and %s must both be set or both empty", EnvTLSCert, EnvTLSKey)
	}
	if hasCert && hasAPIKey {
		return nil, fmt.Errorf("temporal adapter: cannot set both %s and mTLS (%s/%s)", EnvAPIKey, EnvTLSCert, EnvTLSKey)
	}
	if hasCert {
		cert, err := tls.X509KeyPair([]byte(options.TLSCert), []byte(options.TLSKey))
		if err != nil {
			return nil, fmt.Errorf("temporal adapter: parse TLS cert/key: %w", err)
		}
		return &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}, nil
	}
	if hasAPIKey {
		return &tls.Config{MinVersion: tls.VersionTLS12}, nil
	}
	return nil, nil
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
	if options.TLSCert == "" {
		options.TLSCert = strings.TrimSpace(os.Getenv(EnvTLSCert))
	}
	if options.TLSKey == "" {
		options.TLSKey = strings.TrimSpace(os.Getenv(EnvTLSKey))
	}
	return options
}

func isInterrupt(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "interrupt") || strings.Contains(err.Error(), "canceled"))
}
