package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Origens-Dev/go-ai/packages/ai"
)

const toolActorContextKey = "gobeyondActor"

const (
	toolMetadataNamespace = "gobeyond"
	toolTaskQueueKey      = "taskQueue"
)

// ToolConfig is the provider-neutral authoring surface for a model-callable
// function. Schemas use ordinary JSON Schema values (usually map[string]any).
type ToolConfig struct {
	Name        string
	Title       string
	Description string
	// TaskQueue is a logical Temporal queue. Durable agents inherit their own
	// resolved queue when this is empty. Realtime agents reject explicit tool
	// queues because their tools execute as local activities.
	TaskQueue        string
	InputSchema      any
	OutputSchema     any
	RequiresApproval bool
}

type ToolHandler[Input any, Output any] func(context.Context, Actor, Input) (Output, error)

// DefineTool adapts a typed, actor-aware application function to the Go AI SDK
// tool contract. Model input is schema-validated by go-ai before this decoder
// runs; the authenticated actor comes from framework-owned runtime context.
func DefineTool[Input any, Output any](config ToolConfig, handler ToolHandler[Input, Output]) AITool {
	metadata := ai.ProviderMetadata(nil)
	if config.TaskQueue != "" {
		metadata = ai.ProviderMetadata{
			toolMetadataNamespace: map[string]any{toolTaskQueueKey: config.TaskQueue},
		}
	}
	return ai.Tool{
		Name: config.Name, Title: config.Title, Description: config.Description,
		InputSchema: config.InputSchema, OutputSchema: config.OutputSchema,
		ToolMetadata:     metadata,
		RequiresApproval: config.RequiresApproval,
		Execute: func(ctx context.Context, call ai.ToolCall, options ai.ToolExecutionOptions) (any, error) {
			if handler == nil {
				return nil, errors.New("agent tool handler is required")
			}
			actor, err := toolActor(options.Context)
			if err != nil {
				return nil, err
			}
			var input Input
			data, err := json.Marshal(call.Input)
			if err != nil {
				return nil, fmt.Errorf("encode agent tool input: %w", err)
			}
			if err := json.Unmarshal(data, &input); err != nil {
				return nil, fmt.Errorf("decode agent tool input: %w", err)
			}
			return handler(ctx, actor, input)
		},
	}
}

// ToolTaskQueue returns the logical task queue embedded by DefineTool. It is
// used by compiler-generated durable dispatch and never interpreted by model
// providers.
func ToolTaskQueue(tool AITool) string {
	namespace, ok := tool.ToolMetadata[toolMetadataNamespace].(map[string]any)
	if !ok {
		return ""
	}
	queue, _ := namespace[toolTaskQueueKey].(string)
	return queue
}

func toolActor(value any) (Actor, error) {
	if values, ok := value.(map[string]any); ok {
		value = values[toolActorContextKey]
	}
	if actor, ok := value.(Actor); ok {
		return actor, actor.Validate()
	}
	data, err := json.Marshal(value)
	if err != nil {
		return Actor{}, errors.New("agent tool actor is unavailable")
	}
	var actor Actor
	if err := json.Unmarshal(data, &actor); err != nil || actor.Validate() != nil {
		return Actor{}, errors.New("agent tool actor is unavailable")
	}
	return actor, nil
}
