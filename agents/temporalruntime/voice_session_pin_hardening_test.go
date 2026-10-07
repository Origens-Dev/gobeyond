package temporalruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Origens-Dev/gobeyond/agents"
	"github.com/Origens-Dev/gobeyond/agents/voicecontract"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

func TestVoiceWriteRecoveredSuccessValidatedWithoutRerun(t *testing.T) {
	authority := newMemoryVoiceWriteStore()
	resetVoiceWriteLedgerWithAuthority(t.TempDir(), authority)
	calls := 0
	req := retainVoiceWriteLookup(t, func(context.Context, agents.Actor, map[string]any) (any, error) {
		calls++
		return map[string]any{"ok": true}, nil
	})
	first, err := VoiceSessionExecuteToolActivity(context.Background(), req)
	if err != nil || first.Error != "" || calls != 1 {
		t.Fatalf("first write err=%v result=%#v calls=%d", err, first, calls)
	}
	identity := voiceWriteLedgerIdentity(req.SessionID, req.ToolName, req.ToolCallID)
	rec, ok, loadErr := authority.Load(identity)
	if loadErr != nil || !ok || !rec.Complete {
		t.Fatalf("authority seed ok=%v err=%v", ok, loadErr)
	}
	rec.Result = VoiceSessionExecuteToolResult{Result: []byte(`{"ok":true,"extra":"poison"}`)}
	if err := authority.Persist(identity, rec); err != nil {
		t.Fatal(err)
	}
	replaceHostVoiceWriteLedger(t.TempDir())
	req.WriteReconcileOnly = true
	_, err = VoiceSessionExecuteToolActivity(context.Background(), req)
	if !errors.Is(err, errWriteOutcomeUnknown) {
		t.Fatalf("poisoned recovered success err=%v", err)
	}
	if calls != 1 {
		t.Fatalf("validation failure re-ran mutation calls=%d", calls)
	}
	// Invalid recovered payload must not stick as process-local complete: after
	// authority repair, the next reconcile must observe the fixed result.
	rec.Result = VoiceSessionExecuteToolResult{Result: []byte(`{"ok":true}`)}
	if err := authority.Persist(identity, rec); err != nil {
		t.Fatal(err)
	}
	repaired, err := VoiceSessionExecuteToolActivity(context.Background(), req)
	if err != nil || repaired.Error != "" || string(repaired.Result) != `{"ok":true}` {
		t.Fatalf("repaired authority still invisible: err=%v result=%#v", err, repaired)
	}
	if calls != 1 {
		t.Fatalf("repair path re-ran mutation calls=%d", calls)
	}
}

func TestVoiceWriteGenericSessionQuotaIncludesWrites(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivityWithOptions(func(context.Context, VoiceSessionExecuteToolInput) (VoiceSessionExecuteToolResult, error) {
		return VoiceSessionExecuteToolResult{Result: []byte(`{"ok":true}`)}, nil
	}, activity.RegisterOptions{Name: voiceSessionExecuteToolActivityName})
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		budget := &voiceToolBudget{policy: voicecontract.BudgetPolicyGenericV1}
		writes := newVoiceWriteWorkflowState()
		writes.budget = budget
		for i := 0; i < genericVoiceSessionToolCap; i++ {
			if err := budget.consume("list-text-messages", fmt.Sprintf("read-%d", i), "d", 0); err != nil {
				return err
			}
		}
		ctxn := voicecontract.Context{
			ExecutionID: "execution-1", OrganizationID: "org-1", ProjectID: "project-1", EnvironmentID: "env-1", NetworkID: "network-1",
			CallID: "call-write", SessionID: "session-write", ActorID: "user-1", ActorKind: "user", AgentID: "support", AgentRevision: "revision-1",
			ManifestDigest: "digest", Generation: 1, Scope: voicecontract.Scope{Kind: "agent", LineID: "line-1"},
		}
		in := VoiceSessionInput{Context: &ctxn, AgentID: ctxn.AgentID, CallID: ctxn.CallID, SessionID: ctxn.SessionID, ExecutionID: ctxn.ExecutionID}
		input, _ := json.Marshal(map[string]any{"q": "hello"})
		req, identity, replay, err := bindVoiceWriteRequest(in, VoiceSessionExecuteToolInput{
			AgentID: ctxn.AgentID, ToolName: "lookup", ToolCallID: "write-1", Input: input,
			ActorID: ctxn.ActorID, ActorKind: ctxn.ActorKind, NetworkID: ctxn.NetworkID,
		})
		if err != nil {
			return err
		}
		result, done, err := writes.reserve(ctx, identity, req.ToolName, req.ToolCallID, replay)
		if err != nil || !done || result.Error != "voice session tool quota exceeded" {
			return fmt.Errorf("write outside session quota: done=%v err=%v result=%#v", done, err, result)
		}
		return nil
	})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
}

func TestVoiceWriteExpiredApprovalStillReconcilesUnknown(t *testing.T) {
	authority := newMemoryVoiceWriteStore()
	resetVoiceWriteLedgerWithAuthority(t.TempDir(), authority)
	schema, output := voiceWriteClosedSchemas()
	calls := 0
	writeTool := agents.DefineTool(agents.ToolConfig{
		Name: "rename_network", Description: "Rename", InputSchema: schema, OutputSchema: output,
		VoiceWrite: true, RequiresApproval: true,
	}, func(context.Context, agents.Actor, map[string]any) (any, error) {
		calls++
		return map[string]any{"ok": true}, nil
	})
	definition := agents.DefineAI(agents.AIConfig{Revision: "revision-1", Tools: map[string]agents.AITool{"rename_network": writeTool}})
	_, rawManifest, manifestDigest, err := definition.CompileVoiceManifest()
	if err != nil {
		t.Fatal(err)
	}
	reg := NewVoiceRegistry()
	reg.mu.Lock()
	reg.definitions["support"] = definition
	reg.manifests = map[string][]byte{"support": rawManifest}
	reg.manifestDigests = map[string]string{"support": manifestDigest}
	reg.mu.Unlock()
	RetainVoiceRegistry(reg)
	t.Cleanup(func() {
		RetainVoiceRegistry(nil)
		resetVoiceWriteLedger()
	})
	ctxn := voicecontract.Context{
		ExecutionID: "execution-1", OrganizationID: "org-1", ProjectID: "project-1", EnvironmentID: "env-1", NetworkID: "network-1",
		CallID: "call-write", SessionID: "session-write", ActorID: "user-1", ActorKind: "user", AgentID: "support", AgentRevision: "revision-1",
		ManifestDigest: manifestDigest, Generation: 1, Scope: voicecontract.Scope{Kind: "agent", LineID: "line-1"},
	}
	input, _ := json.Marshal(map[string]any{"q": "hello"})
	base := VoiceSessionExecuteToolInput{
		AgentID: ctxn.AgentID, ToolName: "rename_network", ToolCallID: "call-1", Input: input,
		ActorID: ctxn.ActorID, ActorKind: ctxn.ActorKind, NetworkID: ctxn.NetworkID, AllowedToolIDs: []string{"rename_network"},
	}
	in := VoiceSessionInput{Context: &ctxn, AgentID: ctxn.AgentID, CallID: ctxn.CallID, SessionID: ctxn.SessionID, ExecutionID: ctxn.ExecutionID, BudgetPolicy: voicecontract.BudgetPolicyGenericV1}

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	drop := true
	env.RegisterActivityWithOptions(func(ctx context.Context, req VoiceSessionExecuteToolInput) (VoiceSessionExecuteToolResult, error) {
		out, activityErr := VoiceSessionExecuteToolActivity(ctx, req)
		if drop && req.ApprovalConfirmed {
			drop = false
			replaceHostVoiceWriteLedger(t.TempDir())
			if activityErr != nil {
				return out, activityErr
			}
			return VoiceSessionExecuteToolResult{}, errors.New("lost response")
		}
		return out, activityErr
	}, activity.RegisterOptions{Name: voiceSessionExecuteToolActivityName})
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		pendingApprovals := map[string]VoiceSessionExecuteToolInput{}
		pendingByCall := map[string]string{}
		writes := newVoiceWriteWorkflowState()
		writes.budget = &voiceToolBudget{policy: voicecontract.BudgetPolicyGenericV1}
		req, identity, replay, bindErr := bindVoiceWriteRequest(in, base)
		if bindErr != nil {
			return bindErr
		}
		if _, done, reserveErr := writes.reserve(ctx, identity, req.ToolName, req.ToolCallID, replay); reserveErr != nil || done {
			return errors.New("reserve")
		}
		result, execErr := executeVoiceWriteToolLocal(ctx, req)
		if execErr != nil || result.Approval == nil {
			return fmt.Errorf("expected approval got %#v err=%v", result, execErr)
		}
		writes.pending[identity] = false
		req.ApprovalExpiresAt = workflow.Now(ctx).Add(5 * time.Minute)
		result.Approval.ExpiresAt = req.ApprovalExpiresAt
		pendingApprovals[result.Approval.InteractionID] = req
		pendingByCall[req.ToolCallID] = result.Approval.InteractionID

		approved := pendingApprovals[result.Approval.InteractionID]
		approved.ApprovalConfirmed = true
		result, execErr = executeVoiceWriteToolLocal(ctx, approved)
		if _, finishErr := writes.finish(identity, result, execErr); !errors.Is(finishErr, errWriteOutcomeUnknown) {
			return fmt.Errorf("unknown finish: %v / %v", execErr, finishErr)
		}
		// Advance past approval expiry, then reconcile-only must still work.
		_ = workflow.Sleep(ctx, 6*time.Minute)
		if !writes.unknown[identity] {
			return errors.New("expected unknown before expired reconcile")
		}
		expired := !approved.ApprovalExpiresAt.IsZero() && !workflow.Now(ctx).Before(approved.ApprovalExpiresAt)
		if !expired {
			return errors.New("approval should be expired")
		}
		approved.WriteReconcileOnly = true
		result, execErr = executeVoiceWriteToolLocal(ctx, approved)
		result, execErr = writes.finish(identity, result, execErr)
		if execErr != nil || result.Error != "" || len(result.Result) == 0 {
			return fmt.Errorf("expired approval blocked reconcile: %#v err=%v", result, execErr)
		}
		return nil
	})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("mutations=%d want 1", calls)
	}
}

func TestMailboxPlaybackBudgetRetiredForNewWorkflows(t *testing.T) {
	raw, err := os.ReadFile("../voicecontract/testdata/command.json")
	if err != nil {
		t.Fatal(err)
	}
	var command voicecontract.Command
	if err := json.Unmarshal(raw, &command); err != nil {
		t.Fatal(err)
	}
	command.Context.Scope.Kind = "agent"
	command.Context.Scope.DIDID = ""
	command.Context.AgentID = "call-operator"
	command.Context.TransportCallID = "transport"
	command.Context.ParentCallID = "parent"
	command.Context.HopID = "hop"
	command.Context.HopCount = 1
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.OnGetVersion(mailboxBudgetVersionChange, workflow.DefaultVersion, 1).Return(workflow.Version(1))
	env.OnGetVersion(mailboxPlaybackRuntimeRetiredVersionChange, workflow.DefaultVersion, 1).Return(workflow.Version(1))
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		c := command.Context
		in := VoiceSessionInput{BudgetPolicy: voicecontract.BudgetPolicyOperatorMailboxPlaybackV1, Context: &c, AgentID: c.AgentID}
		if err := configureVoiceToolBudget(ctx, in, &voiceToolBudget{}); err == nil {
			return errors.New("retired mailbox playback runtime still executable")
		}
		in.BudgetPolicy = voicecontract.BudgetPolicyOperatorMailboxV1
		if err := configureVoiceToolBudget(ctx, in, &voiceToolBudget{}); err != nil {
			return err
		}
		in.BudgetPolicy = voicecontract.BudgetPolicyGenericV1
		c.AgentID = "support-agent"
		in.Context = &c
		in.AgentID = c.AgentID
		if err := configureVoiceToolBudget(ctx, in, &voiceToolBudget{}); err != nil {
			return err
		}
		return nil
	})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
}
