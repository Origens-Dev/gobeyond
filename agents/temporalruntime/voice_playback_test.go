package temporalruntime

import (
	"context"
	"github.com/Origens-Dev/gobeyond/agents"
	"testing"
)

func TestPlaybackCompletionNeverEntersProviderProjection(t *testing.T) {
	hidden := agents.DefineTool(agents.ToolConfig{Name: "complete_text_message_playback", Description: "Hidden mutation", VoicePlaybackCompletion: true}, func(context.Context, agents.Actor, map[string]any) (string, error) {
		t.Fatal("completion executed through model")
		return "", nil
	})
	tools := map[string]agents.AITool{"hidden": hidden}
	if len(liveToolsFromSelected(tools)) != 0 || len(clientToolsFromSelected(tools)) != 0 {
		t.Fatal("hidden completion exposed to Live provider")
	}
	definition := agents.DefineAI(agents.AIConfig{Tools: tools})
	if len(voiceToolsFromDefinition(definition, nil)) != 0 {
		t.Fatal("hidden completion exposed by ordinary voice")
	}
}
