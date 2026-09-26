package temporalruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Origens-Dev/go-ai/packages/ai"
	"github.com/Origens-Dev/gobeyond/agents"
	"github.com/Origens-Dev/gobeyond/agents/voicecontract"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

const (
	VoiceSessionWorkflowName = "gobeyond.agents.voice_session.v1"

	// VoiceSessionExecuteToolUpdate is the Maglev/API → workflow Update that
	// runs a Live tool as a LocalActivity on the agent worker (P2 remote path
	// and P3 colocated LocalActivity tools).
	VoiceSessionExecuteToolUpdate = "gobeyond.agents.voice_session.execute_tool.v1"
	// VoiceSessionApproveToolUpdate responds to one exact workflow-owned pending
	// call and returns its final result after execution or denial.
	VoiceSessionApproveToolUpdate    = "gobeyond.agents.voice_session.approve_tool.v1"
	VoiceSessionPendingApprovalQuery = "gobeyond.agents.voice_session.pending_approval.v1"

	voiceSessionExecuteToolActivityName = "gobeyond.agents.voice_session.execute_tool"
	maxVoiceSessionToolCalls            = 8
)

// VoiceSessionInput is the durable voice-call workflow argument.
type VoiceSessionInput struct {
	Context     *voicecontract.Context `json:"context,omitempty"`
	AgentID     string                 `json:"agent_id"`
	CallID      string                 `json:"call_id"`
	SessionID   string                 `json:"session_id"`
	ExecutionID string                 `json:"execution_id"`
}

// VoiceSessionExecuteToolInput is the Update / LocalActivity payload for one
// Gemini Live function call.
type VoiceSessionExecuteToolInput struct {
	Grant      string                     `json:"grant,omitempty"`
	RemoteRead *voicecontract.ReadRequest `json:"remote_read,omitempty"`
	// CallControl belongs to the dedicated asynchronous current-grant path.
	CallControl    *voicecontract.Command `json:"call_control,omitempty"`
	AgentID        string                 `json:"agent_id"`
	ToolName       string                 `json:"tool_name"`
	ToolCallID     string                 `json:"tool_call_id,omitempty"`
	Input          json.RawMessage        `json:"input,omitempty"`
	ActorID        string                 `json:"actor_id,omitempty"`
	ActorKind      string                 `json:"actor_kind,omitempty"`
	NetworkID      string                 `json:"network_id,omitempty"`
	ManifestDigest string                 `json:"manifest_digest,omitempty"`
	AgentRevision  string                 `json:"agent_revision,omitempty"`
	// AllowedToolIDs is derived from the verified voice grant by the API. It is
	// optional for colocated/internal tests and older direct workflow callers.
	AllowedToolIDs []string `json:"allowed_tool_ids,omitempty"`
	// Set only by the workflow after validating an approval response. The public
	// execute-tool update always clears this field before dispatch.
	ApprovalConfirmed bool      `json:"approval_confirmed,omitempty"`
	ApprovalExpiresAt time.Time `json:"approval_expires_at,omitempty"`
}

type VoiceSessionToolApproval struct {
	InteractionID string          `json:"interaction_id"`
	ActorID       string          `json:"actor_id"`
	ActorKind     string          `json:"actor_kind"`
	ToolCallID    string          `json:"tool_call_id"`
	ToolName      string          `json:"tool_name"`
	Input         json.RawMessage `json:"input"`
	InputHash     string          `json:"input_hash"`
	ExpiresAt     time.Time       `json:"expires_at"`
}

// VoiceSessionApprovalResponse is an internal workflow update payload. The
// API derives actor fields from its verified grant; clients submit only ID and
// decision.
type VoiceSessionApprovalResponse struct {
	InteractionID string `json:"interaction_id"`
	Approved      bool   `json:"approved"`
	ActorID       string `json:"actor_id"`
	ActorKind     string `json:"actor_kind"`
}

// VoiceSessionExecuteToolResult is returned to Maglev so it can SendToolResponse.
type VoiceSessionExecuteToolResult struct {
	Approval  *VoiceSessionToolApproval     `json:"approval,omitempty"`
	Operation *voicecontract.Operation      `json:"operation,omitempty"`
	Terminal  *voicecontract.TerminalResult `json:"terminal,omitempty"`
	Result    json.RawMessage               `json:"result,omitempty"`
	Error     string                        `json:"error,omitempty"`
}

// VoiceSessionWorkflow is the lifecycle workflow for an AI phone/softphone
// call. Media may stay on Maglev (P0–P2) or colocate on the realtime
// RoleWorker (P3); this workflow is the hosted Agents SoR handle and the
// LocalActivity home for Live tool Updates.
func VoiceSessionWorkflow(ctx workflow.Context, in VoiceSessionInput) error {
	logger := workflow.GetLogger(ctx)
	logger.Info("voice session started",
		"agent_id", in.AgentID,
		"call_id", in.CallID,
		"session_id", in.SessionID,
		"execution_id", in.ExecutionID,
	)

	toolCalls := 0
	completed := map[string]VoiceSessionExecuteToolResult{}
	pendingApprovals := map[string]VoiceSessionExecuteToolInput{}
	pendingByCall := map[string]string{}
	type approvalOutcome struct {
		actorID, actorKind string
		approved           bool
		result             VoiceSessionExecuteToolResult
	}
	approvalResults := map[string]approvalOutcome{}
	if err := workflow.SetQueryHandler(ctx, VoiceSessionPendingApprovalQuery, func() (*VoiceSessionToolApproval, error) {
		for interactionID, req := range pendingApprovals {
			if req.ApprovalExpiresAt.IsZero() || workflow.Now(ctx).Before(req.ApprovalExpiresAt) {
				return makeVoiceApprovalResult(interactionID, req).Approval, nil
			}
		}
		return nil, nil
	}); err != nil {
		return err
	}
	control := newVoiceControlWorkflowState()
	reads := newVoiceReadWorkflowState()
	reads.budget = control.budget
	if err := workflow.SetUpdateHandler(ctx, VoiceSessionExecuteToolUpdate,
		func(ctx workflow.Context, req VoiceSessionExecuteToolInput) (VoiceSessionExecuteToolResult, error) {
			if req.RemoteRead != nil {
				if req.CallControl != nil {
					return VoiceSessionExecuteToolResult{}, errors.New("mixed remote dispatch")
				}
				return reads.execute(ctx, in, req)
			}
			if req.CallControl != nil {
				return control.execute(ctx, in, req)
			}
			if strings.TrimSpace(req.AgentID) == "" {
				req.AgentID = in.AgentID
			}
			if in.Context != nil && (req.ActorID != in.Context.ActorID || req.ActorKind != in.Context.ActorKind || req.AgentID != in.Context.AgentID || req.NetworkID != in.Context.NetworkID) {
				return VoiceSessionExecuteToolResult{}, errors.New("voice tool actor or scope does not match the verified session")
			}
			if in.Context == nil {
				return VoiceSessionExecuteToolResult{}, errors.New("voice action requires a verified session context")
			}
			req.ManifestDigest = in.Context.ManifestDigest
			req.AgentRevision = in.Context.AgentRevision
			callID := strings.TrimSpace(req.ToolCallID)
			if callID == "" {
				return VoiceSessionExecuteToolResult{Error: "tool_call_id required"}, nil
			}
			// The caller cannot self-assert approval on this initial update.
			req.ApprovalConfirmed = false
			if result, ok := completed[callID]; ok {
				return result, nil
			}
			if interactionID := pendingByCall[callID]; interactionID != "" {
				pending := pendingApprovals[interactionID]
				if pending.ToolName != req.ToolName || voiceToolInputHash(pending.Input) != voiceToolInputHash(req.Input) || pending.ActorID != req.ActorID || pending.ActorKind != req.ActorKind {
					return VoiceSessionExecuteToolResult{}, errors.New("conflicting replay for pending approval")
				}
				return makeVoiceApprovalResult(interactionID, pending), nil
			}
			if toolCalls >= maxVoiceSessionToolCalls {
				return VoiceSessionExecuteToolResult{Error: "voice session tool quota exceeded"}, nil
			}
			toolCalls++
			result, err := executeVoiceSessionToolLocal(ctx, req)
			if err == nil && result.Approval != nil {
				req.ApprovalExpiresAt = workflow.Now(ctx).Add(5 * time.Minute)
				result.Approval.ExpiresAt = req.ApprovalExpiresAt
				pendingApprovals[result.Approval.InteractionID] = req
				pendingByCall[callID] = result.Approval.InteractionID
				return result, nil
			}
			if err == nil && callID != "" {
				completed[callID] = result
			}
			return result, err
		}); err != nil {
		return err
	}
	if err := workflow.SetUpdateHandler(ctx, VoiceSessionApproveToolUpdate,
		func(ctx workflow.Context, response VoiceSessionApprovalResponse) (VoiceSessionExecuteToolResult, error) {
			interactionID := strings.TrimSpace(response.InteractionID)
			if prior, ok := approvalResults[interactionID]; ok {
				if prior.actorID != response.ActorID || prior.actorKind != response.ActorKind || prior.approved != response.Approved {
					return VoiceSessionExecuteToolResult{}, errors.New("conflicting or unauthorized replay for voice approval")
				}
				return prior.result, nil
			}
			req, ok := pendingApprovals[interactionID]
			if !ok {
				return VoiceSessionExecuteToolResult{}, errors.New("pending approval not found")
			}
			if response.ActorID != req.ActorID || response.ActorKind != req.ActorKind || response.ActorID == "" {
				return VoiceSessionExecuteToolResult{}, errors.New("approval actor does not own the pending voice tool call")
			}
			if !req.ApprovalExpiresAt.IsZero() && !workflow.Now(ctx).Before(req.ApprovalExpiresAt) {
				delete(pendingApprovals, interactionID)
				delete(pendingByCall, req.ToolCallID)
				return VoiceSessionExecuteToolResult{Error: "tool approval expired"}, nil
			}
			var result VoiceSessionExecuteToolResult
			if !response.Approved {
				result = VoiceSessionExecuteToolResult{Error: "tool approval denied"}
			} else {
				req.ApprovalConfirmed = true
				var err error
				result, err = executeVoiceSessionToolLocal(ctx, req)
				if err != nil {
					return VoiceSessionExecuteToolResult{}, err
				}
				if result.Approval != nil {
					return VoiceSessionExecuteToolResult{}, errors.New("approved voice tool unexpectedly requested another approval")
				}
			}
			approvalResults[interactionID] = approvalOutcome{actorID: response.ActorID, actorKind: response.ActorKind, approved: response.Approved, result: result}
			completed[req.ToolCallID] = result
			delete(pendingApprovals, interactionID)
			delete(pendingByCall, req.ToolCallID)
			return result, nil
		}); err != nil {
		return err
	}

	// Block until cancel/terminate from Maglev hangup (or a future complete signal).
	// 30m matches Maglev Live session token TTL.
	_ = workflow.Sleep(ctx, 30*time.Minute)
	logger.Info("voice session timed out", "session_id", in.SessionID)
	return nil
}

func executeVoiceSessionToolLocal(ctx workflow.Context, req VoiceSessionExecuteToolInput) (VoiceSessionExecuteToolResult, error) {
	var out VoiceSessionExecuteToolResult
	laCtx := workflow.WithLocalActivityOptions(ctx, workflow.LocalActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
	})
	err := workflow.ExecuteLocalActivity(laCtx, voiceSessionExecuteToolActivityName, req).Get(ctx, &out)
	if err != nil {
		return VoiceSessionExecuteToolResult{Error: err.Error()}, nil
	}
	return out, nil
}

func newVoiceSessionToolApproval(req VoiceSessionExecuteToolInput, call ai.ToolCall) (*VoiceSessionToolApproval, error) {
	input, err := json.Marshal(call.Input)
	if err != nil {
		return nil, fmt.Errorf("encode voice tool approval input: %w", err)
	}
	inputHash := voiceToolInputHash(input)
	idDigest := sha256.Sum256([]byte(strings.Join([]string{
		req.AgentID, req.ActorID, req.ActorKind, req.ToolCallID, req.ToolName, inputHash,
	}, "\x00")))
	return &VoiceSessionToolApproval{
		InteractionID: "approval_" + hex.EncodeToString(idDigest[:16]),
		ActorID:       strings.TrimSpace(req.ActorID), ActorKind: strings.TrimSpace(req.ActorKind),
		ToolCallID: strings.TrimSpace(req.ToolCallID), ToolName: strings.TrimSpace(req.ToolName),
		Input: input, InputHash: inputHash, ExpiresAt: req.ApprovalExpiresAt,
	}, nil
}

func voiceToolInputHash(input []byte) string {
	var value any
	if json.Unmarshal(input, &value) == nil {
		if canonical, err := json.Marshal(value); err == nil {
			input = canonical
		}
	}
	digest := sha256.Sum256(input)
	return hex.EncodeToString(digest[:])
}

func sameVoiceToolInput(left, right []byte) bool {
	return voiceToolInputHash(left) == voiceToolInputHash(right)
}

func makeVoiceApprovalResult(interactionID string, req VoiceSessionExecuteToolInput) VoiceSessionExecuteToolResult {
	var args map[string]any
	_ = json.Unmarshal(req.Input, &args)
	approval, _ := newVoiceSessionToolApproval(req, ai.ToolCall{ToolCallID: req.ToolCallID, ToolName: req.ToolName, Input: args})
	return VoiceSessionExecuteToolResult{Approval: approval}
}

// VoiceSessionExecuteToolActivity resolves the agent tool from ProcessVoiceRegistry
// and runs it in-process on the realtime worker (LocalActivity). Maglev cannot
// hold customer tools; the agent worker can.
func VoiceSessionExecuteToolActivity(ctx context.Context, req VoiceSessionExecuteToolInput) (VoiceSessionExecuteToolResult, error) {
	if req.RemoteRead != nil {
		if req.CallControl != nil {
			return VoiceSessionExecuteToolResult{}, errors.New("mixed remote dispatch")
		}
		return executeVoiceRemoteReadActivity(ctx, req)
	}
	if req.CallControl != nil {
		return executeVoiceControlActivity(ctx, req)
	}
	agentID := strings.TrimSpace(req.AgentID)
	toolName := strings.TrimSpace(req.ToolName)
	if agentID == "" || toolName == "" {
		return VoiceSessionExecuteToolResult{Error: "agent_id and tool_name required"}, nil
	}
	reg := ProcessVoiceRegistry()
	if reg == nil {
		return VoiceSessionExecuteToolResult{Error: "voice registry unavailable (not colocated)"}, nil
	}
	definition, ok := reg.Definition(agentID)
	if !ok {
		return VoiceSessionExecuteToolResult{Error: fmt.Sprintf("agent %q has no voice definition", agentID)}, nil
	}
	tool, ok := lookupDefinitionTool(definition, toolName)
	if !ok || tool.Execute == nil {
		return VoiceSessionExecuteToolResult{Error: fmt.Sprintf("unknown tool %q", toolName)}, nil
	}

	if _, read := agents.VoiceRemoteReadPolicy(tool); read {
		return VoiceSessionExecuteToolResult{}, errors.New("remote read requires current scoped dispatch")
	}
	if _, controlled := agents.VoiceControlPolicy(tool); controlled {
		return VoiceSessionExecuteToolResult{}, errors.New("voice control tool requires current grant operation dispatch")
	}
	if !agents.VoiceActionPolicy(tool) || !tool.RequiresApproval {
		return VoiceSessionExecuteToolResult{Error: "tool is not an approved voice action"}, nil
	}
	manifestRaw, manifestDigest, ok := reg.Manifest(agentID)
	if !ok || manifestDigest == "" || req.ManifestDigest != manifestDigest || req.AgentRevision != definition.AI.Revision {
		return VoiceSessionExecuteToolResult{}, errors.New("voice action manifest binding mismatch")
	}
	var manifest voicecontract.Manifest
	if err := voicecontract.Decode(manifestRaw, voicecontract.MaxManifestBytes, &manifest); err != nil || manifest.CompiledRevision != req.AgentRevision {
		return VoiceSessionExecuteToolResult{}, errors.New("voice action manifest unavailable")
	}
	var spec *voicecontract.Tool
	for i := range manifest.Tools {
		if manifest.Tools[i].Name == tool.Name && manifest.Tools[i].ID == toolName {
			spec = &manifest.Tools[i]
			break
		}
	}
	if spec == nil || !spec.IsAction() || !spec.RequiresApproval || !voiceToolAllowed(req.AllowedToolIDs, spec.ID) {
		return VoiceSessionExecuteToolResult{Error: "voice action is not allowed"}, nil
	}
	canonicalInput, err := voicecontract.ValidateToolInput(*spec, req.Input)
	if err != nil {
		return VoiceSessionExecuteToolResult{Error: "voice action input rejected"}, nil
	}
	var args map[string]any
	if len(canonicalInput) > 0 {
		if err := json.Unmarshal(canonicalInput, &args); err != nil {
			return VoiceSessionExecuteToolResult{Error: fmt.Sprintf("invalid tool input: %v", err)}, nil
		}
	}
	actorMetadata := map[string]string{}
	if networkID := strings.TrimSpace(req.NetworkID); networkID != "" {
		actorMetadata["network_id"] = networkID
	}
	actor := agents.Actor{
		ID:       strings.TrimSpace(req.ActorID),
		Kind:     strings.TrimSpace(req.ActorKind),
		Metadata: actorMetadata,
	}
	if err := actor.Validate(); err != nil {
		return VoiceSessionExecuteToolResult{Error: "voice session actor unavailable"}, nil
	}
	if len(req.AllowedToolIDs) > 0 && !voiceToolAllowed(req.AllowedToolIDs, toolName) {
		return VoiceSessionExecuteToolResult{Error: fmt.Sprintf("tool %q is not allowed", toolName)}, nil
	}
	toolCall := ai.ToolCall{
		ToolCallID: strings.TrimSpace(req.ToolCallID),
		ToolName:   toolName,
		Input:      args,
	}
	decision := ai.ApprovalDecision{Type: ai.ApprovalDecisionNotApplicable}
	if tool.NeedsApproval != nil {
		var err error
		decision, err = ai.ResolveToolApproval(ctx, map[string]ai.Tool{toolName: tool}, toolCall)
		if err != nil {
			return VoiceSessionExecuteToolResult{Error: "tool approval policy failed"}, nil
		}
	}
	if decision.Type == ai.ApprovalDecisionDenied {
		return VoiceSessionExecuteToolResult{Error: decision.Reason}, nil
	}
	requiresUserApproval := tool.RequiresApproval || decision.Type == ai.ApprovalDecisionUserApproval
	if requiresUserApproval && !req.ApprovalConfirmed {
		approval, err := newVoiceSessionToolApproval(req, toolCall)
		if err != nil {
			return VoiceSessionExecuteToolResult{}, err
		}
		return VoiceSessionExecuteToolResult{Approval: approval}, nil
	}
	result, err := tool.Execute(ctx, toolCall, ai.ToolExecutionOptions{
		Context: map[string]any{"gobeyondActor": actor},
	})
	if err != nil {
		return VoiceSessionExecuteToolResult{Error: err.Error()}, nil
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return VoiceSessionExecuteToolResult{Error: fmt.Sprintf("encode tool result: %v", err)}, nil
	}
	return VoiceSessionExecuteToolResult{Result: raw}, nil
}

func voiceToolAllowed(ids []string, name string) bool {
	name = strings.TrimSpace(name)
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == name || (id == "web_search" && name == "web-search") {
			return true
		}
	}
	return false
}

func lookupDefinitionTool(definition agents.AIDefinition, name string) (ai.Tool, bool) {
	if tool, ok := definition.AI.Tools[name]; ok {
		return tool, true
	}
	for key, tool := range definition.AI.Tools {
		if strings.TrimSpace(tool.Name) == name || key == name {
			return tool, true
		}
	}
	return ai.Tool{}, false
}

// RegisterVoiceSessionWorkflow registers the platform voice-session workflow
// and its LocalActivity tool executor.
func RegisterVoiceSessionWorkflow(w worker.Registry) {
	if w == nil {
		return
	}
	w.RegisterWorkflowWithOptions(VoiceSessionWorkflow, workflow.RegisterOptions{Name: VoiceSessionWorkflowName})
	w.RegisterActivityWithOptions(VoiceSessionExecuteToolActivity, activity.RegisterOptions{
		Name: voiceSessionExecuteToolActivityName,
	})
}
