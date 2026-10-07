package temporalruntime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Origens-Dev/gobeyond/agents"
	"github.com/Origens-Dev/gobeyond/agents/voicecontract"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

func TestVoiceWriteWorkflowLostResponseReconcilesOneMutation(t *testing.T) {
	resetVoiceWriteLedgerWithDir(t.TempDir())
	schema, output := voiceWriteClosedSchemas()
	calls := 0
	writeTool := agents.DefineTool(agents.ToolConfig{Name: "lookup", Description: "Lookup", InputSchema: schema, OutputSchema: output, VoiceWrite: true}, func(context.Context, agents.Actor, map[string]any) (any, error) {
		calls++
		return map[string]any{"ok": true}, nil
	})
	definition := agents.DefineAI(agents.AIConfig{Revision: "revision-1", Tools: map[string]agents.AITool{
		"lookup": writeTool,
	}})
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
		AgentID: ctxn.AgentID, ToolName: "lookup", ToolCallID: "call-1", Input: input,
		ActorID: ctxn.ActorID, ActorKind: ctxn.ActorKind, NetworkID: ctxn.NetworkID,
		AllowedToolIDs: []string{"lookup"},
	}
	in := VoiceSessionInput{Context: &ctxn, AgentID: ctxn.AgentID, CallID: ctxn.CallID, SessionID: ctxn.SessionID, ExecutionID: ctxn.ExecutionID}

	drop := true
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivityWithOptions(func(ctx context.Context, req VoiceSessionExecuteToolInput) (VoiceSessionExecuteToolResult, error) {
		out, activityErr := VoiceSessionExecuteToolActivity(ctx, req)
		if drop {
			drop = false
			if activityErr != nil {
				return out, activityErr
			}
			// Replacement worker: durable reservation remains, process cache does not.
			reopenVoiceWriteLedger()
			return VoiceSessionExecuteToolResult{}, errors.New("lost response")
		}
		return out, activityErr
	}, activity.RegisterOptions{Name: voiceSessionExecuteToolActivityName})
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		writes := newVoiceWriteWorkflowState()
		req, identity, replay, bindErr := bindVoiceWriteRequest(in, base)
		if bindErr != nil {
			return bindErr
		}
		if _, done, reserveErr := writes.reserve(ctx, identity, replay); reserveErr != nil || done {
			return errors.New("first reserve")
		}
		result, execErr := executeVoiceWriteToolLocal(ctx, req)
		if _, finishErr := writes.finish(identity, result, execErr); !errors.Is(finishErr, errWriteOutcomeUnknown) {
			return errors.New("lost response was not marked unknown")
		}
		if _, done, reserveErr := writes.reserve(ctx, identity, replay); reserveErr != nil || done {
			return errors.New("unknown reserve must lookup")
		}
		req.WriteReconcileOnly = true
		result, execErr = executeVoiceWriteToolLocal(ctx, req)
		result, execErr = writes.finish(identity, result, execErr)
		if execErr != nil || result.Error != "" || len(result.Result) == 0 {
			return errors.New("reconcile failed")
		}
		cached, done, reserveErr := writes.reserve(ctx, identity, replay)
		if reserveErr != nil || !done || string(cached.Result) != string(result.Result) {
			return errors.New("cached write missing")
		}
		changed := base
		changed.Input, _ = json.Marshal(map[string]any{"q": "other"})
		_, _, changedReplay, bindErr := bindVoiceWriteRequest(in, changed)
		if bindErr != nil {
			return bindErr
		}
		if _, _, reserveErr = writes.reserve(ctx, identity, changedReplay); !errors.Is(reserveErr, errWriteConflict) {
			return errors.New("changed input reused tool_call_id")
		}
		return nil
	})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("mutations=%d want 1", calls)
	}
	if drop {
		t.Fatal("lost-response wrapper never ran")
	}
	ledgerID := voiceWriteLedgerIdentity(in.SessionID, base.ToolName, base.ToolCallID)
	complete, unknown, metered, result := voiceWriteLedgerState(ledgerID)
	if !complete || unknown || !metered || len(result.Result) == 0 {
		t.Fatalf("durable ledger complete=%v unknown=%v metered=%v result=%s", complete, unknown, metered, result.Result)
	}
}

func TestVoiceWriteEmptyCacheRetryAfterCommitIsOneMutation(t *testing.T) {
	resetVoiceWriteLedgerWithDir(t.TempDir())
	calls := 0
	req := retainVoiceWriteLookup(t, func(context.Context, agents.Actor, map[string]any) (any, error) {
		calls++
		return map[string]any{"ok": true}, nil
	})
	first, err := VoiceSessionExecuteToolActivity(context.Background(), req)
	if err != nil || first.Error != "" || calls != 1 {
		t.Fatalf("commit first=%#v err=%v calls=%d", first, err, calls)
	}
	reopenVoiceWriteLedger()
	second, err := VoiceSessionExecuteToolActivity(context.Background(), req)
	if err != nil || second.Error != "" || calls != 1 || string(second.Result) != string(first.Result) {
		t.Fatalf("empty-cache retry first=%s second=%s err=%v calls=%d", first.Result, second.Result, err, calls)
	}
	complete, unknown, metered, result := voiceWriteLedgerState(voiceWriteLedgerIdentity(req.SessionID, req.ToolName, req.ToolCallID))
	if !complete || unknown || !metered || string(result.Result) != string(first.Result) {
		t.Fatalf("metering not retained complete=%v unknown=%v metered=%v result=%s", complete, unknown, metered, result.Result)
	}
}

func TestVoiceWriteEmptyCacheUnknownDoesNotExecute(t *testing.T) {
	resetVoiceWriteLedgerWithDir(t.TempDir())
	calls := 0
	req := retainVoiceWriteLookup(t, func(context.Context, agents.Actor, map[string]any) (any, error) {
		calls++
		return map[string]any{"ok": true}, nil
	})
	canonical, err := voicecontract.CanonicalJSON(req.Input, voicecontract.MaxSchemaBytes)
	if err != nil {
		t.Fatal(err)
	}
	req.Input = canonical
	digest := voicecontract.Digest(canonical)
	key, err := deriveVoiceWriteKey(req.SessionID, req.CallID, req.ToolName, req.ToolCallID, digest)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := voiceWriteReplayDigest(req, digest)
	if err != nil {
		t.Fatal(err)
	}
	seedVoiceWriteUnknown(voiceWriteLedgerIdentity(req.SessionID, req.ToolName, req.ToolCallID), key, replay)
	reopenVoiceWriteLedger()
	if _, err := VoiceSessionExecuteToolActivity(context.Background(), req); !errors.Is(err, errWriteOutcomeUnknown) {
		t.Fatalf("unknown after empty cache err=%v", err)
	}
	if calls != 0 {
		t.Fatalf("unknown empty-cache retry executed handler calls=%d", calls)
	}

	resetVoiceWriteLedgerWithDir(t.TempDir())
	req.WriteReconcileOnly = true
	if _, err := VoiceSessionExecuteToolActivity(context.Background(), req); !errors.Is(err, errWriteOutcomeUnknown) {
		t.Fatalf("reconcile-only miss err=%v", err)
	}
	if calls != 0 {
		t.Fatalf("reconcile-only miss executed handler calls=%d", calls)
	}
}

func retainVoiceWriteLookup(t *testing.T, handler func(context.Context, agents.Actor, map[string]any) (any, error)) VoiceSessionExecuteToolInput {
	t.Helper()
	schema, output := voiceWriteClosedSchemas()
	definition := agents.DefineAI(agents.AIConfig{Revision: "revision-1", Tools: map[string]agents.AITool{
		"lookup": agents.DefineTool(agents.ToolConfig{Name: "lookup", Description: "Lookup", InputSchema: schema, OutputSchema: output, VoiceWrite: true}, handler),
	}})
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
	input, _ := json.Marshal(map[string]any{"q": "hello"})
	return VoiceSessionExecuteToolInput{
		AgentID: "support", ToolName: "lookup", ToolCallID: "call-1", Input: input,
		ActorID: "user-1", ActorKind: "user", AllowedToolIDs: []string{"lookup"},
		ManifestDigest: manifestDigest, AgentRevision: "revision-1",
		SessionID: "session-write", CallID: "call-write",
	}
}
