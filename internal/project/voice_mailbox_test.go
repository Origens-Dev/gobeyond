package project

import (
	"context"
	"encoding/json"
	"github.com/Origens-Dev/gobeyond/agents"
	"github.com/Origens-Dev/gobeyond/agents/voicecontract"
	"go/ast"
	"go/parser"
	"testing"
)

const mailboxSource = `agents.DefineTool(agents.ToolConfig{
Name:"leave_text_message",Description:"Leave a message",VoiceMailboxMessage:true,
InputSchema:map[string]any{"type":"object","additionalProperties":false,"properties":map[string]any{"confirmed":map[string]any{"type":"boolean"}},"required":[]string{"confirmed"}},
OutputSchema:map[string]any{"type":"object","additionalProperties":false,"properties":map[string]any{"ok":map[string]any{"type":"boolean"}},"required":[]string{"ok"}},
},handler)`

func TestMailboxCompilerRuntimeManifestParity(t *testing.T) {
	expr, err := parser.ParseExpr(mailboxSource)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := parseVoiceTool("leave-text-message", expr.(*ast.CallExpr))
	if err != nil {
		t.Fatal(err)
	}
	var input, output any
	json.Unmarshal(spec.InputSchema, &input)
	json.Unmarshal(spec.OutputSchema, &output)
	d := agents.DefineAI(agents.AIConfig{Revision: "compiled_1", Tools: map[string]agents.AITool{"leave-text-message": agents.DefineTool(agents.ToolConfig{Name: spec.Name, Description: spec.Description, VoiceMailboxMessage: true, InputSchema: input, OutputSchema: output}, func(context.Context, agents.Actor, map[string]any) (any, error) { return nil, nil })}})
	_, runtime, digest, err := d.CompileVoiceManifest()
	if err != nil {
		t.Fatal(err)
	}
	compiled, other, err := voicecontract.FreezeManifest(voicecontract.Manifest{Version: voicecontract.Version, Revision: "compiled_1", CompiledRevision: "compiled_1", Tools: []voicecontract.Tool{*spec}})
	if err != nil || digest != other || string(runtime) != string(compiled) {
		t.Fatalf("compiler/runtime differ %s %s %v", runtime, compiled, err)
	}
	if _, err := voicecontract.ValidateToolInput(*spec, []byte(`{"confirmed":true}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := voicecontract.ValidateToolInput(*spec, []byte(`{"confirmed":"true"}`)); err == nil {
		t.Fatal("string confirmation accepted")
	}
}
