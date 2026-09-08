package project

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"strings"
	"testing"

	"github.com/Origens-Dev/gobeyond/agents/voicecontract"
)

const voiceToolSource = `agents.DefineToolWithCall[Input, Output](agents.ToolConfig{
Name:"dial_contact", Description:"Call one permitted extension.",
InputSchema:map[string]any{"type":"object","additionalProperties":false,"properties":map[string]any{"destination_id":map[string]any{"type":"string","maxLength":128,"minLength":1}},"required":[]string{"destination_id"}},
VoiceControl:&agents.VoiceToolPolicy{DestinationClasses:[]string{"extension"},TerminalOnSuccess:true},
}, handler)`

func TestVoiceManifestCompilerArtifact(t *testing.T) {
	expr, err := parser.ParseExpr(voiceToolSource)
	if err != nil {
		t.Fatal(err)
	}
	tool, err := parseVoiceTool("dial-contact", expr.(*ast.CallExpr))
	if err != nil {
		t.Fatal(err)
	}
	defs := []AgentDefinition{{ID: "operator", Revision: "build-1", Tools: []AgentToolDefinition{{ID: "dial-contact", VoiceControl: tool}}}}
	m := portableAgentsManifest(defs, "build-1")
	if err = attachVoiceManifests(&m, defs); err != nil {
		t.Fatal(err)
	}
	got := m.Agents[0]
	raw, digest, err := voicecontract.FreezeManifest(*got.VoiceManifest)
	if err != nil || digest != got.VoiceManifestDigest {
		t.Fatalf("artifact digest %s %v", digest, err)
	}
	if !strings.Contains(string(raw), `"compiled_revision":"build-1"`) {
		t.Fatal("build identity missing")
	}
	encoded, err := json.Marshal(m)
	if err != nil || !strings.Contains(string(encoded), `"voiceManifest"`) {
		t.Fatalf("publication missing: %s %v", encoded, err)
	}
}
func TestVoiceManifestRejectsDynamicAndUnboundedSchemas(t *testing.T) {
	for _, src := range []string{strings.Replace(voiceToolSource, `"maxLength":128`, `"maxLength":limit`, 1), strings.Replace(voiceToolSource, `"maxLength":128,`, ``, 1), strings.Replace(voiceToolSource, `"additionalProperties":false`, `"additionalProperties":true`, 1), strings.Replace(voiceToolSource, `"type":"object"`, `"$ref":"private"`, 1)} {
		expr, err := parser.ParseExpr(src)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = parseVoiceTool("dial-contact", expr.(*ast.CallExpr)); err == nil {
			t.Fatalf("accepted dynamic/unbounded schema %s", src)
		}
	}
}
