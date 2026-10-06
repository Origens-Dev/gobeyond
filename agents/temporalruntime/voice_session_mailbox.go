package temporalruntime

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Origens-Dev/go-ai/packages/ai"
	"github.com/Origens-Dev/gobeyond/agents"
	"github.com/Origens-Dev/gobeyond/agents/internal/toolsession"
	"github.com/Origens-Dev/gobeyond/agents/voicecontract"
	"go.temporal.io/sdk/workflow"
)

// Mailbox mutations are neither reads, approval-UI actions, nor call-control operations.
// The application owns its offer/draft/readback/confirm protocol.
type voiceMailboxWorkflowState struct {
	digests map[string]string
	pending map[string]bool
	results map[string]VoiceSessionExecuteToolResult
}

func newVoiceMailboxWorkflowState() *voiceMailboxWorkflowState {
	return &voiceMailboxWorkflowState{map[string]string{}, map[string]bool{}, map[string]VoiceSessionExecuteToolResult{}}
}
func (s *voiceMailboxWorkflowState) execute(ctx workflow.Context, in VoiceSessionInput, req VoiceSessionExecuteToolInput) (VoiceSessionExecuteToolResult, error) {
	r := req.MailboxMessage
	if ctx.Err() != nil {
		return VoiceSessionExecuteToolResult{}, ctx.Err()
	}
	if !exclusiveVoiceDispatch(req) || req.BudgetPolicy != "" || r == nil || r.Validate() != nil || in.Context == nil || *in.Context != r.Context || in.AgentID != r.Context.AgentID || in.CallID != r.Context.CallID || in.SessionID != r.Context.SessionID || in.ExecutionID != r.Context.ExecutionID {
		return VoiceSessionExecuteToolResult{}, errors.New("mailbox workflow scope mismatch")
	}
	id, err := WorkflowID(r.Context.SessionID, r.Context.ExecutionID)
	if err != nil || id != workflow.GetInfo(ctx).WorkflowExecution.ID {
		return VoiceSessionExecuteToolResult{}, errors.New("mailbox workflow identity mismatch")
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return VoiceSessionExecuteToolResult{}, err
	}
	raw, err = voicecontract.CanonicalJSON(raw, voicecontract.MaxEnvelopeBytes)
	if err != nil {
		return VoiceSessionExecuteToolResult{}, err
	}
	digest := voicecontract.Digest(raw)
	key := r.ToolCallID
	if previous, ok := s.digests[key]; ok {
		if previous != digest {
			return VoiceSessionExecuteToolResult{}, errors.New("conflicting mailbox message replay")
		}
		if err := workflow.Await(ctx, func() bool { return !s.pending[key] }); err != nil {
			return VoiceSessionExecuteToolResult{}, err
		}
		return s.results[key], nil
	}
	if len(s.digests) >= 16 {
		return VoiceSessionExecuteToolResult{}, errors.New("mailbox message budget exhausted")
	}
	s.digests[key] = digest
	s.pending[key] = true
	result, err := executeVoiceSessionToolLocal(ctx, req)
	s.pending[key] = false
	if err != nil {
		result = VoiceSessionExecuteToolResult{Error: "mailbox message unavailable"}
	}
	s.results[key] = result
	return result, err
}

func executeVoiceMailboxActivity(ctx context.Context, req VoiceSessionExecuteToolInput) (VoiceSessionExecuteToolResult, error) {
	if ctx.Err() != nil {
		return VoiceSessionExecuteToolResult{}, ctx.Err()
	}
	r := req.MailboxMessage
	if !exclusiveVoiceDispatch(req) || req.BudgetPolicy != "" || r == nil || r.Validate() != nil || len(req.Grant) == 0 || len(req.Grant) > 8192 {
		return VoiceSessionExecuteToolResult{}, errors.New("invalid mailbox message dispatch")
	}
	c := r.Context
	if req.AgentID != "" && req.AgentID != c.AgentID || req.ActorID != "" && req.ActorID != c.ActorID || req.ActorKind != "" && req.ActorKind != c.ActorKind || req.NetworkID != "" && req.NetworkID != c.NetworkID || req.ToolCallID != "" && req.ToolCallID != r.ToolCallID || req.ToolName != "" && req.ToolName != voicecontract.MailboxMessageToolName || req.ManifestDigest != "" && req.ManifestDigest != c.ManifestDigest || req.AgentRevision != "" && req.AgentRevision != c.AgentRevision || len(req.Input) != 0 {
		return VoiceSessionExecuteToolResult{}, errors.New("conflicting mailbox message fields")
	}
	reg := ProcessVoiceRegistry()
	if reg == nil {
		return VoiceSessionExecuteToolResult{}, errors.New("voice registry unavailable")
	}
	raw, digest, ok := reg.Manifest(c.AgentID)
	if !ok || digest != c.ManifestDigest {
		return VoiceSessionExecuteToolResult{}, errors.New("mailbox manifest mismatch")
	}
	var manifest voicecontract.Manifest
	if err := voicecontract.Decode(raw, voicecontract.MaxManifestBytes, &manifest); err != nil {
		return VoiceSessionExecuteToolResult{}, err
	}
	if manifest.CompiledRevision != c.AgentRevision {
		return VoiceSessionExecuteToolResult{}, errors.New("mailbox revision mismatch")
	}
	var spec *voicecontract.Tool
	for i := range manifest.Tools {
		if manifest.Tools[i].ID == r.ToolID {
			spec = &manifest.Tools[i]
			break
		}
	}
	if spec == nil || !spec.IsMailboxMessage() || spec.Name != voicecontract.MailboxMessageToolName || spec.RequiresApproval {
		return VoiceSessionExecuteToolResult{}, errors.New("mailbox tool not deployed")
	}
	input, err := voicecontract.ValidateToolInput(*spec, r.Arguments)
	if err != nil {
		return VoiceSessionExecuteToolResult{}, err
	}
	definition, ok := reg.Definition(c.AgentID)
	if !ok {
		return VoiceSessionExecuteToolResult{}, errors.New("mailbox definition unavailable")
	}
	tool, ok := definition.AI.Tools[r.ToolID]
	if !ok || tool.Execute == nil || !agents.VoiceMailboxMessagePolicy(tool) || tool.RequiresApproval || tool.NeedsApproval != nil {
		return VoiceSessionExecuteToolResult{}, errors.New("mailbox policy unavailable")
	}
	actor, err := voiceScopedActor(c, req.Grant, "", 0, true)
	if err != nil {
		return VoiceSessionExecuteToolResult{}, err
	}
	actor.Metadata["mailbox_line_id"] = c.Scope.LineID
	var args map[string]any
	if err = json.Unmarshal(input, &args); err != nil {
		return VoiceSessionExecuteToolResult{}, err
	}
	result, err := tool.Execute(ctx, ai.ToolCall{ToolCallID: r.ToolCallID, ToolName: spec.Name, Input: args}, ai.ToolExecutionOptions{Context: toolsession.ExecutionContext(actor, c.SessionID)})
	if err != nil {
		return VoiceSessionExecuteToolResult{Error: "mailbox message unavailable"}, nil
	}
	output, err := json.Marshal(result)
	if err != nil {
		return VoiceSessionExecuteToolResult{}, err
	}
	output, err = voicecontract.ValidateToolOutput(*spec, output)
	if err != nil {
		return VoiceSessionExecuteToolResult{}, errors.New("mailbox message result rejected")
	}
	return VoiceSessionExecuteToolResult{Result: output}, nil
}
