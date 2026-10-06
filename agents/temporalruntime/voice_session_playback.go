package temporalruntime

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Origens-Dev/gobeyond/agents/voicecontract"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
	"time"
)

const playbackExecutionVersionChange = "operator-mailbox-playback-execution-v1"

const maxPlaybackDispatchAttempts = 3

func configureVoicePlayback(ctx workflow.Context, b *voiceToolBudget) *voicePlaybackWorkflowState {
	return newVoicePlaybackWorkflowState(b, workflow.GetVersion(ctx, playbackExecutionVersionChange, workflow.DefaultVersion, 1) == 1)
}

func exclusiveVoiceDispatch(r VoiceSessionExecuteToolInput) bool {
	n := 0
	for _, on := range []bool{r.MailboxMessage != nil, r.RemoteRead != nil, r.CallControl != nil, r.SourcePlayback != nil, r.HiddenCompletion != nil} {
		if on {
			n++
		}
	}
	return n <= 1
}

type voicePlaybackWorkflowState struct {
	budget   *voiceToolBudget
	enabled  bool
	digests  map[string]string
	pending  map[string]bool
	results  map[string]VoiceSessionExecuteToolResult
	sources  map[string]bool
	errors   map[string]error
	attempts map[string]int
}

func newVoicePlaybackWorkflowState(b *voiceToolBudget, enabled bool) *voicePlaybackWorkflowState {
	return &voicePlaybackWorkflowState{budget: b, enabled: enabled, digests: map[string]string{}, pending: map[string]bool{}, results: map[string]VoiceSessionExecuteToolResult{}, sources: map[string]bool{}, errors: map[string]error{}, attempts: map[string]int{}}
}
func (s *voicePlaybackWorkflowState) execute(ctx workflow.Context, in VoiceSessionInput, req VoiceSessionExecuteToolInput) (VoiceSessionExecuteToolResult, error) {
	fail := func(msg string) (VoiceSessionExecuteToolResult, error) {
		return VoiceSessionExecuteToolResult{}, errors.New(msg)
	}
	if !s.enabled || s.budget.policy != voicecontract.BudgetPolicyOperatorMailboxPlaybackV1 || !exclusiveVoiceDispatch(req) || (req.BudgetPolicy != "" && req.BudgetPolicy != s.budget.policy) {
		return fail("playback workflow admission unavailable")
	}
	var c voicecontract.Context
	var id, tool string
	var raw []byte
	if r := req.SourcePlayback; r != nil {
		if r.Validate() != nil {
			return fail("invalid playback source")
		}
		c, id, tool = r.Context, r.ToolCallID, r.ToolID
		raw, _ = json.Marshal(r)
	} else if r := req.HiddenCompletion; r != nil {
		if r.Validate() != nil || !s.sources[r.SourceToolCallID] || len(req.Input) != 0 && string(req.Input) != "{}" {
			return fail("invalid completion source")
		}
		c, id, tool = r.Context, r.ToolCallID, r.ToolID
		raw, _ = json.Marshal(r)
	} else {
		return fail("missing playback dispatch")
	}
	expected, e := WorkflowID(c.SessionID, c.ExecutionID)
	if e != nil || in.Context == nil || *in.Context != c || in.AgentID != c.AgentID || in.CallID != c.CallID || in.SessionID != c.SessionID || in.ExecutionID != c.ExecutionID || expected != workflow.GetInfo(ctx).WorkflowExecution.ID {
		return fail("playback workflow scope mismatch")
	}
	canonical, e := voicecontract.CanonicalJSON(raw, 16384)
	if e != nil {
		return fail("invalid playback envelope")
	}
	digest := voicecontract.Digest(canonical)
	key := tool + "/" + id
	if old, ok := s.digests[key]; ok {
		if old != digest {
			return fail("conflicting playback replay")
		}
		if e = workflow.Await(ctx, func() bool { return !s.pending[key] }); e != nil {
			return VoiceSessionExecuteToolResult{}, e
		}
		previous := s.results[key]
		if s.errors[key] == nil && previous.Error == "" && len(previous.Result) > 0 || s.attempts[key] >= maxPlaybackDispatchAttempts {
			return previous, s.errors[key]
		}
	} else if e = s.budget.consume(tool, id, digest, 0); e != nil {
		return fail("playback budget exhausted")
	}
	if e = ctx.Err(); e != nil {
		return VoiceSessionExecuteToolResult{}, e
	}
	req.BudgetPolicy = s.budget.policy
	s.digests[key] = digest
	s.pending[key] = true
	s.attempts[key]++
	result, e := executeVoicePlaybackToolLocal(ctx, req)
	s.pending[key] = false
	if e == nil && result.Error == "" && len(result.Result) == 0 {
		result.Error = "playback activity returned no result"
	}
	s.results[key] = result
	s.errors[key] = e
	if req.SourcePlayback != nil && e == nil && result.Error == "" && len(result.Result) > 0 {
		s.sources[id] = true
	}
	return result, e
}
func executeVoiceSourcePlaybackActivity(ctx context.Context, req VoiceSessionExecuteToolInput) (VoiceSessionExecuteToolResult, error) {
	if req.SourcePlayback == nil || req.SourcePlayback.Validate() != nil || req.BudgetPolicy != voicecontract.BudgetPolicyOperatorMailboxPlaybackV1 {
		return VoiceSessionExecuteToolResult{}, errors.New("invalid playback source dispatch")
	}
	return executeVoiceRegistryActivityKind(ctx, req, true, true)
}

// Playback retries are owned by the canonical workflow command, not an
// unbounded local-activity retry loop. Successful source/completion stays cached.
func executeVoicePlaybackToolLocal(ctx workflow.Context, req VoiceSessionExecuteToolInput) (VoiceSessionExecuteToolResult, error) {
	var out VoiceSessionExecuteToolResult
	ctx = workflow.WithLocalActivityOptions(ctx, workflow.LocalActivityOptions{StartToCloseTimeout: 30 * time.Second, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 1}})
	err := workflow.ExecuteLocalActivity(ctx, voiceSessionExecuteToolActivityName, req).Get(ctx, &out)
	return out, err
}
