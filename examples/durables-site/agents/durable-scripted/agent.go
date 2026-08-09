package durablescripted

import (
	"context"

	"github.com/Origens-Dev/go-ai/packages/ai"
	gbagents "github.com/Origens-Dev/gobeyond/agents"
)

type LookupInput struct {
	OrderID string `json:"orderId"`
}

var lookup = gbagents.DefineTool(gbagents.ToolConfig{
	Description: "Look up the deterministic demo order",
	InputSchema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"orderId": map[string]any{"type": "string"},
		},
		"required": []string{"orderId"},
	},
}, func(_ context.Context, actor gbagents.Actor, input LookupInput) (map[string]string, error) {
	return map[string]string{"actorId": actor.ID, "orderId": input.OrderID, "status": "ready"}, nil
})

var model = scriptedModel()

var provider = &ai.MockProvider{LanguageModels: map[string]ai.LanguageModel{"scripted": model}}

var Agent = gbagents.DefineAI(gbagents.AIConfig{
	Model:    "scripted",
	Provider: provider,
	Tools:    map[string]gbagents.AITool{"lookup": lookup},
	Durable:  true,
	Public:   true,
	MaxSteps: 4,
})

func scriptedModel() *ai.MockLanguageModel {
	model := ai.NewMockLanguageModel("scripted")
	model.GenerateFunc = func(_ context.Context, options ai.LanguageModelCallOptions) (*ai.LanguageModelGenerateResult, error) {
		for _, message := range options.Prompt {
			if message.Role == ai.RoleTool {
				return &ai.LanguageModelGenerateResult{
					Content:      []ai.Part{ai.TextPart{Text: "The deterministic order is ready."}},
					FinishReason: ai.FinishReason{Unified: ai.FinishStop, Raw: "stop"},
				}, nil
			}
		}
		return &ai.LanguageModelGenerateResult{
			Content: []ai.Part{ai.ToolCallPart{
				ToolCallID: "lookup-1", ToolName: "lookup",
				Input: map[string]any{"orderId": "demo-1"},
			}},
			FinishReason: ai.FinishReason{Unified: ai.FinishToolCalls, Raw: "tool-calls"},
		}, nil
	}
	return model
}
