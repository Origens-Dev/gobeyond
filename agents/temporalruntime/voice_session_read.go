package temporalruntime

import (
	"encoding/json"
	"errors"
	"github.com/Origens-Dev/gobeyond/agents/voicecontract"
	"go.temporal.io/sdk/workflow"
)

// reservedPlaybackCompletion binds a reserved completion slot to the frozen
// playback pair and authenticated completion call identity so another tool
// cannot steal the slot by reusing the call ID.
type reservedPlaybackCompletion struct {
	sourceToolID     string
	sourceCallID     string
	sourceDigest     string
	completionToolID string
}

type voiceToolBudget struct {
	count               int
	policy              string
	mailboxPlayback     string
	buckets             map[string]int
	calls               map[string]string
	reservedCompletions map[string]reservedPlaybackCompletion
}

const genericVoiceSessionToolCap = 8

func (b *voiceToolBudget) consume(toolID, callID, digest string, legacyLimit int) error {
	if b.policy == "" {
		if b.count >= legacyLimit {
			return errors.New("voice tool budget exhausted")
		}
		b.count++
		return nil
	}
	if b.calls == nil {
		b.calls = map[string]string{}
	}
	// Reserved completion slots are claimable only through the authenticated
	// playback-completion path (claimPlaybackCompletion). Ordinary consume
	// must not steal them by call-ID reuse.
	if _, reserved := b.reservedCompletions[callID]; reserved {
		return errors.New("conflicting cross-tool replay")
	}
	if _, exists := b.calls[callID]; exists {
		return errors.New("conflicting cross-tool replay")
	}
	if voicecontract.IsGenericBudgetPolicy(b.policy) {
		if b.count >= genericVoiceSessionToolCap {
			return errors.New("voice tool budget exhausted")
		}
		b.count++
		b.calls[callID] = toolID + "/" + digest
		return nil
	}
	bucket, limit, err := voicecontract.ToolBudget(b.policy, toolID)
	if err != nil {
		return err
	}
	if b.buckets == nil {
		b.buckets = map[string]int{}
	}
	if b.buckets[bucket] >= limit {
		return errors.New("voice tool budget exhausted")
	}
	b.buckets[bucket]++
	b.calls[callID] = toolID + "/" + digest
	return nil
}

// claimPlaybackCompletion consumes a slot reserved at source playback
// admission. The completion tool ID and source call ID must match the frozen
// pair binding sealed at reserve time; call-ID reuse by another tool cannot
// claim the reservation.
func (b *voiceToolBudget) claimPlaybackCompletion(completionToolID, completionCallID, digest, sourceCallID string) error {
	if b == nil || b.policy == "" || completionToolID == "" || completionCallID == "" || sourceCallID == "" {
		return errors.New("voice tool budget exhausted")
	}
	reserved, ok := b.reservedCompletions[completionCallID]
	if !ok {
		return errors.New("voice tool budget exhausted")
	}
	if reserved.completionToolID != completionToolID || reserved.sourceCallID != sourceCallID {
		return errors.New("conflicting cross-tool replay")
	}
	if b.calls == nil {
		b.calls = map[string]string{}
	}
	if _, exists := b.calls[completionCallID]; exists {
		return errors.New("conflicting cross-tool replay")
	}
	delete(b.reservedCompletions, completionCallID)
	b.calls[completionCallID] = completionToolID + "/" + digest
	return nil
}

// reservePlaybackPair admits source playback only when its authenticated
// completion slot is also available, and reserves both atomically bound to
// the frozen playback pair (source tool + completion tool + call IDs).
func (b *voiceToolBudget) reservePlaybackPair(sourceToolID, sourceCallID, sourceDigest, completionToolID, completionCallID string) error {
	if b.policy == "" || sourceToolID == "" || sourceCallID == "" || completionToolID == "" || completionCallID == "" || sourceCallID == completionCallID || sourceToolID == completionToolID {
		return errors.New("voice tool budget exhausted")
	}
	if b.calls == nil {
		b.calls = map[string]string{}
	}
	if b.reservedCompletions == nil {
		b.reservedCompletions = map[string]reservedPlaybackCompletion{}
	}
	if _, exists := b.calls[sourceCallID]; exists {
		return errors.New("conflicting cross-tool replay")
	}
	if _, exists := b.calls[completionCallID]; exists {
		return errors.New("conflicting cross-tool replay")
	}
	// A reserved completion ID cannot be reused as another source's call ID
	// (or overwritten as another pair's completion reservation).
	if _, exists := b.reservedCompletions[sourceCallID]; exists {
		return errors.New("conflicting cross-tool replay")
	}
	if _, exists := b.reservedCompletions[completionCallID]; exists {
		return errors.New("conflicting cross-tool replay")
	}
	binding := reservedPlaybackCompletion{
		sourceToolID:     sourceToolID,
		sourceCallID:     sourceCallID,
		sourceDigest:     sourceDigest,
		completionToolID: completionToolID,
	}
	if voicecontract.IsGenericBudgetPolicy(b.policy) {
		if b.count+2 > genericVoiceSessionToolCap {
			return errors.New("voice tool budget exhausted")
		}
		b.count += 2
		b.calls[sourceCallID] = sourceToolID + "/" + sourceDigest
		b.reservedCompletions[completionCallID] = binding
		return nil
	}
	sourceBucket, sourceLimit, err := voicecontract.ToolBudget(b.policy, sourceToolID)
	if err != nil {
		return err
	}
	completionBucket, completionLimit, err := voicecontract.ToolBudget(b.policy, completionToolID)
	if err != nil {
		return err
	}
	if b.buckets == nil {
		b.buckets = map[string]int{}
	}
	if b.buckets[sourceBucket] >= sourceLimit || b.buckets[completionBucket] >= completionLimit {
		return errors.New("voice tool budget exhausted")
	}
	b.buckets[sourceBucket]++
	b.buckets[completionBucket]++
	b.calls[sourceCallID] = sourceToolID + "/" + sourceDigest
	b.reservedCompletions[completionCallID] = binding
	return nil
}

type voiceReadWorkflowState struct {
	budget  *voiceToolBudget
	digests map[string]string
	results map[string]VoiceSessionExecuteToolResult
	pending map[string]bool
}

func newVoiceReadWorkflowState() *voiceReadWorkflowState {
	return &voiceReadWorkflowState{budget: &voiceToolBudget{}, digests: map[string]string{}, results: map[string]VoiceSessionExecuteToolResult{}, pending: map[string]bool{}}
}
func (s *voiceReadWorkflowState) execute(ctx workflow.Context, in VoiceSessionInput, req VoiceSessionExecuteToolInput) (VoiceSessionExecuteToolResult, error) {
	r := req.RemoteRead
	if req.BudgetPolicy != "" && req.BudgetPolicy != s.budget.policy {
		return VoiceSessionExecuteToolResult{}, errors.New("request-only budget policy rejected")
	}
	req.BudgetPolicy = s.budget.policy
	if r == nil || r.Validate() != nil || in.Context == nil || *in.Context != r.Context || in.AgentID != r.Context.AgentID || in.CallID != r.Context.CallID || in.SessionID != r.Context.SessionID || in.ExecutionID != r.Context.ExecutionID {
		return VoiceSessionExecuteToolResult{}, errors.New("read workflow scope mismatch")
	}
	expected, err := WorkflowID(r.Context.SessionID, r.Context.ExecutionID)
	if err != nil || expected != workflow.GetInfo(ctx).WorkflowExecution.ID {
		return VoiceSessionExecuteToolResult{}, errors.New("read workflow identity mismatch")
	}
	raw, _ := json.Marshal(r)
	raw, err = voicecontract.CanonicalJSON(raw, 16384)
	if err != nil {
		return VoiceSessionExecuteToolResult{}, err
	}
	digest := voicecontract.Digest(raw)
	key := r.ToolID + "/" + r.ToolCallID
	if previous, ok := s.digests[key]; ok {
		if previous != digest {
			return VoiceSessionExecuteToolResult{}, errors.New("conflicting read replay")
		}
		if err = workflow.Await(ctx, func() bool { return !s.pending[key] }); err != nil {
			return VoiceSessionExecuteToolResult{}, err
		}
		return s.results[key], nil
	}
	limit := 2
	if r.Context.Scope.Kind == "platform_support" {
		// Support can inspect several networks in one session. Keep reads bounded
		// by the same per-session limit used for authored voice actions.
		limit = maxVoiceSessionToolCalls
	}
	if err := s.budget.consume(r.ToolID, r.ToolCallID, digest, limit); err != nil {
		if r.Context.Scope.Kind == "platform_support" {
			return VoiceSessionExecuteToolResult{Error: "support read budget exhausted"}, nil
		}
		return VoiceSessionExecuteToolResult{Error: "directory search budget exhausted"}, nil
	}
	s.digests[key] = digest
	s.pending[key] = true
	result, err := executeVoiceRemoteReadToolLocal(ctx, req, voiceRemoteReadActivityTimeout)
	s.pending[key] = false
	if err != nil {
		result = VoiceSessionExecuteToolResult{Error: "directory unavailable"}
	}
	s.results[key] = result
	return result, err
}
