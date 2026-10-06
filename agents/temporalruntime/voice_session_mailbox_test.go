package temporalruntime

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Origens-Dev/gobeyond/agents"
	"github.com/Origens-Dev/gobeyond/agents/voicecontract"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
	"strings"
	"testing"
)

func mailboxFixture(t *testing.T, calls *int) (VoiceSessionExecuteToolInput, *VoiceRegistry) {
	t.Helper()
	schema := map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"action": map[string]any{"type": "string", "maxLength": 16, "enum": []string{"offer", "draft", "confirm", "decline"}}, "confirmed": map[string]any{"type": "boolean"}, "message_text": map[string]any{"type": "string", "maxLength": 280}}, "required": []string{"action"}}
	output := map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{"ok": map[string]any{"type": "boolean"}}, "required": []string{"ok"}}
	tool := agents.DefineTool(agents.ToolConfig{Name: voicecontract.MailboxMessageToolName, Description: "Leave a message", InputSchema: schema, OutputSchema: output, VoiceMailboxMessage: true}, func(ctx context.Context, a agents.Actor, _ map[string]any) (any, error) {
		*calls++
		if a.Metadata["network_id"] != "destination_network" || a.Metadata["mailbox_line_id"] != "parker_line" || a.Metadata["session_id"] != "session_1" || a.Metadata["operation_id"] != "" || a.Metadata["voice_session_grant"] != "current-grant" {
			return nil, errors.New("wrong scoped actor")
		}
		return map[string]any{"ok": true}, nil
	})
	d := agents.DefineAI(agents.AIConfig{Revision: "revision_1", Tools: map[string]agents.AITool{voicecontract.MailboxMessageToolID: tool}})
	_, raw, digest, err := d.CompileVoiceManifest()
	if err != nil {
		t.Fatal(err)
	}
	reg := NewVoiceRegistry()
	reg.definitions["voice-mail"] = d
	reg.manifests = map[string][]byte{"voice-mail": raw}
	reg.manifestDigests = map[string]string{"voice-mail": digest}
	c := voicecontract.Context{ExecutionID: "execution_1", OrganizationID: "org_1", ProjectID: "project_1", EnvironmentID: "env_1", NetworkID: "destination_network", CallID: "call_1", SessionID: "session_1", ActorID: "external_caller", ActorKind: "external_call", AgentID: "voice-mail", AgentRevision: "revision_1", ManifestDigest: digest, Generation: 2, TransportCallID: "call_1", ParentCallID: "parent_1", HopID: "hop_1", HopCount: 1, Scope: voicecontract.Scope{Kind: "agent", LineID: "parker_line"}}
	args := json.RawMessage(`{"action":"offer"}`)
	r := &voicecontract.MailboxMessageRequest{Version: "2", Context: c, ToolID: "leave-text-message", ToolCallID: "tool_1", Arguments: args, InputDigest: voicecontract.Digest(args)}
	return VoiceSessionExecuteToolInput{Grant: "current-grant", MailboxMessage: r}, reg
}
func TestMailboxActualRegistryScopeAndRejection(t *testing.T) {
	calls := 0
	req, reg := mailboxFixture(t, &calls)
	old := ProcessVoiceRegistry()
	RetainVoiceRegistry(reg)
	defer RetainVoiceRegistry(old)
	result, err := VoiceSessionExecuteToolActivity(context.Background(), req)
	if err != nil || string(result.Result) != `{"ok":true}` || calls != 1 {
		t.Fatalf("dispatch failed: %+v %v calls%d", result, err, calls)
	}
	for _, mutate := range []func(*VoiceSessionExecuteToolInput){
		func(r *VoiceSessionExecuteToolInput) { r.MailboxMessage.Context.AgentID = "other" },
		func(r *VoiceSessionExecuteToolInput) { r.MailboxMessage.Context.Scope.LineID = "" },
		func(r *VoiceSessionExecuteToolInput) {
			r.MailboxMessage.Context.ManifestDigest = "sha256:" + strings.Repeat("0", 64)
		},
		func(r *VoiceSessionExecuteToolInput) { r.NetworkID = "caller_network" },
		func(r *VoiceSessionExecuteToolInput) { r.RemoteRead = &voicecontract.ReadRequest{} },
		func(r *VoiceSessionExecuteToolInput) { r.Grant = "" },
		func(r *VoiceSessionExecuteToolInput) {
			r.MailboxMessage.Arguments = []byte(`{"action":"offer","mailbox_line_id":"other"}`)
			r.MailboxMessage.InputDigest = voicecontract.Digest(r.MailboxMessage.Arguments)
		},
	} {
		bad := req
		copy := *req.MailboxMessage
		bad.MailboxMessage = &copy
		mutate(&bad)
		if _, err := VoiceSessionExecuteToolActivity(context.Background(), bad); err == nil {
			t.Fatal("invalid dispatch accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := VoiceSessionExecuteToolActivity(ctx, req); err == nil {
		t.Fatal("canceled dispatch accepted")
	}
	if calls != 1 {
		t.Fatal("rejected dispatch executed handler")
	}
	if _, err := VoiceSessionExecuteToolActivity(context.Background(), VoiceSessionExecuteToolInput{AgentID: "voice-mail", ToolName: "leave_text_message"}); err == nil {
		t.Fatal("legacy dispatch accepted")
	}
}
func TestMailboxWorkflowActualActivityReplayAndBudget(t *testing.T) {
	calls := 0
	req, reg := mailboxFixture(t, &calls)
	old := ProcessVoiceRegistry()
	RetainVoiceRegistry(reg)
	defer RetainVoiceRegistry(old)
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	id, _ := WorkflowID(req.MailboxMessage.Context.SessionID, req.MailboxMessage.Context.ExecutionID)
	env.SetStartWorkflowOptions(client.StartWorkflowOptions{ID: id})
	env.RegisterActivityWithOptions(VoiceSessionExecuteToolActivity, activity.RegisterOptions{Name: voiceSessionExecuteToolActivityName})
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		s := newVoiceMailboxWorkflowState()
		c := req.MailboxMessage.Context
		in := VoiceSessionInput{Context: &c, AgentID: c.AgentID, CallID: c.CallID, SessionID: c.SessionID, ExecutionID: c.ExecutionID}
		first, err := s.execute(ctx, in, req)
		if err != nil {
			return err
		}
		second, err := s.execute(ctx, in, req)
		if err != nil || string(first.Result) != string(second.Result) {
			return errors.New("replay changed")
		}
		changed := req
		copy := *req.MailboxMessage
		changed.MailboxMessage = &copy
		copy.Arguments = []byte(`{"action":"confirm","confirmed":true}`)
		copy.InputDigest = voicecontract.Digest(copy.Arguments)
		if _, err = s.execute(ctx, in, changed); err == nil {
			return errors.New("changed input replay accepted")
		}
		c.Scope.LineID = "other"
		if _, err = s.execute(ctx, in, req); err == nil {
			return errors.New("different mailbox session accepted")
		}
		c = req.MailboxMessage.Context
		s.digests = map[string]string{}
		for i := 0; i < 16; i++ {
			s.digests[strings.Repeat("x", i+1)] = "used"
		}
		if _, err = s.execute(ctx, in, req); err == nil {
			return errors.New("budget accepted")
		}
		return nil
	})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("mutation executed%d times", calls)
	}
}
