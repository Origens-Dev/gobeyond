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
	// Derived by the API from a verified signed grant and frozen declaration.
	BudgetPolicy string                 `json:"budget_policy,omitempty"`
	Context      *voicecontract.Context `json:"context,omitempty"`
	AgentID      string                 `json:"agent_id"`
	CallID       string                 `json:"call_id"`
	SessionID    string                 `json:"session_id"`
	ExecutionID  string                 `json:"execution_id"`
}

// VoiceSessionExecuteToolInput is the Update / LocalActivity payload for one
// Gemini Live function call.
type VoiceSessionExecuteToolInput struct {
	SourcePlayback   *voicecontract.SourcePlaybackRequest   `json:"source_playback,omitempty"`
	HiddenCompletion *voicecontract.HiddenCompletionRequest `json:"hidden_completion,omitempty"`
	// Workflow-owned propagation; callers cannot select a broader policy.
	BudgetPolicy string                     `json:"budget_policy,omitempty"`
	Grant        string                     `json:"grant,omitempty"`
	RemoteRead   *voicecontract.ReadRequest `json:"remote_read,omitempty"`
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
	SessionID      string   `json:"session_id,omitempty"`
	CallID         string   `json:"call_id,omitempty"`
	// IdempotencyKey is platform-derived by the voice session workflow. Callers
	// cannot select it; the activity re-derives and rejects a mismatch.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
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

const mailboxBudgetVersionChange = "operator-mailbox-budget-v1"

func configureVoiceToolBudget(ctx workflow.Context, in VoiceSessionInput, budget *voiceToolBudget) error {
	if voicecontract.IsGenericBudgetPolicy(in.BudgetPolicy) {
		if in.Context == nil || in.Context.Validate() != nil || in.AgentID != in.Context.AgentID {
			return errors.New("invalid verified workflow budget policy")
		}
		// Skeleton: recognized without mailbox buckets or a product agent-id bind.
		return nil
	}
	version := workflow.GetVersion(ctx, mailboxBudgetVersionChange, workflow.DefaultVersion, 1)
	// Historical executions retain their shared two-operation budget even when
	// replayed by a worker that understands the new opt-in.
	if version == workflow.DefaultVersion {
		return nil
	}
	if in.BudgetPolicy == "" {
		return nil
	}
	if !voicecontract.IsMailboxBudgetPolicy(in.BudgetPolicy) || in.Context == nil || in.Context.Validate() != nil || in.Context.AgentID != "call-operator" || in.Context.Scope.Kind != "agent" || in.AgentID != in.Context.AgentID {
		return errors.New("invalid verified workflow budget policy")
	}
	budget.policy = in.BudgetPolicy
	return nil
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
	writes := newVoiceWriteWorkflowState()
	playback := configureVoicePlayback(ctx, control.budget)
	if err := configureVoiceToolBudget(ctx, in, control.budget); err != nil {
		return err
	}

	if err := workflow.SetUpdateHandler(ctx, VoiceSessionExecuteToolUpdate,
		func(ctx workflow.Context, req VoiceSessionExecuteToolInput) (VoiceSessionExecuteToolResult, error) {
			if !exclusiveVoiceDispatch(req) {
				return VoiceSessionExecuteToolResult{}, errors.New("mixed remote dispatch")
			}
			if req.SourcePlayback != nil || req.HiddenCompletion != nil {
				return playback.execute(ctx, in, req)
			}
			if req.RemoteRead != nil {
				if req.CallControl != nil {
					return VoiceSessionExecuteToolResult{}, errors.New("mixed remote dispatch")
				}
				return reads.execute(ctx, in, req)
			}
			if req.CallControl != nil {
				return control.execute(ctx, in, req)
			}
			req, identity, replay, err := bindVoiceWriteRequest(in, req)
			if err != nil {
				if errors.Is(err, errWriteToolCallID) {
					return VoiceSessionExecuteToolResult{Error: err.Error()}, nil
				}
				return VoiceSessionExecuteToolResult{}, err
			}
			callID := strings.TrimSpace(req.ToolCallID)
			req.ApprovalConfirmed = false
			if interactionID := pendingByCall[callID]; interactionID != "" {
				pending := pendingApprovals[interactionID]
				if pending.ToolName != req.ToolName || voiceToolInputHash(pending.Input) != voiceToolInputHash(req.Input) || pending.ActorID != req.ActorID || pending.ActorKind != req.ActorKind {
					return VoiceSessionExecuteToolResult{}, errors.New("conflicting replay for pending approval")
				}
				return makeVoiceApprovalResult(interactionID, pending), nil
			}
			cached, done, err := writes.reserve(ctx, identity, replay)
			if err != nil || done {
				return cached, err
			}
			result, err := executeVoiceWriteToolLocal(ctx, req)
			if err == nil && result.Approval != nil {
				writes.pending[identity] = false
				req.ApprovalExpiresAt = workflow.Now(ctx).Add(5 * time.Minute)
				result.Approval.ExpiresAt = req.ApprovalExpiresAt
				pendingApprovals[result.Approval.InteractionID] = req
				pendingByCall[callID] = result.Approval.InteractionID
				return result, nil
			}
			return writes.finish(identity, result, err)
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
				result, _ := writes.finish(voiceWriteIdentity(req.ToolName, req.ToolCallID), VoiceSessionExecuteToolResult{Error: "tool approval expired"}, nil)
				return result, nil
			}
			var result VoiceSessionExecuteToolResult
			identity := voiceWriteIdentity(req.ToolName, req.ToolCallID)
			if !response.Approved {
				result, _ = writes.finish(identity, VoiceSessionExecuteToolResult{Error: "tool approval denied"}, nil)
			} else {
				req.ApprovalConfirmed = true
				var err error
				result, err = executeVoiceWriteToolLocal(ctx, req)
				if result.Approval != nil && err == nil {
					return VoiceSessionExecuteToolResult{}, errors.New("approved voice tool unexpectedly requested another approval")
				}
				result, err = writes.finish(identity, result, err)
				if err != nil {
					return VoiceSessionExecuteToolResult{}, err
				}
			}
			approvalResults[interactionID] = approvalOutcome{actorID: response.ActorID, actorKind: response.ActorKind, approved: response.Approved, result: result}
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
	if !exclusiveVoiceDispatch(req) {
		return VoiceSessionExecuteToolResult{}, errors.New("mixed remote dispatch")
	}
	if req.HiddenCompletion != nil {
		return executeVoicePlaybackCompletionActivity(ctx, req)
	}
	if req.SourcePlayback != nil {
		return executeVoiceSourcePlaybackActivity(ctx, req)
	}
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

	if _, playback := agents.VoicePlaybackPolicyFor(tool); playback || agents.VoicePlaybackCompletionPolicy(tool) {
		return VoiceSessionExecuteToolResult{}, errors.New("playback requires typed runtime dispatch")
	}
	if _, read := agents.VoiceRemoteReadPolicy(tool); read {
		return VoiceSessionExecuteToolResult{}, errors.New("remote read requires current scoped dispatch")
	}
	if _, controlled := agents.VoiceControlPolicy(tool); controlled {
		return VoiceSessionExecuteToolResult{}, errors.New("voice control tool requires current grant operation dispatch")
	}
	return executeVoiceWriteActivity(ctx, req, definition, tool, toolName)
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
