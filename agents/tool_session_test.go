package agents

import (
	"context"
	"encoding/json"
	"github.com/Origens-Dev/go-ai/packages/ai"
	"github.com/Origens-Dev/gobeyond/agents/internal/toolsession"
	"testing"
)

func TestToolSessionUsesRuntimeProjectionAcrossSerialization(t *testing.T) {
	actor := Actor{ID: "same-user", Kind: "user", Metadata: map[string]string{"session_id": "forged", "gobeyondSessionID": "forged"}}
	tool := DefineToolWithCall(ToolConfig{}, func(ctx context.Context, _ Actor, _ ai.ToolCall, _ map[string]any) (string, error) {
		id, ok := ToolSessionID(ctx)
		if !ok {
			return "", nil
		}
		return id, nil
	})
	for _, sessionID := range []string{"session-one", "session-two"} {
		raw, err := json.Marshal(toolsession.ExecutionContext(actor, sessionID))
		if err != nil {
			t.Fatal(err)
		}
		var decoded any
		if err = json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		result, err := tool.Execute(context.Background(), ai.ToolCall{Input: map[string]any{"session_id": "model-forged"}}, ai.ToolExecutionOptions{Context: decoded})
		if err != nil || result != sessionID {
			t.Fatalf("session %q: result=%v err=%v", sessionID, result, err)
		}
	}
	// Neither actor metadata nor model arguments create a session for old callers.
	result, err := tool.Execute(context.Background(), ai.ToolCall{Input: map[string]any{"session_id": "model-forged"}}, ai.ToolExecutionOptions{Context: map[string]any{"gobeyondActor": actor}})
	if err != nil || result != "" {
		t.Fatalf("legacy result=%v err=%v", result, err)
	}
	// Empty/malformed framework projection clears inherited state rather than leaks it.
	inherited := toolsession.WithID(context.Background(), "previous-session")
	for _, value := range []any{"", nil, 42} {
		result, err = tool.Execute(inherited, ai.ToolCall{Input: map[string]any{}}, ai.ToolExecutionOptions{Context: map[string]any{"gobeyondActor": actor, "gobeyondSessionID": value}})
		if err != nil || result != "" {
			t.Fatalf("invalid projection %v: result=%v err=%v", value, result, err)
		}
	}
	if id, ok := ToolSessionID(nil); ok || id != "" {
		t.Fatalf("nil context session=%q/%v", id, ok)
	}
}
