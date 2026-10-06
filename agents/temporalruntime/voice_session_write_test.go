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
	resetVoiceWriteLedger()
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
}
