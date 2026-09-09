package temporalruntime

import (
	"context"
	"testing"

	"github.com/Origens-Dev/gobeyond/agents"
)

func TestLegacyVoiceNeverSelectsControlTools(t *testing.T) {
	control := agents.DefineTool(agents.ToolConfig{Name: "dial_contact", VoiceControl: &agents.VoiceToolPolicy{DestinationClasses: []string{"extension"}}}, func(context.Context, agents.Actor, map[string]any) (string, error) {
		t.Fatal("control executed")
		return "", nil
	})
	d := agents.DefineAI(agents.AIConfig{Tools: map[string]agents.AITool{"dial-contact": control, "web_search": {Name: "web_search"}}})
	for _, enabled := range [][]string{nil, {"dial_contact", "web_search"}} {
		got := voiceToolsFromDefinition(d, enabled)
		if len(got) != 1 || got["web_search"].Name != "web_search" {
			t.Fatalf("legacy tool selection: %#v", got)
		}
	}
	r := NewVoiceRegistry()
	r.definitions["operator"] = d
	old := ProcessVoiceRegistry()
	RetainVoiceRegistry(r)
	defer RetainVoiceRegistry(old)
	if _, e := VoiceSessionExecuteToolActivity(context.Background(), VoiceSessionExecuteToolInput{AgentID: "operator", ToolName: "dial_contact"}); e == nil {
		t.Fatal("legacy activity accepted control tool")
	}
}
func TestVoiceManifestAccessorCopies(t *testing.T) {
	r := NewVoiceRegistry()
	r.manifests = map[string][]byte{"operator": []byte("original")}
	r.manifestDigests = map[string]string{"operator": "digest"}
	a, d, ok := r.Manifest("operator")
	if !ok || d != "digest" {
		t.Fatal("missing manifest")
	}
	a[0] = 'x'
	b, _, _ := r.Manifest("operator")
	if string(b) != "original" {
		t.Fatal("registry manifest mutated")
	}
}
