package agents

import "github.com/Origens-Dev/gobeyond/agents/voicecontract"

// VoicePlaybackPolicy is a static compiler-owned mapping of an authorized tool's
// exact text and immutable identity. It never asks a language model to read text.
type VoicePlaybackPolicy = voicecontract.PlaybackPolicy

func VoicePlaybackPolicyFor(tool AITool) (VoicePlaybackPolicy, bool) {
	ns, _ := tool.ToolMetadata[toolMetadataNamespace].(map[string]any)
	p, ok := ns["voicePlayback"].(VoicePlaybackPolicy)
	return p, ok
}
func VoicePlaybackCompletionPolicy(tool AITool) bool {
	ns, _ := tool.ToolMetadata[toolMetadataNamespace].(map[string]any)
	_, hidden := ns["voicePlaybackCompletion"].(voicePlaybackCompletionMarker)
	return hidden
}

// ModelTools removes hidden completion mutations from every provider projection.
// It does not authorize dispatch or enable exact-text playback.
func ModelTools(tools map[string]AITool) map[string]AITool {
	out := make(map[string]AITool, len(tools))
	for id, tool := range tools {
		if !VoicePlaybackCompletionPolicy(tool) {
			out[id] = tool
		}
	}
	return out
}

// A decoded metadata map cannot acquire completion dispatch classification.
type voicePlaybackCompletionMarker struct{}
