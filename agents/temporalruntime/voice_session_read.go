package temporalruntime

import (
	"encoding/json"
	"errors"
	"github.com/Origens-Dev/gobeyond/agents/voicecontract"
	"go.temporal.io/sdk/workflow"
)

type voiceReadWorkflowState struct {
	digests map[string]string
	results map[string]VoiceSessionExecuteToolResult
	pending map[string]bool
}

func newVoiceReadWorkflowState() *voiceReadWorkflowState {
	return &voiceReadWorkflowState{map[string]string{}, map[string]VoiceSessionExecuteToolResult{}, map[string]bool{}}
}
func (s *voiceReadWorkflowState) execute(ctx workflow.Context, in VoiceSessionInput, req VoiceSessionExecuteToolInput) (VoiceSessionExecuteToolResult, error) {
	r := req.RemoteRead
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
	if len(s.digests) >= 2 {
		return VoiceSessionExecuteToolResult{Error: "directory search budget exhausted"}, nil
	}
	s.digests[key] = digest
	s.pending[key] = true
	result, err := executeVoiceSessionToolLocal(ctx, req)
	s.pending[key] = false
	if err != nil {
		result = VoiceSessionExecuteToolResult{Error: "directory unavailable"}
	}
	s.results[key] = result
	return result, err
}
