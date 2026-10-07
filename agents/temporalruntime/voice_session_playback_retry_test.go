package temporalruntime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/Origens-Dev/gobeyond/agents"
	"github.com/Origens-Dev/gobeyond/agents/voicecontract"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

func TestPlaybackWorkflowFailedSourceRetriesBoundedAndCannotComplete(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	r := sourceRequestFixture(t)
	id, _ := WorkflowID(r.Context.SessionID, r.Context.ExecutionID)
	env.SetStartWorkflowOptions(client.StartWorkflowOptions{ID: id})
	calls := 0
	env.RegisterActivityWithOptions(func(context.Context, VoiceSessionExecuteToolInput) (VoiceSessionExecuteToolResult, error) {
		calls++
		return VoiceSessionExecuteToolResult{}, errors.New("source unavailable")
	}, activity.RegisterOptions{Name: voiceSessionExecuteToolActivityName})
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		b := &voiceToolBudget{policy: voicecontract.BudgetPolicyOperatorMailboxPlaybackV1}
		s := newVoicePlaybackWorkflowState(b, true)
		in := VoiceSessionInput{Context: &r.Context, AgentID: r.Context.AgentID, CallID: r.Context.CallID, SessionID: r.Context.SessionID, ExecutionID: r.Context.ExecutionID}
		req := VoiceSessionExecuteToolInput{SourcePlayback: &r, Grant: "opaque"}
		for i := 0; i < 5; i++ {
			if _, e := s.execute(ctx, in, req); e == nil {
				return errors.New("activity failure replay became success")
			}
		}
		if s.attempts[r.ToolID+"/"+r.ToolCallID] != 3 || s.sources[r.ToolCallID] {
			return errors.New("retry cap/source authority failure")
		}
		h := voicecontract.HiddenCompletionRequest{Version: r.Version, Context: r.Context, ToolID: "complete-text-message-playback", ToolCallID: voicecontract.PlaybackCompletionCallID(r.Context, r.ToolCallID), SourceToolCallID: r.ToolCallID, ClipID: voicecontract.PlaybackClipID(r.Context, r.ToolCallID)}
		if _, e := s.execute(ctx, in, VoiceSessionExecuteToolInput{HiddenCompletion: &h}); e == nil {
			return errors.New("failed source unlocked completion")
		}
		changed := r
		changed.Arguments = []byte(`{"changed":true}`)
		changed.InputDigest = voicecontract.Digest(changed.Arguments)
		if _, e := s.execute(ctx, in, VoiceSessionExecuteToolInput{SourcePlayback: &changed}); e == nil {
			return errors.New("altered failed replay accepted")
		}
		return nil
	})
	if e := env.GetWorkflowError(); e != nil {
		t.Fatal(e)
	}
	if calls != 3 {
		t.Fatalf("calls=%d", calls)
	}
}

func TestPlaybackWorkflowRetriesTransientCompletionWithFreshReceipt(t *testing.T) {
	mutations := 0
	req, receipt := completionActivityFixture(t, func(ctx context.Context, _ agents.Actor, _ map[string]any) (string, error) {
		if _, ok := agents.PlaybackCompletionFromContext(ctx); !ok {
			t.Fatal("missing verified receipt")
		}
		mutations++
		return "read", nil
	})
	proofs := 0
	completionSocket(t, func(w http.ResponseWriter, r *http.Request) {
		proofs++
		if proofs == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"receipt": receipt})
	})
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	id, _ := WorkflowID(receipt.Context.SessionID, receipt.Context.ExecutionID)
	env.SetStartWorkflowOptions(client.StartWorkflowOptions{ID: id})
	sources := 0
	env.RegisterActivityWithOptions(func(ctx context.Context, in VoiceSessionExecuteToolInput) (VoiceSessionExecuteToolResult, error) {
		if in.SourcePlayback != nil {
			sources++
			if sources == 1 {
				return VoiceSessionExecuteToolResult{}, errors.New("initial source activity failure")
			}
			return VoiceSessionExecuteToolResult{Result: []byte(`{"text":"frozen"}`)}, nil
		}
		return VoiceSessionExecuteToolActivity(ctx, in)
	}, activity.RegisterOptions{Name: voiceSessionExecuteToolActivityName})
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		b := &voiceToolBudget{policy: voicecontract.BudgetPolicyOperatorMailboxPlaybackV1}
		s := newVoicePlaybackWorkflowState(b, true)
		c := receipt.Context
		in := VoiceSessionInput{Context: &c, AgentID: c.AgentID, CallID: c.CallID, SessionID: c.SessionID, ExecutionID: c.ExecutionID}
		source := voicecontract.SourcePlaybackRequest{Version: voicecontract.Version, Context: c, ToolID: receipt.ToolID, ToolCallID: receipt.ToolCallID, CompletionToolID: receipt.CompletionToolID, Arguments: []byte(`{}`), InputDigest: voicecontract.Digest([]byte(`{}`))}
		if _, e := s.execute(ctx, in, VoiceSessionExecuteToolInput{SourcePlayback: &source}); e == nil {
			return errors.New("initial source failure lost")
		}
		if _, e := s.execute(ctx, in, req); e == nil {
			return errors.New("failed source unlocked completion")
		}
		for i := 0; i < 2; i++ {
			if _, e := s.execute(ctx, in, VoiceSessionExecuteToolInput{SourcePlayback: &source}); e != nil {
				return e
			}
		}
		if _, e := s.execute(ctx, in, req); e == nil {
			return errors.New("transient CP failure became success")
		}
		// Exact replay retries the resolver, but neither source nor completion quota
		// is consumed twice. A successful completion then remains cached.
		for i := 0; i < 2; i++ {
			out, e := s.execute(ctx, in, req)
			if e != nil || len(out.Result) == 0 {
				return errors.New("fresh receipt retry failed")
			}
		}
		if s.attempts[source.ToolID+"/"+source.ToolCallID] != 2 || s.attempts[req.HiddenCompletion.ToolID+"/"+req.HiddenCompletion.ToolCallID] != 2 {
			return errors.New("successful dispatch reran")
		}
		if b.buckets["playback"] != 1 || b.buckets["completion"] != 1 {
			return errors.New("retry consumed quota again")
		}
		altered := *req.HiddenCompletion
		altered.SourceToolCallID = "other"
		if _, e := s.execute(ctx, in, VoiceSessionExecuteToolInput{HiddenCompletion: &altered}); e == nil {
			return errors.New("altered completion accepted")
		}
		return nil
	})
	if e := env.GetWorkflowError(); e != nil {
		t.Fatal(e)
	}
	if sources != 2 || mutations != 1 || proofs != 2 {
		t.Fatalf("sources%d mutations%d proofs%d", sources, mutations, proofs)
	}
}
