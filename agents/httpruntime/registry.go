// Package httpruntime provides the local HTTP transport for GoBeyond agents.
// It deliberately keeps durable execution behind Dispatcher so the package has
// no Temporal dependency or workflow implementation.
package httpruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/Origens-Dev/gobeyond/agents"
)

var (
	// ErrRespondUnsupported is returned by adapters that do not accept an
	// out-of-band response for an active run.
	ErrRespondUnsupported = errors.New("agent does not accept responses")
	// ErrUnauthenticated lets an ActorResolver report that no authenticated
	// principal is present without exposing provider-specific auth errors.
	ErrUnauthenticated = errors.New("agent actor is not authenticated")
)

// EventEmitter appends an ordered event to a session. Event types must contain
// only lowercase letters, digits, dots, underscores, and hyphens.
type EventEmitter interface {
	Emit(context.Context, string, any) error
}

// StartCall is the transport-neutral input to either a direct adapter or a
// durable dispatcher.
type StartCall struct {
	Session agents.Session
	Run     agents.Run
	Actor   agents.Actor
	Input   json.RawMessage
}

// RespondCall carries a client response to an active run.
type RespondCall struct {
	Session  agents.Session
	Run      agents.Run
	Actor    agents.Actor
	Response json.RawMessage
}

// CancelCall identifies a running cancellation candidate. The HTTP runtime
// records its terminal cancellation only after the adapter or dispatcher
// acknowledges this call.
type CancelCall struct {
	Session agents.Session
	Run     agents.Run
	Actor   agents.Actor
	Reason  string
}

// Adapter is the compiler-generated registration seam. Generated registries
// register one adapter per authored agent ID. Direct adapters execute here;
// durable adapters contribute Config while Dispatcher owns execution.
type Adapter interface {
	Config() agents.Config
	Start(context.Context, StartCall, EventEmitter) error
	Respond(context.Context, RespondCall, EventEmitter) error
	Cancel(context.Context, CancelCall, EventEmitter) error
}

// AdapterFuncs makes direct handlers convenient before compiler-generated
// typed adapters are available.
type AdapterFuncs struct {
	AgentConfig agents.Config
	StartFunc   func(context.Context, StartCall, EventEmitter) error
	RespondFunc func(context.Context, RespondCall, EventEmitter) error
	CancelFunc  func(context.Context, CancelCall, EventEmitter) error
}

func (adapter AdapterFuncs) Config() agents.Config { return adapter.AgentConfig }

func (adapter AdapterFuncs) Start(ctx context.Context, call StartCall, emit EventEmitter) error {
	if adapter.StartFunc == nil {
		return errors.New("agent start handler is required")
	}
	return adapter.StartFunc(ctx, call, emit)
}

func (adapter AdapterFuncs) Respond(ctx context.Context, call RespondCall, emit EventEmitter) error {
	if adapter.RespondFunc == nil {
		return ErrRespondUnsupported
	}
	return adapter.RespondFunc(ctx, call, emit)
}

func (adapter AdapterFuncs) Cancel(ctx context.Context, call CancelCall, emit EventEmitter) error {
	if adapter.CancelFunc == nil {
		return nil
	}
	return adapter.CancelFunc(ctx, call, emit)
}

// Adapt wraps the current typed authoring definition in the runtime Adapter
// seam. A generated registry can emit:
//
//	registry.Register("support", httpruntime.Adapt(agentpkg.Agent))
func Adapt[Input any, Output any](definition agents.Definition[Input, Output]) Adapter {
	return typedAdapter[Input, Output]{definition: definition}
}

// AdaptAI wraps a compiler-discovered framework-owned Go AI SDK definition.
// Direct agents stream text/tool progress through the existing ordered native
// event log; durable dispatchers use AIDefinition to construct Temporal input.
func AdaptAI(definition agents.AIDefinition) Adapter {
	return aiAdapter{definition: definition}
}

// RegisterAI validates the native AI feature set before installing a direct
// adapter. Generated registrations use this helper so unsupported approval
// policies fail at process startup rather than hanging an active run.
func RegisterAI(registry Registerer, agentID string, definition agents.AIDefinition) error {
	if registry == nil {
		return errors.New("agent registry is required")
	}
	if err := definition.ValidateRegistration(); err != nil {
		return fmt.Errorf("AI agent %q: %w", strings.TrimSpace(agentID), err)
	}
	// ProbeLiveModel is separate from text LanguageModel() validation so agents
	// without LiveModel keep the existing startup path unchanged.
	if err := definition.ProbeLiveModel(); err != nil {
		return fmt.Errorf("AI agent %q: %w", strings.TrimSpace(agentID), err)
	}
	return registry.Register(agentID, AdaptAI(definition))
}

type aiAdapter struct{ definition agents.AIDefinition }

func (adapter aiAdapter) Config() agents.Config { return adapter.definition.Config }

func (adapter aiAdapter) AIDefinition() agents.AIDefinition { return adapter.definition }

func (adapter aiAdapter) Start(ctx context.Context, call StartCall, emit EventEmitter) error {
	var input agents.AIInput
	payload := call.Input
	if len(payload) == 0 || string(payload) == "null" {
		payload = json.RawMessage("{}")
	}
	if err := json.Unmarshal(payload, &input); err != nil {
		return fmt.Errorf("decode AI agent input: %w", err)
	}
	definition := adapter.definition
	definition.AI.Instructions = agents.ResolveInstructions(definition.AI.Instructions, call.Session.Metadata)
	// G4 StartConfig.Instructions should use the same ResolveInstructions overlay
	// before opening a Live session (voice path acceptance is deferred to G4).
	result, err := definition.Stream(ctx, call.Actor, input)
	if err != nil {
		return err
	}
	for part := range result.Stream {
		switch part.Type {
		case "text-delta":
			if part.TextDelta != "" {
				if err := emit.Emit(ctx, "agent.text.delta", map[string]any{
					"delta": part.TextDelta, "id": part.ID,
					"stepId": part.StepID, "stepNumber": part.StepNumber,
				}); err != nil {
					return err
				}
			}
		case "reasoning-delta":
			if part.ReasoningDelta != "" {
				if err := emit.Emit(ctx, "agent.reasoning.delta", map[string]any{
					"delta": part.ReasoningDelta, "id": part.ID,
					"stepId": part.StepID, "stepNumber": part.StepNumber,
				}); err != nil {
					return err
				}
			}
		case "tool-call":
			if err := emit.Emit(ctx, "agent.tool.call", map[string]any{
				"toolCallId": part.ToolCallID, "toolName": part.ToolName,
				"input": part.ToolInput, "stepId": part.StepID,
			}); err != nil {
				return err
			}
		case "error":
			if part.Err != nil {
				return part.Err
			}
			return errors.New("AI agent stream failed")
		case "abort":
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("AI agent stream aborted: %s", part.AbortReason)
		}
	}
	if result.OutputErr != nil {
		return result.OutputErr
	}
	return emit.Emit(ctx, "agent.output", agents.AIOutput{
		Text: result.Text, FinishReason: result.FinishReason,
		RawFinishReason: result.RawFinishReason, Model: adapter.definition.AI.Model,
	})
}

func (aiAdapter) Respond(context.Context, RespondCall, EventEmitter) error {
	return ErrRespondUnsupported
}

func (aiAdapter) Cancel(context.Context, CancelCall, EventEmitter) error { return nil }

type typedAdapter[Input any, Output any] struct {
	definition agents.Definition[Input, Output]
}

func (adapter typedAdapter[Input, Output]) Config() agents.Config {
	return adapter.definition.Config
}

func (adapter typedAdapter[Input, Output]) Start(ctx context.Context, call StartCall, emit EventEmitter) error {
	var input Input
	payload := call.Input
	if len(payload) == 0 {
		payload = json.RawMessage("null")
	}
	if err := json.Unmarshal(payload, &input); err != nil {
		return fmt.Errorf("decode agent input: %w", err)
	}
	output, err := adapter.definition.Invoke(ctx, call.Actor, input)
	if err != nil {
		return err
	}
	return emit.Emit(ctx, "agent.output", output)
}

func (typedAdapter[Input, Output]) Respond(context.Context, RespondCall, EventEmitter) error {
	return ErrRespondUnsupported
}

func (typedAdapter[Input, Output]) Cancel(context.Context, CancelCall, EventEmitter) error {
	return nil
}

// Dispatcher is the durable execution seam. A future Temporal integration can
// implement it without changing the HTTP contract or generated Adapter API.
type Dispatcher interface {
	Start(context.Context, Adapter, StartCall, EventEmitter) error
	Respond(context.Context, Adapter, RespondCall, EventEmitter) error
	Cancel(context.Context, Adapter, CancelCall, EventEmitter) error
}

// Registry is the lookup contract consumed by Runtime.
type Registry interface {
	Lookup(agentID string) (Adapter, bool)
}

// Registerer is the narrow interface accepted by compiler-generated registry
// functions.
type Registerer interface {
	Register(agentID string, adapter Adapter) error
}

// MemoryRegistry is a concurrency-safe local registry.
type MemoryRegistry struct {
	mu       sync.RWMutex
	adapters map[string]Adapter
}

func NewRegistry() *MemoryRegistry {
	return &MemoryRegistry{adapters: map[string]Adapter{}}
}

func (registry *MemoryRegistry) Register(agentID string, adapter Adapter) error {
	if registry == nil {
		return errors.New("agent registry is required")
	}
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return errors.New("agent ID is required")
	}
	if adapter == nil {
		return errors.New("agent adapter is required")
	}
	if aiRuntime, ok := adapter.(interface{ AIDefinition() agents.AIDefinition }); ok {
		if err := aiRuntime.AIDefinition().ValidateRegistration(); err != nil {
			return fmt.Errorf("AI agent %q: %w", agentID, err)
		}
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.adapters == nil {
		registry.adapters = map[string]Adapter{}
	}
	if _, exists := registry.adapters[agentID]; exists {
		return fmt.Errorf("agent %q is already registered", agentID)
	}
	registry.adapters[agentID] = adapter
	return nil
}

func (registry *MemoryRegistry) Lookup(agentID string) (Adapter, bool) {
	if registry == nil {
		return nil, false
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	adapter, ok := registry.adapters[agentID]
	return adapter, ok
}
