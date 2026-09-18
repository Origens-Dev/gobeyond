package temporal

import (
	"encoding/json"
	"strings"
)

const voiceExecuteToolActivity = "gobeyond.agents.voice_session.execute_tool"

// stampVoiceToolName copies only the product tool identifier onto the SoR
// activity payload. Grants, tool input, and call-control arguments stay out.
func stampVoiceToolName(activityType string, args []any, payload map[string]string) {
	if payload == nil || activityType != voiceExecuteToolActivity || len(args) == 0 {
		return
	}
	if name := voiceToolName(args[0]); name != "" {
		payload["tool_name"] = name
	}
}

func voiceToolName(arg any) string {
	raw, err := json.Marshal(arg)
	if err != nil {
		return ""
	}
	var probe struct {
		ToolName    string `json:"tool_name"`
		CallControl *struct {
			ToolID string `json:"tool_id"`
		} `json:"call_control"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return ""
	}
	if name := sanitizeToolName(probe.ToolName); name != "" {
		return name
	}
	if probe.CallControl != nil {
		return sanitizeToolName(probe.CallControl.ToolID)
	}
	return ""
}

func sanitizeToolName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 {
		return ""
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
		default:
			return ""
		}
	}
	return name
}
