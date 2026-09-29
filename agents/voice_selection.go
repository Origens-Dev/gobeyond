package agents

import "strings"

// VoiceToolEnabled applies a session's selection to an authored tool. A nil
// selection preserves the legacy authored default; an explicit empty selection
// disables all optional tools. IDs and names match exactly, except for the
// existing native web-search aliases. A selection never grants execution rights:
// remote tools still require their deployed manifest, grant, and approval policy.
func VoiceToolEnabled(enabled []string, ids ...string) bool {
	if enabled == nil {
		return true
	}
	for _, selected := range enabled {
		selected = strings.TrimSpace(selected)
		if selected == "" {
			continue
		}
		for _, id := range ids {
			id = strings.TrimSpace(id)
			if id != "" && (selected == id || (nativeVoiceSearch(selected) && nativeVoiceSearch(id))) {
				return true
			}
		}
	}
	return false
}

func nativeVoiceSearch(id string) bool {
	switch strings.ToLower(id) {
	case "web_search", "web-search", "search_web":
		return true
	default:
		return false
	}
}
