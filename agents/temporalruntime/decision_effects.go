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

	"github.com/Origens-Dev/gobeyond/agents"
	decisionv1 "github.com/Origens-Dev/gobeyond/agents/decisioncontract/v1"
	"github.com/Origens-Dev/gobeyond/agents/decisions"
	"github.com/Origens-Dev/gobeyond/agents/httpruntime"
	"github.com/Origens-Dev/gobeyond/agents/voicecontract"
)

var (
	ErrDecisionEffectAuthorityRequired = errors.New("decision effect authority is unavailable")
	ErrDecisionEffectOutcomeUnknown    = errors.New("decision effect outcome is unknown; reconcile before retry")
	ErrDecisionEffectApprovalStale     = errors.New("decision effect approval is stale or does not match the pending effect")
)

type DecisionEffectReconciliationState string

const (
	DecisionEffectNotAttempted DecisionEffectReconciliationState = "not_attempted"
	DecisionEffectPending      DecisionEffectReconciliationState = "pending"
	DecisionEffectHasReceipt   DecisionEffectReconciliationState = "receipt"
)

// DecisionEffectReconciliation is a projection read from the existing product
// operation or VoiceWrite receipt authority. NotAttempted is the only state
// that permits a new voice-tool update. Pending includes uncertain provider
// outcomes and must never be interpreted as permission to retry.
type DecisionEffectReconciliation struct {
	State   DecisionEffectReconciliationState `json:"state"`
	Receipt *decisionv1.EffectReceipt         `json:"receipt,omitempty"`
}

// DecisionEffectAuthorization is a short-lived, current-policy projection.
// The adapter resolves the exact opaque target in the old protected snapshot
// and current product policy; it must not accept a menu ordinal or substitute
// a newly ranked candidate. Target fingerprints are digests of the exact
// resolved destination under the old snapshot and current policy.
type DecisionEffectAuthorization struct {
	// VoiceExecutionID identifies the already-running voice session workflow.
	// It is intentionally separate from the decision run ID because the ORI-69
	// reducer workflow owns that ID.
	VoiceExecutionID     string                          `json:"voiceExecutionId"`
	ToolRequest          VoiceSessionExecuteToolInput    `json:"toolRequest"`
	CandidateSnapshot    decisionv1.CandidateSetSnapshot `json:"candidateSnapshot"`
	TargetOpaqueID       string                          `json:"targetOpaqueId,omitempty"`
	TargetSnapshotSHA256 string                          `json:"targetSnapshotSha256,omitempty"`
	OriginalTargetSHA256 string                          `json:"originalTargetSha256,omitempty"`
	CurrentTargetSHA256  string                          `json:"currentTargetSha256,omitempty"`
	PermissionExpiresAt  time.Time                       `json:"permissionExpiresAt"`
}

type decisionApprovalPayload struct {
	InteractionID string
	ToolCallID    string
	InputHash     string
	Effect        decisionv1.EffectIdentity
	Approved      bool
}

func parseDecisionApprovalPayload(raw json.RawMessage) (decisionApprovalPayload, bool, error) {
	var response struct {
		InteractionID string                     `json:"interactionId"`
		ApprovalID    string                     `json:"approvalId"`
		ToolCallID    string                     `json:"toolCallId"`
		InputHash     string                     `json:"inputHash"`
		Effect        *decisionv1.EffectIdentity `json:"effectIdentity"`
		Approved      *bool                      `json:"approved"`
		Answers       struct {
			ToolCallID string                     `json:"toolCallId"`
			InputHash  string                     `json:"inputHash"`
			Effect     *decisionv1.EffectIdentity `json:"effectIdentity"`
			Approved   *bool                      `json:"approved"`
		} `json:"answers"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return decisionApprovalPayload{}, false, fmt.Errorf("decode decision approval response: %w", err)
	}
	interactionID := strings.TrimSpace(response.InteractionID)
	if interactionID == "" {
		interactionID = strings.TrimSpace(response.ApprovalID)
	}
	toolCallID := strings.TrimSpace(response.ToolCallID)
	if toolCallID == "" {
		toolCallID = strings.TrimSpace(response.Answers.ToolCallID)
	}
	inputHash := strings.TrimSpace(response.InputHash)
	if inputHash == "" {
		inputHash = strings.TrimSpace(response.Answers.InputHash)
	}
	approved := response.Approved
	if approved == nil {
		approved = response.Answers.Approved
	}
	effect := response.Effect
	if effect == nil {
		effect = response.Answers.Effect
	}
	if toolCallID == "" && inputHash == "" && approved == nil && effect == nil {
		return decisionApprovalPayload{}, false, nil
	}
	if interactionID == "" || approved == nil || toolCallID == "" || inputHash == "" || effect == nil {
		return decisionApprovalPayload{}, true, errors.New("decision approval response requires approved, toolCallId, inputHash, and effectIdentity")
	}
	if err := effect.Validate(); err != nil {
		return decisionApprovalPayload{}, true, fmt.Errorf("decision approval effect identity: %w", err)
	}
	return decisionApprovalPayload{InteractionID: interactionID, ToolCallID: toolCallID, InputHash: inputHash, Effect: *effect, Approved: *approved}, true, nil
}

// DecisionEffectAdapter is the product-owned boundary for live authority.
// ReconcileDecisionEffect must read the existing operation/receipt SoR. It may
// return NotAttempted only when that SoR has no reservation or effect outcome.
// PrepareDecisionEffect must reauthorize current membership and permission,
// verify the old protected target mapping, and return the existing voice tool
// request. ApplyDecisionOwnershipCAS must invoke the host's existing owner CAS
// with the exact receipt; it must be idempotent for the effect identity.
//
// The dispatcher below performs the decision-to-voice-tool binding and calls
// the existing VoiceSessionExecuteToolUpdate. This interface does not replace
// that execution path or create another receipt database.
type DecisionEffectAdapter interface {
	DecisionSessionAdapter
	ReconcileDecisionEffect(context.Context, httpruntime.RespondCall, decisions.Effect) (DecisionEffectReconciliation, error)
	PrepareDecisionEffect(context.Context, httpruntime.RespondCall, DecisionSessionIdentity, decisions.Effect) (DecisionEffectAuthorization, error)
	VerifyDecisionEffectToolCeiling(context.Context, httpruntime.RespondCall, decisions.Effect, VoiceSessionExecuteToolInput) error
	VerifyDecisionEffectReceipt(context.Context, httpruntime.RespondCall, decisionv1.EffectReceipt) error
	ApplyDecisionOwnershipCAS(context.Context, httpruntime.RespondCall, decisionv1.EffectRequest, decisionv1.EffectReceipt) error
}

func decisionEffectUpdateID(identity decisionv1.EffectIdentity) (string, error) {
	if err := identity.Validate(); err != nil {
		return "", err
	}
	return "decision-effect-" + identity.ID, nil
}

func decisionApprovalUpdateID(prefix string, identity decisionv1.EffectIdentity, approvalID, toolCallID, inputHash, actorID, actorKind string, approved bool) (string, error) {
	if err := identity.Validate(); err != nil {
		return "", err
	}
	if strings.TrimSpace(approvalID) == "" || strings.TrimSpace(toolCallID) == "" || strings.TrimSpace(inputHash) == "" ||
		strings.TrimSpace(actorID) == "" || strings.TrimSpace(actorKind) == "" {
		return "", errors.New("decision approval update identity is incomplete")
	}
	encoded, err := json.Marshal(struct {
		Effect     decisionv1.EffectIdentity `json:"effect"`
		ApprovalID string                    `json:"approvalId"`
		ToolCallID string                    `json:"toolCallId"`
		InputHash  string                    `json:"inputHash"`
		ActorID    string                    `json:"actorId"`
		ActorKind  string                    `json:"actorKind"`
		Approved   bool                      `json:"approved"`
	}{identity, approvalID, toolCallID, inputHash, actorID, actorKind, approved})
	if err != nil {
		return "", err
	}
	canonical, err := voicecontract.CanonicalJSON(encoded, voicecontract.MaxEnvelopeBytes)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return prefix + hex.EncodeToString(digest[:]), nil
}

func decisionReceiptEventID(receipt decisionv1.EffectReceipt) (string, error) {
	if err := receipt.Validate(); err != nil {
		return "", err
	}
	// Observation time is metadata, not receipt identity. Excluding it lets
	// duplicate copies of one authoritative receipt share a stable event ID.
	raw, err := json.Marshal(struct {
		Identity          decisionv1.EffectIdentity `json:"identity"`
		Status            decisionv1.EffectStatus   `json:"status"`
		ReceiptID         string                    `json:"receiptId,omitempty"`
		ProviderRequestID string                    `json:"providerRequestId,omitempty"`
		TargetOpaqueID    string                    `json:"targetOpaqueId,omitempty"`
		FailureCode       string                    `json:"failureCode,omitempty"`
	}{receipt.Identity, receipt.Status, receipt.ReceiptID, receipt.ProviderRequestID, receipt.TargetOpaqueID, receipt.FailureCode})
	if err != nil {
		return "", err
	}
	canonical, err := voicecontract.CanonicalJSON(raw, voicecontract.MaxEnvelopeBytes)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return "effectreceipt_" + hex.EncodeToString(digest[:]), nil
}

func decisionOperationID(identity decisionv1.EffectIdentity) string {
	return "decision_" + identity.ID
}

func hasDecisionEffectWork(effects []decisions.Effect) bool {
	for _, effect := range effects {
		if effect.Kind == decisions.EffectDispatchIntent || effect.Kind == decisions.EffectReleaseOwnership {
			return true
		}
	}
	return false
}

func validateDecisionEffectRequestBinding(identity DecisionSessionIdentity, effect decisions.Effect) error {
	if effect.Request == nil {
		return errors.New("decision effect has no typed request")
	}
	request := effect.Request
	if err := request.Validate(time.Time{}); err != nil {
		return fmt.Errorf("decision effect request: %w", err)
	}
	if request.Identity.TenantID != identity.TenantID || request.Identity.SessionID != identity.SessionID ||
		request.Identity.Generation != identity.Generation || request.Identity.GraphSHA256 != identity.GraphSHA256 ||
		request.Identity.RouteEntryID != effect.RouteEntryID || request.Identity.InputID != effect.InputID ||
		request.Identity.ActionID != effect.ActionID {
		return errors.New("decision effect request does not match its pinned session and reducer step")
	}
	return nil
}

func decisionReceiptUpdate(call httpruntime.RespondCall, identity DecisionSessionIdentity, effect decisions.Effect, receipt decisionv1.EffectReceipt) (DecisionSessionUpdate, string, error) {
	if err := validateDecisionEffectRequestBinding(identity, effect); err != nil {
		return DecisionSessionUpdate{}, "", err
	}
	if err := receipt.Validate(); err != nil {
		return DecisionSessionUpdate{}, "", fmt.Errorf("decision effect receipt: %w", err)
	}
	if receipt.Identity != effect.Request.Identity || receipt.TargetOpaqueID != effect.Request.TargetOpaqueID {
		return DecisionSessionUpdate{}, "", errors.New("decision effect receipt does not match the requested identity and target")
	}
	eventID, err := decisionReceiptEventID(receipt)
	if err != nil {
		return DecisionSessionUpdate{}, "", err
	}
	event := decisionv1.NormalizedEvent{
		ID: eventID, Kind: decisionv1.EventEffectReceipt,
		TenantID: identity.TenantID, SessionID: identity.SessionID,
		Generation: identity.Generation, RouteID: effect.RouteID,
		RouteEntryID: effect.RouteEntryID, Channel: decisionv1.ChannelVoice,
		ReceivedAt: receipt.ObservedAt, Effect: &receipt,
	}
	update := DecisionSessionUpdate{
		Identity: identity, ExpectedRouteEntryID: effect.RouteEntryID,
		Event: decisions.Event{Normalized: &event},
	}
	updateID := "receipt-" + eventID
	if call.Session.ID != identity.SessionID || call.Run.ID != identity.RunID {
		return DecisionSessionUpdate{}, "", errors.New("decision receipt callback does not match its session run")
	}
	return update, updateID, nil
}

func (dispatcher *Dispatcher) dispatchDecisionTransition(ctx context.Context, authority DecisionEffectAdapter, call httpruntime.RespondCall, result DecisionSessionAdvanceResult, emit httpruntime.EventEmitter) error {
	for _, effect := range result.Effects {
		switch effect.Kind {
		case decisions.EffectDispatchIntent:
			if result.Snapshot.Status != DecisionSessionRunning {
				return errors.New("cancelled or stopped decision sessions cannot start a new effect")
			}
			if err := dispatcher.dispatchDecisionIntent(ctx, authority, call, result.Snapshot.Identity, authority.DecisionDefinition(), effect, emit); err != nil {
				return err
			}
		case decisions.EffectReleaseOwnership:
			if err := dispatcher.applyDecisionOwnershipCAS(ctx, authority, call, result.Snapshot.Identity, effect, result, emit); err != nil {
				return err
			}
		}
	}
	return nil
}

// recoverPendingDecisionEffect closes the crash windows between reducer
// transition, provider submission, receipt persistence, and owner CAS. It
// reconciles first on every retry; only a running session with a proven-empty
// existing receipt SoR may enter the voice workflow again.
func (dispatcher *Dispatcher) recoverPendingDecisionEffect(ctx context.Context, authority DecisionEffectAdapter, call httpruntime.RespondCall, emit httpruntime.EventEmitter) (bool, error) {
	workflowID, err := WorkflowID(call.Session.ID, call.Run.ID)
	if err != nil {
		return false, err
	}
	encoded, err := dispatcher.client.QueryWorkflow(ctx, workflowID, "", DecisionSessionPendingEffectQuery)
	if err != nil {
		return false, fmt.Errorf("query pending decision effect: %w", err)
	}
	if encoded == nil {
		return false, errors.New("Temporal returned no pending decision effect query result")
	}
	var pending DecisionSessionPendingEffect
	if err := encoded.Get(&pending); err != nil {
		return false, fmt.Errorf("decode pending decision effect: %w", err)
	}
	if pending.Effect == nil {
		return false, nil
	}
	if pending.Identity.SessionID != call.Session.ID || pending.Identity.RunID != call.Run.ID {
		return false, errors.New("pending decision effect query returned a different session run")
	}
	effect := cloneDecisionEffect(*pending.Effect)
	if effect.Kind == decisions.EffectReleaseOwnership {
		if err := validateDecisionEffectRequestBinding(pending.Identity, effect); err != nil {
			return false, err
		}
		transition := DecisionSessionAdvanceResult{Snapshot: pending.Snapshot, Effects: []decisions.Effect{effect}}
		if err := dispatcher.applyDecisionOwnershipCAS(ctx, authority, call, pending.Identity, effect, transition, emit); err != nil {
			return true, err
		}
		return false, nil
	}
	if effect.Kind != decisions.EffectDispatchIntent && effect.Kind != decisions.EffectAwaitReceipt {
		return false, errors.New("pending decision effect query returned a non-dispatchable effect")
	}
	if err := validateDecisionEffectRequestBinding(pending.Identity, effect); err != nil {
		return false, err
	}
	if pending.ApprovalCommit != nil {
		return dispatcher.completeDecisionApprovalCommit(ctx, authority, call, pending.Identity, effect, *pending.ApprovalCommit, emit)
	}
	effect.Kind = decisions.EffectDispatchIntent
	reconciliation, err := authority.ReconcileDecisionEffect(ctx, call, effect)
	if err != nil {
		return true, fmt.Errorf("reconcile pending decision effect: %w", err)
	}
	switch reconciliation.State {
	case DecisionEffectHasReceipt:
		if err := dispatcher.applyDecisionReceipt(ctx, authority, call, pending.Identity, effect, reconciliation.Receipt, emit); err != nil {
			return true, err
		}
		return false, nil
	case DecisionEffectPending:
		if err := emitDecisionEffectPending(ctx, emit, pending.Identity, effect.Request.Identity.ID); err != nil {
			return true, err
		}
		return true, nil
	case DecisionEffectNotAttempted:
		if pending.Status == DecisionSessionCancelled {
			if pending.DispatchCommitted {
				if err := dispatcher.dispatchCommittedDecisionIntent(ctx, authority, call, pending.Identity, effect, emit); err != nil {
					return true, err
				}
				encoded, err = dispatcher.client.QueryWorkflow(ctx, workflowID, "", DecisionSessionPendingEffectQuery)
				if err != nil {
					return true, fmt.Errorf("recheck committed cancelled effect after recovery: %w", err)
				}
				if encoded == nil {
					return true, errors.New("Temporal returned no committed cancelled effect recovery query result")
				}
				pending = DecisionSessionPendingEffect{}
				if err := encoded.Get(&pending); err != nil {
					return true, fmt.Errorf("decode committed cancelled effect recovery state: %w", err)
				}
				return pending.Effect != nil, nil
			}
			abandon := DecisionSessionAbandonEffect{Identity: pending.Identity, EffectID: effect.Request.Identity.ID}
			ackID := "abandon-" + effect.Request.Identity.ID
			var snapshot DecisionSessionSnapshot
			if err := decisionSessionUpdate(ctx, dispatcher.client, workflowID, DecisionSessionAbandonEffectUpdate, ackID, abandon, &snapshot); err != nil {
				return true, fmt.Errorf("abandon cancelled effect after authoritative no-attempt result: %w", err)
			}
			return false, nil
		}
		if pending.Status != DecisionSessionRunning {
			return true, errors.New("non-running decision session cannot retry an effect")
		}
		if err := dispatcher.dispatchDecisionIntent(ctx, authority, call, pending.Identity, authority.DecisionDefinition(), effect, emit); err != nil {
			return true, err
		}
		encoded, err = dispatcher.client.QueryWorkflow(ctx, workflowID, "", DecisionSessionPendingEffectQuery)
		if err != nil {
			return true, fmt.Errorf("recheck pending decision effect after recovery: %w", err)
		}
		if encoded == nil {
			return true, errors.New("Temporal returned no decision effect recovery query result")
		}
		pending = DecisionSessionPendingEffect{}
		if err := encoded.Get(&pending); err != nil {
			return true, fmt.Errorf("decode recovered decision effect state: %w", err)
		}
		// An accepted approval or uncertain operation remains pending. Avoid
		// advancing the reducer while its prior action is unresolved.
		return pending.Effect != nil, nil
	default:
		return true, errors.New("pending decision effect reconciliation returned an invalid state")
	}
}

func (dispatcher *Dispatcher) queryPendingDecisionEffect(ctx context.Context, call httpruntime.RespondCall) (string, DecisionSessionPendingEffect, error) {
	workflowID, err := WorkflowID(call.Session.ID, call.Run.ID)
	if err != nil {
		return "", DecisionSessionPendingEffect{}, err
	}
	encoded, err := dispatcher.client.QueryWorkflow(ctx, workflowID, "", DecisionSessionPendingEffectQuery)
	if err != nil {
		return "", DecisionSessionPendingEffect{}, fmt.Errorf("query pending decision effect: %w", err)
	}
	if encoded == nil {
		return "", DecisionSessionPendingEffect{}, errors.New("Temporal returned no pending decision effect query result")
	}
	var pending DecisionSessionPendingEffect
	if err := encoded.Get(&pending); err != nil {
		return "", DecisionSessionPendingEffect{}, fmt.Errorf("decode pending decision effect: %w", err)
	}
	if pending.Identity.SessionID != call.Session.ID || pending.Identity.RunID != call.Run.ID {
		return "", DecisionSessionPendingEffect{}, errors.New("pending decision effect query returned a different session run")
	}
	return workflowID, pending, nil
}

func (dispatcher *Dispatcher) respondDecisionApproval(ctx context.Context, authority DecisionEffectAdapter, call httpruntime.RespondCall, payload decisionApprovalPayload, emit httpruntime.EventEmitter) error {
	workflowID, pending, err := dispatcher.queryPendingDecisionEffect(ctx, call)
	if err != nil {
		return err
	}
	if pending.Effect == nil || pending.Effect.Request == nil {
		return ErrDecisionEffectApprovalStale
	}
	effect := cloneDecisionEffect(*pending.Effect)
	request := effect.Request
	if err := validateDecisionEffectRequestBinding(pending.Identity, effect); err != nil {
		return err
	}
	if request.Identity != payload.Effect || payload.ToolCallID != request.Identity.ID ||
		!pending.Submitted || !pending.DispatchCommitted || effect.Kind != decisions.EffectAwaitReceipt {
		return ErrDecisionEffectApprovalStale
	}
	commit := DecisionSessionApprovalCommit{
		Identity: pending.Identity, EffectID: request.Identity.ID, Effect: request.Identity,
		ApprovalID: payload.InteractionID, ToolCallID: payload.ToolCallID, InputHash: payload.InputHash,
		ActorID: call.Actor.ID, ActorKind: call.Actor.Kind, Approved: payload.Approved,
	}
	if pending.ApprovalCommit != nil {
		if *pending.ApprovalCommit != commit {
			return ErrDecisionEffectApprovalStale
		}
		pendingCommitted := cloneDecisionSessionApprovalCommit(pending.ApprovalCommit)
		returnErr, err := dispatcher.completeDecisionApprovalCommit(ctx, authority, call, pending.Identity, effect, *pendingCommitted, emit)
		if err != nil {
			return err
		}
		if returnErr {
			return ErrDecisionEffectOutcomeUnknown
		}
		return nil
	}
	if pending.Status != DecisionSessionRunning {
		return ErrDecisionEffectApprovalStale
	}
	reconciliation, err := authority.ReconcileDecisionEffect(ctx, call, effect)
	if err != nil {
		return fmt.Errorf("reconcile decision effect before approval: %w", err)
	}
	if reconciliation.State == DecisionEffectHasReceipt {
		if !payload.Approved && (reconciliation.Receipt == nil || reconciliation.Receipt.Status != decisionv1.EffectFailed) {
			return ErrDecisionEffectApprovalStale
		}
		return dispatcher.applyDecisionReceipt(ctx, authority, call, pending.Identity, effect, reconciliation.Receipt, emit)
	}
	if reconciliation.State != DecisionEffectNotAttempted && reconciliation.State != DecisionEffectPending {
		return errors.New("decision effect reconciliation returned an invalid state before approval")
	}
	authorization, voiceRequest, err := prepareCurrentDecisionEffectAuthorization(ctx, authority, call, pending.Identity, effect)
	if err != nil {
		return err
	}
	voiceWorkflowID, err := WorkflowID(voiceRequest.SessionID, authorization.VoiceExecutionID)
	if err != nil {
		return err
	}
	encoded, err := dispatcher.client.QueryWorkflow(ctx, voiceWorkflowID, "", VoiceSessionPendingDecisionEffectApprovalQuery, request.Identity)
	if err != nil {
		return fmt.Errorf("query exact pending voice approval: %w", err)
	}
	if encoded == nil {
		return errors.New("Temporal returned no exact pending voice approval query result")
	}
	var approval VoiceSessionDecisionApprovalSnapshot
	if err := encoded.Get(&approval); err != nil {
		return fmt.Errorf("decode exact pending voice approval: %w", err)
	}
	if !approval.Found || approval.InteractionID != payload.InteractionID || approval.ToolCallID != payload.ToolCallID ||
		approval.InputHash != payload.InputHash || approval.InputHash != voiceToolInputHash(voiceRequest.Input) ||
		approval.ActorID != call.Actor.ID || approval.ActorKind != call.Actor.Kind || approval.ToolName != voiceRequest.ToolName ||
		approval.DecisionEffectIdentity == nil || *approval.DecisionEffectIdentity != request.Identity {
		return ErrDecisionEffectApprovalStale
	}
	commitID, err := decisionApprovalUpdateID("decision-approval-commit-", request.Identity, commit.ApprovalID,
		commit.ToolCallID, commit.InputHash, commit.ActorID, commit.ActorKind, commit.Approved)
	if err != nil {
		return err
	}
	var commitSnapshot DecisionSessionSnapshot
	if err := decisionSessionUpdate(ctx, dispatcher.client, workflowID, DecisionSessionApprovalCommitUpdate, commitID, commit, &commitSnapshot); err != nil {
		return fmt.Errorf("commit authenticated decision approval before voice update: %w", err)
	}
	if commitSnapshot.Identity != pending.Identity || commitSnapshot.Status != DecisionSessionRunning {
		return ErrDecisionEffectApprovalStale
	}
	pending.ApprovalCommit = &commit
	pending.DispatchCommitted = true
	if retry, err := dispatcher.completeDecisionApprovalCommit(ctx, authority, call, pending.Identity, effect, commit, emit); err != nil {
		return err
	} else if retry {
		return ErrDecisionEffectOutcomeUnknown
	}
	return nil
}

func prepareCurrentDecisionEffectAuthorization(ctx context.Context, authority DecisionEffectAdapter, call httpruntime.RespondCall, identity DecisionSessionIdentity, effect decisions.Effect) (DecisionEffectAuthorization, VoiceSessionExecuteToolInput, error) {
	authorization, err := authority.PrepareDecisionEffect(ctx, call, identity, effect)
	if err != nil {
		return DecisionEffectAuthorization{}, VoiceSessionExecuteToolInput{}, fmt.Errorf("reauthorize decision effect: %w", err)
	}
	request, err := validateDecisionEffectAuthorization(authority.DecisionDefinition(), identity, effect, authorization, call)
	if err != nil {
		return DecisionEffectAuthorization{}, VoiceSessionExecuteToolInput{}, fmt.Errorf("decision effect authorization rejected: %w", err)
	}
	if err := authority.VerifyDecisionEffectToolCeiling(ctx, call, effect, request); err != nil {
		return DecisionEffectAuthorization{}, VoiceSessionExecuteToolInput{}, fmt.Errorf("verify current signed voice tool ceiling: %w", err)
	}
	return authorization, request, nil
}

func (dispatcher *Dispatcher) completeDecisionApprovalCommit(ctx context.Context, authority DecisionEffectAdapter, call httpruntime.RespondCall, identity DecisionSessionIdentity, effect decisions.Effect, commit DecisionSessionApprovalCommit, emit httpruntime.EventEmitter) (bool, error) {
	if err := validateDecisionEffectRequestBinding(identity, effect); err != nil {
		return true, err
	}
	if commit.Identity != identity || commit.Effect != effect.Request.Identity ||
		commit.EffectID != effect.Request.Identity.ID || commit.ToolCallID != effect.Request.Identity.ID ||
		call.Actor.ID != commit.ActorID || call.Actor.Kind != commit.ActorKind {
		return true, ErrDecisionEffectApprovalStale
	}
	reconciliation, err := authority.ReconcileDecisionEffect(ctx, call, effect)
	if err != nil {
		return true, fmt.Errorf("reconcile decision effect before committed approval update: %w", err)
	}
	if reconciliation.State == DecisionEffectHasReceipt {
		if !commit.Approved && (reconciliation.Receipt == nil || reconciliation.Receipt.Status != decisionv1.EffectFailed) {
			return true, ErrDecisionEffectApprovalStale
		}
		if err := dispatcher.applyDecisionReceipt(ctx, authority, call, identity, effect, reconciliation.Receipt, emit); err != nil {
			return true, err
		}
		return false, nil
	}
	if reconciliation.State != DecisionEffectNotAttempted && reconciliation.State != DecisionEffectPending {
		return true, errors.New("decision effect reconciliation returned an invalid state for committed approval")
	}
	authorization, voiceRequest, err := prepareCurrentDecisionEffectAuthorization(ctx, authority, call, identity, effect)
	if err != nil {
		return true, err
	}
	voiceWorkflowID, err := WorkflowID(voiceRequest.SessionID, authorization.VoiceExecutionID)
	if err != nil {
		return true, err
	}
	response := VoiceSessionApprovalResponse{
		InteractionID: commit.ApprovalID, Approved: commit.Approved, ActorID: commit.ActorID,
		ActorKind: commit.ActorKind, ToolCallID: commit.ToolCallID, InputHash: commit.InputHash,
		DecisionEffectIdentity: cloneEffectIdentity(&commit.Effect),
	}
	updateID, err := decisionApprovalUpdateID("decision-voice-approval-", commit.Effect,
		commit.ApprovalID, commit.ToolCallID, commit.InputHash, commit.ActorID, commit.ActorKind, commit.Approved)
	if err != nil {
		return true, err
	}
	var toolResult VoiceSessionExecuteToolResult
	updateErr := decisionSessionUpdate(ctx, dispatcher.client, voiceWorkflowID, VoiceSessionApproveToolUpdate, updateID, response, &toolResult)
	if toolResult.Approval != nil {
		return true, errors.New("voice approval update returned another pending approval")
	}
	reconciliation, err = authority.ReconcileDecisionEffect(ctx, call, effect)
	if err != nil {
		return true, fmt.Errorf("reconcile decision effect after voice approval: %w", err)
	}
	if reconciliation.State == DecisionEffectHasReceipt {
		if !commit.Approved && (reconciliation.Receipt == nil || reconciliation.Receipt.Status != decisionv1.EffectFailed) {
			return true, errors.New("denied voice approval did not reconcile to an authoritative failed receipt")
		}
		if err := dispatcher.applyDecisionReceipt(ctx, authority, call, identity, effect, reconciliation.Receipt, emit); err != nil {
			return true, err
		}
		return false, nil
	}
	if reconciliation.State == DecisionEffectNotAttempted && updateErr == nil &&
		((!commit.Approved && toolResult.Error == "tool approval denied") || toolResult.Error == "tool approval expired") {
		failureCode := "approval_denied"
		if toolResult.Error == "tool approval expired" {
			failureCode = "approval_expired"
		}
		if err := dispatcher.applyDecisionApprovalFailure(ctx, authority, call, identity, effect, commit, failureCode, emit); err != nil {
			return true, err
		}
		return false, nil
	}
	if reconciliation.State == DecisionEffectPending || reconciliation.State == DecisionEffectNotAttempted {
		if err := emitDecisionEffectPending(ctx, emit, identity, commit.EffectID); err != nil {
			return true, err
		}
		if updateErr != nil {
			return true, fmt.Errorf("voice approval update outcome is unresolved: %w", updateErr)
		}
		return true, ErrDecisionEffectOutcomeUnknown
	}
	return true, errors.New("decision effect reconciliation returned an invalid state after voice approval")
}

func (dispatcher *Dispatcher) applyDecisionApprovalFailure(ctx context.Context, authority DecisionEffectAdapter, call httpruntime.RespondCall, identity DecisionSessionIdentity, effect decisions.Effect, commit DecisionSessionApprovalCommit, failureCode string, emit httpruntime.EventEmitter) error {
	if err := validateDecisionEffectRequestBinding(identity, effect); err != nil {
		return err
	}
	if commit.Identity != identity || commit.Effect != effect.Request.Identity ||
		(failureCode == "approval_denied" && commit.Approved) ||
		(failureCode != "approval_denied" && failureCode != "approval_expired") {
		return ErrDecisionEffectApprovalStale
	}
	receiptID, err := decisionApprovalUpdateID("", commit.Effect, commit.ApprovalID,
		commit.ToolCallID, commit.InputHash, commit.ActorID, commit.ActorKind, commit.Approved)
	if err != nil {
		return err
	}
	receipt := decisionv1.EffectReceipt{
		Identity: commit.Effect, Status: decisionv1.EffectFailed,
		ReceiptID: "approval-failure-" + receiptID[:32], TargetOpaqueID: effect.Request.TargetOpaqueID,
		FailureCode: failureCode, ObservedAt: time.Now().UTC(),
	}
	if err := receipt.Validate(); err != nil {
		return err
	}
	update, updateID, err := decisionReceiptUpdate(call, identity, effect, receipt)
	if err != nil {
		return err
	}
	workflowID, err := WorkflowID(call.Session.ID, call.Run.ID)
	if err != nil {
		return err
	}
	var advanced DecisionSessionAdvanceResult
	if err := decisionSessionUpdate(ctx, dispatcher.client, workflowID, DecisionSessionReceiptUpdate, updateID, update, &advanced); err != nil {
		return fmt.Errorf("apply durably committed decision approval failure: %w", err)
	}
	if err := dispatcher.emitDecisionTransition(ctx, emit, advanced); err != nil {
		return err
	}
	return dispatcher.dispatchDecisionTransition(ctx, authority, call, advanced, emit)
}

func (dispatcher *Dispatcher) dispatchDecisionIntent(ctx context.Context, authority DecisionEffectAdapter, call httpruntime.RespondCall, identity DecisionSessionIdentity, definition decisionv1.Definition, effect decisions.Effect, emit httpruntime.EventEmitter) error {
	if err := validateDecisionEffectRequestBinding(identity, effect); err != nil {
		return err
	}
	request := *effect.Request
	reconciliation, err := authority.ReconcileDecisionEffect(ctx, call, effect)
	if err != nil {
		return fmt.Errorf("reconcile decision effect before dispatch: %w", err)
	}
	switch reconciliation.State {
	case DecisionEffectHasReceipt:
		if err := dispatcher.submitAndCommitDecisionEffect(ctx, call, identity, effect); err != nil {
			return fmt.Errorf("record reconciled decision effect before receipt: %w", err)
		}
		return dispatcher.applyDecisionReceipt(ctx, authority, call, identity, effect, reconciliation.Receipt, emit)
	case DecisionEffectPending:
		if err := dispatcher.submitAndCommitDecisionEffect(ctx, call, identity, effect); err != nil {
			return fmt.Errorf("record pending decision effect before waiting: %w", err)
		}
		return emitDecisionEffectPending(ctx, emit, identity, request.Identity.ID)
	case DecisionEffectNotAttempted:
	default:
		return errors.New("decision effect reconciliation returned an invalid state")
	}
	if reconciliation.Receipt != nil {
		return errors.New("decision effect not-attempted state carried a receipt")
	}

	authorization, err := authority.PrepareDecisionEffect(ctx, call, identity, effect)
	if err != nil {
		return fmt.Errorf("reauthorize decision effect: %w", err)
	}
	voiceRequest, err := validateDecisionEffectAuthorization(definition, identity, effect, authorization, call)
	if err != nil {
		return fmt.Errorf("decision effect authorization rejected: %w", err)
	}
	if err := authority.VerifyDecisionEffectToolCeiling(ctx, call, effect, voiceRequest); err != nil {
		return fmt.Errorf("verify current signed voice tool ceiling: %w", err)
	}
	if err := dispatcher.submitAndCommitDecisionEffect(ctx, call, identity, effect); err != nil {
		return fmt.Errorf("commit decision effect before voice update: %w", err)
	}
	return dispatcher.dispatchCommittedDecisionIntent(ctx, authority, call, identity, effect, emit)
}

func (dispatcher *Dispatcher) submitAndCommitDecisionEffect(ctx context.Context, call httpruntime.RespondCall, identity DecisionSessionIdentity, effect decisions.Effect) error {
	if effect.Request == nil {
		return errors.New("decision effect submission has no typed request")
	}
	workflowID, err := WorkflowID(call.Session.ID, call.Run.ID)
	if err != nil {
		return err
	}
	var submitted DecisionSessionAdvanceResult
	if err := decisionSessionUpdate(ctx, dispatcher.client, workflowID, DecisionSessionSubmissionUpdate,
		"submission-"+effect.Request.Identity.ID,
		DecisionSessionEffectSubmission{Identity: identity, Effect: cloneDecisionEffect(effect)}, &submitted); err != nil {
		return fmt.Errorf("record decision effect submission: %w", err)
	}
	if err := dispatcher.commitDecisionEffectDispatch(ctx, workflowID, identity, effect.Request.Identity.ID); err != nil {
		return err
	}
	return nil
}

// dispatchCommittedDecisionIntent resumes only after the reducer has durably
// linearized dispatch against cancellation. It may be used after cancellation
// only for an already-committed intent whose existing effect authority proves
// that no provider attempt exists.
func (dispatcher *Dispatcher) dispatchCommittedDecisionIntent(ctx context.Context, authority DecisionEffectAdapter, call httpruntime.RespondCall, identity DecisionSessionIdentity, effect decisions.Effect, emit httpruntime.EventEmitter) error {
	if err := validateDecisionEffectRequestBinding(identity, effect); err != nil {
		return err
	}
	request := effect.Request
	authorization, voiceRequest, err := prepareCurrentDecisionEffectAuthorization(ctx, authority, call, identity, effect)
	if err != nil {
		return err
	}
	voiceWorkflowID, err := WorkflowID(voiceRequest.SessionID, authorization.VoiceExecutionID)
	if err != nil {
		return err
	}
	updateID, err := decisionEffectUpdateID(request.Identity)
	if err != nil {
		return err
	}
	var toolResult VoiceSessionExecuteToolResult
	dispatchErr := decisionSessionUpdate(ctx, dispatcher.client, voiceWorkflowID, VoiceSessionExecuteToolUpdate, updateID, voiceRequest, &toolResult)
	if toolResult.Approval != nil && dispatchErr == nil {
		if err := validateDecisionEffectVoiceApproval(call, request.Identity, voiceRequest, *toolResult.Approval); err != nil {
			return fmt.Errorf("voice tool approval binding rejected: %w", err)
		}
		if err := emitDecisionApproval(ctx, emit, identity, request.Identity, *toolResult.Approval); err != nil {
			return err
		}
		return emitDecisionEffectPending(ctx, emit, identity, request.Identity.ID)
	}
	reconciliation, err := authority.ReconcileDecisionEffect(ctx, call, effect)
	if err != nil {
		return fmt.Errorf("reconcile decision effect after committed voice dispatch: %w", err)
	}
	switch reconciliation.State {
	case DecisionEffectHasReceipt:
		return dispatcher.applyDecisionReceipt(ctx, authority, call, identity, effect, reconciliation.Receipt, emit)
	case DecisionEffectPending:
		return emitDecisionEffectPending(ctx, emit, identity, request.Identity.ID)
	case DecisionEffectNotAttempted:
		if dispatchErr != nil {
			return fmt.Errorf("dispatch committed decision effect through voice session: %w", dispatchErr)
		}
		return errors.New("committed voice tool returned without an authoritative decision effect receipt")
	default:
		return errors.New("committed decision effect reconciliation returned an invalid state")
	}
}

func (dispatcher *Dispatcher) commitDecisionEffectDispatch(ctx context.Context, workflowID string, identity DecisionSessionIdentity, effectID string) error {
	var snapshot DecisionSessionSnapshot
	updateID := "dispatch-commit-" + effectID
	if err := decisionSessionUpdate(ctx, dispatcher.client, workflowID, DecisionSessionDispatchCommitUpdate, updateID,
		DecisionSessionEffectDispatchCommit{Identity: identity, EffectID: effectID}, &snapshot); err != nil {
		return err
	}
	if snapshot.Identity != identity || snapshot.Status != DecisionSessionRunning {
		return errors.New("decision effect dispatch commit was not accepted by the running session")
	}
	return nil
}

func (dispatcher *Dispatcher) applyDecisionReceipt(ctx context.Context, authority DecisionEffectAdapter, call httpruntime.RespondCall, identity DecisionSessionIdentity, effect decisions.Effect, receipt *decisionv1.EffectReceipt, emit httpruntime.EventEmitter) error {
	if err := validateDecisionEffectRequestBinding(identity, effect); err != nil {
		return err
	}
	if receipt == nil {
		return errors.New("decision effect receipt authority returned no receipt")
	}
	if err := receipt.Validate(); err != nil {
		return fmt.Errorf("authoritative decision effect receipt: %w", err)
	}
	if receipt.Identity != effect.Request.Identity || receipt.TargetOpaqueID != effect.Request.TargetOpaqueID {
		return errors.New("authoritative decision effect receipt does not match the pending effect")
	}
	if err := authority.VerifyDecisionEffectReceipt(ctx, call, *receipt); err != nil {
		return fmt.Errorf("verify decision effect receipt against existing authority: %w", err)
	}
	update, updateID, err := decisionReceiptUpdate(call, identity, effect, *receipt)
	if err != nil {
		return err
	}
	workflowID, err := WorkflowID(call.Session.ID, call.Run.ID)
	if err != nil {
		return err
	}
	var advanced DecisionSessionAdvanceResult
	if err := decisionSessionUpdate(ctx, dispatcher.client, workflowID, DecisionSessionReceiptUpdate, updateID, update, &advanced); err != nil {
		return fmt.Errorf("apply decision effect receipt: %w", err)
	}
	if err := dispatcher.emitDecisionTransition(ctx, emit, advanced); err != nil {
		return err
	}
	return dispatcher.dispatchDecisionTransition(ctx, authority, call, advanced, emit)
}

func (dispatcher *Dispatcher) applyDecisionOwnershipCAS(ctx context.Context, authority DecisionEffectAdapter, call httpruntime.RespondCall, identity DecisionSessionIdentity, effect decisions.Effect, transition DecisionSessionAdvanceResult, emit httpruntime.EventEmitter) error {
	if err := validateDecisionEffectRequestBinding(identity, effect); err != nil {
		return err
	}
	if effect.ReceiptID == "" {
		return errors.New("decision ownership release is missing its confirmed request receipt")
	}
	if effect.Request.Identity.TenantID != identity.TenantID || effect.Request.Identity.SessionID != identity.SessionID || effect.Request.Identity.Generation != identity.Generation {
		return errors.New("decision ownership release does not match the pinned session")
	}
	if transition.Snapshot.View.Ownership.State != decisionv1.OwnershipReleased || transition.Snapshot.View.Ownership.ReleaseReceiptID != effect.ReceiptID {
		return errors.New("decision ownership release is not backed by the reducer's confirmed receipt")
	}
	if transition.Snapshot.Status == DecisionSessionCancelled {
		// Cancellation cannot roll back an already-confirmed provider effect.
		// The existing owner CAS still has to reconcile the exact receipt.
	}
	reconciliation, err := authority.ReconcileDecisionEffect(ctx, call, effect)
	if err != nil {
		return fmt.Errorf("reconcile confirmed effect before owner compare-and-swap: %w", err)
	}
	if reconciliation.State != DecisionEffectHasReceipt || reconciliation.Receipt == nil ||
		reconciliation.Receipt.Status != decisionv1.EffectConfirmed || reconciliation.Receipt.Identity != effect.Request.Identity ||
		reconciliation.Receipt.ReceiptID != effect.ReceiptID || reconciliation.Receipt.TargetOpaqueID != effect.Request.TargetOpaqueID {
		return errors.New("call-owner compare-and-swap requires the authoritative confirmed receipt")
	}
	if err := authority.VerifyDecisionEffectReceipt(ctx, call, *reconciliation.Receipt); err != nil {
		return fmt.Errorf("verify confirmed effect before owner compare-and-swap: %w", err)
	}
	if err := authority.ApplyDecisionOwnershipCAS(ctx, call, *effect.Request, *reconciliation.Receipt); err != nil {
		return fmt.Errorf("apply existing call-owner compare-and-swap: %w", err)
	}
	workflowID, err := WorkflowID(call.Session.ID, call.Run.ID)
	if err != nil {
		return err
	}
	ack := DecisionSessionOwnershipCAS{Identity: identity, EffectID: effect.Request.Identity.ID, ReceiptID: effect.ReceiptID}
	ackID := "owner-cas-" + effect.Request.Identity.ID + "-" + effect.ReceiptID
	var snapshot DecisionSessionSnapshot
	if err := decisionSessionUpdate(ctx, dispatcher.client, workflowID, DecisionSessionOwnershipCASUpdate, ackID, ack, &snapshot); err != nil {
		return fmt.Errorf("acknowledge committed existing call-owner compare-and-swap: %w", err)
	}
	return nil
}

func (dispatcher *Dispatcher) emitDecisionTransition(ctx context.Context, emit httpruntime.EventEmitter, result DecisionSessionAdvanceResult) error {
	if len(result.Effects) == 0 {
		return nil
	}
	if emit == nil {
		return errors.New("decision effect event emitter is required")
	}
	return emit.Emit(ctx, "agent.decision.transition", result)
}

func emitDecisionEffectPending(ctx context.Context, emit httpruntime.EventEmitter, identity DecisionSessionIdentity, effectID string) error {
	if emit == nil {
		return nil
	}
	return emit.Emit(ctx, "agent.decision.effect_pending", struct {
		Identity DecisionSessionIdentity `json:"identity"`
		EffectID string                  `json:"effectId"`
	}{Identity: identity, EffectID: effectID})
}

func emitDecisionApproval(ctx context.Context, emit httpruntime.EventEmitter, identity DecisionSessionIdentity, effectIdentity decisionv1.EffectIdentity, approval VoiceSessionToolApproval) error {
	if emit == nil {
		return errors.New("decision effect approval emitter is required")
	}
	// Do not publish the approval's raw tool input. The existing voice approval
	// workflow remains the authority for actor binding and approval completion.
	return emit.Emit(ctx, "agent.decision.effect_approval", struct {
		Identity       DecisionSessionIdentity   `json:"identity"`
		EffectID       string                    `json:"effectId"`
		EffectIdentity decisionv1.EffectIdentity `json:"effectIdentity"`
		InteractionID  string                    `json:"interactionId"`
		ActorID        string                    `json:"actorId"`
		ActorKind      string                    `json:"actorKind"`
		ToolCallID     string                    `json:"toolCallId"`
		ToolName       string                    `json:"toolName"`
		InputHash      string                    `json:"inputHash"`
		ExpiresAt      time.Time                 `json:"expiresAt"`
	}{Identity: identity, EffectID: effectIdentity.ID, EffectIdentity: effectIdentity, InteractionID: approval.InteractionID,
		ActorID: approval.ActorID, ActorKind: approval.ActorKind, ToolName: approval.ToolName,
		ToolCallID: approval.ToolCallID, InputHash: approval.InputHash, ExpiresAt: approval.ExpiresAt})
}

func validateDecisionEffectVoiceApproval(call httpruntime.RespondCall, identity decisionv1.EffectIdentity, request VoiceSessionExecuteToolInput, approval VoiceSessionToolApproval) error {
	if approval.DecisionEffectIdentity == nil || *approval.DecisionEffectIdentity != identity ||
		approval.ToolCallID != identity.ID || request.ToolCallID != identity.ID || approval.ToolName != request.ToolName ||
		approval.ActorID != call.Actor.ID || approval.ActorKind != call.Actor.Kind ||
		approval.InputHash == "" || approval.InputHash != voiceToolInputHash(request.Input) ||
		!approval.ExpiresAt.IsZero() && !approval.ExpiresAt.After(time.Now().UTC()) {
		return errors.New("voice approval does not match the current effect, actor, tool input, or expiry")
	}
	return nil
}

func validateDecisionEffectAuthorization(definition decisionv1.Definition, identity DecisionSessionIdentity, effect decisions.Effect, authorization DecisionEffectAuthorization, call httpruntime.RespondCall) (VoiceSessionExecuteToolInput, error) {
	request := effect.Request
	if request == nil {
		return VoiceSessionExecuteToolInput{}, errors.New("decision effect request is required")
	}
	if err := definition.ValidateForActivation(); err != nil {
		return VoiceSessionExecuteToolInput{}, fmt.Errorf("decision definition is not activation-ready: %w", err)
	}
	if err := request.ValidateForDispatch(definition, effect.RouteID, &authorization.CandidateSnapshot,
		identity.TenantID, identity.SessionID, identity.Generation, time.Now().UTC()); err != nil {
		return VoiceSessionExecuteToolInput{}, err
	}
	if request.TargetBinding != "" && request.TargetOpaqueID == "" {
		return VoiceSessionExecuteToolInput{}, errors.New("decision action has no exact protected target")
	}
	if request.TargetOpaqueID != "" {
		if authorization.TargetOpaqueID != request.TargetOpaqueID || authorization.TargetSnapshotSHA256 != request.CandidateSetSHA256 ||
			authorization.OriginalTargetSHA256 == "" || authorization.CurrentTargetSHA256 == "" || authorization.OriginalTargetSHA256 != authorization.CurrentTargetSHA256 {
			return VoiceSessionExecuteToolInput{}, errors.New("current target no longer matches the protected candidate")
		}
	}
	if !authorization.PermissionExpiresAt.IsZero() && !authorization.PermissionExpiresAt.After(time.Now().UTC()) {
		return VoiceSessionExecuteToolInput{}, errors.New("current decision effect permission is expired")
	}
	voiceRequest := authorization.ToolRequest
	if strings.TrimSpace(authorization.VoiceExecutionID) == "" || authorization.VoiceExecutionID == identity.RunID ||
		voiceRequest.AgentID != call.Run.AgentID || voiceRequest.SessionID != call.Session.ID || voiceRequest.ToolCallID != request.Identity.ID {
		return VoiceSessionExecuteToolInput{}, errors.New("voice tool request is not bound to this decision session and effect")
	}
	if voiceRequest.ActorID != call.Actor.ID || voiceRequest.ActorKind != call.Actor.Kind {
		return VoiceSessionExecuteToolInput{}, errors.New("voice tool request actor does not match the authenticated decision actor")
	}
	if networkID := strings.TrimSpace(call.Actor.Metadata["network_id"]); voiceRequest.NetworkID != networkID {
		return VoiceSessionExecuteToolInput{}, errors.New("voice tool request network does not match the authenticated decision actor")
	}
	return validateDecisionVoiceToolGrant(definition, effect, voiceRequest)
}

func validateDecisionVoiceToolGrant(decisionDefinition decisionv1.Definition, effect decisions.Effect, request VoiceSessionExecuteToolInput) (VoiceSessionExecuteToolInput, error) {
	if effect.Request == nil {
		return VoiceSessionExecuteToolInput{}, errors.New("decision effect request is required")
	}
	registry := ProcessVoiceRegistry()
	if registry == nil {
		return VoiceSessionExecuteToolInput{}, errors.New("voice tool registry unavailable")
	}
	raw, digest, ok := registry.Manifest(request.AgentID)
	if !ok || digest == "" || digest != request.ManifestDigest {
		return VoiceSessionExecuteToolInput{}, errors.New("decision tool manifest does not match current voice registry")
	}
	var manifest voicecontract.Manifest
	if err := voicecontract.Decode(raw, voicecontract.MaxManifestBytes, &manifest); err != nil {
		return VoiceSessionExecuteToolInput{}, fmt.Errorf("decode decision voice manifest: %w", err)
	}
	if manifest.CompiledRevision != request.AgentRevision {
		return VoiceSessionExecuteToolInput{}, errors.New("decision tool revision does not match current voice registry")
	}
	var spec *voicecontract.Tool
	for index := range manifest.Tools {
		if manifest.Tools[index].ID == effect.Request.ToolID {
			spec = &manifest.Tools[index]
			break
		}
	}
	if spec == nil {
		return VoiceSessionExecuteToolInput{}, errors.New("decision tool is not in the current voice manifest")
	}
	definition, ok := registry.Definition(request.AgentID)
	if !ok {
		return VoiceSessionExecuteToolInput{}, errors.New("decision voice definition unavailable")
	}
	tool, ok := lookupDefinitionTool(definition, spec.Name)
	if !ok || tool.Execute == nil {
		return VoiceSessionExecuteToolInput{}, errors.New("decision tool schema does not match its frozen grant")
	}
	grant, ok := decisionToolGrant(decisionDefinition, effect.Request.ToolID)
	if !ok || grant.SchemaSHA256 != strings.TrimPrefix(spec.SchemaDigest, "sha256:") || (grant.ApprovalSHA256 != "") != spec.RequiresApproval {
		return VoiceSessionExecuteToolInput{}, errors.New("decision tool exceeds or differs from its frozen authority grant")
	}
	if !voiceToolAllowed(request.AllowedToolIDs, spec.ID) && !voiceToolAllowed(request.AllowedToolIDs, spec.Name) {
		return VoiceSessionExecuteToolInput{}, errors.New("signed voice tool ceiling does not allow this decision action")
	}
	switch effect.Request.Kind {
	case decisionv1.EffectWrite, decisionv1.EffectCustom:
		if !spec.IsWrite() || !agents.VoiceWritePolicy(tool) || request.CallControl != nil {
			return VoiceSessionExecuteToolInput{}, errors.New("decision write effect is not bound to an existing voice write tool")
		}
	case decisionv1.EffectConnect, decisionv1.EffectHandoff:
		if spec.ExecutionKind != "" && spec.ExecutionKind != "call_control" || request.CallControl == nil {
			return VoiceSessionExecuteToolInput{}, errors.New("decision call-control effect is not bound to an existing call-control tool")
		}
		if _, ok := agents.VoiceControlPolicy(tool); !ok {
			return VoiceSessionExecuteToolInput{}, errors.New("decision call-control tool has no authored voice-control policy")
		}
		command := *request.CallControl
		if command.Version != voicecontract.Version || command.ToolID != spec.ID || command.Context.Scope.Kind != "agent" ||
			command.Context.AgentID != request.AgentID || command.Context.SessionID != request.SessionID ||
			command.Context.Generation != effect.Request.Identity.Generation || command.Context.ManifestDigest != request.ManifestDigest {
			return VoiceSessionExecuteToolInput{}, errors.New("decision call-control command is outside the current signed session scope")
		}
		command.OperationID = decisionOperationID(effect.Request.Identity)
		command.ToolCallID = effect.Request.Identity.ID
		command.InputDigest = voicecontract.Digest(command.Arguments)
		if err := command.Validate(); err != nil {
			return VoiceSessionExecuteToolInput{}, fmt.Errorf("decision call-control command: %w", err)
		}
		request.CallControl = &command
		request.Grant = strings.TrimSpace(request.Grant)
		if request.Grant == "" {
			return VoiceSessionExecuteToolInput{}, errors.New("decision call-control signed grant is required")
		}
	default:
		return VoiceSessionExecuteToolInput{}, errors.New("decision effect kind is not executable through the voice tool authority")
	}
	request.ToolName = spec.Name
	request.ToolCallID = effect.Request.Identity.ID
	effectIdentity := effect.Request.Identity
	request.DecisionEffectIdentity = &effectIdentity
	request.IdempotencyKey = ""
	request.WriteReconcileOnly = false
	// Narrow the API-verified tool list to the exact inherited tool for this
	// operation. The current signed grant check above remains authoritative.
	request.AllowedToolIDs = []string{spec.ID}
	return request, nil
}

func decisionToolGrant(definition decisionv1.Definition, toolID string) (decisionv1.ToolGrant, bool) {
	for _, grant := range definition.Graph.Authority.Tools {
		if grant.ID == toolID {
			return grant, true
		}
	}
	return decisionv1.ToolGrant{}, false
}
