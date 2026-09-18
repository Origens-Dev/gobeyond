package temporal

import "testing"

func TestStampVoiceToolNameKeepsIdentifierOnly(t *testing.T) {
	payload := map[string]string{"task_queue": "voice"}
	stampVoiceToolName(voiceExecuteToolActivity, []any{map[string]any{
		"tool_name": "dial_contact",
		"grant":     "secret-grant",
		"input":     map[string]any{"phone": "+15551212"},
	}}, payload)
	if payload["tool_name"] != "dial_contact" {
		t.Fatalf("tool_name=%q", payload["tool_name"])
	}
	if _, ok := payload["grant"]; ok {
		t.Fatal("grant leaked into SoR payload")
	}
	if _, ok := payload["input"]; ok {
		t.Fatal("input leaked into SoR payload")
	}
	if payload["task_queue"] != "voice" {
		t.Fatalf("payload=%v", payload)
	}
}

func TestStampVoiceToolNameFallsBackToCallControlID(t *testing.T) {
	payload := map[string]string{}
	stampVoiceToolName(voiceExecuteToolActivity, []any{map[string]any{
		"call_control": map[string]any{
			"tool_id":   "dial-contact",
			"arguments": map[string]any{"to": "+1555"},
		},
	}}, payload)
	if payload["tool_name"] != "dial-contact" || len(payload) != 1 {
		t.Fatalf("payload=%v", payload)
	}
}

func TestStampVoiceToolNameRejectsUnsafeOrUnrelated(t *testing.T) {
	payload := map[string]string{}
	stampVoiceToolName(voiceExecuteToolActivity, []any{map[string]any{"tool_name": "not a tool"}}, payload)
	stampVoiceToolName("Fetch", []any{map[string]any{"tool_name": "dial_contact"}}, payload)
	if len(payload) != 0 {
		t.Fatalf("payload=%v", payload)
	}
}
