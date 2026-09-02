package agents

import "strings"

// Canonical session metadata keys for per-session overlays. Values are
// snake_case everywhere (HTTP metadata, SIP envelopes, and voice StartConfig).
const (
	MetadataKeyInstructions = "instructions"
	MetadataKeyVoiceName    = "voice_name"
)

// ResolveInstructions returns metadata["instructions"] when non-empty after
// trim; otherwise the authored base instructions.
func ResolveInstructions(base string, metadata map[string]string) string {
	if overlay := strings.TrimSpace(metadataValue(metadata, MetadataKeyInstructions)); overlay != "" {
		return overlay
	}
	return strings.TrimSpace(base)
}

// ResolveVoiceName returns metadata["voice_name"] when non-empty after trim;
// otherwise the agent default VoiceName.
func ResolveVoiceName(defaultName string, metadata map[string]string) string {
	if overlay := strings.TrimSpace(metadataValue(metadata, MetadataKeyVoiceName)); overlay != "" {
		return overlay
	}
	return strings.TrimSpace(defaultName)
}

func metadataValue(metadata map[string]string, key string) string {
	if metadata == nil {
		return ""
	}
	return metadata[key]
}
