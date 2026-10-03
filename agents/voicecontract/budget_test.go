package voicecontract

import (
	"encoding/json"
	"strings"
	"testing"
)

func mailboxBudgetManifest() Manifest {
	schema := json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{},"required":[]}`)
	schema, _ = CanonicalJSON(schema, MaxSchemaBytes)
	tools := []Tool{}
	for _, id := range []string{"list-text-messages", "get-text-message", "search-operator-directory"} {
		tools = append(tools, Tool{ID: id, Name: strings.ReplaceAll(id, "-", "_"), Description: "Read scoped mailbox", ExecutionKind: "read", InputSchema: schema, SchemaDigest: Digest(schema), OutputSchema: schema, OutputSchemaDigest: Digest(schema), MaxResultBytes: 4096})
	}
	tools = append(tools, Tool{ID: "dial-contact", Name: "dial_contact", Description: "Place authorized call", InputSchema: schema, SchemaDigest: Digest(schema), DestinationClasses: []string{"extension"}, TerminalOnSuccess: true})
	return Manifest{Version: Version, Revision: "revision", CompiledRevision: "revision", BudgetPolicy: BudgetPolicyOperatorMailboxV1, Tools: tools}
}
func TestOperatorMailboxBudgetRequiresFrozenDeclarationAndScope(t *testing.T) {
	m := mailboxBudgetManifest()
	c := v2TestContext()
	c.AgentID = "call-operator"
	if _, _, err := FreezeManifest(m); err != nil {
		t.Fatal(err)
	}
	if err := ValidateBudgetPolicy(BudgetPolicyOperatorMailboxV1, c, m); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Context, *Manifest){func(c *Context, m *Manifest) { c.AgentID = "other" }, func(c *Context, m *Manifest) { c.Scope.Kind = "platform_support" }, func(c *Context, m *Manifest) { m.BudgetPolicy = "" }, func(c *Context, m *Manifest) { m.Tools = m.Tools[1:] }, func(c *Context, m *Manifest) { m.Tools[0].ExecutionKind = "action" }} {
		changedContext, changed := c, m
		changed.Tools = append([]Tool(nil), m.Tools...)
		mutate(&changedContext, &changed)
		if err := ValidateBudgetPolicy(BudgetPolicyOperatorMailboxV1, changedContext, changed); err == nil {
			t.Fatal("forged or incomplete budget admitted")
		}
	}
	if _, _, err := ToolBudget(BudgetPolicyOperatorMailboxV1, "mark-text-message-read"); err == nil {
		t.Fatal("reserved mutation enabled")
	}
	m.BudgetPolicy = ""
	raw, _, err := FreezeManifest(m)
	if err != nil || strings.Contains(string(raw), "budget_policy") {
		t.Fatalf("legacy manifest changed: %s %v", raw, err)
	}
}
func TestOperatorMailboxBucketCapsPreservePlacementAndHangup(t *testing.T) {
	for id, want := range map[string]int{"list-text-messages": 4, "get-text-message": 12, "search-operator-directory": 2, "dial-contact": 2, "hang-up": 1} {
		bucket, limit, err := ToolBudget(BudgetPolicyOperatorMailboxV1, id)
		if err != nil || bucket == "" || limit != want {
			t.Fatalf("%s %s %d %v", id, bucket, limit, err)
		}
	}
}
