package temporalruntime

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Origens-Dev/gobeyond/agents/voicecontract"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

func TestReservePlaybackPairRejectsLastGenericSlotWithoutCompletion(t *testing.T) {
	b := &voiceToolBudget{policy: voicecontract.BudgetPolicyGenericV1}
	for i := 0; i < genericVoiceSessionToolCap-1; i++ {
		if err := b.consume("list-text-messages", fmt.Sprintf("read-%d", i), "digest", 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.reservePlaybackPair("play-text-message", "source", "source-digest", "complete-text-message-playback", "completion_reserved"); err == nil {
		t.Fatal("last generic slot admitted playback without completion capacity")
	}
	if b.count != genericVoiceSessionToolCap-1 || len(b.reservedCompletions) != 0 {
		t.Fatalf("partial reserve leaked count=%d reserved=%v", b.count, b.reservedCompletions)
	}
	// Free one slot so a full pair fits; completion claim must not double-charge.
	b.count = genericVoiceSessionToolCap - 2
	if err := b.reservePlaybackPair("play-text-message", "source", "source-digest", "complete-text-message-playback", "completion_reserved"); err != nil {
		t.Fatal(err)
	}
	if b.count != genericVoiceSessionToolCap {
		t.Fatalf("pair reserve count=%d", b.count)
	}
	if reserved, ok := b.reservedCompletions["completion_reserved"]; !ok || reserved.completionToolID != "complete-text-message-playback" || reserved.sourceCallID != "source" {
		t.Fatalf("pair binding missing reserved=%v ok=%v", reserved, ok)
	}
	if err := b.consume("list-text-messages", "completion_reserved", "stolen", 0); err == nil {
		t.Fatal("ordinary consume stole reserved completion call ID")
	}
	if err := b.claimPlaybackCompletion("complete-text-message-playback", "completion_reserved", "completion-digest", "source"); err != nil {
		t.Fatal(err)
	}
	if b.count != genericVoiceSessionToolCap || len(b.reservedCompletions) != 0 {
		t.Fatalf("completion claim mutated cap count=%d reserved=%v", b.count, b.reservedCompletions)
	}
}

func TestReservePlaybackPairRejectsStolenCompletionTool(t *testing.T) {
	b := &voiceToolBudget{policy: voicecontract.BudgetPolicyGenericV1}
	if err := b.reservePlaybackPair("play-custom", "source", "digest", "complete-custom", "completion_reserved"); err != nil {
		t.Fatal(err)
	}
	if err := b.claimPlaybackCompletion("other-tool", "completion_reserved", "digest", "source"); err == nil {
		t.Fatal("wrong completion tool claimed reserved pair")
	}
	if err := b.claimPlaybackCompletion("complete-custom", "completion_reserved", "digest", "other-source"); err == nil {
		t.Fatal("wrong source call claimed reserved pair")
	}
	if err := b.claimPlaybackCompletion("complete-custom", "completion_reserved", "digest", "source"); err != nil {
		t.Fatal(err)
	}
}

func TestReservePlaybackPairRejectsMailboxPlaybackWhenCompletionBucketFull(t *testing.T) {
	b := &voiceToolBudget{policy: voicecontract.BudgetPolicyOperatorMailboxPlaybackV1, buckets: map[string]int{"completion": 12}}
	if err := b.reservePlaybackPair("play-text-message", "source", "digest", "complete-text-message-playback", "completion_reserved"); err == nil {
		t.Fatal("mailbox playback admitted with exhausted completion bucket")
	}
	if b.buckets["playback"] != 0 || b.buckets["completion"] != 12 || len(b.reservedCompletions) != 0 {
		t.Fatalf("partial mailbox reserve leaked buckets=%v reserved=%v", b.buckets, b.reservedCompletions)
	}
	b.buckets["completion"] = 11
	if err := b.reservePlaybackPair("play-text-message", "source-a", "digest-a", "complete-text-message-playback", "completion-a"); err != nil {
		t.Fatal(err)
	}
	if err := b.reservePlaybackPair("play-text-message", "source-b", "digest-b", "complete-text-message-playback", "completion-b"); err == nil {
		t.Fatal("second playback admitted after last completion slot reserved")
	}
	if b.buckets["playback"] != 1 || b.buckets["completion"] != 12 {
		t.Fatalf("second admit leaked buckets=%v", b.buckets)
	}
}

func TestPlaybackWorkflowReservesCompletionBeforeSourceAudio(t *testing.T) {
	for _, policy := range []string{voicecontract.BudgetPolicyGenericV1, voicecontract.BudgetPolicyOperatorMailboxPlaybackV1} {
		t.Run(policy, func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			r := sourceRequestFixture(t)
			id, _ := WorkflowID(r.Context.SessionID, r.Context.ExecutionID)
			env.SetStartWorkflowOptions(client.StartWorkflowOptions{ID: id})
			sourceCalls := 0
			env.RegisterActivityWithOptions(func(_ context.Context, req VoiceSessionExecuteToolInput) (VoiceSessionExecuteToolResult, error) {
				if req.SourcePlayback != nil {
					sourceCalls++
					return VoiceSessionExecuteToolResult{Result: []byte(`{"source":"exact"}`)}, nil
				}
				return VoiceSessionExecuteToolResult{Result: []byte(`{"complete":true}`)}, nil
			}, activity.RegisterOptions{Name: voiceSessionExecuteToolActivityName})
			env.ExecuteWorkflow(func(ctx workflow.Context) error {
				b := &voiceToolBudget{policy: policy}
				if policy == voicecontract.BudgetPolicyGenericV1 {
					for i := 0; i < genericVoiceSessionToolCap-1; i++ {
						if err := b.consume("list-text-messages", fmt.Sprintf("fill-%d", i), "d", 0); err != nil {
							return err
						}
					}
				} else {
					b.buckets = map[string]int{"completion": 12}
				}
				s := newVoicePlaybackWorkflowState(b, true)
				in := VoiceSessionInput{Context: &r.Context, AgentID: r.Context.AgentID, CallID: r.Context.CallID, SessionID: r.Context.SessionID, ExecutionID: r.Context.ExecutionID}
				if _, e := s.execute(ctx, in, VoiceSessionExecuteToolInput{SourcePlayback: &r, Grant: "opaque"}); e == nil {
					return errors.New("source admitted without completion capacity")
				}
				if policy == voicecontract.BudgetPolicyGenericV1 {
					b.count = genericVoiceSessionToolCap - 2
				} else {
					b.buckets["completion"] = 11
				}
				if _, e := s.execute(ctx, in, VoiceSessionExecuteToolInput{SourcePlayback: &r, Grant: "opaque"}); e != nil {
					return e
				}
				h := voicecontract.HiddenCompletionRequest{
					Version: r.Version, Context: r.Context, ToolID: r.CompletionToolID,
					ToolCallID:       voicecontract.PlaybackCompletionCallID(r.Context, r.ToolCallID),
					SourceToolCallID: r.ToolCallID, ClipID: voicecontract.PlaybackClipID(r.Context, r.ToolCallID),
				}
				if _, e := s.execute(ctx, in, VoiceSessionExecuteToolInput{HiddenCompletion: &h}); e != nil {
					return e
				}
				return nil
			})
			if e := env.GetWorkflowError(); e != nil {
				t.Fatal(e)
			}
			if sourceCalls != 1 {
				t.Fatalf("sourceCalls=%d", sourceCalls)
			}
		})
	}
}
