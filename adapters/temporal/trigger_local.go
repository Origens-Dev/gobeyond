package temporal

import (
	"context"
	"fmt"
	"strings"
	"sync"

	gb "github.com/Origens-Dev/gobeyond"
	"github.com/google/uuid"
	"go.temporal.io/sdk/client"
)

type localTemporalBridge interface {
	start(ctx context.Context, workflowType, taskQueue, workflowID string, args []any) (StartHandle, error)
	signal(ctx context.Context, workflowID, signalName string, args []any) error
	getRun(ctx context.Context, workflowID, runID string) (client.WorkflowRun, error)
	close() error
}

type sdkLocalBridge struct {
	client client.Client
}

func (b *sdkLocalBridge) start(ctx context.Context, workflowType, taskQueue, workflowID string, args []any) (StartHandle, error) {
	run, err := b.client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:        workflowID,
		TaskQueue: taskQueue,
	}, workflowType, args...)
	if err != nil {
		return StartHandle{}, err
	}
	return StartHandle{WorkflowID: run.GetID(), RunID: run.GetRunID()}, nil
}

func (b *sdkLocalBridge) signal(ctx context.Context, workflowID, signalName string, args []any) error {
	var arg any
	switch len(args) {
	case 0:
		arg = nil
	case 1:
		arg = args[0]
	default:
		arg = args
	}
	return b.client.SignalWorkflow(ctx, workflowID, "", signalName, arg)
}

func (b *sdkLocalBridge) getRun(ctx context.Context, workflowID, runID string) (client.WorkflowRun, error) {
	return b.client.GetWorkflow(ctx, workflowID, runID), nil
}

func (b *sdkLocalBridge) close() error {
	if b.client != nil {
		b.client.Close()
	}
	return nil
}

type localTrigger struct {
	endpoint    string
	namespace   string
	workerID    string
	environment string
	dial        func() (localTemporalBridge, error)
	mu          sync.Mutex
	bridge      localTemporalBridge
	dialErr     error
}

func newLocalTrigger(opts ClientOptions) *localTrigger {
	endpoint := strings.TrimSpace(opts.Endpoint)
	if endpoint == "" {
		endpoint = getenv(EnvAddress, defaultTemporalAddress)
	}
	namespace := strings.TrimSpace(opts.Namespace)
	if namespace == "" {
		namespace = getenv(EnvNamespace, defaultTemporalNamespace)
	}
	environment := strings.TrimSpace(opts.Environment)
	if environment == "" {
		environment = getenv(EnvTemporalEnvironment, gb.LocalEnvironment)
	}
	lt := &localTrigger{
		endpoint:    endpoint,
		namespace:   namespace,
		workerID:    strings.TrimSpace(opts.WorkerID),
		environment: environment,
	}
	if opts.LocalDial != nil {
		lt.dial = opts.LocalDial
	} else {
		lt.dial = func() (localTemporalBridge, error) {
			c, err := client.Dial(client.Options{
				HostPort:  lt.endpoint,
				Namespace: lt.namespace,
			})
			if err != nil {
				return nil, wrapDialError(lt.endpoint, err)
			}
			return &sdkLocalBridge{client: c}, nil
		}
	}
	return lt
}

func (l *localTrigger) bridgeOrDial() (localTemporalBridge, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.bridge != nil {
		return l.bridge, nil
	}
	if l.dialErr != nil {
		return nil, l.dialErr
	}
	bridge, err := l.dial()
	if err != nil {
		l.dialErr = err
		return nil, err
	}
	l.bridge = bridge
	return bridge, nil
}

func (l *localTrigger) start(ctx context.Context, req startRequest) (StartHandle, error) {
	taskQueue, err := resolveLocalTaskQueue(req.TaskQueue, req.WorkerID, l.workerID, l.environment)
	if err != nil {
		return StartHandle{}, err
	}
	workflowID := strings.TrimSpace(req.WorkflowID)
	if workflowID == "" {
		workflowID = fmt.Sprintf("%s-%s", req.WorkflowName, uuid.NewString())
	}
	bridge, err := l.bridgeOrDial()
	if err != nil {
		return StartHandle{}, err
	}
	return bridge.start(ctx, req.WorkflowName, taskQueue, workflowID, req.Args)
}

func (l *localTrigger) signal(ctx context.Context, req signalRequest) error {
	if strings.TrimSpace(req.WorkflowID) == "" {
		return fmt.Errorf("temporal trigger: signal requires workflow_id")
	}
	bridge, err := l.bridgeOrDial()
	if err != nil {
		return err
	}
	return bridge.signal(ctx, req.WorkflowID, req.SignalName, req.Args)
}

func (l *localTrigger) getRun(ctx context.Context, workflowID, runID string) (client.WorkflowRun, error) {
	bridge, err := l.bridgeOrDial()
	if err != nil {
		return nil, err
	}
	sdk, ok := bridge.(*sdkLocalBridge)
	if !ok {
		return nil, fmt.Errorf("temporal trigger: Wait requires a Temporal SDK local bridge")
	}
	return sdk.getRun(ctx, workflowID, runID)
}

func (l *localTrigger) close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.bridge == nil {
		return nil
	}
	err := l.bridge.close()
	l.bridge = nil
	l.dialErr = nil
	return err
}

func resolveLocalTaskQueue(explicit, reqWorkerID, defaultWorkerID, environment string) (string, error) {
	if q := strings.TrimSpace(explicit); q != "" {
		sep := strings.Index(q, gb.TaskQueueSeparator)
		if sep <= 0 || sep != strings.LastIndex(q, gb.TaskQueueSeparator) {
			return "", fmt.Errorf("temporal trigger: task queue %q must be {workerId}__{environment}", q)
		}
		return gb.TaskQueueName(q[:sep], q[sep+len(gb.TaskQueueSeparator):])
	}
	workerID := strings.TrimSpace(reqWorkerID)
	if workerID == "" {
		workerID = defaultWorkerID
	}
	return gb.TaskQueueName(workerID, environment)
}

func wrapDialError(endpoint string, err error) error {
	if isConnectionRefused(err) {
		return fmt.Errorf(
			"temporal trigger: cannot reach Temporal at %s. Start Docker Temporal from the gobeyond repo, e.g. `docker compose -f examples/durables-site/docker-compose.temporal.yml up -d`: %w",
			endpoint, err,
		)
	}
	return fmt.Errorf("temporal trigger: dial failed: %w", err)
}

func isConnectionRefused(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "connection refused") || strings.Contains(msg, "connect: connection refused")
}
