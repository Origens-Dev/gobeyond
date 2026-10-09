package agents

import (
	"context"
	"testing"

	"github.com/Origens-Dev/go-ai/packages/ai"
)

func TestBindVoiceDurableDispatcherRequiresVoiceWritePolicy(t *testing.T) {
	tool := DefineTool(ToolConfig{Name: "lookup", InputSchema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{}, "required": []string{}}}, func(context.Context, Actor, map[string]any) (map[string]any, error) {
		return map[string]any{}, nil
	})
	if _, err := BindVoiceDurableDispatcher(tool); err == nil {
		t.Fatal("bound a tool without VoiceWritePolicy")
	}
}

func TestVoiceWriteMarkerDoesNotGrantDurableDispatcher(t *testing.T) {
	schema := map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{}, "required": []string{}}
	tool := DefineTool(ToolConfig{Name: "archive_desk_note", VoiceWrite: true, InputSchema: schema}, func(context.Context, Actor, map[string]any) (map[string]any, error) {
		return map[string]any{"ok": true}, nil
	})
	if !VoiceWritePolicy(tool) || VoiceDurableWriteDispatcher(tool) {
		t.Fatal("VoiceWritePolicy must not imply the host dispatcher")
	}
	ns, _ := tool.ToolMetadata[toolMetadataNamespace].(map[string]any)
	ns["voiceDurableWriteDispatcher"] = true
	if VoiceDurableWriteDispatcher(tool) {
		t.Fatal("boolean metadata acquired durable dispatcher")
	}
	bound, err := BindVoiceDurableDispatcher(tool)
	if err != nil || !VoiceDurableWriteDispatcher(bound) || !VoiceWritePolicy(bound) {
		t.Fatalf("bind failed: %#v %v", bound.ToolMetadata, err)
	}
	if VoiceDurableWriteDispatcher(tool) {
		t.Fatal("bind mutated the original tool")
	}
}

func TestDecodedMetadataCannotAcquireDurableDispatcher(t *testing.T) {
	tool := ai.Tool{
		Name: "archive_desk_note",
		ToolMetadata: ai.ProviderMetadata{
			toolMetadataNamespace: map[string]any{
				"voiceWrite":                  true,
				"voiceDurableWriteDispatcher": "execute-tool",
			},
		},
	}
	if VoiceWritePolicy(tool) || VoiceDurableWriteDispatcher(tool) {
		t.Fatal("decoded metadata maps acquired write dispatch")
	}
}
