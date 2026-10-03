package agents

import (
	"context"
	"testing"

	"github.com/Origens-Dev/gobeyond/agents/voicecontract"
)

func TestVoiceBudgetFrozenOptInChangesManifestAndRequiresEligibleTools(t *testing.T) {
	schema := map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{}, "required": []string{}}
	tools := map[string]AITool{}
	for _, id := range []string{"list-text-messages", "get-text-message"} {
		tools[id] = DefineTool(ToolConfig{Name: id, Description: "Read calling line messages", InputSchema: schema, OutputSchema: schema, VoiceRemoteRead: &VoiceReadPolicy{MaxResultBytes: 4096}}, func(context.Context, Actor, map[string]any) (map[string]any, error) { return map[string]any{}, nil })
	}
	tools["dial-contact"] = DefineTool(ToolConfig{Name: "dial_contact", Description: "Place an authorized call", InputSchema: schema, VoiceControl: &VoiceToolPolicy{DestinationClasses: []string{"extension"}, TerminalOnSuccess: true}}, func(context.Context, Actor, map[string]any) (map[string]any, error) { return map[string]any{}, nil })
	d := DefineAI(AIConfig{Revision: "build", VoiceBudgetPolicy: voicecontract.BudgetPolicyOperatorMailboxV1, Tools: tools})
	m, _, digest, err := d.CompileVoiceManifest()
	if err != nil {
		t.Fatal(err)
	}
	if m.BudgetPolicy != voicecontract.BudgetPolicyOperatorMailboxV1 {
		t.Fatal("missing frozen declaration")
	}
	d.AI.VoiceBudgetPolicy = ""
	_, _, legacy, err := d.CompileVoiceManifest()
	if err != nil || digest == legacy {
		t.Fatalf("policy not bound by digest %v", err)
	}
	d.AI.VoiceBudgetPolicy = voicecontract.BudgetPolicyOperatorMailboxV1
	delete(d.AI.Tools, "get-text-message")
	if _, _, _, err = d.CompileVoiceManifest(); err == nil {
		t.Fatal("partial mailbox declaration admitted")
	}
}
