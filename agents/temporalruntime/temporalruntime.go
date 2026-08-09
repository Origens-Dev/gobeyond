// Package temporalruntime dispatches durable agent runs to compiler-generated
// Temporal workflows. Workflow and activity worker registration remains owned
// by generated project code.
package temporalruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Origens-Dev/go-temporal-ai-sdk/activities"
	"github.com/Origens-Dev/go-temporal-ai-sdk/temporalai"
	"github.com/Origens-Dev/go-temporal-ai-sdk/updates"
	gb "github.com/Origens-Dev/gobeyond"
	"github.com/Origens-Dev/gobeyond/agents"
	"github.com/Origens-Dev/gobeyond/agents/httpruntime"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

const (
	EnvAddress     = "GOBEYOND_TEMPORAL_ADDRESS"
	EnvNamespace   = "GOBEYOND_TEMPORAL_NAMESPACE"
	EnvEnvironment = "GOBEYOND_TEMPORAL_ENVIRONMENT"

	DefaultAddress   = "localhost:7233"
	DefaultNamespace = "default"

	workflowNamePrefix = "gobeyond.agent.v1."
	workflowNameSuffix = ".workflow"
	activityNameSuffix = ".activity"
	workflowIDPrefix   = "gobeyond-agent-run/"
	activityTimeout    = time.Hour
)

var (
	agentIDPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	runIDPattern   = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	ErrClosed      = errors.New("agent Temporal dispatcher is closed")
)

// Client is the narrow Temporal client surface used by Dispatcher. The SDK's
// client.Client implements this interface directly.
type Client interface {
	ExecuteWorkflow(context.Context, client.StartWorkflowOptions, interface{}, ...interface{}) (client.WorkflowRun, error)
	SignalWorkflow(context.Context, string, string, string, interface{}) error
	CancelWorkflow(context.Context, string, string) error
	Close()
}

// AIRegistry is the queue-wide worker runtime resolver for compiled AI agents.
// The Temporal AI SDK registers its stable workflow and activity names once per
// polling process, then selects provider/tools by AgentID and revision.
type AIRegistry struct {
	mu          sync.RWMutex
	definitions map[aiRuntimeKey]agents.AIDefinition
	registered  bool
}

type aiRuntimeKey struct {
	agentID          string
	compiledRevision string
}

func NewAIRegistry() *AIRegistry {
	return &AIRegistry{definitions: map[aiRuntimeKey]agents.AIDefinition{}}
}

func RegisterAI(_ worker.Worker, runtimes *AIRegistry, agentID string, definition agents.AIDefinition) error {
	if runtimes == nil {
		return errors.New("agent AI runtime registry is required")
	}
	if definition.Config.Mode() != agents.DurableMode {
		return fmt.Errorf("AI agent %q must set Durable: true before Temporal registration", agentID)
	}
	if err := definition.ValidateRegistration(); err != nil {
		return fmt.Errorf("AI agent %q: %w", strings.TrimSpace(agentID), err)
	}
	agentID, err := normalizeAgentID(agentID)
	if err != nil {
		return err
	}
	compiledRevision := strings.TrimSpace(definition.AI.Revision)
	if compiledRevision == "" {
		return fmt.Errorf("AI agent %q compiled revision is required", agentID)
	}
	definition.AI.Revision = compiledRevision
	if _, err := definition.LanguageModel(); err != nil {
		return fmt.Errorf("AI agent %q: %w", agentID, err)
	}
	runtimes.mu.Lock()
	defer runtimes.mu.Unlock()
	if runtimes.registered {
		return errors.New("agent AI runtimes cannot be added after worker registration")
	}
	if runtimes.definitions == nil {
		runtimes.definitions = map[aiRuntimeKey]agents.AIDefinition{}
	}
	key := aiRuntimeKey{agentID: agentID, compiledRevision: compiledRevision}
	if _, exists := runtimes.definitions[key]; exists {
		return fmt.Errorf("AI agent %q revision %q is already registered", agentID, compiledRevision)
	}
	runtimes.definitions[key] = definition
	return nil
}

// Register installs the stable SDK workflow and activities exactly once for a
// generated queue worker. It is a no-op when the queue contains no AI agents.
func (runtimes *AIRegistry) Register(registry worker.Worker) error {
	if runtimes == nil {
		return errors.New("agent AI runtime registry is required")
	}
	if registry == nil {
		return errors.New("agent Temporal worker is required")
	}
	runtimes.mu.Lock()
	defer runtimes.mu.Unlock()
	if runtimes.registered {
		return errors.New("agent AI runtime registry was already registered")
	}
	if len(runtimes.definitions) == 0 {
		return nil
	}
	temporalai.RegisterAgentWorkflow(registry)
	temporalai.RegisterActivities(registry, activities.New(activities.Options{
		RuntimeResolver: runtimes,
		UpdateConnector: updates.NoopConnector{},
	}))
	runtimes.registered = true
	return nil
}

func (runtimes *AIRegistry) ResolveAgentRuntime(_ context.Context, scope activities.RuntimeScope) (activities.AgentRuntime, error) {
	if runtimes == nil {
		return activities.AgentRuntime{}, errors.New("agent AI runtime registry is required")
	}
	agentID, err := normalizeAgentID(scope.AgentID)
	compiledRevision := strings.TrimSpace(scope.CompiledRevision)
	if err != nil || compiledRevision == "" {
		return activities.AgentRuntime{}, runtimeMismatch(scope)
	}
	key := aiRuntimeKey{agentID: agentID, compiledRevision: compiledRevision}
	runtimes.mu.RLock()
	definition, ok := runtimes.definitions[key]
	runtimes.mu.RUnlock()
	if !ok {
		return activities.AgentRuntime{}, runtimeMismatch(scope)
	}
	return activities.AgentRuntime{
		AgentID: agentID, CompiledRevision: compiledRevision,
		ModelProvider: definition.RuntimeProvider(), Tools: definition.AI.Tools,
	}, nil
}

// runtimeMismatch is deliberately non-retryable: a worker binary only knows
// the exact revisions compiled into it. Missing or stale routing must fail
// safely instead of silently selecting another revision or retrying forever.
func runtimeMismatch(scope activities.RuntimeScope) error {
	mismatch := &activities.RuntimeMismatchError{
		RequestedAgentID:          scope.AgentID,
		RequestedCompiledRevision: scope.CompiledRevision,
	}
	return temporal.NewNonRetryableApplicationError(
		mismatch.Error(),
		activities.RuntimeMismatchErrorType,
		nil,
		*mismatch,
	)
}

// ClientFactory creates a client from non-secret connection options. It is
// injectable so hosted construction and tests do not require a package-global
// Temporal connection.
type ClientFactory func(context.Context, client.Options) (Client, error)

// Options configures a dispatcher. Client and Factory are mutually exclusive.
// Dispatcher takes ownership of Client and closes it from Close.
type Options struct {
	Client      Client
	Factory     ClientFactory
	Address     string
	Namespace   string
	Environment string
}

// Dispatcher implements httpruntime.Dispatcher for compiler-generated durable
// agent workflows.
type Dispatcher struct {
	client      Client
	environment string
	closed      atomic.Bool
	closeOnce   sync.Once
}

// New constructs a dispatcher and dials Temporal when Client is not supplied.
func New(ctx context.Context, options Options) (*Dispatcher, error) {
	if ctx == nil {
		return nil, errors.New("agent Temporal dispatcher context is required")
	}
	if options.Client != nil && options.Factory != nil {
		return nil, errors.New("agent Temporal dispatcher Client and Factory are mutually exclusive")
	}
	environment := strings.TrimSpace(options.Environment)
	if environment == "" {
		environment = gb.LocalEnvironment
	}
	var err error
	environment, err = gb.NormalizeEnvironment(environment)
	if err != nil {
		return nil, fmt.Errorf("agent Temporal dispatcher environment: %w", err)
	}

	temporalClient := options.Client
	if temporalClient == nil {
		address := strings.TrimSpace(options.Address)
		if address == "" {
			address = DefaultAddress
		}
		namespace := strings.TrimSpace(options.Namespace)
		if namespace == "" {
			namespace = DefaultNamespace
		}
		factory := options.Factory
		if factory == nil {
			factory = dialClient
		}
		temporalClient, err = factory(ctx, client.Options{HostPort: address, Namespace: namespace})
		if err != nil {
			return nil, fmt.Errorf("agent Temporal dispatcher dial: %w", err)
		}
		if temporalClient == nil {
			return nil, errors.New("agent Temporal dispatcher factory returned a nil client")
		}
	}
	return &Dispatcher{client: temporalClient, environment: environment}, nil
}

func dialClient(ctx context.Context, options client.Options) (Client, error) {
	return client.DialContext(ctx, options)
}

// NewLocalFromEnv dials a local/plaintext Temporal client. It reads only the
// address, namespace, and queue environment slug; it never reads or logs API
// keys or certificate material.
func NewLocalFromEnv(ctx context.Context) (*Dispatcher, error) {
	return New(ctx, localOptionsFromEnv())
}

// NewLazyLocalFromEnv constructs the local dispatcher without requiring
// Temporal to be reachable at site startup. The first durable invocation
// establishes the connection; direct agents and the rest of the site remain
// usable while a local Temporal server is starting.
func NewLazyLocalFromEnv(ctx context.Context) (*Dispatcher, error) {
	options := localOptionsFromEnv()
	options.Factory = func(_ context.Context, clientOptions client.Options) (Client, error) {
		return client.NewLazyClient(clientOptions)
	}
	return New(ctx, options)
}

func localOptionsFromEnv() Options {
	return Options{
		Address:     strings.TrimSpace(os.Getenv(EnvAddress)),
		Namespace:   strings.TrimSpace(os.Getenv(EnvNamespace)),
		Environment: strings.TrimSpace(os.Getenv(EnvEnvironment)),
	}
}

// WorkflowName returns the stable name generated workers must register for an
// agent's durable workflow.
func WorkflowName(agentID string) (string, error) {
	agentID, err := normalizeAgentID(agentID)
	if err != nil {
		return "", err
	}
	return workflowNamePrefix + agentID + workflowNameSuffix, nil
}

// ActivityName returns the stable name generated workers must register for an
// agent's typed execution activity.
func ActivityName(agentID string) (string, error) {
	agentID, err := normalizeAgentID(agentID)
	if err != nil {
		return "", err
	}
	return workflowNamePrefix + agentID + activityNameSuffix, nil
}

// Register installs the stable workflow and typed execution activity for one
// compiler-discovered durable agent. Generated worker code calls this helper,
// keeping Temporal types and serialization out of authored agent packages.
func Register[Input any, Output any](registry worker.Registry, agentID string, definition agents.Definition[Input, Output]) error {
	if registry == nil {
		return errors.New("agent Temporal registry is required")
	}
	if definition.Config.Mode() != agents.DurableMode {
		return fmt.Errorf("agent %q must set Durable: true before Temporal registration", agentID)
	}
	workflowName, err := WorkflowName(agentID)
	if err != nil {
		return err
	}
	activityName, err := ActivityName(agentID)
	if err != nil {
		return err
	}
	registry.RegisterActivityWithOptions(
		func(ctx context.Context, input agents.DurableRunInput) (agents.DurableRunOutput, error) {
			return executeActivity(ctx, definition, input)
		},
		activity.RegisterOptions{Name: activityName},
	)
	registry.RegisterWorkflowWithOptions(
		func(ctx workflow.Context, input agents.DurableRunInput) (agents.DurableRunOutput, error) {
			return executeWorkflow(ctx, activityName, input, definition.Config.Realtime)
		},
		workflow.RegisterOptions{Name: workflowName},
	)
	return nil
}

func executeActivity[Input any, Output any](ctx context.Context, definition agents.Definition[Input, Output], input agents.DurableRunInput) (agents.DurableRunOutput, error) {
	stopHeartbeat := startActivityHeartbeat(ctx)
	defer stopHeartbeat()
	var typedInput Input
	payload := input.Input
	if len(payload) == 0 {
		payload = json.RawMessage("null")
	}
	if err := json.Unmarshal(payload, &typedInput); err != nil {
		return agents.DurableRunOutput{}, fmt.Errorf("decode durable agent input: %w", err)
	}
	output, err := definition.Invoke(ctx, input.Actor, typedInput)
	if err != nil {
		return agents.DurableRunOutput{}, err
	}
	encoded, err := json.Marshal(output)
	if err != nil {
		return agents.DurableRunOutput{}, fmt.Errorf("encode durable agent output: %w", err)
	}
	return agents.DurableRunOutput{Output: encoded}, nil
}

func startActivityHeartbeat(ctx context.Context) func() {
	if !activity.IsActivity(ctx) || activity.GetInfo(ctx).IsLocalActivity {
		return func() {}
	}
	activity.RecordHeartbeat(ctx, "agent-handler")
	done := make(chan struct{})
	var once sync.Once
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-done:
				return
			case <-ticker.C:
				activity.RecordHeartbeat(ctx, "agent-handler")
			}
		}
	}()
	return func() { once.Do(func() { close(done) }) }
}

func executeWorkflow(ctx workflow.Context, activityName string, input agents.DurableRunInput, realtime bool) (agents.DurableRunOutput, error) {
	var output agents.DurableRunOutput
	var future workflow.Future
	if realtime {
		ctx = workflow.WithLocalActivityOptions(ctx, workflow.LocalActivityOptions{StartToCloseTimeout: activityTimeout})
		future = workflow.ExecuteLocalActivity(ctx, activityName, input)
	} else {
		ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
			StartToCloseTimeout: activityTimeout,
			HeartbeatTimeout:    30 * time.Second,
		})
		future = workflow.ExecuteActivity(ctx, activityName, input)
	}
	if err := future.Get(ctx, &output); err != nil {
		return agents.DurableRunOutput{}, err
	}
	return output, nil
}

func normalizeAgentID(agentID string) (string, error) {
	agentID = strings.TrimSpace(strings.ToLower(agentID))
	if !agentIDPattern.MatchString(agentID) {
		return "", fmt.Errorf("agent ID %q must match [a-z0-9]+(?:-[a-z0-9]+)*", agentID)
	}
	return agentID, nil
}

// WorkflowID returns the stable Temporal execution ID for one session run.
func WorkflowID(sessionID, runID string) (string, error) {
	if !runIDPattern.MatchString(sessionID) {
		return "", fmt.Errorf("invalid agent session ID %q", sessionID)
	}
	if !runIDPattern.MatchString(runID) {
		return "", fmt.Errorf("invalid agent run ID %q", runID)
	}
	return workflowIDPrefix + sessionID + "/" + runID, nil
}

// Start launches the generated workflow, waits for its JSON result, and emits
// the canonical agent.output event through the HTTP runtime.
func (dispatcher *Dispatcher) Start(ctx context.Context, adapter httpruntime.Adapter, call httpruntime.StartCall, emit httpruntime.EventEmitter) error {
	if err := dispatcher.ready(); err != nil {
		return err
	}
	if adapter == nil {
		return errors.New("agent Temporal dispatcher adapter is required")
	}
	if emit == nil {
		return errors.New("agent Temporal dispatcher emitter is required")
	}
	if adapter.Config().Mode() != agents.DurableMode {
		return errors.New("agent Temporal dispatcher requires a durable agent")
	}
	if call.Session.ID == "" || call.Run.SessionID != call.Session.ID {
		return errors.New("agent Temporal dispatcher session and run do not match")
	}
	agentID := call.Run.AgentID
	if agentID == "" || call.Session.AgentID != agentID {
		return errors.New("agent Temporal dispatcher agent IDs do not match")
	}
	if aiAdapter, ok := adapter.(interface{ AIDefinition() agents.AIDefinition }); ok {
		return dispatcher.startAI(ctx, aiAdapter.AIDefinition(), call, emit)
	}
	workflowName, err := WorkflowName(agentID)
	if err != nil {
		return err
	}
	workflowID, err := WorkflowID(call.Session.ID, call.Run.ID)
	if err != nil {
		return err
	}
	physicalQueue, err := gb.TaskQueueName(adapter.Config().TaskQueue, dispatcher.environment)
	if err != nil {
		return fmt.Errorf("agent Temporal dispatcher task queue: %w", err)
	}
	payload := cloneRaw(call.Input)
	if len(payload) == 0 {
		payload = json.RawMessage("null")
	}
	if !json.Valid(payload) {
		return errors.New("agent Temporal dispatcher input must be valid JSON")
	}
	run, err := dispatcher.client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID: workflowID, TaskQueue: physicalQueue,
	}, workflowName, agents.DurableRunInput{
		Session: call.Session, Run: call.Run, Actor: call.Actor, Input: payload,
	})
	if err != nil {
		return fmt.Errorf("start durable agent workflow: %w", err)
	}
	if run == nil {
		return errors.New("start durable agent workflow: Temporal returned a nil run")
	}
	var output agents.DurableRunOutput
	if err := run.Get(ctx, &output); err != nil {
		return fmt.Errorf("wait for durable agent workflow: %w", err)
	}
	if len(output.Output) == 0 || !json.Valid(output.Output) {
		return errors.New("durable agent workflow returned invalid JSON output")
	}
	return emit.Emit(ctx, "agent.output", cloneRaw(output.Output))
}

func (dispatcher *Dispatcher) startAI(ctx context.Context, definition agents.AIDefinition, call httpruntime.StartCall, emit httpruntime.EventEmitter) error {
	workflowID, err := WorkflowID(call.Session.ID, call.Run.ID)
	if err != nil {
		return err
	}
	physicalQueue, err := gb.TaskQueueName(definition.Config.TaskQueue, dispatcher.environment)
	if err != nil {
		return fmt.Errorf("AI agent Temporal task queue: %w", err)
	}
	payload := call.Input
	if len(payload) == 0 || string(payload) == "null" {
		payload = json.RawMessage("{}")
	}
	var input agents.AIInput
	if err := json.Unmarshal(payload, &input); err != nil {
		return fmt.Errorf("decode durable AI agent input: %w", err)
	}
	messages, err := input.ToAIMessages()
	if err != nil {
		return err
	}
	maxSteps := definition.AI.MaxSteps
	if maxSteps <= 0 {
		maxSteps = 8
	}
	toolDefinitions, err := durableToolDefinitions(definition, dispatcher.environment)
	if err != nil {
		return err
	}
	agentInput := temporalai.AgentInput{
		AgentID: definitionID(call), CompiledRevision: definition.AI.Revision,
		ModelID: definition.AI.Model, Instructions: definition.AI.Instructions,
		Prompt: input.PromptText(), Messages: activities.MessagesFromAI(messages),
		Tools: toolDefinitions, MaxSteps: maxSteps,
		Stream:      updates.Options{StreamID: call.Run.ID, Scope: updates.Scope{AgentID: call.Run.AgentID}},
		ToolContext: map[string]any{"gobeyondActor": call.Actor},
	}
	if definition.Config.Realtime {
		agentInput.DefaultModelBoundary = activities.ToolExecutionBoundaryLocalActivity
		agentInput.DefaultToolBoundary = activities.ToolExecutionBoundaryLocalActivity
		agentInput.LocalToolTimeoutFallback = temporalai.LocalToolTimeoutFallbackNone
	}
	run, err := dispatcher.client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID: workflowID, TaskQueue: physicalQueue,
	}, temporalai.AgentWorkflowName, agentInput)
	if err != nil {
		return fmt.Errorf("start durable AI agent workflow: %w", err)
	}
	if run == nil {
		return errors.New("start durable AI agent workflow: Temporal returned a nil run")
	}
	var result temporalai.AgentResult
	if err := run.Get(ctx, &result); err != nil {
		return fmt.Errorf("wait for durable AI agent workflow: %w", err)
	}
	return emit.Emit(ctx, "agent.output", agents.AIOutput{
		Text: result.Text, FinishReason: result.FinishReason,
		RawFinishReason: result.RawFinishReason, Model: result.ModelID,
	})
}

func durableToolDefinitions(definition agents.AIDefinition, environment string) ([]activities.ToolDefinition, error) {
	definitions := activities.ToolDefinitionsFromAI(definition.AI.Tools)
	for index := range definitions {
		if definition.Config.Realtime {
			definitions[index].ExecutionBoundary = activities.ToolExecutionBoundaryLocalActivity
			definitions[index].TaskQueue = ""
			continue
		}
		logicalQueue := definition.Config.TaskQueue
		for key, tool := range definition.AI.Tools {
			name := key
			if strings.TrimSpace(tool.Name) != "" {
				name = tool.Name
			}
			if name == definitions[index].Name {
				if queue := strings.TrimSpace(agents.ToolTaskQueue(tool)); queue != "" {
					logicalQueue = queue
				}
				break
			}
		}
		physicalQueue, err := gb.TaskQueueName(logicalQueue, environment)
		if err != nil {
			return nil, fmt.Errorf("AI agent tool %q task queue: %w", definitions[index].Name, err)
		}
		definitions[index].TaskQueue = physicalQueue
	}
	return definitions, nil
}

func definitionID(call httpruntime.StartCall) string { return call.Run.AgentID }

// Respond signals a pending durable AI tool approval. Typed handler agents keep
// their legacy unsupported response behavior.
func (dispatcher *Dispatcher) Respond(ctx context.Context, adapter httpruntime.Adapter, call httpruntime.RespondCall, _ httpruntime.EventEmitter) error {
	if err := dispatcher.ready(); err != nil {
		return err
	}
	if _, ok := adapter.(interface{ AIDefinition() agents.AIDefinition }); !ok {
		return httpruntime.ErrRespondUnsupported
	}
	var response struct {
		InteractionID string `json:"interactionId"`
		ApprovalID    string `json:"approvalId"`
		Approved      *bool  `json:"approved"`
		Reason        string `json:"reason"`
		Answers       struct {
			Approved *bool  `json:"approved"`
			Reason   string `json:"reason"`
		} `json:"answers"`
	}
	if err := json.Unmarshal(call.Response, &response); err != nil {
		return fmt.Errorf("decode durable AI agent response: %w", err)
	}
	approvalID := strings.TrimSpace(response.InteractionID)
	if approvalID == "" {
		approvalID = strings.TrimSpace(response.ApprovalID)
	}
	if approvalID == "" {
		return errors.New("durable AI agent response interactionId is required")
	}
	approved := response.Approved
	if approved == nil {
		approved = response.Answers.Approved
	}
	if approved == nil {
		return errors.New("durable AI agent response approved is required")
	}
	reason := response.Reason
	if reason == "" {
		reason = response.Answers.Reason
	}
	workflowID, err := WorkflowID(call.Session.ID, call.Run.ID)
	if err != nil {
		return err
	}
	if err := dispatcher.client.SignalWorkflow(ctx, workflowID, "", temporalai.ToolApprovalResponseSignalName(approvalID), temporalai.ToolApprovalResponse{
		ApprovalID: approvalID, Approved: *approved, Reason: reason,
	}); err != nil {
		return fmt.Errorf("signal durable AI agent approval: %w", err)
	}
	return nil
}

// Cancel requests cancellation of the stable workflow execution for the run.
func (dispatcher *Dispatcher) Cancel(ctx context.Context, _ httpruntime.Adapter, call httpruntime.CancelCall, _ httpruntime.EventEmitter) error {
	if err := dispatcher.ready(); err != nil {
		return err
	}
	workflowID, err := WorkflowID(call.Session.ID, call.Run.ID)
	if err != nil {
		return err
	}
	if err := dispatcher.client.CancelWorkflow(ctx, workflowID, ""); err != nil {
		return fmt.Errorf("cancel durable agent workflow: %w", err)
	}
	return nil
}

func (dispatcher *Dispatcher) ready() error {
	if dispatcher == nil || dispatcher.client == nil || dispatcher.closed.Load() {
		return ErrClosed
	}
	return nil
}

// Close releases the Temporal client. It is safe to call more than once.
func (dispatcher *Dispatcher) Close() {
	if dispatcher == nil {
		return
	}
	dispatcher.closeOnce.Do(func() {
		dispatcher.closed.Store(true)
		if dispatcher.client != nil {
			dispatcher.client.Close()
		}
	})
}

func cloneRaw(input json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), input...)
}

var _ httpruntime.Dispatcher = (*Dispatcher)(nil)
