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
