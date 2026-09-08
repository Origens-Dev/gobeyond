package agents

import (
	"context"
	"encoding/json"
	"os"
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
