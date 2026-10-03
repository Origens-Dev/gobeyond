package agents

import (
	"context"
	"testing"
)

func TestHiddenPlaybackCompletionExcludedAndMetadataCannotSpoof(t *testing.T) {
	handler := func(context.Context, Actor, map[string]any) (map[string]any, error) { return nil, nil }
	hidden := DefineTool(ToolConfig{Name: "complete_text_message_playback", VoicePlaybackCompletion: true}, handler)
	visible := DefineTool(ToolConfig{Name: "visible"}, handler)
	if len(ModelTools(map[string]AITool{"hidden": hidden, "visible": visible})) != 1 {
		t.Fatal("hidden completion exposed")
	}
	visible.ToolMetadata = map[string]any{toolMetadataNamespace: map[string]any{"voicePlayback": map[string]any{"TextField": "evil"}}}
	if _, ok := VoicePlaybackPolicyFor(visible); ok {
		t.Fatal("untyped caller metadata acquired playback")
	}
}

func TestPlaybackRejectsApprovalAndDecodedCompletionMarker(t *testing.T) {
	handler := func(context.Context, Actor, map[string]any) (string, error) { return "", nil }
	tool := DefineTool(ToolConfig{Name: "play", VoicePlayback: &VoicePlaybackPolicy{}, RequiresApproval: true}, handler)
	d := DefineAI(AIConfig{Revision: "revision", Tools: map[string]AITool{"play": tool}})
	if _, _, _, err := d.CompileVoiceManifest(); err == nil {
		t.Fatal("approval playback declaration accepted")
	}
	tool.ToolMetadata = map[string]any{toolMetadataNamespace: map[string]any{"voicePlaybackCompletion": true}}
	if VoicePlaybackCompletionPolicy(tool) {
		t.Fatal("decoded metadata acquired hidden mutation classification")
	}
}
