package voicecontract

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMailboxRequestAllowsBoundedUnicodeRejectsDigestAndIdentity(t *testing.T) {
	c := Context{ExecutionID: "execution", OrganizationID: "org", ProjectID: "project", EnvironmentID: "env", NetworkID: "destination", CallID: "call_1", SessionID: "session", ActorID: "caller", ActorKind: "external_call", AgentID: MailboxMessageAgentID, AgentRevision: "revision", ManifestDigest: "sha256:" + strings.Repeat("0", 64), Generation: 1, TransportCallID: "call_1", ParentCallID: "parent_1", HopID: "hop", HopCount: 1, Scope: Scope{Kind: "agent", LineID: "line"}}
	raw, _ := json.Marshal(map[string]string{"message_text": strings.Repeat("😀", 280), "action": "draft"})
	raw, _ = CanonicalJSON(raw, MaxMailboxMessageInputBytes)
	r := MailboxMessageRequest{Version: Version, Context: c, ToolID: MailboxMessageToolID, ToolCallID: "tool_1", Arguments: raw, InputDigest: Digest(raw)}
	if len(raw) <= 1024 {
		t.Fatal("fixture does not cover unicode byte budget")
	}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*MailboxMessageRequest){func(r *MailboxMessageRequest) { r.Version = LegacyVersion }, func(r *MailboxMessageRequest) { r.Context.AgentID = "operator" }, func(r *MailboxMessageRequest) { r.Context.Scope.LineID = "" }, func(r *MailboxMessageRequest) { r.ToolID = "other" }, func(r *MailboxMessageRequest) { r.InputDigest = Digest([]byte(`{}`)) }, func(r *MailboxMessageRequest) {
		r.Arguments = json.RawMessage(`{"x":"` + strings.Repeat("x", 4096) + `"}`)
		r.InputDigest = Digest(r.Arguments)
	}} {
		copy := r
		change(&copy)
		if copy.Validate() == nil {
			t.Fatal("invalid request accepted")
		}
	}
}
