package temporalruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Origens-Dev/go-ai/packages/ai"
	"github.com/Origens-Dev/gobeyond/agents"
	"github.com/Origens-Dev/gobeyond/agents/internal/toolsession"
	"github.com/Origens-Dev/gobeyond/agents/voicecontract"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"
)

var errWriteOutcomeUnknown = errors.New("write outcome unknown; reconcile only")
var errWriteToolCallID = errors.New("tool_call_id required")
var errWriteConflict = errors.New("conflicting write replay")
var errWriteForgedKey = errors.New("forged write idempotency key")

type voiceWriteRecord struct {
	key      string
	digest   string
	result   VoiceSessionExecuteToolResult
	complete bool
	unknown  bool
	wait     chan struct{}
}

type voiceWriteLedger struct {
	mu      sync.Mutex
	records map[string]voiceWriteRecord
}

func newVoiceWriteLedger() *voiceWriteLedger {
	return &voiceWriteLedger{records: map[string]voiceWriteRecord{}}
}

func (l *voiceWriteLedger) dispatch(identity, key, digest string, run func() (VoiceSessionExecuteToolResult, error)) (VoiceSessionExecuteToolResult, error) {
	for {
		l.mu.Lock()
		rec, ok := l.records[identity]
		if ok {
			if rec.key != key || rec.digest != digest {
				l.mu.Unlock()
				return VoiceSessionExecuteToolResult{}, errWriteConflict
			}
			if rec.complete {
				result := rec.result
				l.mu.Unlock()
				return result, nil
			}
			if rec.unknown && rec.wait == nil {
				l.mu.Unlock()
				return VoiceSessionExecuteToolResult{}, errWriteOutcomeUnknown
			}
			if rec.wait != nil {
				wait := rec.wait
				l.mu.Unlock()
				<-wait
				continue
			}
		}
		wait := make(chan struct{})
		l.records[identity] = voiceWriteRecord{key: key, digest: digest, wait: wait}
		l.mu.Unlock()

		result, err := run()

		l.mu.Lock()
		if err != nil {
			l.records[identity] = voiceWriteRecord{key: key, digest: digest, unknown: true}
		} else {
			l.records[identity] = voiceWriteRecord{key: key, digest: digest, result: result, complete: true}
		}
		close(wait)
		l.mu.Unlock()
		if err != nil {
			return VoiceSessionExecuteToolResult{}, err
		}
		return result, nil
	}
}

var processWriteLedger = newVoiceWriteLedger()

func resetVoiceWriteLedger() { processWriteLedger = newVoiceWriteLedger() }

type voiceWriteWorkflowState struct {
	digests map[string]string
	results map[string]VoiceSessionExecuteToolResult
	pending map[string]bool
	unknown map[string]bool
}

func newVoiceWriteWorkflowState() *voiceWriteWorkflowState {
	return &voiceWriteWorkflowState{digests: map[string]string{}, results: map[string]VoiceSessionExecuteToolResult{}, pending: map[string]bool{}, unknown: map[string]bool{}}
}

func voiceWriteIdentity(toolName, toolCallID string) string {
	return toolName + "/" + toolCallID
}

func voiceWriteLedgerIdentity(sessionID, toolID, toolCallID string) string {
	return sessionID + "/" + toolID + "/" + toolCallID
}

func deriveVoiceWriteKey(sessionID, callID, toolID, toolCallID, inputDigest string) (string, error) {
	raw, err := json.Marshal(map[string]string{
		"call_id": callID, "input_digest": inputDigest, "session_id": sessionID, "tool_call_id": toolCallID, "tool_id": toolID,
	})
	if err != nil {
		return "", err
	}
	canonical, err := voicecontract.CanonicalJSON(raw, voicecontract.MaxSchemaBytes)
	if err != nil {
		return "", err
	}
	return voicecontract.Digest(canonical), nil
}

func voiceWriteReplayDigest(req VoiceSessionExecuteToolInput, inputDigest string) (string, error) {
	raw, err := json.Marshal(map[string]string{
		"actor_id": req.ActorID, "actor_kind": req.ActorKind, "agent_id": req.AgentID, "input_digest": inputDigest,
		"manifest_digest": req.ManifestDigest, "network_id": req.NetworkID, "tool_call_id": req.ToolCallID, "tool_name": req.ToolName,
	})
	if err != nil {
		return "", err
	}
	canonical, err := voicecontract.CanonicalJSON(raw, voicecontract.MaxSchemaBytes)
	if err != nil {
		return "", err
	}
	return voicecontract.Digest(canonical), nil
}

func bindVoiceWriteRequest(in VoiceSessionInput, req VoiceSessionExecuteToolInput) (VoiceSessionExecuteToolInput, string, string, error) {
	if in.Context == nil {
		return req, "", "", errors.New("voice write requires a verified session context")
	}
	if strings.TrimSpace(req.AgentID) == "" {
		req.AgentID = in.AgentID
	}
	if req.ActorID != in.Context.ActorID || req.ActorKind != in.Context.ActorKind || req.AgentID != in.Context.AgentID || req.NetworkID != in.Context.NetworkID {
		return req, "", "", errors.New("voice tool actor or scope does not match the verified session")
	}
	req.ManifestDigest = in.Context.ManifestDigest
	req.AgentRevision = in.Context.AgentRevision
	req.SessionID = in.SessionID
	req.CallID = in.CallID
	if strings.TrimSpace(req.ToolCallID) == "" {
		return req, "", "", errWriteToolCallID
	}
	canonical, err := voicecontract.CanonicalJSON(req.Input, voicecontract.MaxSchemaBytes)
	if err != nil {
		return req, "", "", errors.New("invalid write input")
	}
	inputDigest := voicecontract.Digest(canonical)
	req.Input = canonical
	replay, err := voiceWriteReplayDigest(req, inputDigest)
	if err != nil {
		return req, "", "", err
	}
	key, err := deriveVoiceWriteKey(in.SessionID, in.CallID, req.ToolName, req.ToolCallID, inputDigest)
	if err != nil {
		return req, "", "", err
	}
	req.IdempotencyKey = key
	return req, voiceWriteIdentity(req.ToolName, req.ToolCallID), replay, nil
}

func (s *voiceWriteWorkflowState) reserve(ctx workflow.Context, identity, replay string) (VoiceSessionExecuteToolResult, bool, error) {
	if previous, ok := s.digests[identity]; ok {
		if previous != replay {
			return VoiceSessionExecuteToolResult{}, true, errWriteConflict
		}
		if err := workflow.Await(ctx, func() bool { return !s.pending[identity] }); err != nil {
			return VoiceSessionExecuteToolResult{}, true, err
		}
		if result, ok := s.results[identity]; ok {
			return result, true, nil
		}
		if s.unknown[identity] {
			// Outcome unknown: dispatch again for ledger lookup only. The
			// activity must not re-run the handler.
			s.pending[identity] = true
			return VoiceSessionExecuteToolResult{}, false, nil
		}
		return VoiceSessionExecuteToolResult{}, false, nil
	}
	if len(s.digests) >= maxVoiceSessionToolCalls {
		return VoiceSessionExecuteToolResult{Error: "voice session tool quota exceeded"}, true, nil
	}
	s.digests[identity] = replay
	s.pending[identity] = true
	return VoiceSessionExecuteToolResult{}, false, nil
}

func (s *voiceWriteWorkflowState) finish(identity string, result VoiceSessionExecuteToolResult, err error) (VoiceSessionExecuteToolResult, error) {
	s.pending[identity] = false
	if err != nil {
		s.unknown[identity] = true
		return VoiceSessionExecuteToolResult{}, errWriteOutcomeUnknown
	}
	s.unknown[identity] = false
	s.results[identity] = result
	return result, nil
}

func executeVoiceWriteToolLocal(ctx workflow.Context, req VoiceSessionExecuteToolInput) (VoiceSessionExecuteToolResult, error) {
	var out VoiceSessionExecuteToolResult
	laCtx := workflow.WithLocalActivityOptions(ctx, workflow.LocalActivityOptions{
		StartToCloseTimeout: 30 * time.Second,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 1},
	})
	err := workflow.ExecuteLocalActivity(laCtx, voiceSessionExecuteToolActivityName, req).Get(ctx, &out)
	if err != nil {
		return VoiceSessionExecuteToolResult{}, err
	}
	return out, nil
}

func executeVoiceWriteActivity(ctx context.Context, req VoiceSessionExecuteToolInput, definition agents.AIDefinition, tool ai.Tool, toolName string) (VoiceSessionExecuteToolResult, error) {
	if !agents.VoiceWritePolicy(tool) {
		return VoiceSessionExecuteToolResult{Error: "tool is not a voice write"}, nil
	}
	agentID := strings.TrimSpace(req.AgentID)
	toolCallID := strings.TrimSpace(req.ToolCallID)
	if toolCallID == "" {
		return VoiceSessionExecuteToolResult{Error: errWriteToolCallID.Error()}, nil
	}
	sessionID := strings.TrimSpace(req.SessionID)
	callID := strings.TrimSpace(req.CallID)
	if sessionID == "" || callID == "" {
		return VoiceSessionExecuteToolResult{}, errors.New("write session identity unavailable")
	}
	reg := ProcessVoiceRegistry()
	if reg == nil {
		return VoiceSessionExecuteToolResult{Error: "voice registry unavailable (not colocated)"}, nil
	}
	manifestRaw, manifestDigest, ok := reg.Manifest(agentID)
	if !ok || manifestDigest == "" || req.ManifestDigest != manifestDigest || req.AgentRevision != definition.AI.Revision {
		return VoiceSessionExecuteToolResult{}, errors.New("voice write manifest binding mismatch")
	}
	var manifest voicecontract.Manifest
	if err := voicecontract.Decode(manifestRaw, voicecontract.MaxManifestBytes, &manifest); err != nil || manifest.CompiledRevision != req.AgentRevision {
		return VoiceSessionExecuteToolResult{}, errors.New("voice write manifest unavailable")
	}
	var spec *voicecontract.Tool
	for i := range manifest.Tools {
		if manifest.Tools[i].Name == tool.Name && (manifest.Tools[i].ID == toolName || manifest.Tools[i].Name == toolName) {
			spec = &manifest.Tools[i]
			break
		}
	}
	allowed := spec != nil && spec.IsWrite() && (voiceToolAllowed(req.AllowedToolIDs, spec.ID) || voiceToolAllowed(req.AllowedToolIDs, spec.Name) || voiceToolAllowed(req.AllowedToolIDs, toolName))
	if !allowed {
		return VoiceSessionExecuteToolResult{Error: "voice write is not allowed"}, nil
	}
	canonicalInput, err := voicecontract.ValidateToolInput(*spec, req.Input)
	if err != nil {
		return VoiceSessionExecuteToolResult{Error: "voice write input rejected"}, nil
	}
	inputDigest := voicecontract.Digest(canonicalInput)
	key, err := deriveVoiceWriteKey(sessionID, callID, toolName, toolCallID, inputDigest)
	if err != nil {
		return VoiceSessionExecuteToolResult{}, err
	}
	if strings.TrimSpace(req.IdempotencyKey) != "" && req.IdempotencyKey != key {
		return VoiceSessionExecuteToolResult{}, errWriteForgedKey
	}
	req.IdempotencyKey = key
	req.Input = canonicalInput
	replay, err := voiceWriteReplayDigest(req, inputDigest)
	if err != nil {
		return VoiceSessionExecuteToolResult{}, err
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
	toolCall := ai.ToolCall{
		ToolCallID: toolCallID,
		ToolName:   toolName,
		Input:      args,
	}
	decision := ai.ApprovalDecision{Type: ai.ApprovalDecisionNotApplicable}
	if tool.NeedsApproval != nil {
		var resolveErr error
		decision, resolveErr = ai.ResolveToolApproval(ctx, map[string]ai.Tool{toolName: tool}, toolCall)
		if resolveErr != nil {
			return VoiceSessionExecuteToolResult{Error: "tool approval policy failed"}, nil
		}
	}
	if decision.Type == ai.ApprovalDecisionDenied {
		return VoiceSessionExecuteToolResult{Error: decision.Reason}, nil
	}
	requiresUserApproval := spec.RequiresApproval || tool.RequiresApproval || decision.Type == ai.ApprovalDecisionUserApproval
	if requiresUserApproval && !req.ApprovalConfirmed {
		approval, approvalErr := newVoiceSessionToolApproval(req, toolCall)
		if approvalErr != nil || approval == nil || strings.TrimSpace(approval.InteractionID) == "" {
			return VoiceSessionExecuteToolResult{Error: "voice write approval is unavailable"}, nil
		}
		return VoiceSessionExecuteToolResult{Approval: approval}, nil
	}
	identity := voiceWriteLedgerIdentity(sessionID, toolName, toolCallID)
	return processWriteLedger.dispatch(identity, key, replay, func() (VoiceSessionExecuteToolResult, error) {
		result, execErr := tool.Execute(ctx, toolCall, ai.ToolExecutionOptions{
			Context: toolsession.ExecutionContextWithWrite(actor, sessionID, key),
		})
		if execErr != nil {
			return VoiceSessionExecuteToolResult{Error: execErr.Error()}, nil
		}
		raw, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			return VoiceSessionExecuteToolResult{Error: fmt.Sprintf("encode tool result: %v", marshalErr)}, nil
		}
		raw, marshalErr = voicecontract.ValidateToolOutput(*spec, raw)
		if marshalErr != nil {
			return VoiceSessionExecuteToolResult{Error: "write result rejected"}, nil
		}
		return VoiceSessionExecuteToolResult{Result: raw}, nil
	})
}
