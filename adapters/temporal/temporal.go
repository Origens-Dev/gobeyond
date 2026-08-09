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
//   - GOBEYOND_TEMPORAL_DEPLOYMENT_NAME — optional stable project-environment deployment name
//   - GOBEYOND_TEMPORAL_BUILD_ID — optional immutable build version (requires DEPLOYMENT_NAME)
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
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

const (
	EnvAddress   = "GOBEYOND_TEMPORAL_ADDRESS"
	EnvNamespace = "GOBEYOND_TEMPORAL_NAMESPACE"
	EnvTaskQueue = "GOBEYOND_TEMPORAL_TASK_QUEUE"
	EnvAPIKey    = "GOBEYOND_TEMPORAL_API_KEY"
	EnvTLSCert   = "GOBEYOND_TEMPORAL_TLS_CERT"
	EnvTLSKey    = "GOBEYOND_TEMPORAL_TLS_KEY"
	// DeploymentName and BuildID opt hosted workers into Temporal Worker
	// Deployment Versioning. Both must be set; local dev leaves both empty.
	EnvDeploymentName = "GOBEYOND_TEMPORAL_DEPLOYMENT_NAME"
	EnvBuildID        = "GOBEYOND_TEMPORAL_BUILD_ID"
	// Hosted readiness contract matches adapters/listen (gbhost unixgram nonce).
	EnvReadinessNonce  = "GOBEYOND_READINESS_NONCE"
	EnvReadinessSignal = "GOBEYOND_READINESS_SIGNAL"
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
	TLSCert        string
	TLSKey         string
	DeploymentName string
	BuildID        string
	Register       RegisterFunc
}

// Serve dials Temporal, runs one worker for Options.TaskQueue, and blocks
// until SIGINT/SIGTERM or ctx cancellation.
//
// After Dial, the namespace probe (ListOpenWorkflow) overlaps worker.Start.
// Hosted unixgram readiness is signaled only when both succeed (fail-closed:
// Dial alone is not enough; a failed probe Stops the worker and never signals
// ready). Start is synchronous so Stop cannot race ahead of poller start
// (Temporal SDK panics if Stop runs before Run's internal Start).
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
	if err := validateDeploymentVersioning(options); err != nil {
		return err
	}

	clientOptions, err := clientOptions(options)
	if err != nil {
		return err
	}

	dialStart := time.Now()
	c, err := client.Dial(clientOptions)
	if err != nil {
		return fmt.Errorf("temporal adapter: dial: %w", err)
	}
	defer c.Close()
	log.Printf("temporal adapter: dial ok in %s", time.Since(dialStart).Round(time.Millisecond))

	runCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Fail closed before hosted readiness. CheckHealth/GetSystemInfo is not
	// namespace-scoped on Temporal Cloud; ListOpenWorkflow proves the sealed
	// mTLS leaf can operate in Options.Namespace (catches
	// "Request unauthorized" that CheckHealth alone can miss).
	// Overlap the probe RTT with poller start so readiness waits on both.
	probeCh := make(chan error, 1)
	go func() {
		probeStart := time.Now()
		_, err := c.ListOpenWorkflow(runCtx, &workflowservice.ListOpenWorkflowExecutionsRequest{
			Namespace:       options.Namespace,
			MaximumPageSize: 1,
		})
		if err != nil {
			log.Printf("temporal adapter: namespace probe failed after %s: %v", time.Since(probeStart).Round(time.Millisecond), err)
			probeCh <- err
			return
		}
		log.Printf("temporal adapter: namespace probe ok in %s", time.Since(probeStart).Round(time.Millisecond))
		probeCh <- nil
	}()

	runStart := time.Now()
	tracker := &healthTracker{maxConcurrent: 100}
	w := worker.New(c, options.TaskQueue, temporalWorkerOptions(options, tracker))
	registerSorActivities(w)
	options.Register(w)
	startHealthReporter(runCtx, tracker)
	_ = ReportWorkerHealth(runCtx, tracker.snapshot())

	// Start synchronously before any fail-closed Stop(). worker.Run+manual Stop
	// races when the probe fails before Run's internal Start (SDK panics:
	// "attempted to start a worker that has been stopped before").
	if err := w.Start(); err != nil {
		<-probeCh
		return fmt.Errorf("temporal adapter: start worker: %w", err)
	}
	log.Printf("temporal adapter: worker started in %s", time.Since(runStart).Round(time.Millisecond))

	errCh := make(chan error, 1)
	go func() {
		// Already started; nil interrupt waits for Stop() or a fatal poller error.
		errCh <- w.Run(nil)
	}()

	if err := waitProbeOrWorker(runCtx, probeCh, errCh, w); err != nil {
		return err
	}

	// Hosted RoleWorker readiness: same unixgram nonce contract as adapters/listen.
	// When the platform injects a nonce, the signal target is required — an empty
	// target would no-op and falsely complete gbhost readiness.
	readinessNonce := os.Getenv(EnvReadinessNonce)
	readinessSignal := os.Getenv(EnvReadinessSignal)
	if readinessNonce != "" && strings.TrimSpace(readinessSignal) == "" {
		w.Stop()
		<-errCh
		return fmt.Errorf("temporal adapter: %s is required when %s is set", EnvReadinessSignal, EnvReadinessNonce)
	}
	if err := signalReadiness(readinessSignal, readinessNonce); err != nil {
		w.Stop()
		<-errCh
		return fmt.Errorf("temporal adapter: signal readiness: %w", err)
	}

	cont := make(chan os.Signal, 1)
	signal.Notify(cont, syscall.SIGCONT)
	defer signal.Stop(cont)

	for {
		select {
		case <-cont:
			_ = signalReadiness(
				os.Getenv(EnvReadinessSignal),
				os.Getenv(EnvReadinessNonce),
			)
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
}

// waitProbeOrWorker waits for the namespace probe while watching for an early
// worker exit or cancellation. On probe failure the worker is stopped and
// readiness must not be signaled (fail-closed).
func waitProbeOrWorker(ctx context.Context, probeCh <-chan error, errCh <-chan error, w worker.Worker) error {
	select {
	case err := <-probeCh:
		if err != nil {
			w.Stop()
			<-errCh
			return fmt.Errorf("temporal adapter: namespace probe: %w", err)
		}
		// Probe succeeded; surface an immediate poller failure before ready.
		select {
		case err := <-errCh:
			if err == nil || isInterrupt(err) {
				return fmt.Errorf("temporal adapter: worker exited before ready")
			}
			return fmt.Errorf("temporal adapter: worker: %w", err)
		default:
			return nil
		}
	case err := <-errCh:
		// Drain probe so the goroutine can exit; ignore its result — worker
		// failure already means we must not signal ready.
		<-probeCh
		if err == nil || isInterrupt(err) {
			return fmt.Errorf("temporal adapter: worker exited before ready")
		}
		return fmt.Errorf("temporal adapter: worker: %w", err)
	case <-ctx.Done():
		w.Stop()
		<-errCh
		<-probeCh
		if err := ctx.Err(); err != nil {
			return err
		}
		return nil
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
	codec, err := claimCodecFromEnv()
	if err != nil {
		return client.Options{}, err
	}
	if codec != nil {
		out.DataConverter = converter.NewCodecDataConverter(
			converter.GetDefaultDataConverter(),
			codec,
		)
		log.Printf("temporal adapter: claim-check PayloadCodec enabled")
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
	if options.DeploymentName == "" {
		options.DeploymentName = strings.TrimSpace(os.Getenv(EnvDeploymentName))
	}
	if options.BuildID == "" {
		options.BuildID = strings.TrimSpace(os.Getenv(EnvBuildID))
	}
	options.DeploymentName = strings.TrimSpace(options.DeploymentName)
	options.BuildID = strings.TrimSpace(options.BuildID)
	return options
}

func validateDeploymentVersioning(options Options) error {
	hasDeployment := strings.TrimSpace(options.DeploymentName) != ""
	hasBuild := strings.TrimSpace(options.BuildID) != ""
	if hasDeployment != hasBuild {
		return fmt.Errorf("temporal adapter: %s and %s must both be set or both empty", EnvDeploymentName, EnvBuildID)
	}
	return nil
}

func temporalWorkerOptions(options Options, tracker *healthTracker) worker.Options {
	result := worker.Options{
		MaxConcurrentActivityExecutionSize: int(tracker.maxConcurrent),
		Interceptors: []interceptor.WorkerInterceptor{
			&healthInterceptor{tracker: tracker},
			&sorWorkerInterceptor{},
		},
	}
	if options.DeploymentName != "" {
		result.DeploymentOptions = worker.DeploymentOptions{
			UseVersioning: true,
			Version: worker.WorkerDeploymentVersion{
				DeploymentName: options.DeploymentName,
				BuildID:        options.BuildID,
			},
			// Generated authored workflows, durable agents, and wrapper workflows
			// all register on this worker. Pinned is the safe compatibility default.
			DefaultVersioningBehavior: workflow.VersioningBehaviorPinned,
		}
	}
	return result
}

func isInterrupt(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "interrupt") || strings.Contains(err.Error(), "canceled"))
}

func signalReadiness(target, nonce string) error {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil
	}
	const prefix = "unixgram://"
	if !strings.HasPrefix(target, prefix) {
		return errors.New("readiness signal target must use unixgram://")
	}
	path := strings.TrimPrefix(target, prefix)
	if !strings.HasPrefix(path, "/") {
		return errors.New("readiness signal target must use an absolute path")
	}
	if nonce == "" {
		return errors.New("readiness signal requires a nonce")
	}
	address, err := net.ResolveUnixAddr("unixgram", path)
	if err != nil {
		return err
	}
	connection, err := net.DialUnix("unixgram", nil, address)
	if err != nil {
		return err
	}
	defer connection.Close()
	if _, err := connection.Write([]byte(nonce)); err != nil {
		return err
	}
	return nil
}
