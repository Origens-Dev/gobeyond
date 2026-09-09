package temporalruntime

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/Origens-Dev/go-ai/packages/ai"
	"github.com/Origens-Dev/gobeyond/agents"
	"github.com/Origens-Dev/gobeyond/agents/voicecontract"
	"go.temporal.io/sdk/workflow"
)

type voiceControlWorkflowState struct {
	budget      *voiceToolBudget
	digests     map[string]string
	results     map[string]VoiceSessionExecuteToolResult
	pending     map[string]bool
	lastBarrier uint64
}

func newVoiceControlWorkflowState() *voiceControlWorkflowState {
	return &voiceControlWorkflowState{budget: &voiceToolBudget{}, digests: map[string]string{}, results: map[string]VoiceSessionExecuteToolResult{}, pending: map[string]bool{}}
}
func (s *voiceControlWorkflowState) execute(ctx workflow.Context, in VoiceSessionInput, req VoiceSessionExecuteToolInput) (VoiceSessionExecuteToolResult, error) {
	c := req.CallControl
	expected, err := WorkflowID(c.Context.SessionID, c.Context.ExecutionID)
	if err != nil || c.Validate() != nil || in.Context == nil || *in.Context != c.Context || c.Context.AgentID != in.AgentID || c.Context.CallID != in.CallID || c.Context.SessionID != in.SessionID || c.Context.ExecutionID != in.ExecutionID || expected != workflow.GetInfo(ctx).WorkflowExecution.ID {
		return VoiceSessionExecuteToolResult{}, errors.New("call control workflow scope mismatch")
	}
	// No sensitive screening/voicemail inputs may enter this durable tool path.
	legacyControl := c.Version == voicecontract.LegacyVersion && c.ToolID == "dial-contact" && c.Context.Scope.Kind == "operator"
	genericControl := c.Version == voicecontract.Version && c.Context.Scope.Kind == "agent"
	if !legacyControl && !genericControl {
		return VoiceSessionExecuteToolResult{}, errors.New("unsupported durable control phase")
	}
	key := c.ToolID + "/" + c.ToolCallID
	if digest, ok := s.digests[key]; ok {
		if digest != c.InputDigest {
			return VoiceSessionExecuteToolResult{}, errors.New("conflicting control replay")
		}
		if err = workflow.Await(ctx, func() bool { return !s.pending[key] }); err != nil {
			return VoiceSessionExecuteToolResult{}, err
		}
		return s.results[key], nil
	}
	if s.budget.count >= 2 || c.AnnouncementBarrierID <= s.lastBarrier {
		return VoiceSessionExecuteToolResult{}, errors.New("control budget or drain fence exhausted")
	}
	s.budget.count++
	s.lastBarrier = c.AnnouncementBarrierID
	s.digests[key] = c.InputDigest
	s.pending[key] = true
	result, err := executeVoiceSessionToolLocal(ctx, req)
	s.pending[key] = false
	if err != nil {
		result = VoiceSessionExecuteToolResult{Error: "call control could not start"}
	}
	s.results[key] = result
	return result, err
}

// executeVoiceControlActivity consumes API-verified grant context and rechecks the
// deployed registry. The customer tool must revalidate the opaque grant against
// the platform owner service before placing any call. No schemas come from req.
func executeVoiceControlActivity(ctx context.Context, req VoiceSessionExecuteToolInput) (VoiceSessionExecuteToolResult, error) {
	return executeVoiceRegistryActivity(ctx, req, false)
}
func executeVoiceRemoteReadActivity(ctx context.Context, req VoiceSessionExecuteToolInput) (VoiceSessionExecuteToolResult, error) {
	return executeVoiceRegistryActivity(ctx, req, true)
}
func executeVoiceRegistryActivity(ctx context.Context, req VoiceSessionExecuteToolInput, read bool) (VoiceSessionExecuteToolResult, error) {
	c := req.CallControl
	if read {
		r := req.RemoteRead
		if r == nil || r.Validate() != nil {
			return VoiceSessionExecuteToolResult{}, errors.New("invalid remote read")
		}
		c = &voicecontract.Command{Version: r.Version, Context: r.Context, ToolID: r.ToolID, ToolCallID: r.ToolCallID, InputDigest: r.InputDigest, Arguments: r.Arguments}
	}
	validControl := c != nil && c.Validate() == nil && ((c.Version == voicecontract.LegacyVersion && c.Context.Scope.Kind == "operator" && c.ToolID == "dial-contact") || (c.Version == voicecontract.Version && c.Context.Scope.Kind == "agent"))
	validRead := c != nil && c.Context.Scope.Kind == "agent" && c.Version == voicecontract.Version || c != nil && c.Context.Scope.Kind == "operator" && c.Version == voicecontract.LegacyVersion
	if c == nil || (!read && !validControl) || (read && !validRead) || len(req.Grant) == 0 || len(req.Grant) > 8192 {
		return VoiceSessionExecuteToolResult{}, errors.New("invalid control request")
	}
	if req.AgentID != "" && req.AgentID != c.Context.AgentID || req.ActorID != "" && req.ActorID != c.Context.ActorID || req.ActorKind != "" && req.ActorKind != c.Context.ActorKind || req.NetworkID != "" && req.NetworkID != c.Context.NetworkID || req.ToolCallID != "" && req.ToolCallID != c.ToolCallID {
		return VoiceSessionExecuteToolResult{}, errors.New("conflicting legacy control fields")
	}
	reg := ProcessVoiceRegistry()
	if reg == nil {
		return VoiceSessionExecuteToolResult{}, errors.New("voice registry unavailable")
	}
	raw, digest, ok := reg.Manifest(c.Context.AgentID)
	if !ok || digest != c.Context.ManifestDigest {
		return VoiceSessionExecuteToolResult{}, errors.New("control manifest mismatch")
	}
	var manifest voicecontract.Manifest
	if err := voicecontract.Decode(raw, voicecontract.MaxManifestBytes, &manifest); err != nil {
		return VoiceSessionExecuteToolResult{}, err
	}
	if manifest.CompiledRevision != c.Context.AgentRevision {
		return VoiceSessionExecuteToolResult{}, errors.New("control revision mismatch")
	}
	var spec *voicecontract.Tool
	for i := range manifest.Tools {
		if manifest.Tools[i].ID == c.ToolID {
			spec = &manifest.Tools[i]
			break
		}
	}
	if spec == nil || spec.IsRead() != read {
		return VoiceSessionExecuteToolResult{}, errors.New("control tool not deployed")
	}
	if req.ToolName != "" && req.ToolName != spec.Name {
		return VoiceSessionExecuteToolResult{}, errors.New("control tool name mismatch")
	}
	input, err := voicecontract.ValidateToolInput(*spec, c.Arguments)
	if err != nil {
		return VoiceSessionExecuteToolResult{}, err
	}
	definition, ok := reg.Definition(c.Context.AgentID)
	if !ok {
		return VoiceSessionExecuteToolResult{}, errors.New("control definition unavailable")
	}
	tool, ok := definition.AI.Tools[c.ToolID]
	if !ok || tool.Execute == nil {
		return VoiceSessionExecuteToolResult{}, errors.New("control handler unavailable")
	}
	_, controlPolicy := agents.VoiceControlPolicy(tool)
	_, readPolicy := agents.VoiceRemoteReadPolicy(tool)
	if (!read && !controlPolicy) || (read && !readPolicy) {
		return VoiceSessionExecuteToolResult{}, errors.New("control policy unavailable")
	}
	metadata := map[string]string{"organization_id": c.Context.OrganizationID, "project_id": c.Context.ProjectID, "environment_id": c.Context.EnvironmentID, "network_id": c.Context.NetworkID, "line_id": c.Context.Scope.LineID, "call_id": c.Context.CallID, "session_id": c.Context.SessionID, "execution_id": c.Context.ExecutionID, "agent_id": c.Context.AgentID, "agent_revision": c.Context.AgentRevision, "manifest_digest": c.Context.ManifestDigest, "generation": strconv.FormatUint(c.Context.Generation, 10), "voice_session_grant": req.Grant, "operation_id": c.OperationID, "announcement_barrier_id": strconv.FormatUint(c.AnnouncementBarrierID, 10)}
	if read {
		delete(metadata, "operation_id")
		delete(metadata, "announcement_barrier_id")
	}
	actor := agents.Actor{ID: c.Context.ActorID, Kind: c.Context.ActorKind, Metadata: metadata}
	if err = actor.Validate(); err != nil {
		return VoiceSessionExecuteToolResult{}, err
	}
	var args map[string]any
	if err = json.Unmarshal(input, &args); err != nil {
		return VoiceSessionExecuteToolResult{}, err
	}
	result, err := tool.Execute(ctx, ai.ToolCall{ToolCallID: c.ToolCallID, ToolName: spec.Name, Input: args}, ai.ToolExecutionOptions{Context: map[string]any{"gobeyondActor": actor}})
	if err != nil {
		return VoiceSessionExecuteToolResult{Error: "call control could not start"}, nil
	}
	if read {
		raw, e := json.Marshal(result)
		if e != nil {
			return VoiceSessionExecuteToolResult{}, e
		}
		raw, e = voicecontract.ValidateToolOutput(*spec, raw)
		if e != nil {
			return VoiceSessionExecuteToolResult{Error: "directory result rejected"}, nil
		}
		return VoiceSessionExecuteToolResult{Result: raw}, nil
	}
	operation, ok := result.(voicecontract.Operation)
	if !ok {
		if provider, yes := result.(interface {
			VoiceOperation() voicecontract.Operation
		}); yes {
			operation = provider.VoiceOperation()
			ok = true
		}
	}
	if !ok || operation.Validate() != nil || operation.Version != c.Version || operation.Context != c.Context || operation.OperationID != c.OperationID || operation.State != "accepted" || operation.Sequence != 1 {
		return VoiceSessionExecuteToolResult{}, errors.New("invalid operation acknowledgement")
	}
	return VoiceSessionExecuteToolResult{Operation: &operation}, nil
}
