package httpruntime

import (
	"context"
	"github.com/Origens-Dev/go-ai/packages/ai"
	"github.com/Origens-Dev/gobeyond/agents"
	"testing"
)

func TestDirectToolsUseConversationIdentityNotActorOrSessionMetadata(t *testing.T) {
	model := ai.NewMockLanguageModel("assistant")
	calls := 0
	model.StreamFunc = func(context.Context, ai.LanguageModelCallOptions) (*ai.LanguageModelStreamResult, error) {
		calls++
		stream := make(chan ai.StreamPart, 2)
		if calls%2 == 1 {
			stream <- ai.StreamPart{Type: "tool-call", ToolCallID: "call", ToolName: "lookup", ToolInput: `{"session_id":"model-forged"}`}
			stream <- ai.StreamPart{Type: "finish", FinishReason: ai.FinishReason{Unified: ai.FinishToolCalls}}
		} else {
			stream <- ai.StreamPart{Type: "text-delta", ID: "answer", TextDelta: "ok"}
			stream <- ai.StreamPart{Type: "finish", FinishReason: ai.FinishReason{Unified: ai.FinishStop}}
		}
		close(stream)
		return &ai.LanguageModelStreamResult{Stream: stream}, nil
	}
	provider := ai.NewMockProvider()
	provider.LanguageModels["assistant"] = model
	var seen []string
	tool := agents.DefineTool(agents.ToolConfig{Name: "lookup", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, _ agents.Actor, _ map[string]any) (string, error) {
		id, ok := agents.ToolSessionID(ctx)
		if !ok {
			t.Fatal("runtime session missing")
		}
		seen = append(seen, id)
		return "ok", nil
	})
	adapter := AdaptAI(agents.DefineAI(agents.AIConfig{Model: "assistant", Provider: provider, MaxSteps: 3, Tools: map[string]ai.Tool{"lookup": tool}}))
	actor := agents.Actor{ID: "same-user", Kind: "user", Metadata: map[string]string{"session_id": "actor-forged"}}
	for _, id := range []string{"session-one", "session-two"} {
		call := StartCall{Session: agents.Session{ID: id, Metadata: map[string]string{"session_id": "client-forged", "gobeyondSessionID": "client-forged"}}, Actor: actor, Input: []byte(`{"message":"lookup"}`)}
		if err := adapter.Start(context.Background(), call, &approvalTestEmitter{events: make(chan approvalTestEvent, 32)}); err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 2 || seen[0] != "session-one" || seen[1] != "session-two" {
		t.Fatalf("tool sessions=%v", seen)
	}
}
