package agents

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/Origens-Dev/go-ai/packages/ai"
	"github.com/Origens-Dev/go-ai/packages/anthropic"
	"github.com/Origens-Dev/go-ai/packages/bedrock"
	"github.com/Origens-Dev/go-ai/packages/community/openrouter"
	"github.com/Origens-Dev/go-ai/packages/vertex"
	"github.com/Origens-Dev/go-temporal-ai-sdk/updates"
)

const (
	defaultAIMaxSteps = 8

	// EnvHostReportSocket is the slot-private host-report UDS. LanguageModel
	// stats this path at resolve time and must not dial Cloud Map.
	EnvHostReportSocket = "GOBEYOND_HOST_REPORT_SOCKET"
	// EnvHostedRuntime marks a hosted app or worker slot. Catalog model ids
	// then require the host-report socket instead of falling through to an
	// ambient OPENROUTER_API_KEY.
	EnvHostedRuntime = "GOBEYOND_HOSTED_RUNTIME"

	languageModelViaProvider  = "provider"
	languageModelViaInference = "inference"
	languageModelViaLegacy    = "legacy"
	languageModelViaGateway   = "gateway"
	languageModelViaLocalEnv  = "local-env"
)

// AITool is the Go AI SDK tool definition used by a filesystem agent. Keeping
// the alias here lets authored agents define tools without importing framework
// runtime packages.
type AITool = ai.Tool

// DurableUpdateStore is the customer-owned durable half of a hosted agent
// conversation connector. GoBeyond never receives its credentials. Hosted
// workers compose it with the slot-private host review publisher; local
// workers keep using the durable store without requiring platform services.
type DurableUpdateStore interface {
	updates.PreviewStore
	updates.RecordStore
}

// AIConfig declares a framework-owned model/tool loop. Model is an OpenRouter
// catalog id (for example openai/gpt-4o-mini or google/gemini-2.5-flash).
// Known first-segment providers stay {openrouter, anthropic, bedrock, vertex};
// do not author openai/, google/, x-ai/, or grok/ as built-in providers.
//
// Inference selects a process-local BYOK provider (openrouter, vertex,
// anthropic, or bedrock) and is an unmetered hosted bypass. It is not copied
// into the agents manifest or Temporal workflow input. Provider may be
// supplied for a custom provider; credentials stay runtime-only.
type AIConfig struct {
	TaskQueue string
	Durable   bool
	Realtime  bool
	Public    bool

	Model        string
	Inference    string
	MaxSteps     int
	Tools        map[string]ai.Tool
	Provider     ai.Provider
	Instructions string
	Revision     string

	DurableUpdates             DurableUpdateStore
	OnReviewPublicationFailure func(context.Context, updates.UpdateEvent, error)
}

func (config AIConfig) baseConfig() Config {
	return Config{TaskQueue: config.TaskQueue, Durable: config.Durable, Realtime: config.Realtime, Public: config.Public}
}

// AIDefinition is the fixed conversational agent definition produced by
// DefineAI. The compiler fills Instructions and Revision from instructions.md
// and the compiled filesystem definition.
type AIDefinition struct {
	Config Config
	AI     AIConfig
	Slots  Slots
}

// DefineAI declares a framework-owned conversational AI agent. The authored
// folder must also contain instructions.md; the project compiler embeds it in
// the generated registration.
func DefineAI(config AIConfig, slots ...Slots) AIDefinition {
	definition := AIDefinition{Config: config.baseConfig(), AI: config}
	if len(slots) > 0 {
		definition.Slots = slots[0]
	}
	return definition
}

// ValidateRegistration rejects capabilities that the native GoBeyond agent
// transport cannot complete safely yet. Approval-gated tools must not enter
// either direct or durable registries until pending interactions are delivered
// through the native session event contract.
func (definition AIDefinition) ValidateRegistration() error {
	toolIDs := make([]string, 0, len(definition.AI.Tools))
	for toolID := range definition.AI.Tools {
		toolIDs = append(toolIDs, toolID)
	}
	sort.Strings(toolIDs)
	for _, toolID := range toolIDs {
		tool := definition.AI.Tools[toolID]
		if !tool.RequiresApproval && tool.NeedsApproval == nil {
			continue
		}
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			name = toolID
		}
		return fmt.Errorf("AI agent tool %q requires approval, but native approval delivery is not available", name)
	}
	return nil
}

// AIMessage is the text-message subset shared by the native HTTP client and
// AI SDK chat transports. Content supports simple clients; Parts supports AI
// SDK UIMessage text parts.
type AIMessage struct {
	Role    string          `json:"role"`
	Content string          `json:"content,omitempty"`
	Parts   []AIMessagePart `json:"parts,omitempty"`
}

type AIMessagePart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// AIInput accepts both prompt-oriented callers and AI SDK chat transports.
// Prompt wins over the Message/Text convenience aliases when more than one is
// provided.
type AIInput struct {
	Prompt   string      `json:"prompt,omitempty"`
	Message  string      `json:"message,omitempty"`
	Text     string      `json:"text,omitempty"`
	Messages []AIMessage `json:"messages,omitempty"`
}

// AIOutput is the stable native agent.output payload for both direct and
// durable AI agents.
type AIOutput struct {
	Text            string `json:"text"`
	FinishReason    string `json:"finishReason,omitempty"`
	RawFinishReason string `json:"rawFinishReason,omitempty"`
	Model           string `json:"model,omitempty"`
}

// Invoke runs the Go AI SDK tool loop without exposing provider construction,
// prompt conversion, or callback plumbing to authored agents.
func (definition AIDefinition) Invoke(ctx context.Context, actor Actor, input AIInput) (AIOutput, error) {
	if err := actor.Validate(); err != nil {
		return AIOutput{}, err
	}
	agent, call, err := definition.preparedAgent(actor, input)
	if err != nil {
		return AIOutput{}, err
	}
	result, err := agent.Generate(ctx, call)
	if err != nil {
		return AIOutput{}, err
	}
	if result == nil {
		return AIOutput{}, errors.New("AI agent returned no result")
	}
	return AIOutput{
		Text: result.Text, FinishReason: result.FinishReason,
		RawFinishReason: result.RawFinishReason, Model: strings.TrimSpace(definition.AI.Model),
	}, nil
}

// Stream starts the Go AI SDK streaming tool loop. The caller owns consuming
// the returned stream before reading its final result fields.
func (definition AIDefinition) Stream(ctx context.Context, actor Actor, input AIInput) (*ai.StreamTextResult, error) {
	if err := actor.Validate(); err != nil {
		return nil, err
	}
	agent, call, err := definition.preparedAgent(actor, input)
	if err != nil {
		return nil, err
	}
	return agent.Stream(ctx, ai.AgentStreamOptions{AgentCallOptions: call})
}

func (definition AIDefinition) preparedAgent(actor Actor, input AIInput) (*ai.ToolLoopAgent, ai.AgentCallOptions, error) {
	model, err := definition.LanguageModel()
	if err != nil {
		return nil, ai.AgentCallOptions{}, err
	}
	messages, err := input.aiMessages()
	if err != nil {
		return nil, ai.AgentCallOptions{}, err
	}
	maxSteps := definition.AI.MaxSteps
	if maxSteps <= 0 {
		maxSteps = defaultAIMaxSteps
	}
	settings := ai.ToolLoopAgentSettings{
		Instructions: strings.TrimSpace(definition.AI.Instructions),
		Model:        model,
		Tools:        definition.AI.Tools,
		StopWhen:     []ai.StopCondition{ai.StepCount(maxSteps)},
		PrepareStep: func(ai.PrepareStepOptions) (*ai.PrepareStepResult, error) {
			return &ai.PrepareStepResult{ToolsContext: map[string]any{toolActorContextKey: actor}}, nil
		},
	}
	return ai.NewToolLoopAgent(settings), ai.AgentCallOptions{
		Prompt: input.prompt(), Messages: messages, AllowSystemInMessages: true,
	}, nil
}

func (input AIInput) prompt() string {
	for _, value := range []string{input.Prompt, input.Message, input.Text} {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

// PromptText and ToAIMessages expose the canonical conversion to framework
// runtimes while keeping transport-specific UI message parsing in one place.
func (input AIInput) PromptText() string { return input.prompt() }

func (input AIInput) ToAIMessages() ([]ai.Message, error) { return input.aiMessages() }

func (input AIInput) aiMessages() ([]ai.Message, error) {
	messages := make([]ai.Message, 0, len(input.Messages))
	for index, message := range input.Messages {
		text := strings.TrimSpace(message.Content)
		if text == "" {
			var builder strings.Builder
			for _, part := range message.Parts {
				if part.Type == "text" || part.Type == "reasoning" || part.Type == "" {
					builder.WriteString(part.Text)
				}
			}
			text = strings.TrimSpace(builder.String())
		}
		if text == "" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(message.Role)) {
		case "system":
			messages = append(messages, ai.SystemMessage(text))
		case "user":
			messages = append(messages, ai.UserMessage(text))
		case "assistant":
			messages = append(messages, ai.AssistantMessage(text))
		default:
			return nil, fmt.Errorf("AI message %d has unsupported role %q", index, message.Role)
		}
	}
	return messages, nil
}

// LanguageModel resolves the authored model reference. Built-in providers read
// their normal environment credentials; no secret is copied into manifests or
// Temporal workflow input. Hosted catalog ids use the host-report socket
// client; RegisterAI may call this at worker start and must only stat that
// UDS, never dial Cloud Map.
func (definition AIDefinition) LanguageModel() (ai.LanguageModel, error) {
	model, _, err := definition.resolveLanguageModel()
	return model, err
}

func (definition AIDefinition) resolveLanguageModel() (ai.LanguageModel, string, error) {
	modelRef := strings.TrimSpace(definition.AI.Model)
	if modelRef == "" {
		return nil, "", errors.New("AI agent model is required")
	}
	if definition.AI.Provider != nil {
		model := definition.AI.Provider.LanguageModel(modelRef)
		if model == nil {
			return nil, "", fmt.Errorf("AI agent model %q was not found by its custom provider", modelRef)
		}
		return model, languageModelViaProvider, nil
	}
	if inference := strings.TrimSpace(definition.AI.Inference); inference != "" {
		provider, err := builtInProvider(inference)
		if err != nil {
			return nil, "", err
		}
		model := provider.LanguageModel(modelRef)
		if model == nil {
			return nil, "", fmt.Errorf("AI agent model %q was not found", modelRef)
		}
		return model, languageModelViaInference, nil
	}
	providerName, modelID, found := strings.Cut(modelRef, "/")
	if found && strings.TrimSpace(modelID) != "" && isBuiltInProvider(providerName) {
		provider, err := builtInProvider(providerName)
		if err != nil {
			return nil, "", err
		}
		model := provider.LanguageModel(modelID)
		if model == nil {
			return nil, "", fmt.Errorf("AI agent model %q was not found", modelRef)
		}
		return model, languageModelViaLegacy, nil
	}
	if socketPath, ok := hostReportSocketPath(); ok {
		model := gatewayLanguageModel(socketPath, modelRef)
		if model == nil {
			return nil, "", fmt.Errorf("AI agent model %q was not found", modelRef)
		}
		return model, languageModelViaGateway, nil
	}
	if hostedRuntime() {
		return nil, "", fmt.Errorf("AI agent model %q requires the hosted model gateway host-report socket", modelRef)
	}
	if strings.TrimSpace(os.Getenv("OPENROUTER_API_KEY")) != "" {
		model := openrouter.New(openrouter.Settings{}).LanguageModel(modelRef)
		if model == nil {
			return nil, "", fmt.Errorf("AI agent model %q was not found", modelRef)
		}
		return model, languageModelViaLocalEnv, nil
	}
	if !found || strings.TrimSpace(modelID) == "" {
		return nil, "", fmt.Errorf("AI agent model %q must use provider/model syntax", modelRef)
	}
	return nil, "", fmt.Errorf("AI agent model provider %q is not built in; set AIConfig.Provider for a custom provider, or use a catalog id with the hosted model gateway or OPENROUTER_API_KEY", providerName)
}

func isBuiltInProvider(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "anthropic", "openrouter", "bedrock", "vertex":
		return true
	default:
		return false
	}
}

func builtInProvider(name string) (ai.Provider, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "anthropic":
		return anthropic.New(anthropic.Settings{}), nil
	case "openrouter":
		return openrouter.New(openrouter.Settings{}), nil
	case "bedrock":
		return bedrock.New(bedrock.Settings{}), nil
	case "vertex":
		return vertex.New(vertex.Settings{}), nil
	default:
		return nil, fmt.Errorf("AI agent Inference %q is not supported; use openrouter, vertex, anthropic, or bedrock", name)
	}
}

func hostedRuntime() bool {
	return strings.TrimSpace(os.Getenv(EnvHostedRuntime)) == "1"
}

// RuntimeProvider binds Temporal activity lookups to this process-local
// definition. Provider values are never serialized into workflow input.
func (definition AIDefinition) RuntimeProvider() ai.Provider {
	return aiProvider{definition: definition}
}

type aiProvider struct{ definition AIDefinition }

func (provider aiProvider) LanguageModel(modelID string) ai.LanguageModel {
	definition := provider.definition
	definition.AI.Model = modelID
	model, _ := definition.LanguageModel()
	return model
}
