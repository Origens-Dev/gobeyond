package agents

type voiceMailboxMessageMarker struct{}

// VoiceMailboxMessagePolicy accepts only the private marker installed by DefineTool.
func VoiceMailboxMessagePolicy(tool AITool) bool {
	ns, ok := tool.ToolMetadata[toolMetadataNamespace].(map[string]any)
	if !ok {
		return false
	}
	_, ok = ns["voiceMailboxMessage"].(voiceMailboxMessageMarker)
	return ok
}
