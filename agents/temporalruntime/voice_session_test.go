package temporalruntime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Origens-Dev/go-ai/packages/ai"
	"github.com/Origens-Dev/gobeyond/agents"
)

func TestVoiceSessionExecuteToolActivityRegistryMiss(t *testing.T) {
	RetainVoiceRegistry(nil)
	out, err := VoiceSessionExecuteToolActivity(context.Background(), VoiceSessionExecuteToolInput{
		AgentID: "call-operator", ToolName: "lookup",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Error == "" {
		t.Fatalf("expected registry error, got %+v", out)
	}
}

func TestVoiceSessionExecuteToolActivityRunsTool(t *testing.T) {
	reg := NewVoiceRegistry()
	reg.mu.Lock()
	reg.definitions["call-operator"] = agents.AIDefinition{
		AI: agents.AIConfig{
			Tools: map[string]ai.Tool{
				"lookup": {
					Name: "lookup",
					Execute: func(ctx context.Context, call ai.ToolCall, _ ai.ToolExecutionOptions) (any, error) {
						args, _ := call.Input.(map[string]any)
						return map[string]any{"ok": true, "q": args["q"]}, nil
					},
				},
			},
		},
	}
	reg.mu.Unlock()
	RetainVoiceRegistry(reg)
	t.Cleanup(func() { RetainVoiceRegistry(nil) })

	input, _ := json.Marshal(map[string]any{"q": "hello"})
	out, err := VoiceSessionExecuteToolActivity(context.Background(), VoiceSessionExecuteToolInput{
		AgentID: "call-operator", ToolName: "lookup", ToolCallID: "call_1", Input: input,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Error != "" {
		t.Fatalf("unexpected error: %s", out.Error)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Result, &got); err != nil {
		t.Fatal(err)
	}
	if got["ok"] != true {
		t.Fatalf("%v", got)
	}
}
