// Package agents is the public Go authoring surface for GoBeyond agents.
//
// An agent is authored in agents/<id>/agent.go as an exported package var:
//
//	var Agent = agents.Define(agents.Config{Durable: true}, Run, agents.Slots{...})
//
// The project compiler reads that declaration without executing it. Direct
// execution is the zero-value default; Durable opts the agent into a durable
// run and requires the generated workflow wiring supplied by the compiler.
package agents

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Mode describes how a run is executed.
type Mode string

const (
	// DirectMode executes in the request/runtime process. It is the default.
	DirectMode Mode = "direct"
	// DurableMode executes through the generated durable workflow runtime.
	DurableMode Mode = "durable"
)

// Config is compiler-visible agent metadata. TaskQueue is a logical queue
// name; generated durable workers append the active environment suffix.
type Config struct {
	TaskQueue string
	Durable   bool
	// Realtime keeps the agent durable while selecting a compiler-owned,
	// agent-unique task queue and local model/tool activity boundaries. It is
	// intentionally an execution hint rather than a persistence mode.
	Realtime bool
	Public   bool
}

// Mode resolves the configured execution model. The zero value is direct.
func (config Config) Mode() Mode {
	if config.Durable || config.Realtime {
		return DurableMode
	}
	return DirectMode
}

// Actor is the authenticated principal that invoked an agent. Identity is
// explicit so direct and durable invocations have the same author-facing type.
type Actor struct {
	ID       string            `json:"id"`
	Kind     string            `json:"kind"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// LoopbackDevActorID identifies the development-only loopback actor.
const LoopbackDevActorID = "dev-loopback"

// LoopbackDevActor returns the typed actor used by local development when no
// external identity provider is configured. Hosted runtimes must supply their
// authenticated actor explicitly.
func LoopbackDevActor() Actor {
	return Actor{ID: LoopbackDevActorID, Kind: "loopback"}
}

// DevLoopbackActor is an alias for LoopbackDevActor kept for call sites that
// read the environment qualifier first.
func DevLoopbackActor() Actor { return LoopbackDevActor() }

// Validate rejects incomplete actor identities before an agent is invoked.
func (actor Actor) Validate() error {
	if strings.TrimSpace(actor.ID) == "" {
		return errors.New("agent actor ID is required")
	}
	if strings.TrimSpace(actor.Kind) == "" {
		return errors.New("agent actor kind is required")
	}
	return nil
}

// Tool, Skill, Subagent, Schedule, and Channel are compiler-visible slots.
// IDs are stable authored references; their provider-specific implementations
// are deliberately kept outside this initial package boundary.
type Tool struct {
	ID string
}

type Skill struct {
	ID string
}

type Subagent struct {
	ID string
}

type Schedule struct {
	ID   string
	Cron string
}

type Channel struct {
	ID        string
	Connector string
}

// Slots declares every extensibility slot on an agent in one compiler-visible
// literal. Empty slots are valid and do not change the direct default.
type Slots struct {
	Tools     []Tool
	Skills    []Skill
	Subagents []Subagent
	Schedules []Schedule
	Channels  []Channel
}

// Handler is the type-safe authoring signature for an agent run.
type Handler[Input any, Output any] func(context.Context, Actor, Input) (Output, error)

// Definition associates compiler-visible metadata with a typed handler.
type Definition[Input any, Output any] struct {
	Config  Config
	Handler Handler[Input, Output]
	Slots   Slots
}

// Define declares an agent. Omitting slots is equivalent to Slots{}.
func Define[Input any, Output any](config Config, handler Handler[Input, Output], slots ...Slots) Definition[Input, Output] {
	definition := Definition[Input, Output]{Config: config, Handler: handler}
	if len(slots) > 0 {
		definition.Slots = slots[0]
	}
	return definition
}

// Invoke runs the typed handler after validating the actor. Durable dispatch is
// intentionally compiler/runtime-owned; this method remains useful for direct
// agents and loopback development tests.
func (definition Definition[Input, Output]) Invoke(ctx context.Context, actor Actor, input Input) (Output, error) {
	var zero Output
	if err := actor.Validate(); err != nil {
		return zero, err
	}
	if definition.Handler == nil {
		return zero, errors.New("agent handler is required")
	}
	return definition.Handler(ctx, actor, input)
}

// ModelMetadata identifies the model selected for a run without leaking
// provider credentials or mutable runtime configuration into authored code.
type ModelMetadata struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

// Session is the durable conversation identity shared by one or more runs.
type Session struct {
	ID        string            `json:"id"`
	AgentID   string            `json:"agentId"`
	Actor     Actor             `json:"actor"`
	Model     ModelMetadata     `json:"model,omitempty"`
	CreatedAt time.Time         `json:"createdAt"`
	UpdatedAt time.Time         `json:"updatedAt"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

// Run is one execution attempt in a session. TaskQueue is empty for direct
// runs unless the host records a selected logical queue for observability.
type Run struct {
	ID        string        `json:"id"`
	SessionID string        `json:"sessionId"`
	AgentID   string        `json:"agentId"`
	Mode      Mode          `json:"mode"`
	TaskQueue string        `json:"taskQueue,omitempty"`
	Model     ModelMetadata `json:"model,omitempty"`
	Status    string        `json:"status"`
	CreatedAt time.Time     `json:"createdAt"`
	UpdatedAt time.Time     `json:"updatedAt"`
}

// DurableRunInput is the serialization contract shared by the HTTP Temporal
// dispatcher and compiler-generated durable agent workflows. Generated
// workflows pass Input to the typed agent activity after decoding it there.
type DurableRunInput struct {
	Session Session         `json:"session"`
	Run     Run             `json:"run"`
	Actor   Actor           `json:"actor"`
	Input   json.RawMessage `json:"input"`
}

// DurableRunOutput is returned by compiler-generated durable agent workflows.
// Output must contain one complete JSON value so the transport can publish the
// same agent.output event shape used by direct agents.
type DurableRunOutput struct {
	Output json.RawMessage `json:"output"`
}
