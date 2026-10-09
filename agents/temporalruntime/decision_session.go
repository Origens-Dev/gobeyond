package temporalruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"

	"github.com/Origens-Dev/gobeyond/agents"
	decisionv1 "github.com/Origens-Dev/gobeyond/agents/decisioncontract/v1"
	"github.com/Origens-Dev/gobeyond/agents/decisions"
	"github.com/Origens-Dev/gobeyond/agents/httpruntime"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

const (
	DecisionSessionWorkflowName         = "gobeyond.agents.decision_session.v1"
	DecisionSessionPendingEffectQuery   = "gobeyond.agents.decision_session.pending_effect.v1"
	DecisionSessionInitialUpdate        = "gobeyond.agents.decision_session.initial.v1"
	DecisionSessionAdvanceUpdate        = "gobeyond.agents.decision_session.advance.v1"
	DecisionSessionSubmissionUpdate     = "gobeyond.agents.decision_session.effect_submission.v1"
	DecisionSessionDispatchCommitUpdate = "gobeyond.agents.decision_session.effect_dispatch_commit.v1"
	DecisionSessionApprovalCommitUpdate = "gobeyond.agents.decision_session.effect_approval_commit.v1"
	DecisionSessionReceiptUpdate        = "gobeyond.agents.decision_session.effect_receipt.v1"
	DecisionSessionOwnershipCASUpdate   = "gobeyond.agents.decision_session.ownership_cas.v1"
	DecisionSessionAbandonEffectUpdate  = "gobeyond.agents.decision_session.abandon_effect.v1"
	DecisionSessionCancelUpdate         = "gobeyond.agents.decision_session.cancel.v1"
	DecisionSessionSnapshotQuery        = "gobeyond.agents.decision_session.snapshot.v1"
)

var (
	ErrDecisionSessionAdapterRequired = errors.New("durable decision adapter does not provide the dormant session seam")
	ErrDecisionSessionStopped         = errors.New("decision session stopped safely")
)

// DecisionSessionInput contains only the frozen graph, its verified session
// pin, and a normalized admission event. It must never contain raw audio,
// transcript text, rendered prompt text, credentials, or provider clients.
type DecisionSessionInput struct {
	Definition decisionv1.Definition      `json:"definition"`
	Pin        decisionv1.SessionPin      `json:"pin"`
	Policy     decisions.Policy           `json:"policy,omitempty"`
	Admission  decisionv1.NormalizedEvent `json:"admission"`
	RunID      string                     `json:"runId"`
}

// DecisionSessionIdentity is echoed on callbacks so a replaced or stale host
// cannot move a workflow pinned to another graph, snapshot, locale, prompt, or
// ownership generation.
type DecisionSessionIdentity struct {
	TenantID             string `json:"tenantId"`
	SessionID            string `json:"sessionId"`
	RunID                string `json:"runId"`
	SchemaVersion        string `json:"schemaVersion"`
	ReleaseSHA256        string `json:"releaseSha256"`
	GraphSHA256          string `json:"graphSha256"`
	PromptFamiliesSHA256 string `json:"promptFamiliesSha256"`
	SnapshotSHA256       string `json:"snapshotSha256"`
	Generation           uint64 `json:"generation"`
	Locale               string `json:"locale"`
	PromptFamily         string `json:"promptFamily"`
	PromptVariantLocale  string `json:"promptVariantLocale"`
}

// DecisionSessionUpdate is a server-normalized reducer event. Any protected
// references are opaque references already authorized by the host authority;
// this workflow validates only their scope and explicit expiry. Inline DTMF
// digits are unsupported by this durable seam. Identifier and score-field
// name validation checks syntax only; a trusted adapter must not encode
// sensitive values in otherwise-valid IDs or field names.
type DecisionSessionUpdate struct {
	Identity             DecisionSessionIdentity         `json:"identity"`
	ExpectedRouteEntryID string                          `json:"expectedRouteEntryId"`
	AuthorizedReferences []decisionv1.ProtectedReference `json:"authorizedReferences,omitempty"`
	Event                decisions.Event                 `json:"event"`
}

// DecisionSessionCancel carries the same immutable session identity used by
// every update. Cancellation does not change call ownership or create an
// effect receipt.
type DecisionSessionCancel struct {
	Identity DecisionSessionIdentity `json:"identity"`
}

// DecisionSessionEffectSubmission binds the reducer's typed dispatch intent
// to the exact submission event before any existing voice tool update runs.
type DecisionSessionEffectSubmission struct {
	Identity DecisionSessionIdentity `json:"identity"`
	Effect   decisions.Effect        `json:"effect"`
}

// DecisionSessionEffectDispatchCommit linearizes cancellation against the
// outbound voice-tool update. A successful commit wins a later cancellation;
// cancellation accepted first prevents provider dispatch.
type DecisionSessionEffectDispatchCommit struct {
	Identity DecisionSessionIdentity `json:"identity"`
	EffectID string                  `json:"effectId"`
}

// DecisionSessionApprovalCommit durably binds an authenticated approval
// choice to one submitted effect before the existing voice approval update.
type DecisionSessionApprovalCommit struct {
	Identity   DecisionSessionIdentity   `json:"identity"`
	EffectID   string                    `json:"effectId"`
	Effect     decisionv1.EffectIdentity `json:"effectIdentity"`
	ApprovalID string                    `json:"approvalId"`
	ToolCallID string                    `json:"toolCallId"`
	InputHash  string                    `json:"inputHash"`
	ActorID    string                    `json:"actorId"`
	ActorKind  string                    `json:"actorKind"`
	Approved   bool                      `json:"approved"`
}

// DecisionSessionOwnershipCAS acknowledges the host's existing owner CAS only
// after it has verified the exact confirmed effect receipt.
type DecisionSessionOwnershipCAS struct {
	Identity  DecisionSessionIdentity `json:"identity"`
	EffectID  string                  `json:"effectId"`
	ReceiptID string                  `json:"receiptId"`
}

// DecisionSessionPendingEffect is a minimal recovery projection. It contains
// only the typed intent and workflow identity, never hydrated input or prompt
// material.
type DecisionSessionPendingEffect struct {
	Identity          DecisionSessionIdentity        `json:"identity"`
	Effect            *decisions.Effect              `json:"effect,omitempty"`
	Submitted         bool                           `json:"submitted"`
	DispatchCommitted bool                           `json:"dispatchCommitted"`
	ApprovalCommit    *DecisionSessionApprovalCommit `json:"approvalCommit,omitempty"`
	Status            DecisionSessionStatus          `json:"status"`
	Snapshot          DecisionSessionSnapshot        `json:"snapshot"`
}

type DecisionSessionAbandonEffect struct {
	Identity DecisionSessionIdentity `json:"identity"`
	EffectID string                  `json:"effectId"`
}

type DecisionSessionStatus string

const (
	DecisionSessionRunning   DecisionSessionStatus = "running"
	DecisionSessionCompleted DecisionSessionStatus = "completed"
	DecisionSessionCancelled DecisionSessionStatus = "cancelled"
	DecisionSessionStopped   DecisionSessionStatus = "stopped"
)

// DecisionSessionSnapshot deliberately exposes the reducer's semantic view,
// not its pending inputs, protected payloads, or frozen prompt bytes.
type DecisionSessionSnapshot struct {
	Identity DecisionSessionIdentity `json:"identity"`
	View     decisions.View          `json:"view"`
	Status   DecisionSessionStatus   `json:"status"`
}

// DecisionSessionAdvanceResult returns reducer effects as semantic intents.
// The workflow does not execute them; the existing host effect authority,
// ownership CAS, and authoritative receipts remain required for dispatch.
// Temporal update deduplication does not provide exactly-once effect execution:
// a retried Respond may re-emit a cached intent. EffectRequest.Identity.ID is
// stable for deduplication, and the downstream executor must use authoritative
// receipts to prevent duplicate effects.
type DecisionSessionAdvanceResult struct {
	Accepted bool                    `json:"accepted"`
	Stopped  bool                    `json:"stopped,omitempty"`
	Snapshot DecisionSessionSnapshot `json:"snapshot"`
	Effects  []decisions.Effect      `json:"effects,omitempty"`
}

type DecisionSessionResult struct {
	Snapshot       DecisionSessionSnapshot `json:"snapshot"`
	InitialEffects []decisions.Effect      `json:"initialEffects,omitempty"`
}

// DecisionSessionAdapter is an opt-in, dormant adapter extension. Its methods
// run in the host process and must return only normalized semantic data and
// authorized opaque references. Inline DTMF is rejected because the contract
// carries digits in clear text and defines no protected DTMF representation.
// This package does not supply an implementation that resolves owner CAS,
// protected-payload storage, prompt hydration, or provider execution.
type DecisionSessionAdapter interface {
	httpruntime.DecisionAdapter
	PrepareDecisionSession(httpruntime.StartCall) (DecisionSessionInput, error)
	PrepareDecisionResponse(httpruntime.RespondCall) (DecisionSessionUpdate, error)
	PrepareDecisionCancellation(httpruntime.CancelCall) (DecisionSessionCancel, error)
}

type decisionSessionWorkflowState struct {
	identity                   DecisionSessionIdentity
	reducer                    decisions.State
	status                     DecisionSessionStatus
	initialEffects             []decisions.Effect
	pendingEffect              *decisions.Effect
	effectSubmitted            bool
	effectDispatchCommitted    bool
	approvalCommit             *DecisionSessionApprovalCommit
	pendingOwnershipCAS        string
	initialEffectsAcknowledged bool
}

// DecisionSessionWorkflow applies only deterministic reducer transitions.
// It has no activities and makes no provider, directory, cache, media, storage,
// or external-write calls. Temporal history therefore contains semantic
// events and opaque references, never hydrated personal payloads.
// The SDK update validator rejects privacy-invalid updates before acceptance
// into workflow history. It is not authentication or transport redaction:
// application callers must use dispatcher preflight, and direct Temporal
// clients must be trusted because their requests are transmitted before the
// workflow validator runs. Reference IDs, digests, and semantic identifiers
// remain visible in history. Reference expiry is evaluated when an Advance
// arrives; this workflow does not implement an autonomous idle timeout.
func DecisionSessionWorkflow(ctx workflow.Context, input DecisionSessionInput) (DecisionSessionResult, error) {
	if err := ValidateDecisionSessionInput(input); err != nil {
		return DecisionSessionResult{}, err
	}
	state, initialEffects, err := decisions.Start(input.Definition, input.Admission, input.Policy)
	if err != nil {
		return DecisionSessionResult{}, fmt.Errorf("start decision reducer: %w", err)
	}
	initialSnapshot := DecisionSessionSnapshot{
		Identity: decisionSessionIdentity(input), View: state.View(),
		Status: decisionSessionStatusForView(state.View()),
	}
	session := &decisionSessionWorkflowState{
		identity:                   decisionSessionIdentity(input),
		reducer:                    state,
		status:                     initialSnapshot.Status,
		initialEffects:             cloneDecisionEffects(initialEffects),
		initialEffectsAcknowledged: !hasDecisionEffectWork(initialEffects),
	}
	session.trackEffects(initialEffects)
	if err := workflow.SetQueryHandler(ctx, DecisionSessionSnapshotQuery, func() (DecisionSessionSnapshot, error) {
		return session.snapshot(), nil
	}); err != nil {
		return DecisionSessionResult{}, fmt.Errorf("register decision session query: %w", err)
	}
	if err := workflow.SetQueryHandler(ctx, DecisionSessionPendingEffectQuery, func() (DecisionSessionPendingEffect, error) {
		if session.pendingEffect == nil {
			return DecisionSessionPendingEffect{Identity: session.identity, Status: session.status, Snapshot: session.snapshot()}, nil
		}
		effect := cloneDecisionEffect(*session.pendingEffect)
		return DecisionSessionPendingEffect{Identity: session.identity, Effect: &effect, Submitted: session.effectSubmitted,
			DispatchCommitted: session.effectDispatchCommitted, ApprovalCommit: cloneDecisionSessionApprovalCommit(session.approvalCommit),
			Status: session.status, Snapshot: session.snapshot()}, nil
	}); err != nil {
		return DecisionSessionResult{}, fmt.Errorf("register decision pending-effect query: %w", err)
	}
	if err := workflow.SetUpdateHandlerWithOptions(ctx, DecisionSessionInitialUpdate,
		func(_ workflow.Context, identity DecisionSessionIdentity) (DecisionSessionAdvanceResult, error) {
			if identity != session.identity {
				return DecisionSessionAdvanceResult{}, errors.New("decision initial transition identity does not match the pinned session")
			}
			session.initialEffectsAcknowledged = true
			return DecisionSessionAdvanceResult{Accepted: true, Snapshot: session.snapshot(), Effects: cloneDecisionEffects(session.initialEffects)}, nil
		}, workflow.UpdateHandlerOptions{Validator: func(identity DecisionSessionIdentity) error {
			if err := validateDecisionSessionIdentity(identity); err != nil {
				return err
			}
			if identity != session.identity {
				return errors.New("decision initial transition identity does not match the pinned session")
			}
			return nil
		}}); err != nil {
		return DecisionSessionResult{}, fmt.Errorf("register decision initial transition update: %w", err)
	}
	if err := workflow.SetUpdateHandlerWithOptions(ctx, DecisionSessionAdvanceUpdate,
		func(ctx workflow.Context, update DecisionSessionUpdate) (DecisionSessionAdvanceResult, error) {
			if session.status != DecisionSessionRunning {
				return DecisionSessionAdvanceResult{}, ErrDecisionSessionStopped
			}
			if update.Identity != session.identity {
				return DecisionSessionAdvanceResult{}, errors.New("decision callback identity does not match the pinned session")
			}
			view := session.reducer.View()
			if update.ExpectedRouteEntryID == "" || update.ExpectedRouteEntryID != view.RouteEntryID {
				return DecisionSessionAdvanceResult{}, decisions.ErrStaleEvent
			}
			if err := validateDecisionSessionReferences(update, session.identity, workflow.Now(ctx)); err != nil {
				if errors.Is(err, errDecisionReferenceExpired) {
					session.status = DecisionSessionStopped
					return DecisionSessionAdvanceResult{Stopped: true, Snapshot: session.snapshot()}, nil
				}
				return DecisionSessionAdvanceResult{}, err
			}
			next, effects, err := decisions.Reduce(session.reducer, update.Event)
			if err != nil {
				return DecisionSessionAdvanceResult{}, err
			}
			session.reducer = next
			nextView := next.View()
			session.status = decisionSessionStatusForView(nextView)
			session.trackEffects(effects)
			return DecisionSessionAdvanceResult{
				Accepted: true, Snapshot: session.snapshot(), Effects: effects,
			}, nil
		}, workflow.UpdateHandlerOptions{
			Validator: func(update DecisionSessionUpdate) error {
				return validateDecisionSessionUpdatePrivacy(update, &session.identity)
			},
		}); err != nil {
		return DecisionSessionResult{}, fmt.Errorf("register decision session update: %w", err)
	}
	if err := workflow.SetUpdateHandlerWithOptions(ctx, DecisionSessionSubmissionUpdate,
		func(ctx workflow.Context, submission DecisionSessionEffectSubmission) (DecisionSessionAdvanceResult, error) {
			if submission.Identity != session.identity {
				return DecisionSessionAdvanceResult{}, errors.New("decision effect submission identity does not match the pinned session")
			}
			if session.status != DecisionSessionRunning {
				return DecisionSessionAdvanceResult{}, ErrDecisionSessionStopped
			}
			if submission.Effect.Kind != decisions.EffectDispatchIntent || session.pendingEffect == nil ||
				submission.Effect.RouteID != session.pendingEffect.RouteID || submission.Effect.RouteEntryID != session.pendingEffect.RouteEntryID ||
				submission.Effect.InputID != session.pendingEffect.InputID || submission.Effect.ActionID != session.pendingEffect.ActionID ||
				!sameDecisionEffectRequest(submission.Effect.Request, session.pendingEffect.Request) {
				return DecisionSessionAdvanceResult{}, errors.New("effect submission does not match the active reducer intent")
			}
			if session.effectSubmitted {
				return DecisionSessionAdvanceResult{Accepted: true, Snapshot: session.snapshot(), Effects: []decisions.Effect{{
					Kind: decisions.EffectAwaitReceipt, RouteID: submission.Effect.RouteID, RouteEntryID: submission.Effect.RouteEntryID,
					InputID: submission.Effect.InputID, ActionID: submission.Effect.ActionID, Request: cloneDecisionEffectRequest(submission.Effect.Request),
				}}}, nil
			}
			event := decisionv1.NormalizedEvent{
				ID: "submit_" + submission.Effect.Request.Identity.ID, Kind: decisionv1.EventEffectSubmitted,
				TenantID: session.identity.TenantID, SessionID: session.identity.SessionID, Generation: session.identity.Generation,
				RouteID: submission.Effect.RouteID, RouteEntryID: submission.Effect.RouteEntryID,
				Channel: decisionv1.ChannelVoice, ReceivedAt: workflow.Now(ctx), EffectRequest: cloneDecisionEffectRequest(submission.Effect.Request),
			}
			next, effects, err := decisions.Reduce(session.reducer, decisions.Event{Normalized: &event})
			if err != nil {
				return DecisionSessionAdvanceResult{}, err
			}
			session.reducer = next
			session.effectSubmitted = true
			session.trackEffects(effects)
			return DecisionSessionAdvanceResult{Accepted: true, Snapshot: session.snapshot(), Effects: effects}, nil
		}, workflow.UpdateHandlerOptions{Validator: func(submission DecisionSessionEffectSubmission) error {
			if err := validateDecisionSessionIdentity(submission.Identity); err != nil {
				return err
			}
			if submission.Identity != session.identity || submission.Effect.Kind != decisions.EffectDispatchIntent || submission.Effect.Request == nil {
				return errors.New("invalid decision effect submission")
			}
			return nil
		}}); err != nil {
		return DecisionSessionResult{}, fmt.Errorf("register decision effect submission update: %w", err)
	}
	if err := workflow.SetUpdateHandlerWithOptions(ctx, DecisionSessionDispatchCommitUpdate,
		func(_ workflow.Context, commit DecisionSessionEffectDispatchCommit) (DecisionSessionSnapshot, error) {
			if commit.Identity != session.identity || session.pendingEffect == nil || session.pendingEffect.Request == nil ||
				commit.EffectID != session.pendingEffect.Request.Identity.ID || !session.effectSubmitted {
				return DecisionSessionSnapshot{}, errors.New("decision dispatch commit does not match a submitted effect")
			}
			if session.status != DecisionSessionRunning {
				return DecisionSessionSnapshot{}, ErrDecisionSessionStopped
			}
			session.effectDispatchCommitted = true
			return session.snapshot(), nil
		}, workflow.UpdateHandlerOptions{Validator: func(commit DecisionSessionEffectDispatchCommit) error {
			if err := validateDecisionSessionIdentity(commit.Identity); err != nil {
				return err
			}
			if commit.Identity != session.identity || strings.TrimSpace(commit.EffectID) == "" {
				return errors.New("invalid decision effect dispatch commit")
			}
			return nil
		}}); err != nil {
		return DecisionSessionResult{}, fmt.Errorf("register decision effect dispatch commit update: %w", err)
	}
	if err := workflow.SetUpdateHandlerWithOptions(ctx, DecisionSessionApprovalCommitUpdate,
		func(_ workflow.Context, commit DecisionSessionApprovalCommit) (DecisionSessionSnapshot, error) {
			if commit.Identity != session.identity || session.pendingEffect == nil || session.pendingEffect.Request == nil ||
				commit.EffectID != session.pendingEffect.Request.Identity.ID || commit.Effect != session.pendingEffect.Request.Identity ||
				!session.effectSubmitted || !session.effectDispatchCommitted {
				return DecisionSessionSnapshot{}, errors.New("decision approval does not match the submitted and committed effect")
			}
			if session.approvalCommit != nil {
				if *session.approvalCommit != commit {
					return DecisionSessionSnapshot{}, errors.New("conflicting decision approval replay")
				}
				return session.snapshot(), nil
			}
			if session.status != DecisionSessionRunning {
				return DecisionSessionSnapshot{}, ErrDecisionSessionStopped
			}
			copy := commit
			session.approvalCommit = &copy
			return session.snapshot(), nil
		}, workflow.UpdateHandlerOptions{Validator: func(commit DecisionSessionApprovalCommit) error {
			if err := validateDecisionSessionIdentity(commit.Identity); err != nil {
				return err
			}
			if err := commit.Effect.Validate(); err != nil {
				return err
			}
			if commit.Identity != session.identity || commit.EffectID != commit.Effect.ID ||
				strings.TrimSpace(commit.ApprovalID) == "" || commit.ToolCallID != commit.Effect.ID ||
				strings.TrimSpace(commit.InputHash) == "" || strings.TrimSpace(commit.ActorID) == "" || strings.TrimSpace(commit.ActorKind) == "" {
				return errors.New("invalid decision effect approval commit")
			}
			return nil
		}}); err != nil {
		return DecisionSessionResult{}, fmt.Errorf("register decision effect approval commit update: %w", err)
	}
	if err := workflow.SetUpdateHandlerWithOptions(ctx, DecisionSessionReceiptUpdate,
		func(ctx workflow.Context, update DecisionSessionUpdate) (DecisionSessionAdvanceResult, error) {
			if update.Identity != session.identity {
				return DecisionSessionAdvanceResult{}, errors.New("decision receipt identity does not match the pinned session")
			}
			if update.Event.Normalized == nil || update.Event.Normalized.Kind != decisionv1.EventEffectReceipt || update.Event.Normalized.Effect == nil ||
				session.pendingEffect == nil || session.pendingEffect.Request == nil ||
				!session.effectSubmitted || !session.effectDispatchCommitted ||
				update.Event.Normalized.RouteID != session.pendingEffect.RouteID || update.Event.Normalized.RouteEntryID != session.pendingEffect.RouteEntryID ||
				update.ExpectedRouteEntryID != session.pendingEffect.RouteEntryID ||
				update.Event.Normalized.Effect.Identity != session.pendingEffect.Request.Identity ||
				update.Event.Normalized.Effect.TargetOpaqueID != session.pendingEffect.Request.TargetOpaqueID {
				return DecisionSessionAdvanceResult{}, errors.New("decision receipt does not match an outstanding effect")
			}
			if err := validateDecisionApprovalFailureCommit(session.identity, session.approvalCommit, *update.Event.Normalized.Effect); err != nil {
				return DecisionSessionAdvanceResult{}, err
			}
			if session.status != DecisionSessionRunning && session.status != DecisionSessionCancelled {
				return DecisionSessionAdvanceResult{}, ErrDecisionSessionStopped
			}
			if session.status == DecisionSessionRunning && update.ExpectedRouteEntryID != session.reducer.View().RouteEntryID {
				return DecisionSessionAdvanceResult{}, decisions.ErrStaleEvent
			}
			if err := validateDecisionSessionReferences(update, session.identity, workflow.Now(ctx)); err != nil {
				return DecisionSessionAdvanceResult{}, err
			}
			next, effects, err := decisions.Reduce(session.reducer, update.Event)
			if err != nil {
				return DecisionSessionAdvanceResult{}, err
			}
			session.reducer = next
			if session.status == DecisionSessionCancelled {
				effects = cancelledReceiptEffects(effects)
			} else {
				session.status = decisionSessionStatusForView(next.View())
			}
			session.trackReceiptEffects(effects)
			return DecisionSessionAdvanceResult{Accepted: true, Snapshot: session.snapshot(), Effects: effects}, nil
		}, workflow.UpdateHandlerOptions{Validator: func(update DecisionSessionUpdate) error {
			return validateDecisionSessionUpdatePrivacy(update, &session.identity)
		}}); err != nil {
		return DecisionSessionResult{}, fmt.Errorf("register decision effect receipt update: %w", err)
	}
	if err := workflow.SetUpdateHandlerWithOptions(ctx, DecisionSessionOwnershipCASUpdate,
		func(_ workflow.Context, ack DecisionSessionOwnershipCAS) (DecisionSessionSnapshot, error) {
			if ack.Identity != session.identity || session.pendingOwnershipCAS == "" || ack.ReceiptID != session.pendingOwnershipCAS || session.pendingEffect == nil || session.pendingEffect.Request == nil || ack.EffectID != session.pendingEffect.Request.Identity.ID {
				return DecisionSessionSnapshot{}, errors.New("ownership CAS acknowledgement does not match a confirmed pending receipt")
			}
			session.pendingOwnershipCAS = ""
			session.pendingEffect = nil
			session.effectSubmitted = false
			session.effectDispatchCommitted = false
			session.approvalCommit = nil
			return session.snapshot(), nil
		}, workflow.UpdateHandlerOptions{Validator: func(ack DecisionSessionOwnershipCAS) error {
			if err := validateDecisionSessionIdentity(ack.Identity); err != nil {
				return err
			}
			if ack.Identity != session.identity || strings.TrimSpace(ack.EffectID) == "" || strings.TrimSpace(ack.ReceiptID) == "" {
				return errors.New("invalid ownership CAS acknowledgement")
			}
			return nil
		}}); err != nil {
		return DecisionSessionResult{}, fmt.Errorf("register decision ownership CAS acknowledgement update: %w", err)
	}
	if err := workflow.SetUpdateHandlerWithOptions(ctx, DecisionSessionAbandonEffectUpdate,
		func(_ workflow.Context, abandon DecisionSessionAbandonEffect) (DecisionSessionSnapshot, error) {
			if abandon.Identity != session.identity || session.status != DecisionSessionCancelled || !session.effectSubmitted ||
				session.effectDispatchCommitted || session.pendingEffect == nil ||
				session.pendingEffect.Request == nil || abandon.EffectID != session.pendingEffect.Request.Identity.ID {
				return DecisionSessionSnapshot{}, errors.New("decision effect abandonment does not match a cancelled, submitted intent")
			}
			session.pendingEffect = nil
			session.effectSubmitted = false
			session.effectDispatchCommitted = false
			session.approvalCommit = nil
			return session.snapshot(), nil
		}, workflow.UpdateHandlerOptions{Validator: func(abandon DecisionSessionAbandonEffect) error {
			if err := validateDecisionSessionIdentity(abandon.Identity); err != nil {
				return err
			}
			if abandon.Identity != session.identity || strings.TrimSpace(abandon.EffectID) == "" {
				return errors.New("invalid decision effect abandonment")
			}
			return nil
		}}); err != nil {
		return DecisionSessionResult{}, fmt.Errorf("register decision effect abandonment update: %w", err)
	}
	if err := workflow.SetUpdateHandlerWithOptions(ctx, DecisionSessionCancelUpdate,
		func(_ workflow.Context, cancel DecisionSessionCancel) (DecisionSessionSnapshot, error) {
			if cancel.Identity != session.identity {
				return DecisionSessionSnapshot{}, errors.New("decision cancellation identity does not match the pinned session")
			}
			if session.status != DecisionSessionRunning {
				return DecisionSessionSnapshot{}, ErrDecisionSessionStopped
			}
			session.status = DecisionSessionCancelled
			if session.pendingEffect != nil && !session.effectSubmitted {
				session.pendingEffect = nil
				session.effectDispatchCommitted = false
				session.approvalCommit = nil
			}
			return session.snapshot(), nil
		}, workflow.UpdateHandlerOptions{
			Validator: func(cancel DecisionSessionCancel) error {
				if err := validateDecisionSessionIdentity(cancel.Identity); err != nil {
					return err
				}
				if cancel.Identity != session.identity {
					return errors.New("decision cancellation identity does not match the pinned session")
				}
				return nil
			},
		}); err != nil {
		return DecisionSessionResult{}, fmt.Errorf("register decision session cancellation: %w", err)
	}
	if err := workflow.Await(ctx, func() bool {
		return session.initialEffectsAcknowledged && session.status != DecisionSessionRunning && session.pendingEffect == nil && session.pendingOwnershipCAS == ""
	}); err != nil {
		return DecisionSessionResult{}, err
	}
	return DecisionSessionResult{Snapshot: session.snapshot(), InitialEffects: initialEffects}, nil
}

func decisionSessionStatusForView(view decisions.View) DecisionSessionStatus {
	if view.Phase == decisions.PhaseStopped {
		return DecisionSessionStopped
	}
	if view.GraphEnded || view.SessionEnded || view.Phase == decisions.PhaseTerminal {
		return DecisionSessionCompleted
	}
	return DecisionSessionRunning
}

func (session *decisionSessionWorkflowState) snapshot() DecisionSessionSnapshot {
	return DecisionSessionSnapshot{Identity: session.identity, View: session.reducer.View(), Status: session.status}
}

func (session *decisionSessionWorkflowState) trackEffects(effects []decisions.Effect) {
	for _, effect := range effects {
		switch effect.Kind {
		case decisions.EffectDispatchIntent:
			if effect.Request != nil {
				copy := cloneDecisionEffect(effect)
				session.pendingEffect = &copy
				session.effectSubmitted = false
				session.effectDispatchCommitted = false
				session.approvalCommit = nil
			}
		case decisions.EffectAwaitReceipt:
			if effect.Request != nil {
				committed := session.effectDispatchCommitted && session.pendingEffect != nil &&
					session.pendingEffect.Request != nil && session.pendingEffect.Request.Identity == effect.Request.Identity
				copy := cloneDecisionEffect(effect)
				session.pendingEffect = &copy
				session.effectSubmitted = true
				session.effectDispatchCommitted = committed
			}
		case decisions.EffectReleaseOwnership:
			if effect.Request != nil {
				copy := cloneDecisionEffect(effect)
				session.pendingEffect = &copy
				session.effectSubmitted = true
				session.effectDispatchCommitted = true
				session.approvalCommit = nil
				session.pendingOwnershipCAS = effect.ReceiptID
			}
		}
	}
}

func (session *decisionSessionWorkflowState) trackReceiptEffects(effects []decisions.Effect) {
	session.pendingEffect = nil
	session.effectSubmitted = false
	session.effectDispatchCommitted = false
	session.approvalCommit = nil
	session.pendingOwnershipCAS = ""
	for _, effect := range effects {
		if effect.Kind == decisions.EffectAwaitReceipt && effect.Request != nil {
			copy := cloneDecisionEffect(effect)
			session.pendingEffect = &copy
			session.effectSubmitted = true
			session.effectDispatchCommitted = true
			return
		}
		if effect.Kind == decisions.EffectReleaseOwnership && effect.Request != nil {
			copy := cloneDecisionEffect(effect)
			session.pendingEffect = &copy
			session.effectSubmitted = true
			session.effectDispatchCommitted = true
			session.pendingOwnershipCAS = effect.ReceiptID
			return
		}
	}
	if session.status == DecisionSessionRunning {
		session.trackEffects(effects)
	}
}

func cancelledReceiptEffects(effects []decisions.Effect) []decisions.Effect {
	filtered := make([]decisions.Effect, 0, len(effects))
	for _, effect := range effects {
		if effect.Kind == decisions.EffectAwaitReceipt || effect.Kind == decisions.EffectReleaseOwnership {
			filtered = append(filtered, cloneDecisionEffect(effect))
		}
	}
	return filtered
}

func cloneDecisionEffects(effects []decisions.Effect) []decisions.Effect {
	cloned := make([]decisions.Effect, len(effects))
	for index, effect := range effects {
		cloned[index] = cloneDecisionEffect(effect)
	}
	return cloned
}

func cloneDecisionSessionApprovalCommit(commit *DecisionSessionApprovalCommit) *DecisionSessionApprovalCommit {
	if commit == nil {
		return nil
	}
	copy := *commit
	return &copy
}

func validateDecisionApprovalFailureCommit(identity DecisionSessionIdentity, commit *DecisionSessionApprovalCommit, receipt decisionv1.EffectReceipt) error {
	if receipt.FailureCode != "approval_denied" && receipt.FailureCode != "approval_expired" {
		return nil
	}
	if commit == nil || (receipt.FailureCode == "approval_denied" && commit.Approved) ||
		commit.Identity != identity || commit.EffectID != receipt.Identity.ID || commit.Effect != receipt.Identity {
		return errors.New("approval failure receipt lacks its exact committed decision")
	}
	return nil
}

func cloneDecisionEffect(effect decisions.Effect) decisions.Effect {
	effect.Request = cloneDecisionEffectRequest(effect.Request)
	effect.MessageFamilies = append([]string(nil), effect.MessageFamilies...)
	effect.Accept = append([]decisionv1.InputModality(nil), effect.Accept...)
	return effect
}

func cloneDecisionEffectRequest(request *decisionv1.EffectRequest) *decisionv1.EffectRequest {
	if request == nil {
		return nil
	}
	copy := *request
	if request.Payload != nil {
		payload := *request.Payload
		copy.Payload = &payload
	}
	return &copy
}

func sameDecisionEffectRequest(left, right *decisionv1.EffectRequest) bool {
	return left != nil && right != nil && reflect.DeepEqual(left, right)
}

// ValidateDecisionSessionInput verifies the frozen manifest/pin and binds the
// admission event to that pin before the data can enter Temporal history.
func ValidateDecisionSessionInput(input DecisionSessionInput) error {
	if _, err := WorkflowID(input.Admission.SessionID, input.RunID); err != nil {
		return fmt.Errorf("decision session run identity: %w", err)
	}
	if err := input.Definition.ValidateForReview(); err != nil {
		return fmt.Errorf("frozen decision definition: %w", err)
	}
	if err := input.Pin.ValidateFor(input.Definition); err != nil {
		return fmt.Errorf("decision session pin: %w", err)
	}
	if input.Admission.Kind != decisionv1.EventSessionAdmitted {
		return errors.New("decision session admission event is required")
	}
	if input.Admission.Generation != input.Pin.Generation || input.Admission.Locale != input.Pin.Locale {
		return errors.New("decision session admission does not match the pinned generation and locale")
	}
	if input.Admission.ProtectedInput != nil || input.Admission.Digits != "" {
		return errors.New("decision session admission cannot carry inline or protected user input")
	}
	if err := input.Admission.Validate(time.Time{}); err != nil {
		return fmt.Errorf("decision session admission: %w", err)
	}
	return nil
}

func decisionSessionIdentity(input DecisionSessionInput) DecisionSessionIdentity {
	pin := input.Pin
	return DecisionSessionIdentity{
		TenantID: input.Admission.TenantID, SessionID: input.Admission.SessionID, RunID: input.RunID,
		SchemaVersion: pin.SchemaVersion, ReleaseSHA256: pin.ReleaseSHA256,
		GraphSHA256: pin.GraphSHA256, PromptFamiliesSHA256: pin.PromptFamiliesSHA256,
		SnapshotSHA256: pin.SnapshotSHA256, Generation: pin.Generation,
		Locale: pin.Locale, PromptFamily: pin.PromptFamily,
		PromptVariantLocale: pin.PromptVariantLocale,
	}
}

var errDecisionReferenceExpired = errors.New("decision protected reference expired")

func validateDecisionSessionReferences(update DecisionSessionUpdate, identity DecisionSessionIdentity, now time.Time) error {
	if err := validateDecisionSessionUpdatePrivacy(update, &identity); err != nil {
		return err
	}
	refs := decisionSessionReferences(update)
	for _, ref := range refs {
		if !ref.ExpiresAt.IsZero() && !ref.ExpiresAt.After(now) {
			return errDecisionReferenceExpired
		}
		if err := ref.ValidateFor(updateTenantID(update.Event), updateSessionID(update.Event), identity.Generation, now); err != nil {
			return fmt.Errorf("decision protected reference: %w", err)
		}
		if update.Event.Snapshot != nil && update.Event.Snapshot.Bound.Snapshot.ProtectedSnapshot != nil && ref.ID == update.Event.Snapshot.Bound.Snapshot.ProtectedSnapshot.ID && ref.SHA256 != identity.SnapshotSHA256 {
			return errors.New("candidate reference digest does not match the pinned snapshot")
		}
	}
	if decisionEventNeedsPinnedSnapshot(update.Event) {
		pinned := false
		for _, ref := range refs {
			if ref.Purpose == "directory-snapshot" && ref.SHA256 == identity.SnapshotSHA256 {
				pinned = true
				break
			}
		}
		if !pinned {
			return errors.New("decision callback is missing the pinned authorized snapshot reference")
		}
	}
	return nil
}

func validateDecisionSessionIdentity(identity DecisionSessionIdentity) error {
	if err := decisionv1.ValidateIdentifier(identity.TenantID); err != nil {
		return fmt.Errorf("decision session tenant identity: %w", err)
	}
	if _, err := WorkflowID(identity.SessionID, identity.RunID); err != nil {
		return errors.New("decision session identity requires valid session and run IDs")
	}
	if identity.SchemaVersion != decisionv1.SchemaVersion || identity.Generation == 0 {
		return errors.New("decision session identity requires a supported schema and positive generation")
	}
	for _, digest := range []string{identity.ReleaseSHA256, identity.GraphSHA256, identity.PromptFamiliesSHA256, identity.SnapshotSHA256} {
		if err := decisionv1.ValidateSHA256(digest); err != nil {
			return errors.New("decision session identity requires valid pinned digests")
		}
	}
	if err := decisionv1.ValidateLocale(identity.Locale); err != nil {
		return fmt.Errorf("decision session locale identity: %w", err)
	}
	if err := decisionv1.ValidateLocale(identity.PromptVariantLocale); err != nil {
		return fmt.Errorf("decision session prompt locale identity: %w", err)
	}
	if err := decisionv1.ValidateIdentifier(identity.PromptFamily); err != nil {
		return fmt.Errorf("decision session prompt family identity: %w", err)
	}
	return nil
}

func validateDecisionSessionUpdatePrivacy(update DecisionSessionUpdate, expected *DecisionSessionIdentity) error {
	if err := validateDecisionSessionIdentity(update.Identity); err != nil {
		return err
	}
	if expected != nil && update.Identity != *expected {
		return errors.New("decision callback identity does not match the pinned session")
	}
	if err := decisionv1.ValidateIdentifier(update.ExpectedRouteEntryID); err != nil {
		return fmt.Errorf("decision callback route-entry identity: %w", err)
	}
	if err := validateDecisionSessionEventPrivacy(update.Event); err != nil {
		return err
	}
	if updateTenantID(update.Event) != update.Identity.TenantID || updateSessionID(update.Event) != update.Identity.SessionID || decisionSessionEventGeneration(update.Event) != update.Identity.Generation {
		return errors.New("decision callback event scope does not match its session identity")
	}
	refs := decisionSessionReferences(update)
	for _, ref := range refs {
		if err := ref.ValidateFor(update.Identity.TenantID, update.Identity.SessionID, update.Identity.Generation, time.Time{}); err != nil {
			return fmt.Errorf("decision protected reference: %w", err)
		}
	}
	if decisionEventNeedsPinnedSnapshot(update.Event) {
		pinned := false
		for _, ref := range refs {
			if ref.Purpose == "directory-snapshot" && ref.SHA256 == update.Identity.SnapshotSHA256 {
				pinned = true
				break
			}
		}
		if !pinned {
			return errors.New("decision callback is missing the pinned authorized snapshot reference")
		}
	}
	return nil
}

func decisionSessionReferences(update DecisionSessionUpdate) []decisionv1.ProtectedReference {
	refs := make([]decisionv1.ProtectedReference, 0, len(update.AuthorizedReferences)+1)
	refs = append(refs, update.AuthorizedReferences...)
	if update.Event.Normalized != nil && update.Event.Normalized.ProtectedInput != nil {
		refs = append(refs, *update.Event.Normalized.ProtectedInput)
	}
	if update.Event.Normalized != nil && update.Event.Normalized.EffectRequest != nil && update.Event.Normalized.EffectRequest.Payload != nil {
		refs = append(refs, *update.Event.Normalized.EffectRequest.Payload)
	}
	if update.Event.Snapshot != nil && update.Event.Snapshot.Bound.Snapshot.ProtectedSnapshot != nil {
		refs = append(refs, *update.Event.Snapshot.Bound.Snapshot.ProtectedSnapshot)
	}
	return refs
}

func validateDecisionSessionEventPrivacy(event decisions.Event) error {
	if event.Normalized != nil && event.Normalized.Digits != "" {
		return errors.New("inline DTMF input is unsupported in durable decision sessions")
	}
	if event.Normalized != nil && event.Normalized.Decision != nil {
		for _, raw := range event.Normalized.Decision.ProviderScoreFields {
			decoder := json.NewDecoder(strings.NewReader(string(raw)))
			decoder.UseNumber()
			var value any
			if err := decoder.Decode(&value); err != nil {
				return errors.New("provider score field must contain exactly one JSON value")
			}
			if _, ok := value.(json.Number); !ok {
				return errors.New("provider score field must be numeric semantic data")
			}
			var trailing any
			if err := decoder.Decode(&trailing); err != io.EOF {
				return errors.New("provider score field must contain exactly one JSON value")
			}
		}
	}
	return decisions.ValidateEventStructure(event, time.Time{})
}

func decisionEventNeedsPinnedSnapshot(event decisions.Event) bool {
	if event.Control != nil {
		return true
	}
	if event.Normalized == nil {
		return false
	}
	switch event.Normalized.Kind {
	case decisionv1.EventMatchCompleted, decisionv1.EventDecisionCompleted, decisionv1.EventEffectSubmitted:
		return true
	default:
		return false
	}
}

func updateTenantID(event decisions.Event) string {
	switch {
	case event.Normalized != nil:
		return event.Normalized.TenantID
	case event.Snapshot != nil:
		return event.Snapshot.TenantID
	case event.Control != nil:
		return event.Control.TenantID
	case event.Fallback != nil:
		return event.Fallback.TenantID
	default:
		return ""
	}
}

func updateSessionID(event decisions.Event) string {
	switch {
	case event.Normalized != nil:
		return event.Normalized.SessionID
	case event.Snapshot != nil:
		return event.Snapshot.SessionID
	case event.Control != nil:
		return event.Control.SessionID
	case event.Fallback != nil:
		return event.Fallback.SessionID
	default:
		return ""
	}
}

func decisionSessionEventID(event decisions.Event) string {
	switch {
	case event.Normalized != nil:
		return event.Normalized.ID
	case event.Snapshot != nil:
		return event.Snapshot.ID
	case event.Control != nil:
		return event.Control.ID
	case event.Fallback != nil:
		return event.Fallback.ID
	default:
		return ""
	}
}

func decisionSessionEventGeneration(event decisions.Event) uint64 {
	switch {
	case event.Normalized != nil:
		return event.Normalized.Generation
	case event.Snapshot != nil:
		return event.Snapshot.Generation
	case event.Control != nil:
		return event.Control.Generation
	case event.Fallback != nil:
		return event.Fallback.Generation
	default:
		return 0
	}
}

func decisionSessionWorkflowID(call httpruntime.StartCall) (string, error) {
	return WorkflowID(call.Session.ID, call.Run.ID)
}

// validateDecisionSessionRun checks dispatcher routing inputs. The HTTP
// runtime authenticates the actor and resolves the owned session/run before
// calling the dispatcher; direct dispatcher callers must provide that same
// host authorization boundary.
func validateDecisionSessionRun(session agents.Session, run agents.Run) error {
	if session.ID == "" || run.ID == "" || run.SessionID != session.ID {
		return errors.New("decision session run does not belong to the requested session")
	}
	if session.AgentID == "" || run.AgentID == "" || session.AgentID != run.AgentID {
		return errors.New("decision session run does not belong to the requested agent")
	}
	if run.Mode != agents.DurableMode {
		return errors.New("decision session requires a durable run")
	}
	if _, err := WorkflowID(session.ID, run.ID); err != nil {
		return err
	}
	return nil
}

func decisionSessionUpdate(ctx context.Context, temporalClient Client, workflowID, updateName, updateID string, arg any, output any) error {
	if temporalClient == nil {
		return ErrClosed
	}
	updater, ok := temporalClient.(interface {
		UpdateWorkflow(context.Context, client.UpdateWorkflowOptions) (client.WorkflowUpdateHandle, error)
	})
	if !ok {
		return errors.New("Temporal client does not support workflow updates required by decision sessions")
	}
	handle, err := updater.UpdateWorkflow(ctx, client.UpdateWorkflowOptions{
		WorkflowID: workflowID, UpdateName: updateName, UpdateID: updateID,
		Args: []interface{}{arg}, WaitForStage: client.WorkflowUpdateStageCompleted,
	})
	if err != nil {
		return err
	}
	if handle == nil {
		return errors.New("Temporal returned a nil decision session update handle")
	}
	return handle.Get(ctx, output)
}

// RegisterDecisionSessionWorkflow adds only the deterministic workflow to a
// worker registry. It registers no activities or effect executors.
func RegisterDecisionSessionWorkflow(registry worker.Registry) {
	if registry == nil {
		return
	}
	registry.RegisterWorkflowWithOptions(DecisionSessionWorkflow, workflow.RegisterOptions{Name: DecisionSessionWorkflowName})
}

func prepareDecisionSession(adapter httpruntime.Adapter, call httpruntime.StartCall) (DecisionSessionAdapter, DecisionSessionInput, error) {
	durable, ok := adapter.(DecisionSessionAdapter)
	if !ok {
		return nil, DecisionSessionInput{}, ErrDecisionSessionAdapterRequired
	}
	if durable.Config().Mode() != agents.DurableMode {
		return nil, DecisionSessionInput{}, errors.New("decision session requires a durable agent")
	}
	input, err := durable.PrepareDecisionSession(call)
	if err != nil {
		return nil, DecisionSessionInput{}, fmt.Errorf("prepare decision session: %w", err)
	}
	if call.Run.ID == "" || call.Run.SessionID != call.Session.ID || call.Run.AgentID == "" || call.Session.AgentID != call.Run.AgentID {
		return nil, DecisionSessionInput{}, errors.New("decision session call does not bind the run to its session and agent")
	}
	input.RunID = call.Run.ID
	if err := ValidateDecisionSessionInput(input); err != nil {
		return nil, DecisionSessionInput{}, err
	}
	if input.Admission.SessionID != call.Session.ID || input.Admission.Generation != input.Pin.Generation {
		return nil, DecisionSessionInput{}, errors.New("decision session admission does not match the host session")
	}
	adapterDefinition := durable.DecisionDefinition()
	_, _, adapterRelease, err := agents.FreezeDecisionManifest(adapterDefinition)
	if err != nil {
		return nil, DecisionSessionInput{}, fmt.Errorf("freeze decision adapter definition: %w", err)
	}
	_, _, inputRelease, err := agents.FreezeDecisionManifest(input.Definition)
	if err != nil {
		return nil, DecisionSessionInput{}, fmt.Errorf("freeze decision session definition: %w", err)
	}
	if adapterRelease != inputRelease {
		return nil, DecisionSessionInput{}, errors.New("decision session does not match the adapter's frozen definition")
	}
	return durable, input, nil
}

func decisionUpdateID(event decisions.Event) (string, error) {
	id := strings.TrimSpace(decisionSessionEventID(event))
	if id == "" {
		return "", errors.New("decision session event ID is required")
	}
	return "advance-" + id, nil
}
