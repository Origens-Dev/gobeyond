package agents

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/Origens-Dev/gobeyond/agents/voicecontract"
)

func TestAuthoredVoiceManifestConsumer(t *testing.T) {
	raw, e := os.ReadFile("voicecontract/testdata/manifest.json")
	if e != nil {
		t.Fatal(e)
	}
	var golden voicecontract.Manifest
	if e = voicecontract.Decode(raw, voicecontract.MaxManifestBytes, &golden); e != nil {
		t.Fatal(e)
	}
	// Authored voice manifests are compiled on the current generic contract;
	// the fixture remains a compact v1-shaped input for migration coverage.
	golden.Version = voicecontract.Version
	var input any
	if e = json.Unmarshal(golden.Tools[0].InputSchema, &input); e != nil {
		t.Fatal(e)
	}
	tool := DefineTool(ToolConfig{Name: golden.Tools[0].Name, Description: golden.Tools[0].Description, InputSchema: input, VoiceControl: &VoiceToolPolicy{DestinationClasses: []string{"extension"}, TerminalOnSuccess: true}}, func(context.Context, Actor, map[string]any) (string, error) { return "", nil })
	d := DefineAI(AIConfig{Revision: golden.CompiledRevision, Tools: map[string]AITool{"dial-contact": tool}})
	m, _, digest, e := d.CompileVoiceManifest()
	if e != nil {
		t.Fatal(e)
	}
	golden.Revision = golden.CompiledRevision
	golden.Tools[0].ExecutionKind = "call_control"
	_, want, e := voicecontract.FreezeManifest(golden)
	if e != nil {
		t.Fatal(e)
	}
	if digest != want || len(m.Tools) != 1 {
		t.Fatal("authored manifest differs from frozen contract")
	}
	d.AI.Revision = ""
	if _, _, _, e = d.CompileVoiceManifest(); e == nil {
		t.Fatal("missing compiled revision accepted")
	}
}
func TestIdentityVoiceManifestForVoiceChannel(t *testing.T) {
	d := DefineAI(AIConfig{Revision: "build-1", Tools: map[string]AITool{"web-search": {Name: "web_search", Description: "Search"}}}, Slots{Channels: []Channel{{ID: "voice"}}})
	m, raw, digest, err := d.CompileVoiceManifest()
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Tools) != 0 || digest == "" || !strings.Contains(string(raw), `"tools":[]`) {
		t.Fatalf("identity compile: tools=%d digest=%s raw=%s", len(m.Tools), digest, raw)
	}
	d.Slots = Slots{Channels: []Channel{{ID: "web"}}}
	m, raw, digest, err = d.CompileVoiceManifest()
	if err != nil || len(m.Tools) != 0 || digest != "" || len(raw) != 0 {
		t.Fatalf("non-voice empty: tools=%d digest=%q raw=%q err=%v", len(m.Tools), digest, raw, err)
	}
}

func TestClientMetadataCannotOptInVoiceControl(t *testing.T) {
	tool := DefineTool(ToolConfig{Name: "dial"}, func(context.Context, Actor, map[string]any) (string, error) { return "", nil })
	if tool.ToolMetadata == nil {
		tool.ToolMetadata = map[string]any{}
	}
	tool.ToolMetadata["gobeyond"] = map[string]any{"voiceControl": map[string]any{"destination_classes": []string{"extension"}}}
	if _, ok := VoiceControlPolicy(tool); ok {
		t.Fatal("client metadata gained control capability")
	}
}
