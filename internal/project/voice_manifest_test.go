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
func TestIdentityVoiceManifestForVoiceChannelWithoutControlTools(t *testing.T) {
	defs := []AgentDefinition{{
		ID: "trainer", Revision: "build-1",
		Slots: AgentSlots{Channels: []AgentChannel{{ID: "web"}, {ID: "voice", Connector: "assistant-line"}}},
		Tools: []AgentToolDefinition{{ID: "web-search"}},
	}}
	m := portableAgentsManifest(defs, "build-1")
	if err := attachVoiceManifests(&m, defs); err != nil {
		t.Fatal(err)
	}
	got := m.Agents[0]
	if got.VoiceManifest == nil || got.VoiceManifestDigest == "" {
		t.Fatal("expected identity voice manifest")
	}
	if len(got.VoiceManifest.Tools) != 0 {
		t.Fatalf("tools=%d want identity-only", len(got.VoiceManifest.Tools))
	}
	raw, digest, err := voicecontract.FreezeManifest(*got.VoiceManifest)
	if err != nil || digest != got.VoiceManifestDigest {
		t.Fatalf("freeze identity: digest=%s err=%v", digest, err)
	}
	if !strings.Contains(string(raw), `"tools":[]`) {
		t.Fatalf("raw=%s", raw)
	}
}

func TestNoVoiceManifestWithoutVoiceChannelOrControlTools(t *testing.T) {
	defs := []AgentDefinition{{
		ID: "text-only", Revision: "build-1",
		Slots: AgentSlots{Channels: []AgentChannel{{ID: "web"}}},
		Tools: []AgentToolDefinition{{ID: "web-search"}},
	}}
	m := portableAgentsManifest(defs, "build-1")
	if err := attachVoiceManifests(&m, defs); err != nil {
		t.Fatal(err)
	}
	if m.Agents[0].VoiceManifest != nil || m.Agents[0].VoiceManifestDigest != "" {
		t.Fatal("unexpected voice manifest for non-voice agent")
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

const voiceReadToolSource = `agents.DefineTool[Input, Output](agents.ToolConfig{
Name:"search_operator_directory", Description:"Find one callable destination by public label.",
InputSchema:map[string]any{"type":"object","additionalProperties":false,"properties":map[string]any{"query":map[string]any{"type":"string","minLength":1,"maxLength":64}},"required":[]string{"query"}},
OutputSchema:map[string]any{"type":"object","additionalProperties":false,"properties":map[string]any{"entries":map[string]any{"type":"array","maxItems":5,"items":map[string]any{"type":"object","additionalProperties":false,"properties":map[string]any{"destination_id":map[string]any{"type":"string","maxLength":128},"public_label":map[string]any{"type":"string","maxLength":128},"destination_class":map[string]any{"type":"string","maxLength":32,"enum":[]string{"extension","assistant","outside_pstn"}}},"required":[]string{"destination_id","public_label","destination_class"}}}},"required":[]string{"entries"}},
VoiceRemoteRead:&agents.VoiceReadPolicy{MaxResultBytes:4096},
}, handler)`

func TestVoiceManifestIncludesRemoteReadTools(t *testing.T) {
	dialExpr, err := parser.ParseExpr(voiceToolSource)
	if err != nil {
		t.Fatal(err)
	}
	dial, err := parseVoiceTool("dial-contact", dialExpr.(*ast.CallExpr))
	if err != nil {
		t.Fatal(err)
	}
	readExpr, err := parser.ParseExpr(voiceReadToolSource)
	if err != nil {
		t.Fatal(err)
	}
	read, err := parseVoiceTool("search-operator-directory", readExpr.(*ast.CallExpr))
	if err != nil {
		t.Fatal(err)
	}
	if read == nil || !read.IsRead() || read.Name != "search_operator_directory" || read.MaxResultBytes != 4096 {
		t.Fatalf("read tool=%+v", read)
	}
	defs := []AgentDefinition{{
		ID: "operator", Revision: "build-1",
		Slots: AgentSlots{Channels: []AgentChannel{{ID: "voice", Connector: "assistant-line"}}},
		Tools: []AgentToolDefinition{
			{ID: "dial-contact", VoiceControl: dial},
			{ID: "search-operator-directory", VoiceControl: read},
		},
	}}
	m := portableAgentsManifest(defs, "build-1")
	if err = attachVoiceManifests(&m, defs); err != nil {
		t.Fatal(err)
	}
	got := m.Agents[0].VoiceManifest
	if got == nil || len(got.Tools) != 2 {
		t.Fatalf("tools=%v", got)
	}
	var sawDial, sawRead bool
	for _, tool := range got.Tools {
		switch tool.ID {
		case "dial-contact":
			sawDial = !tool.IsRead()
		case "search-operator-directory":
			sawRead = tool.IsRead() && tool.Name == "search_operator_directory"
		}
	}
	if !sawDial || !sawRead {
		t.Fatalf("dial=%v read=%v tools=%+v", sawDial, sawRead, got.Tools)
	}
}

func TestVoiceManifestRejectsControlAndReadOnSameTool(t *testing.T) {
	src := strings.Replace(voiceToolSource,
		`VoiceControl:&agents.VoiceToolPolicy{DestinationClasses:[]string{"extension"},TerminalOnSuccess:true},`,
		`VoiceControl:&agents.VoiceToolPolicy{DestinationClasses:[]string{"extension"},TerminalOnSuccess:true},VoiceRemoteRead:&agents.VoiceReadPolicy{MaxResultBytes:4096},`,
		1)
	expr, err := parser.ParseExpr(src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = parseVoiceTool("dial-contact", expr.(*ast.CallExpr)); err == nil {
		t.Fatal("accepted dual voice policy")
	}
}
