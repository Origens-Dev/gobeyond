package temporalruntime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Origens-Dev/gobeyond/agents"
	"github.com/Origens-Dev/gobeyond/agents/voicecontract"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
)

func TestVoiceSessionWriteV2ErrorAfterCommitReconcilesOneMutation(t *testing.T) {
	// Real VoiceSessionWorkflow + execute_tool.v2 Update path. The first
	// attempt commits at the LocalActivity boundary, then the response is
	// lost. Retry uses a new Temporal UpdateID and the same business key;
	// reconciliation returns the committed result with exactly one write.
	resetVoiceWriteLedgerWithAuthority(t.TempDir(), newMemoryVoiceWriteStore())
	t.Cleanup(resetVoiceWriteLedger)

	var calls atomic.Int32
	committed := make(chan struct{})
	var closeOnce sync.Once
	wantCtx := admittedAgentWriteContext()
	in, req := retainVoiceSessionWriteV2Fixture(t, func(ctx context.Context, actor agents.Actor, input map[string]any) (any, error) {
		sessionID, sessionOK := agents.ToolSessionID(ctx)
		writeID, writeOK := agents.ToolWriteID(ctx)
		binding, bindingOK := agents.ToolResourceBinding(ctx)
		if !sessionOK || sessionID != wantCtx.SessionID || !writeOK || writeID == "" || !bindingOK {
			return nil, errors.New("grant write identity missing")
		}
		if _, forged := input["resource_binding"]; forged {
			return nil, errors.New("model-supplied binding was trusted")
		}
		want := resourceBindingFromGrantScope(wantCtx.Scope)
		if !binding.Equal(want) {
			return nil, errors.New("handler binding was not the verified grant projection")
		}
		if _, mailbox := actor.Metadata["mailbox_line_id"]; mailbox {
			t.Error("mailbox product field leaked onto actor metadata")
		}
		calls.Add(1)
		closeOnce.Do(func() { close(committed) })
		return map[string]any{"ok": true}, nil
	})

	var dropNext atomic.Bool
	dropNext.Store(true)
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivityWithOptions(func(ctx context.Context, payload VoiceSessionExecuteToolInput) (VoiceSessionExecuteToolResult, error) {
		out, activityErr := VoiceSessionExecuteToolActivity(ctx, payload)
		if dropNext.CompareAndSwap(true, false) {
			if activityErr != nil {
				return out, activityErr
			}
			select {
			case <-committed:
			default:
				return VoiceSessionExecuteToolResult{}, errors.New("lost response before commit")
			}
			replaceHostVoiceWriteLedger(t.TempDir())
			return VoiceSessionExecuteToolResult{}, errors.New("lost response after commit")
		}
		select {
		case <-committed:
		case <-time.After(time.Second):
			return VoiceSessionExecuteToolResult{}, errors.New("retry observed result before commit")
		}
		return out, activityErr
	}, activity.RegisterOptions{Name: voiceSessionExecuteToolActivityName})

	var firstErr, secondErr error
	var second VoiceSessionExecuteToolResult
	env.RegisterDelayedCallback(func() {
		env.UpdateWorkflow(VoiceSessionExecuteToolUpdateV2, "write-attempt-1", &testsuite.TestUpdateCallback{
			OnReject:   func(err error) { firstErr = err },
			OnComplete: func(_ interface{}, err error) { firstErr = err },
		}, req)
	}, time.Second)
	env.RegisterDelayedCallback(func() {
		env.UpdateWorkflow(VoiceSessionExecuteToolUpdateV2, "write-attempt-2", &testsuite.TestUpdateCallback{
			OnReject: func(err error) { secondErr = err },
			OnComplete: func(value interface{}, err error) {
				secondErr = err
				if result, ok := value.(VoiceSessionExecuteToolResult); ok {
					second = result
				}
			},
		}, req)
	}, 2*time.Second)

	env.ExecuteWorkflow(VoiceSessionWorkflow, in)
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if firstErr == nil || !errors.Is(firstErr, errWriteOutcomeUnknown) {
		t.Fatalf("first attempt err=%v want unknown", firstErr)
	}
	if secondErr != nil || second.Error != "" || !strings.Contains(string(second.Result), `"ok":true`) {
		t.Fatalf("reconcile result=%s err=%v", second.Result, secondErr)
	}
	if calls.Load() != 1 {
		t.Fatalf("mutations=%d want 1", calls.Load())
	}
	select {
	case <-committed:
	default:
		t.Fatal("commit barrier was never closed")
	}
}

func TestVoiceSessionWriteV2UnparseableResultIsUnknownNotSuccess(t *testing.T) {
	resetVoiceWriteLedgerWithDir(t.TempDir())
	t.Cleanup(resetVoiceWriteLedger)
	in, req := retainVoiceSessionWriteV2Fixture(t, func(context.Context, agents.Actor, map[string]any) (any, error) {
		return map[string]any{"ok": true, "extra": true}, nil
	})
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivityWithOptions(VoiceSessionExecuteToolActivity, activity.RegisterOptions{Name: voiceSessionExecuteToolActivityName})
	var updateErr error
	var succeeded bool
	env.RegisterDelayedCallback(func() {
		env.UpdateWorkflow(VoiceSessionExecuteToolUpdateV2, "write-attempt-unparseable", &testsuite.TestUpdateCallback{
			OnReject: func(err error) { updateErr = err },
			OnComplete: func(_ interface{}, err error) {
				if err != nil {
					updateErr = err
					return
				}
				succeeded = true
			},
		}, req)
	}, time.Second)
	env.ExecuteWorkflow(VoiceSessionWorkflow, in)
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if succeeded || !errors.Is(updateErr, errWriteOutcomeUnknown) {
		t.Fatalf("unparseable result classified as success err=%v succeeded=%v", updateErr, succeeded)
	}
}

func TestVoiceSessionWriteV2ForgedBindingFailsClosed(t *testing.T) {
	resetVoiceWriteLedgerWithDir(t.TempDir())
	t.Cleanup(resetVoiceWriteLedger)
	calls := 0
	in, req := retainVoiceSessionWriteV2Fixture(t, func(context.Context, agents.Actor, map[string]any) (any, error) {
		calls++
		return map[string]any{"ok": true}, nil
	})
	req.ResourceBinding = agents.ResourceBinding{Kind: "agent", ResourceID: "forged_line"}
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivityWithOptions(VoiceSessionExecuteToolActivity, activity.RegisterOptions{Name: voiceSessionExecuteToolActivityName})
	var updateErr error
	var succeeded bool
	env.RegisterDelayedCallback(func() {
		env.UpdateWorkflow(VoiceSessionExecuteToolUpdateV2, "write-attempt-forged", &testsuite.TestUpdateCallback{
			OnReject: func(err error) { updateErr = err },
			OnComplete: func(_ interface{}, err error) {
				if err != nil {
					updateErr = err
					return
				}
				succeeded = true
			},
		}, req)
	}, time.Second)
	env.ExecuteWorkflow(VoiceSessionWorkflow, in)
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if succeeded || !errors.Is(updateErr, errWriteBindingMismatch) || calls != 0 {
		t.Fatalf("forged binding err=%v succeeded=%v calls=%d", updateErr, succeeded, calls)
	}
}

func TestVoiceSessionWriteV2RejectsNonWriteDispatch(t *testing.T) {
	resetVoiceWriteLedgerWithDir(t.TempDir())
	t.Cleanup(resetVoiceWriteLedger)
	in, req := retainVoiceSessionWriteV2Fixture(t, func(context.Context, agents.Actor, map[string]any) (any, error) {
		return map[string]any{"ok": true}, nil
	})
	req.CallControl = &voicecontract.Command{}
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivityWithOptions(VoiceSessionExecuteToolActivity, activity.RegisterOptions{Name: voiceSessionExecuteToolActivityName})
	var updateErr error
	var succeeded bool
	env.RegisterDelayedCallback(func() {
		env.UpdateWorkflow(VoiceSessionExecuteToolUpdateV2, "write-attempt-control", &testsuite.TestUpdateCallback{
			OnReject: func(err error) { updateErr = err },
			OnComplete: func(_ interface{}, err error) {
				if err != nil {
					updateErr = err
					return
				}
				succeeded = true
			},
		}, req)
	}, time.Second)
	env.ExecuteWorkflow(VoiceSessionWorkflow, in)
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if succeeded || !errors.Is(updateErr, errExecuteToolV2WriteOnly) {
		t.Fatalf("non-write v2 err=%v succeeded=%v", updateErr, succeeded)
	}
}

func TestVoiceSessionWriteV1StillHandlesWrite(t *testing.T) {
	resetVoiceWriteLedgerWithDir(t.TempDir())
	t.Cleanup(resetVoiceWriteLedger)
	in, req := retainVoiceSessionWriteV2Fixture(t, func(context.Context, agents.Actor, map[string]any) (any, error) {
		return map[string]any{"ok": true}, nil
	})
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterActivityWithOptions(VoiceSessionExecuteToolActivity, activity.RegisterOptions{Name: voiceSessionExecuteToolActivityName})
	var updateErr error
	var result VoiceSessionExecuteToolResult
	env.RegisterDelayedCallback(func() {
		env.UpdateWorkflow(VoiceSessionExecuteToolUpdate, "write-attempt-v1", &testsuite.TestUpdateCallback{
			OnReject: func(err error) { updateErr = err },
			OnComplete: func(value interface{}, err error) {
				updateErr = err
				if got, ok := value.(VoiceSessionExecuteToolResult); ok {
					result = got
				}
			},
		}, req)
	}, time.Second)
	env.ExecuteWorkflow(VoiceSessionWorkflow, in)
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if updateErr != nil || result.Error != "" || !strings.Contains(string(result.Result), `"ok":true`) {
		t.Fatalf("v1 write broken result=%s err=%v", result.Result, updateErr)
	}
}

func retainVoiceSessionWriteV2Fixture(t *testing.T, handler func(context.Context, agents.Actor, map[string]any) (any, error)) (VoiceSessionInput, VoiceSessionExecuteToolInput) {
	t.Helper()
	ctxn := admittedAgentWriteContext()
	if err := ctxn.ValidateForVersion(voicecontract.Version); err != nil {
		t.Fatalf("admitted agent context rejected: %v", err)
	}
	schema, output := voiceWriteClosedSchemas()
	definition := agents.DefineAI(agents.AIConfig{Revision: ctxn.AgentRevision, Tools: map[string]agents.AITool{
		"lookup": agents.DefineTool(agents.ToolConfig{Name: "lookup", Description: "Lookup", InputSchema: schema, OutputSchema: output, VoiceWrite: true}, handler),
	}})
	_, rawManifest, manifestDigest, err := definition.CompileVoiceManifest()
	if err != nil {
		t.Fatal(err)
	}
	ctxn.ManifestDigest = manifestDigest
	reg := NewVoiceRegistry()
	reg.mu.Lock()
	reg.definitions[ctxn.AgentID] = definition
	reg.manifests = map[string][]byte{ctxn.AgentID: rawManifest}
	reg.manifestDigests = map[string]string{ctxn.AgentID: manifestDigest}
	reg.mu.Unlock()
	RetainVoiceRegistry(reg)
	t.Cleanup(func() { RetainVoiceRegistry(nil) })
	input, _ := json.Marshal(map[string]any{"q": "hello"})
	in := VoiceSessionInput{Context: &ctxn, AgentID: ctxn.AgentID, CallID: ctxn.CallID, SessionID: ctxn.SessionID, ExecutionID: ctxn.ExecutionID}
	req := VoiceSessionExecuteToolInput{
		AgentID: ctxn.AgentID, ToolName: "lookup", ToolCallID: "call-1", Input: input,
		ActorID: ctxn.ActorID, ActorKind: ctxn.ActorKind, NetworkID: ctxn.NetworkID,
		AllowedToolIDs: []string{"lookup"},
	}
	return in, req
}
