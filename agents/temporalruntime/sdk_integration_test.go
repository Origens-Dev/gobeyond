package temporalruntime

import (
	"context"
	"sync"
	"testing"

	"github.com/Origens-Dev/go-ai/packages/ai"
	"github.com/Origens-Dev/go-temporal-ai-sdk/activities"
	"github.com/Origens-Dev/go-temporal-ai-sdk/temporalai"
	"github.com/Origens-Dev/go-temporal-ai-sdk/updates"
	"github.com/Origens-Dev/gobeyond/agents"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

func TestCompiledRuntimeExecutesModelToolModelAndOwnsTerminal(t *testing.T) {
	model := ai.NewMockLanguageModel("assistant")
	var modelCalls int
	model.GenerateFunc = func(_ context.Context, options ai.LanguageModelCallOptions) (*ai.LanguageModelGenerateResult, error) {
		modelCalls++
		if modelCalls == 1 {
			return &ai.LanguageModelGenerateResult{
				Content: []ai.Part{ai.ToolCallPart{
					ToolCallID: "call-1", ToolName: "lookup",
					Input: map[string]any{"orderId": "order-1"},
				}},
				FinishReason: ai.FinishReason{Unified: ai.FinishToolCalls},
			}, nil
		}
		return &ai.LanguageModelGenerateResult{
			Content:      []ai.Part{ai.TextPart{Text: "order-1 is ready"}},
			FinishReason: ai.FinishReason{Unified: ai.FinishStop},
		}, nil
	}
	provider := ai.NewMockProvider()
	provider.LanguageModels["assistant"] = model
	type lookupInput struct {
		OrderID string `json:"orderId"`
	}
	var toolActor agents.Actor
	lookup := agents.DefineTool(agents.ToolConfig{
		Description: "Look up an order",
		InputSchema: map[string]any{
			"type": "object", "properties": map[string]any{"orderId": map[string]any{"type": "string"}},
		},
	}, func(_ context.Context, actor agents.Actor, input lookupInput) (map[string]string, error) {
		toolActor = actor
		return map[string]string{"orderId": input.OrderID, "status": "ready"}, nil
	})
	definition := agents.DefineAI(agents.AIConfig{
		Durable: true, Model: "assistant", Provider: provider,
		Tools: map[string]ai.Tool{"lookup": lookup}, Revision: "revision-1", MaxSteps: 4,
	})
	runtimes := NewAIRegistry()
	if err := RegisterAI(nil, runtimes, "support", definition); err != nil {
		t.Fatal(err)
	}
	connector := &recordingConnector{}
	acts := activities.New(activities.Options{RuntimeResolver: runtimes, UpdateConnector: connector})

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflowWithOptions(temporalai.AgentWorkflow, workflow.RegisterOptions{Name: temporalai.AgentWorkflowName})
	env.RegisterActivityWithOptions(acts.InvokeModel, activity.RegisterOptions{Name: activities.InvokeModelActivity})
	env.RegisterActivityWithOptions(acts.InvokeTool, activity.RegisterOptions{Name: activities.InvokeToolActivity})
	env.RegisterActivityWithOptions(acts.WriteRecord, activity.RegisterOptions{Name: activities.WriteRecordActivity})
	env.RegisterActivityWithOptions(acts.EndStream, activity.RegisterOptions{Name: activities.EndStreamActivity})
	env.ExecuteWorkflow(temporalai.AgentWorkflowName, temporalai.AgentInput{
		AgentID: "support", CompiledRevision: "revision-1", ModelID: "assistant",
		Instructions: "Help the customer.", Prompt: "Where is order-1?", MaxSteps: 4,
		Tools:       activities.ToolDefinitionsFromAI(definition.AI.Tools),
		Stream:      updates.Options{StreamID: "run-1", Scope: updates.Scope{AgentID: "support"}},
		ToolContext: map[string]any{"gobeyondActor": agents.Actor{ID: "user-1", Kind: "user"}},
	})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	var result temporalai.AgentResult
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatal(err)
	}
	if result.Text != "order-1 is ready" || modelCalls != 2 || toolActor.ID != "user-1" {
		t.Fatalf("result/calls/actor = %#v/%d/%#v", result, modelCalls, toolActor)
	}
	connector.mu.Lock()
	defer connector.mu.Unlock()
	if len(connector.records) < 2 {
		t.Fatalf("accepted records = %#v", connector.records)
	}
	if len(connector.terminals) != 1 || connector.terminals[0].Outcome != updates.StreamOutcomeCompleted {
		t.Fatalf("terminal events = %#v", connector.terminals)
	}
}

type recordingConnector struct {
	mu        sync.Mutex
	records   []updates.RecordUpsertEvent
	terminals []updates.StreamEndEvent
}

func (*recordingConnector) BeginPreview(context.Context, updates.PreviewBeginEvent) error { return nil }
func (*recordingConnector) CheckpointPreview(context.Context, updates.PreviewSnapshotEvent) error {
	return nil
}
func (*recordingConnector) EndPreview(context.Context, updates.PreviewEndEvent) error { return nil }
func (connector *recordingConnector) UpsertRecord(_ context.Context, event updates.RecordUpsertEvent) error {
	connector.mu.Lock()
	defer connector.mu.Unlock()
	connector.records = append(connector.records, event)
	return nil
}
func (connector *recordingConnector) EndStream(_ context.Context, event updates.StreamEndEvent) error {
	connector.mu.Lock()
	defer connector.mu.Unlock()
	connector.terminals = append(connector.terminals, event)
	return nil
}
func (*recordingConnector) PublishUpdate(context.Context, updates.UpdateEvent) error { return nil }
