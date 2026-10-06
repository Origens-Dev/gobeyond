package project

import (
	"go/parser"
	"strings"
	"testing"

	"github.com/Origens-Dev/gobeyond/agents/voicecontract"
)

func TestVoiceBudgetDeclarationIsStaticAndCompiled(t *testing.T) {
	expr, err := parser.ParseExpr(`agents.AIConfig{Model:"google/test", VoiceBudgetPolicy:"operator_mailbox_v1"}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, _, _, _, _, _, _, err = parseAgentConfig(expr, AgentKindAI); err != nil {
		t.Fatal(err)
	}
	policy, err := parseVoiceBudgetPolicy(expr)
	if err != nil || policy != voicecontract.BudgetPolicyOperatorMailboxV1 {
		t.Fatalf("policy=%q %v", policy, err)
	}
	generated, err := generatedAgentRegistration(AgentDefinition{ID: "call-operator", Kind: AgentKindAI, PackageName: "operator", VoiceBudgetPolicy: policy})
	if err != nil || !strings.Contains(string(generated), `definition.AI.VoiceBudgetPolicy = "operator_mailbox_v1"`) {
		t.Fatalf("generated declaration missing %s %v", generated, err)
	}
	expr, _ = parser.ParseExpr(`agents.AIConfig{Model:"google/test",VoiceBudgetPolicy:callerMetadata}`)
	if _, _, _, _, _, _, _, _, _, _, err = parseAgentConfig(expr, AgentKindAI); err == nil {
		t.Fatal("dynamic policy admitted")
	}
}
func TestVoiceBudgetCompilerRejectsIncompleteDeclaration(t *testing.T) {
	definitions := []AgentDefinition{{ID: "call-operator", Revision: "revision", VoiceBudgetPolicy: voicecontract.BudgetPolicyOperatorMailboxV1}}
	manifest := portableAgentsManifest(definitions, "revision")
	if err := attachVoiceManifests(&manifest, definitions); err == nil {
		t.Fatal("budget without mailbox tool declaration admitted")
	}
}

func TestGenericVoiceBudgetCompilesWithoutProductAgent(t *testing.T) {
	expr, err := parser.ParseExpr(`agents.AIConfig{Model:"google/test", VoiceBudgetPolicy:"generic_v1"}`)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := parseVoiceBudgetPolicy(expr)
	if err != nil || policy != voicecontract.BudgetPolicyGenericV1 {
		t.Fatalf("policy=%q %v", policy, err)
	}
	generated, err := generatedAgentRegistration(AgentDefinition{ID: "assistant", Kind: AgentKindAI, PackageName: "assistant", VoiceBudgetPolicy: policy})
	if err != nil || !strings.Contains(string(generated), `definition.AI.VoiceBudgetPolicy = "generic_v1"`) {
		t.Fatalf("generated declaration missing %s %v", generated, err)
	}
	definitions := []AgentDefinition{{
		ID: "assistant", Revision: "revision", VoiceBudgetPolicy: voicecontract.BudgetPolicyGenericV1,
		Slots: AgentSlots{Channels: []AgentChannel{{ID: "voice", Connector: "assistant-line"}}},
	}}
	published := portableAgentsManifest(definitions, "revision")
	if err := attachVoiceManifests(&published, definitions); err != nil {
		t.Fatal(err)
	}
	got := published.Agents[0]
	if got.VoiceManifest == nil || got.VoiceManifest.BudgetPolicy != voicecontract.BudgetPolicyGenericV1 || len(got.VoiceManifest.Tools) != 0 {
		t.Fatalf("generic skeleton: %#v", got.VoiceManifest)
	}
}
