// Package decisions contains a pure reducer for frozen decision graphs.
// It emits semantic effects only; adapters own provider IO, effect execution,
// operation receipts, and call-owner compare-and-swap.
package decisions

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	contract "github.com/Origens-Dev/gobeyond/agents/decisioncontract/v1"
)

type Phase string

const (
	PhaseListening       Phase = "listening"
	PhaseMatching        Phase = "matching"
	PhaseDeciding        Phase = "deciding"
	PhaseActing          Phase = "acting"
	PhaseAwaitingReceipt Phase = "awaiting_receipt"
	PhaseFallback        Phase = "full_agent_fallback"
	PhaseTerminal        Phase = "terminal"
	PhaseStopped         Phase = "stopped"
)

type EffectKind string

const (
	EffectEnterRoute        EffectKind = "enter_route"
	EffectSay               EffectKind = "say"
	EffectListen            EffectKind = "listen"
	EffectRefreshCandidates EffectKind = "refresh_candidates"
	EffectRunMatcher        EffectKind = "run_matcher"
	EffectRunDecision       EffectKind = "run_decision"
	EffectDispatchIntent    EffectKind = "dispatch_intent"
	EffectFullAgentFallback EffectKind = "full_agent_fallback"
	EffectAwaitReceipt      EffectKind = "await_receipt"
	EffectEndGraph          EffectKind = "end_graph"
	EffectEndSession        EffectKind = "end_session"
	EffectReleaseOwnership  EffectKind = "release_call_ownership"
	EffectSafeStop          EffectKind = "safe_stop"
)

// Effect is a description of work for a future adapter. It is not an
// authorization token and must never be executed without current adapter
// authorization.
type Effect struct {
	Kind                EffectKind
	RouteID             contract.RouteID
	RouteEntryID        string
	PlaybackOperationID string
	InputWindowID       string
	BindingID           string
	InputID             string
	ServiceID           string
	ActionID            string
	MessageFamilies     []string
	Accept              []contract.InputModality
	Request             *contract.EffectRequest
	Target              contract.Target
	ReceiptID           string
	Reason              string
}

// BoundCandidateSnapshot adds the identity that the v1 wire snapshot itself
// does not carry. The reducer accepts it only for the frozen route's active
// matcher binding and the current route-entry identity.
type BoundCandidateSnapshot struct {
	BindingID    string
	RouteID      contract.RouteID
	RouteEntryID string
	Snapshot     contract.CandidateSetSnapshot
}

// SnapshotRefresh is a server-normalized binding result. BindingID, RouteID,
// and RouteEntryID are checked against the reducer's active frozen route.
type SnapshotRefresh struct {
	ID           string
	TenantID     string
	SessionID    string
	Generation   uint64
	RouteID      contract.RouteID
	RouteEntryID string
	ReceivedAt   time.Time
	Bound        BoundCandidateSnapshot
}

type ControlCommand string

const (
	ControlRepeat ControlCommand = "repeat"
	ControlHelp   ControlCommand = "help"
	ControlCancel ControlCommand = "cancel"
)

// ControlResult is a typed deterministic result for repeat/help/cancel. It is
// correlated to one accepted final input and its active matcher snapshot.
type ControlResult struct {
	ID             string
	TenantID       string
	SessionID      string
	Generation     uint64
	RouteID        contract.RouteID
	RouteEntryID   string
	InputID        string
	BindingID      string
	SnapshotSHA256 string
	Command        ControlCommand
	ReceivedAt     time.Time
}

// FullAgentResult is a bounded escalation result. It cannot nominate an
// opaque call target; only the active deterministic binding or Jev result can
// produce a dispatch intent.
type FullAgentResult struct {
	ID           string
	TenantID     string
	SessionID    string
	Generation   uint64
	RouteID      contract.RouteID
	RouteEntryID string
	InputID      string
	Outcome      contract.Outcome
	ReceivedAt   time.Time
}

// Event is a tagged union. Exactly one of Normalized, Snapshot, Control, or
// Fallback is present. InputID correlates matcher/decision completions to the
// accepted final input that caused them.
type Event struct {
	Normalized *contract.NormalizedEvent
	InputID    string
	Snapshot   *SnapshotRefresh
	Control    *ControlResult
	Fallback   *FullAgentResult
}

// Policy contains only unresolved integration inputs that are not yet part of
// decision.graph/v1. A nil full-agent limit disables that escalation. No
// threshold is inferred by this package.
type Policy struct {
	FullAgentFallbackLimit *uint64
}

type RetryCount struct {
	GroupID string
	Counter contract.RetryCounter
}

// RetryTotals exposes reducer-owned session-wide counters. Group counters
// remain separately available in View.RetryCounters.
type RetryTotals struct {
	Reprompts          uint64
	NoInputReprompts   uint64
	NoMatchReprompts   uint64
	AmbiguousReprompts uint64
}

type routeBudgetUsage struct {
	Visits         uint64
	DecisionCalls  uint64
	EffectAttempts uint64
	Retries        RetryTotals
}

// View is a read-only copy of the reducer's semantic state. It deliberately
// omits the frozen definition, action values, protected payloads, and candidate
// descriptions.
type View struct {
	TenantID           string
	SessionID          string
	Generation         uint64
	RouteID            contract.RouteID
	RouteEntryID       string
	Phase              Phase
	InputWindowID      string
	InputWindowOpen    bool
	LogicalTaskID      string
	RouteVisits        uint64
	DecisionCalls      uint64
	EffectAttempts     uint64
	FullAgentFallbacks uint64
	RetryCounters      []RetryCount
	SessionRetryTotals RetryTotals
	Ownership          contract.GraphOwnership
	GraphEnded         bool
	SessionEnded       bool
}

// State is immutable outside this package. Call Reduce to obtain a new state;
// no standalone route ID or ActStep can be supplied to authorize a transition.
type State struct {
	data *stateData
}

type sessionScope struct {
	tenantID   string
	sessionID  string
	generation uint64
	channel    contract.ChannelKind
	locale     string
}

type stateData struct {
	definition                contract.Definition
	scope                     sessionScope
	policy                    Policy
	routeID                   contract.RouteID
	routeEntryID              string
	phase                     Phase
	inputWindowID             string
	inputWindowOpen           bool
	logicalTaskID             string
	routeVisits               uint64
	decisionCalls             uint64
	effectAttempts            uint64
	fullAgentFallbacks        uint64
	retryCounters             map[string]contract.RetryCounter
	sessionRetryTotals        RetryTotals
	routeBudgetUsage          map[contract.RouteID]routeBudgetUsage
	processedEvents           map[string]string
	candidateSnapshot         *BoundCandidateSnapshot
	pendingInput              *contract.NormalizedEvent
	pendingMatch              bool
	pendingDecision           bool
	pendingFallback           bool
	selectedCandidateID       string
	pendingRequest            *contract.EffectRequest
	pendingRequestValidatedAt time.Time
	pendingActionID           string
	pendingRequestRoute       contract.RouteID
	pendingRequestEntry       string
	pendingSayOperationID     string
	saySequence               uint64
	ownership                 contract.GraphOwnership
	graphEnded                bool
	sessionEnded              bool
}

var (
	ErrInvalidState = errors.New("decision reducer state is invalid")
	ErrStaleEvent   = errors.New("decision event is stale for the active generation or route entry")
)

// Start pins a validated frozen definition and begins at its declared entry.
// Admission identity supplies the initial route-entry token; the graph entry
// itself always comes from the frozen definition.
func Start(definition contract.Definition, admission contract.NormalizedEvent, policy Policy) (State, []Effect, error) {
	frozen, err := cloneDefinition(definition)
	if err != nil {
		return State{}, nil, fmt.Errorf("clone frozen decision definition: %w", err)
	}
	if err := frozen.ValidateForReview(); err != nil {
		return State{}, nil, fmt.Errorf("frozen decision definition: %w", err)
	}
	if admission.Kind != contract.EventSessionAdmitted {
		return State{}, nil, fmt.Errorf("decision session must start with session_admitted")
	}
	if err := admission.Validate(admission.ReceivedAt); err != nil {
		return State{}, nil, fmt.Errorf("session admission: %w", err)
	}
	if admission.RouteEntryID == "" {
		return State{}, nil, fmt.Errorf("session admission requires a trusted initial route-entry identity")
	}
	if !stableIdentifier(admission.RouteEntryID) {
		return State{}, nil, fmt.Errorf("initial route-entry identity is not a stable identifier")
	}
	if admission.RouteID != "" && admission.RouteID != frozen.Graph.Entry {
		return State{}, nil, fmt.Errorf("session admission route does not match the frozen graph entry")
	}
	entryRoute, ok := findRoute(frozen, frozen.Graph.Entry)
	if !ok {
		return State{}, nil, fmt.Errorf("frozen graph entry route is missing")
	}
	if policy.FullAgentFallbackLimit != nil {
		limit := *policy.FullAgentFallbackLimit
		policy.FullAgentFallbackLimit = &limit
	}
	data := &stateData{
		definition:       frozen,
		scope:            sessionScope{tenantID: admission.TenantID, sessionID: admission.SessionID, generation: admission.Generation, channel: admission.Channel, locale: admission.Locale},
		policy:           policy,
		routeID:          frozen.Graph.Entry,
		routeEntryID:     admission.RouteEntryID,
		phase:            PhaseListening,
		logicalTaskID:    admission.ID,
		retryCounters:    map[string]contract.RetryCounter{},
		routeBudgetUsage: map[contract.RouteID]routeBudgetUsage{},
		processedEvents:  map[string]string{},
		ownership:        contract.GraphOwnership{State: contract.OwnershipOwned},
	}
	data.processedEvents[admission.ID] = fingerprint(Event{Normalized: &admission})
	state := State{data: data}
	if !data.enteredWithinRouteBudget(entryRoute.ID) {
		data.phase = PhaseStopped
		return state, []Effect{{Kind: EffectSafeStop, RouteID: data.routeID, RouteEntryID: data.routeEntryID, Reason: "route visit ceiling is unresolved or exhausted"}}, nil
	}
	data.routeVisits++
	routeUsage := data.routeBudgetUsage[entryRoute.ID]
	routeUsage.Visits++
	data.routeBudgetUsage[entryRoute.ID] = routeUsage
	effects := enterEffects(data, entryRoute)
	return state, cloneEffects(effects), nil
}

// Reduce applies one normalized semantic event. It has no clock, provider, or
// transport access; event timestamps are the only time inputs.
func Reduce(state State, event Event) (State, []Effect, error) {
	if state.data == nil {
		return State{}, nil, ErrInvalidState
	}
	data := cloneState(state.data)
	if err := validateEventShape(event); err != nil {
		return state, nil, err
	}
	id, generation, tenantID, sessionID, receivedAt := eventIdentity(event)
	if generation != data.scope.generation {
		return state, nil, ErrStaleEvent
	}
	if tenantID != data.scope.tenantID || sessionID != data.scope.sessionID {
		return state, nil, fmt.Errorf("decision event scope does not match the active session")
	}
	fp := fingerprint(event)
	if old, exists := data.processedEvents[id]; exists {
		if old != fp {
			return state, nil, fmt.Errorf("decision event ID %q was replayed with different content", id)
		}
		return state, nil, nil
	}
	if data.graphEnded || data.sessionEnded || data.phase == PhaseStopped {
		return state, nil, fmt.Errorf("decision session is terminal")
	}
	if event.Normalized != nil {
		if err := event.Normalized.Validate(receivedAt); err != nil {
			return state, nil, fmt.Errorf("normalized decision event: %w", err)
		}
		if err := validateActiveEntry(data, event.Normalized.RouteID, event.Normalized.RouteEntryID); err != nil {
			return state, nil, err
		}
	} else if event.Snapshot != nil {
		if err := validateActiveEntry(data, event.Snapshot.RouteID, event.Snapshot.RouteEntryID); err != nil {
			return state, nil, err
		}
	} else if event.Control != nil {
		if err := validateActiveEntry(data, event.Control.RouteID, event.Control.RouteEntryID); err != nil {
			return state, nil, err
		}
	} else if event.Fallback != nil {
		if err := validateActiveEntry(data, event.Fallback.RouteID, event.Fallback.RouteEntryID); err != nil {
			return state, nil, err
		}
	}
	data.processedEvents[id] = fp

	var effects []Effect
	var err error
	switch {
	case event.Snapshot != nil:
		effects, err = reduceSnapshotRefresh(data, *event.Snapshot)
	case event.Control != nil:
		effects, err = reduceControl(data, *event.Control)
	case event.Fallback != nil:
		effects, err = reduceFallback(data, *event.Fallback)
	default:
		effects, err = reduceNormalized(data, *event.Normalized, event.InputID)
	}
	if err != nil {
		return state, nil, err
	}
	return State{data: data}, cloneEffects(effects), nil
}

// ValidateDispatchIntent confirms that an emitted intent still exactly
// matches the reducer's current frozen route, action, active binding, route
// entry, and candidate snapshot. It does not authorize execution: an adapter
// must still use its current grant, operation/receipt path, and owner CAS.
func (state State) ValidateDispatchIntent(effect Effect, observedAt time.Time) error {
	if state.data == nil || effect.Kind != EffectDispatchIntent {
		return ErrInvalidState
	}
	data := state.data
	if data.pendingRequest == nil || data.candidateSnapshot == nil || data.pendingInput == nil {
		return fmt.Errorf("dispatch intent is not pending in the reducer state")
	}
	if effect.RouteID != data.routeID || effect.RouteEntryID != data.routeEntryID || effect.InputID != data.pendingInput.ID || effect.ActionID != data.pendingActionID || !reflect.DeepEqual(effect.Request, data.pendingRequest) {
		return fmt.Errorf("dispatch intent does not match the trusted active reducer state")
	}
	active, ok := findRoute(data.definition, data.routeID)
	if !ok {
		return fmt.Errorf("active route is missing from the frozen definition")
	}
	action, ok := findAction(active, data.pendingActionID)
	if !ok {
		return fmt.Errorf("pending action is missing from the frozen active route")
	}
	if err := validateBoundSnapshot(data, *data.candidateSnapshot, observedAt); err != nil {
		return err
	}
	return data.pendingRequest.ValidateAgainst(action, &data.candidateSnapshot.Snapshot, data.scope.tenantID, data.scope.sessionID, data.scope.generation, observedAt)
}

func (state State) View() View {
	if state.data == nil {
		return View{}
	}
	d := state.data
	view := View{
		TenantID: d.scope.tenantID, SessionID: d.scope.sessionID, Generation: d.scope.generation,
		RouteID: d.routeID, RouteEntryID: d.routeEntryID, Phase: d.phase,
		InputWindowID: d.inputWindowID, InputWindowOpen: d.inputWindowOpen,
		LogicalTaskID: d.logicalTaskID, RouteVisits: d.routeVisits,
		DecisionCalls: d.decisionCalls, EffectAttempts: d.effectAttempts,
		FullAgentFallbacks: d.fullAgentFallbacks, Ownership: d.ownership,
		SessionRetryTotals: d.sessionRetryTotals,
		GraphEnded:         d.graphEnded, SessionEnded: d.sessionEnded,
	}
	keys := make([]string, 0, len(d.retryCounters))
	for id := range d.retryCounters {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	for _, id := range keys {
		counter := d.retryCounters[id]
		counter.CountedAdmissionEventIDs = append([]string(nil), counter.CountedAdmissionEventIDs...)
		view.RetryCounters = append(view.RetryCounters, RetryCount{GroupID: id, Counter: counter})
	}
	return view
}

func reduceNormalized(data *stateData, event contract.NormalizedEvent, inputID string) ([]Effect, error) {
	active, ok := findRoute(data.definition, data.routeID)
	if !ok {
		return nil, fmt.Errorf("active route is missing from the frozen definition")
	}
	switch event.Kind {
	case contract.EventSpeechStarted:
		if err := requireOpenWindow(data, event.InputWindowID); err != nil {
			return nil, err
		}
		// SpeechStarted represents endpointing/VAD progress only. Partial STT is
		// never a match, decision, or call-control input.
		return nil, nil
	case contract.EventInputFinal:
		if err := requireOpenWindow(data, event.InputWindowID); err != nil {
			return nil, err
		}
		if active.Listen == nil || active.Match == nil {
			return nil, fmt.Errorf("final input is not accepted by the frozen active route")
		}
		if !containsModality(active.Listen.Accept, event.Modality) {
			return nil, fmt.Errorf("final input modality is not accepted by the frozen active route")
		}
		data.inputWindowOpen = false
		data.pendingInput = cloneNormalizedEvent(&event)
		data.pendingMatch = true
		data.pendingDecision = false
		data.phase = PhaseMatching
		return matchEffects(data, active), nil
	case contract.EventInitialSilence:
		if err := requireOpenWindow(data, event.InputWindowID); err != nil {
			return nil, err
		}
		data.inputWindowOpen = false
		return routeOutcome(data, active, contract.SourceInput, contract.OutcomeNoInput, event.ID, "no_input", event.ReceivedAt)
	case contract.EventInputError:
		activeWindow := contract.InputWindowIdentity{
			TenantID: data.scope.tenantID, SessionID: data.scope.sessionID,
			Generation: data.scope.generation, RouteID: data.routeID,
			RouteEntryID: data.routeEntryID, InputWindowID: data.inputWindowID,
			Channel: data.scope.channel,
		}
		if err := event.ValidateForActiveInputWindow(activeWindow, event.ReceivedAt); err != nil {
			return nil, fmt.Errorf("input error is not bound to the active window: %w", err)
		}
		if err := requireOpenWindow(data, event.InputWindowID); err != nil {
			return nil, err
		}
		data.inputWindowOpen = false
		return routeOutcome(data, active, contract.SourceInput, contract.OutcomeError, event.ID, "input_error", event.ReceivedAt)
	case contract.EventUtteranceLimit:
		if err := requireOpenWindow(data, event.InputWindowID); err != nil {
			return nil, err
		}
		data.inputWindowOpen = false
		return routeOutcome(data, active, contract.SourceInput, contract.OutcomeUtteranceLimit, event.ID, "no_match", event.ReceivedAt)
	case contract.EventMatchCompleted:
		if !data.pendingMatch || data.pendingInput == nil || inputID != data.pendingInput.ID {
			return nil, fmt.Errorf("match result does not match the active accepted input")
		}
		if data.candidateSnapshot == nil {
			return nil, fmt.Errorf("match result has no bound active candidate snapshot")
		}
		if event.Match == nil || event.Match.BindingID != active.Match.BindingID {
			return nil, fmt.Errorf("match result binding does not match the frozen active matcher")
		}
		if err := validateBoundSnapshot(data, *data.candidateSnapshot, event.ReceivedAt); err != nil {
			return nil, err
		}
		if err := event.Match.ValidateAgainst(data.candidateSnapshot.Snapshot, data.scope.tenantID, data.scope.sessionID, data.scope.generation, event.ReceivedAt); err != nil {
			return nil, fmt.Errorf("deterministic match result: %w", err)
		}
		data.pendingMatch = false
		if event.Match.Outcome == contract.OutcomeNoMatch && active.Match.OnNoMatch == contract.NoMatchToDecision && active.Decide != nil {
			return requestDecision(data, active, event.ID, event.ReceivedAt)
		}
		if event.Match.Outcome == contract.OutcomeCandidate {
			data.selectedCandidateID = event.Match.CandidateID
		}
		return routeOutcome(data, active, contract.SourceMatch, event.Match.Outcome, event.ID, retryReason(event.Match.Outcome), event.ReceivedAt)
	case contract.EventDecisionCompleted:
		if !data.pendingDecision || data.pendingInput == nil || inputID != data.pendingInput.ID {
			return nil, fmt.Errorf("decision result does not match the active Jev request")
		}
		if data.candidateSnapshot == nil || event.Decision == nil {
			return nil, fmt.Errorf("decision result has no bound active candidate snapshot")
		}
		activeBinding := ""
		if active.Match != nil {
			activeBinding = active.Match.BindingID
		}
		if data.candidateSnapshot.BindingID != activeBinding {
			return nil, fmt.Errorf("decision snapshot is not bound to the frozen active matcher")
		}
		if err := validateBoundSnapshot(data, *data.candidateSnapshot, event.ReceivedAt); err != nil {
			return nil, err
		}
		if err := event.Decision.ValidateAgainst(data.candidateSnapshot.Snapshot, data.scope.tenantID, data.scope.sessionID, data.scope.generation, event.ReceivedAt); err != nil {
			return nil, fmt.Errorf("Jev decision result: %w", err)
		}
		data.pendingDecision = false
		if event.Decision.Outcome == contract.OutcomeError || event.Decision.Outcome == contract.OutcomeUnavailable {
			if canUseFullAgent(data) {
				data.fullAgentFallbacks++
				data.phase = PhaseFallback
				data.pendingFallback = true
				return []Effect{{Kind: EffectFullAgentFallback, RouteID: data.routeID, RouteEntryID: data.routeEntryID, InputID: data.pendingInput.ID, Reason: string(event.Decision.Outcome)}}, nil
			}
		}
		if event.Decision.Outcome == contract.OutcomeCandidate {
			data.selectedCandidateID = event.Decision.CandidateID
		}
		return routeOutcome(data, active, contract.SourceDecision, event.Decision.Outcome, event.ID, retryReason(event.Decision.Outcome), event.ReceivedAt)
	case contract.EventEffectSubmitted:
		if data.phase != PhaseActing || data.pendingRequest == nil || data.pendingInput == nil || event.EffectRequest == nil {
			return nil, fmt.Errorf("effect submission has no reducer-emitted pending intent")
		}
		if !reflect.DeepEqual(*event.EffectRequest, *data.pendingRequest) {
			return nil, fmt.Errorf("effect submission does not match the reducer-emitted intent")
		}
		if err := validateDispatchAction(data, *data.pendingRequest, event.ReceivedAt); err != nil {
			return nil, err
		}
		data.pendingRequestValidatedAt = event.ReceivedAt
		if data.pendingActionReleasesOwnership() {
			req := data.pendingRequest
			to := contract.GraphOwnership{
				State:                     contract.OwnershipReceiptPending,
				PendingEffectID:           req.Identity.ID,
				PendingTargetBinding:      req.TargetBinding,
				PendingTargetOpaqueID:     req.TargetOpaqueID,
				PendingCandidateSetSHA256: req.CandidateSetSHA256,
			}
			if err := contract.ValidateOwnershipTransition(data.ownership, to, nil, req, data.pendingAction()); err != nil {
				return nil, fmt.Errorf("effect submission ownership transition: %w", err)
			}
			data.ownership = to
		}
		data.phase = PhaseAwaitingReceipt
		return []Effect{{Kind: EffectAwaitReceipt, RouteID: data.routeID, RouteEntryID: data.routeEntryID, InputID: data.pendingInput.ID, ActionID: data.pendingActionID, Request: cloneRequest(data.pendingRequest)}}, nil
	case contract.EventEffectReceipt:
		return reduceEffectReceipt(data, active, event, event.ReceivedAt)
	case contract.EventCancelled:
		return routeOutcome(data, active, contract.SourceControl, contract.OutcomeCancelled, event.ID, "cancelled", event.ReceivedAt)
	case contract.EventDisconnected:
		return routeOutcome(data, active, contract.SourceControl, contract.OutcomeDisconnected, event.ID, "disconnected", event.ReceivedAt)
	case contract.EventPlaybackStarted:
		// Playback is adapter-owned. A real receipt may be consumed below, but
		// the reducer never fabricates a playback start/completion event.
		return nil, nil
	case contract.EventPlaybackCompleted, contract.EventPlaybackCleared, contract.EventPlaybackFailed:
		if event.Playback == nil {
			return nil, fmt.Errorf("playback transition requires an adapter receipt")
		}
		if data.pendingSayOperationID == "" || event.Playback.OperationID != data.pendingSayOperationID {
			return nil, fmt.Errorf("playback receipt does not match the active say")
		}
		data.pendingSayOperationID = ""
		if data.pendingInput != nil || data.pendingMatch || data.pendingDecision || data.pendingFallback || data.pendingRequest != nil {
			// Barge-in input is authoritative once accepted. A completion, clear,
			// or failure for its prompt cannot reopen listening or replace it.
			return nil, nil
		}
		outcome := event.Playback.Outcome
		if outcome == contract.OutcomeCompleted && targetFor(active, contract.SourcePlayback, outcome).Phase == contract.PhaseListen {
			// The authored completed edge is the route transition. A say+listen
			// route may already have opened its input window on entry; in that
			// case the edge is satisfied without emitting a second Listen effect.
			if active.Listen != nil && !data.inputWindowOpen {
				data.inputWindowID = windowID(data.routeEntryID)
				data.inputWindowOpen = true
				data.phase = PhaseListening
				return []Effect{{Kind: EffectListen, RouteID: data.routeID, RouteEntryID: data.routeEntryID, InputWindowID: data.inputWindowID, Accept: append([]contract.InputModality(nil), active.Listen.Accept...)}}, nil
			}
			return nil, nil
		}
		return routeOutcome(data, active, contract.SourcePlayback, outcome, event.ID, "", event.ReceivedAt)
	case contract.EventRetryExhausted:
		if active.RetryGroup == "" {
			return nil, fmt.Errorf("retry exhaustion received outside a frozen retry-group route")
		}
		group, ok := findRetryGroup(data.definition, active.RetryGroup)
		if !ok {
			return nil, fmt.Errorf("active retry group is missing from the frozen definition")
		}
		return followTarget(data, active, group.Exhausted, event.ID, "retry_exhausted", event.ReceivedAt)
	case contract.EventHandoffAccepted:
		// The existing voice operation/receipt path remains authoritative. This
		// event is informational and cannot release graph or call ownership.
		return nil, nil
	default:
		return nil, fmt.Errorf("event kind %q is not a reducer input", event.Kind)
	}
}

func reduceSnapshotRefresh(data *stateData, refresh SnapshotRefresh) ([]Effect, error) {
	active, ok := findRoute(data.definition, data.routeID)
	if !ok || active.Match == nil {
		return nil, fmt.Errorf("candidate snapshot refresh has no active frozen matcher")
	}
	if data.pendingDecision || data.pendingFallback || data.pendingRequest != nil ||
		(data.pendingMatch && data.candidateSnapshot != nil) || (data.pendingInput != nil && !data.pendingMatch) {
		return nil, fmt.Errorf("candidate snapshot is pinned while matcher, decision, or effect work is pending")
	}
	if !stableIdentifier(refresh.ID) || refresh.ReceivedAt.IsZero() {
		return nil, fmt.Errorf("candidate snapshot refresh requires stable event and receive-time fields")
	}
	if refresh.Bound.BindingID != active.Match.BindingID || refresh.Bound.RouteID != data.routeID || refresh.Bound.RouteEntryID != data.routeEntryID || refresh.RouteID != data.routeID || refresh.RouteEntryID != data.routeEntryID {
		return nil, fmt.Errorf("candidate snapshot is not bound to the trusted active binding and route entry")
	}
	if err := validateBoundSnapshot(data, refresh.Bound, refresh.ReceivedAt); err != nil {
		return nil, err
	}
	data.candidateSnapshot = cloneBoundSnapshot(&refresh.Bound)
	if data.pendingMatch && data.pendingInput != nil {
		return []Effect{{Kind: EffectRunMatcher, RouteID: data.routeID, RouteEntryID: data.routeEntryID, BindingID: active.Match.BindingID, InputID: data.pendingInput.ID, Reason: data.candidateSnapshot.Snapshot.SHA256}}, nil
	}
	return nil, nil
}

func reduceControl(data *stateData, result ControlResult) ([]Effect, error) {
	if data.pendingInput == nil || !data.pendingMatch || result.InputID != data.pendingInput.ID {
		return nil, fmt.Errorf("control result does not match the active accepted input")
	}
	if err := validateControlSnapshot(data, result); err != nil {
		return nil, err
	}
	active, _ := findRoute(data.definition, data.routeID)
	data.pendingMatch = false
	switch result.Command {
	case ControlRepeat:
		return followTarget(data, active, contract.Target{Route: data.definition.Graph.Entry}, result.ID, "repeat", result.ReceivedAt)
	case ControlHelp:
		return followTarget(data, active, contract.Target{Route: data.definition.Graph.Fallback}, result.ID, "help", result.ReceivedAt)
	case ControlCancel:
		return routeOutcome(data, active, contract.SourceControl, contract.OutcomeCancelled, result.ID, "cancelled", result.ReceivedAt)
	default:
		return nil, fmt.Errorf("unknown deterministic control command %q", result.Command)
	}
}

func reduceFallback(data *stateData, result FullAgentResult) ([]Effect, error) {
	if !data.pendingFallback || data.phase != PhaseFallback || data.pendingInput == nil || result.InputID != data.pendingInput.ID {
		return nil, fmt.Errorf("full-agent result does not match the active bounded fallback")
	}
	if !stableIdentifier(result.ID) || result.ReceivedAt.IsZero() {
		return nil, fmt.Errorf("full-agent result requires stable event and receive-time fields")
	}
	if result.Outcome == contract.OutcomeCandidate {
		return nil, fmt.Errorf("full-agent result cannot nominate a call target")
	}
	data.pendingFallback = false
	active, _ := findRoute(data.definition, data.routeID)
	switch result.Outcome {
	case contract.OutcomeCompleted:
		return enterTerminal(data, contract.Target{Terminal: contract.TerminalEndGraph}, result.ID, "full_agent_completed"), nil
	case contract.OutcomeNoMatch, contract.OutcomeAmbiguous, contract.OutcomeRefusal, contract.OutcomeError, contract.OutcomeUnavailable:
		return routeOutcome(data, active, contract.SourceDecision, result.Outcome, result.ID, retryReason(result.Outcome), result.ReceivedAt)
	case contract.OutcomeCancelled:
		return routeOutcome(data, active, contract.SourceControl, contract.OutcomeCancelled, result.ID, "cancelled", result.ReceivedAt)
	default:
		return nil, fmt.Errorf("full-agent outcome %q is not supported by the reducer", result.Outcome)
	}
}

func requestDecision(data *stateData, active contract.Route, causeID string, observedAt time.Time) ([]Effect, error) {
	if active.Decide == nil {
		return routeOutcome(data, active, contract.SourceDecision, contract.OutcomeUnavailable, causeID, "", observedAt)
	}
	routeUsage := data.routeBudgetUsage[data.routeID]
	if !data.budgetHasRoom(data.routeID, contract.BudgetDecisionCalls, data.decisionCalls, routeUsage.DecisionCalls) {
		return routeOutcome(data, active, contract.SourceDecision, contract.OutcomeUnavailable, causeID, "", observedAt)
	}
	data.decisionCalls++
	routeUsage.DecisionCalls++
	data.routeBudgetUsage[data.routeID] = routeUsage
	data.pendingDecision = true
	data.phase = PhaseDeciding
	return []Effect{{Kind: EffectRunDecision, RouteID: data.routeID, RouteEntryID: data.routeEntryID, InputID: inputID(data), ServiceID: active.Decide.ServiceID, Reason: snapshotDigest(data)}}, nil
}

func matchEffects(data *stateData, active contract.Route) []Effect {
	if data.candidateSnapshot == nil {
		return []Effect{{Kind: EffectRefreshCandidates, RouteID: data.routeID, RouteEntryID: data.routeEntryID, BindingID: active.Match.BindingID, InputID: inputID(data)}}
	}
	return []Effect{{Kind: EffectRunMatcher, RouteID: data.routeID, RouteEntryID: data.routeEntryID, BindingID: active.Match.BindingID, InputID: inputID(data), Reason: snapshotDigest(data)}}
}

func dispatchCandidate(data *stateData, active contract.Route, candidateID string, observedAt time.Time) ([]Effect, error) {
	if candidateID == "" || data.candidateSnapshot == nil || data.pendingInput == nil {
		return nil, fmt.Errorf("candidate action requires the current accepted input and bound snapshot")
	}
	if err := validateBoundSnapshot(data, *data.candidateSnapshot, observedAt); err != nil {
		return nil, err
	}
	actions := make([]contract.ActStep, 0, len(active.Act))
	for _, action := range active.Act {
		if action.TargetBinding != "" {
			actions = append(actions, action)
		}
	}
	if len(actions) != 1 {
		return nil, fmt.Errorf("candidate selection requires exactly one frozen target-bound action")
	}
	routeUsage := data.routeBudgetUsage[data.routeID]
	if !data.budgetHasRoom(data.routeID, contract.BudgetEffectAttempts, data.effectAttempts, routeUsage.EffectAttempts) {
		data.phase = PhaseStopped
		return []Effect{{Kind: EffectSafeStop, RouteID: data.routeID, RouteEntryID: data.routeEntryID, InputID: data.pendingInput.ID, Reason: "inherited or route effect attempt ceiling is unresolved or exhausted"}}, nil
	}
	action := actions[0]
	identity := contract.EffectIdentity{
		TenantID:     data.scope.tenantID,
		SessionID:    data.scope.sessionID,
		Generation:   data.scope.generation,
		RouteEntryID: data.routeEntryID,
		InputID:      data.pendingInput.ID,
		ActionID:     action.ID,
		GraphSHA256:  data.definition.DigestInputs.Graph,
	}
	identity.ID = identity.CanonicalID()
	request := contract.EffectRequest{
		Identity:               identity,
		ToolID:                 action.ToolID,
		Kind:                   action.Kind,
		TargetBinding:          action.TargetBinding,
		TargetOpaqueID:         candidateID,
		CandidateSetSHA256:     data.candidateSnapshot.Snapshot.SHA256,
		ReauthorizeAtExecution: action.ReauthorizeAtExecution,
	}
	if err := request.ValidateAgainst(action, &data.candidateSnapshot.Snapshot, data.scope.tenantID, data.scope.sessionID, data.scope.generation, observedAt); err != nil {
		return nil, fmt.Errorf("frozen action intent: %w", err)
	}
	data.effectAttempts++
	routeUsage.EffectAttempts++
	data.routeBudgetUsage[data.routeID] = routeUsage
	data.phase = PhaseActing
	data.pendingRequest = cloneRequest(&request)
	data.pendingRequestValidatedAt = observedAt
	data.pendingActionID = action.ID
	data.pendingRequestRoute = data.routeID
	data.pendingRequestEntry = data.routeEntryID
	return []Effect{{Kind: EffectDispatchIntent, RouteID: data.routeID, RouteEntryID: data.routeEntryID, InputID: data.pendingInput.ID, ActionID: action.ID, Request: &request}}, nil
}

func routeOutcome(data *stateData, active contract.Route, source contract.ResultSource, outcome contract.Outcome, causeID, reason string, observedAt time.Time) ([]Effect, error) {
	target := targetFor(active, source, outcome)
	if source == contract.SourceDecision && (outcome == contract.OutcomeError || outcome == contract.OutcomeUnavailable) && canUseFullAgent(data) {
		data.fullAgentFallbacks++
		data.pendingDecision = false
		data.phase = PhaseFallback
		data.pendingFallback = true
		return []Effect{{Kind: EffectFullAgentFallback, RouteID: data.routeID, RouteEntryID: data.routeEntryID, InputID: inputID(data), Reason: string(outcome)}}, nil
	}
	return followTarget(data, active, target, causeID, reason, observedAt)
}

func targetFor(route contract.Route, source contract.ResultSource, outcome contract.Outcome) contract.Target {
	for _, transition := range route.Next {
		if transition.Source == source && transition.Outcome == outcome {
			return transition.Target
		}
	}
	return route.Fallback
}

func followTarget(data *stateData, active contract.Route, target contract.Target, causeID, reason string, observedAt time.Time) ([]Effect, error) {
	if target.Route != "" {
		route, ok := findRoute(data.definition, target.Route)
		if !ok {
			data.phase = PhaseStopped
			return []Effect{{Kind: EffectSafeStop, RouteID: data.routeID, RouteEntryID: data.routeEntryID, Reason: "frozen target route is missing"}}, nil
		}
		if route.RetryGroup != "" {
			admitted, exhausted, err := admitRetry(data, route, causeID, reason)
			if err != nil {
				return nil, err
			}
			if !admitted {
				target = exhausted
				route, ok = findRoute(data.definition, target.Route)
				if target.Route != "" && !ok {
					data.phase = PhaseStopped
					return []Effect{{Kind: EffectSafeStop, RouteID: data.routeID, RouteEntryID: data.routeEntryID, Reason: "retry exhaustion target route is missing"}}, nil
				}
				if target.Route != "" {
					return enterRoute(data, route, causeID, reason, observedAt)
				}
				return enterTerminal(data, target, causeID, reason), nil
			}
		}
		return enterRoute(data, route, causeID, reason, observedAt)
	}
	if target.Phase != "" {
		switch target.Phase {
		case contract.PhaseAct:
			if data.selectedCandidateID == "" {
				data.phase = PhaseStopped
				return []Effect{{Kind: EffectSafeStop, RouteID: data.routeID, RouteEntryID: data.routeEntryID, Reason: "frozen act phase has no validated candidate"}}, nil
			}
			candidateID := data.selectedCandidateID
			data.selectedCandidateID = ""
			return dispatchCandidate(data, active, candidateID, observedAt)
		case contract.PhaseDecide:
			return requestDecision(data, active, causeID, observedAt)
		case contract.PhaseListen:
			if active.Listen == nil {
				return nil, fmt.Errorf("frozen listen phase has no listen step")
			}
			data.inputWindowID = windowID(data.routeEntryID)
			data.inputWindowOpen = true
			data.phase = PhaseListening
			return []Effect{{Kind: EffectListen, RouteID: data.routeID, RouteEntryID: data.routeEntryID, InputWindowID: data.inputWindowID, Accept: append([]contract.InputModality(nil), active.Listen.Accept...)}}, nil
		case contract.PhaseSay:
			if active.Say == nil {
				return nil, fmt.Errorf("frozen say phase has no say step")
			}
			return []Effect{sayEffect(data, active)}, nil
		case contract.PhaseMatch:
			if active.Match == nil || data.pendingInput == nil {
				return nil, fmt.Errorf("frozen match phase has no active matcher input")
			}
			data.pendingMatch = true
			return matchEffects(data, active), nil
		default:
			return nil, fmt.Errorf("unknown frozen route phase %q", target.Phase)
		}
	}
	return enterTerminal(data, target, causeID, reason), nil
}

func admitRetry(data *stateData, route contract.Route, causeID, reason string) (bool, contract.Target, error) {
	group, ok := findRetryGroup(data.definition, route.RetryGroup)
	if !ok {
		return false, contract.Target{Terminal: contract.TerminalSafeStop}, fmt.Errorf("retry group %q is missing from the frozen definition", route.RetryGroup)
	}
	counter, exists := data.retryCounters[group.ID]
	if !exists {
		counter = contract.RetryCounter{GroupID: group.ID, LogicalTaskID: data.logicalTaskID}
	}
	for _, admittedID := range counter.CountedAdmissionEventIDs {
		if admittedID == causeID {
			return true, contract.Target{}, nil
		}
	}
	routeUsage := data.routeBudgetUsage[route.ID]
	if !data.budgetHasRoom(route.ID, contract.BudgetReprompts, data.sessionRetryTotals.Reprompts, routeUsage.Retries.Reprompts) {
		return false, group.Exhausted, nil
	}
	if group.MaxReprompts.Value == nil || counter.Reprompts >= *group.MaxReprompts.Value {
		return false, group.Exhausted, nil
	}
	var sessionReasonUsed, routeReasonUsed uint64
	var reasonDimension contract.BudgetDimension
	if reason == "no_input" && (group.MaxNoInputReprompts.Value == nil || counter.NoInputReprompts >= *group.MaxNoInputReprompts.Value) {
		return false, group.Exhausted, nil
	}
	if reason == "no_match" && (group.MaxNoMatchReprompts.Value == nil || counter.NoMatchReprompts >= *group.MaxNoMatchReprompts.Value) {
		return false, group.Exhausted, nil
	}
	if reason == "ambiguous" && (group.MaxAmbiguousReprompts.Value == nil || counter.AmbiguousReprompts >= *group.MaxAmbiguousReprompts.Value) {
		return false, group.Exhausted, nil
	}
	switch reason {
	case "no_input":
		reasonDimension = contract.BudgetNoInputReprompts
		sessionReasonUsed = data.sessionRetryTotals.NoInputReprompts
		routeReasonUsed = routeUsage.Retries.NoInputReprompts
	case "no_match":
		reasonDimension = contract.BudgetNoMatchReprompts
		sessionReasonUsed = data.sessionRetryTotals.NoMatchReprompts
		routeReasonUsed = routeUsage.Retries.NoMatchReprompts
	case "ambiguous":
		reasonDimension = contract.BudgetAmbiguousReprompts
		sessionReasonUsed = data.sessionRetryTotals.AmbiguousReprompts
		routeReasonUsed = routeUsage.Retries.AmbiguousReprompts
	}
	if reasonDimension != "" && !data.budgetHasRoom(route.ID, reasonDimension, sessionReasonUsed, routeReasonUsed) {
		return false, group.Exhausted, nil
	}
	counter.Reprompts++
	data.sessionRetryTotals.Reprompts++
	routeUsage.Retries.Reprompts++
	switch reason {
	case "no_input":
		counter.NoInputReprompts++
		data.sessionRetryTotals.NoInputReprompts++
		routeUsage.Retries.NoInputReprompts++
	case "no_match":
		counter.NoMatchReprompts++
		data.sessionRetryTotals.NoMatchReprompts++
		routeUsage.Retries.NoMatchReprompts++
	case "ambiguous":
		counter.AmbiguousReprompts++
		data.sessionRetryTotals.AmbiguousReprompts++
		routeUsage.Retries.AmbiguousReprompts++
	}
	data.routeBudgetUsage[route.ID] = routeUsage
	counter.CountedAdmissionEventIDs = append(counter.CountedAdmissionEventIDs, causeID)
	if err := counter.ValidateFor(group); err != nil {
		return false, contract.Target{}, fmt.Errorf("retry counter: %w", err)
	}
	data.retryCounters[group.ID] = counter
	return true, contract.Target{}, nil
}

func enterRoute(data *stateData, route contract.Route, causeID, reason string, observedAt time.Time) ([]Effect, error) {
	routeUsage := data.routeBudgetUsage[route.ID]
	if !data.budgetHasRoom(route.ID, contract.BudgetRouteVisits, data.routeVisits, routeUsage.Visits) {
		data.phase = PhaseStopped
		data.inputWindowOpen = false
		return []Effect{{Kind: EffectSafeStop, RouteID: data.routeID, RouteEntryID: data.routeEntryID, Reason: "inherited or route visit ceiling is unresolved or exhausted"}}, nil
	}
	previousEntry := data.routeEntryID
	data.routeID = route.ID
	data.routeEntryID = nextRouteEntry(previousEntry, causeID, route.ID)
	data.routeVisits++
	routeUsage.Visits++
	data.routeBudgetUsage[route.ID] = routeUsage
	data.phase = PhaseListening
	data.inputWindowID = ""
	data.inputWindowOpen = false
	data.candidateSnapshot = nil
	data.pendingInput = nil
	data.pendingMatch = false
	data.pendingDecision = false
	data.pendingFallback = false
	data.selectedCandidateID = ""
	data.pendingRequest = nil
	data.pendingRequestValidatedAt = time.Time{}
	data.pendingActionID = ""
	data.pendingRequestRoute = ""
	data.pendingRequestEntry = ""
	data.pendingSayOperationID = ""
	effects := enterEffects(data, route)
	return effects, nil
}

func enterEffects(data *stateData, route contract.Route) []Effect {
	effects := []Effect{{Kind: EffectEnterRoute, RouteID: route.ID, RouteEntryID: data.routeEntryID}}
	if route.Listen != nil {
		data.inputWindowID = windowID(data.routeEntryID)
		data.inputWindowOpen = true
		data.phase = PhaseListening
		effects = append(effects, Effect{Kind: EffectListen, RouteID: route.ID, RouteEntryID: data.routeEntryID, InputWindowID: data.inputWindowID, Accept: append([]contract.InputModality(nil), route.Listen.Accept...)})
	}
	if route.Say != nil {
		effects = append(effects, sayEffect(data, route))
	}
	return effects
}

func sayEffect(data *stateData, route contract.Route) Effect {
	data.saySequence++
	operationID := playbackOperationID(data.routeEntryID, data.saySequence)
	data.pendingSayOperationID = operationID
	return Effect{
		Kind: EffectSay, RouteID: route.ID, RouteEntryID: data.routeEntryID,
		PlaybackOperationID: operationID, MessageFamilies: append([]string(nil), route.Say.Families...),
	}
}

func playbackOperationID(routeEntryID string, sequence uint64) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("playback\x00%s\x00%d", routeEntryID, sequence)))
	return "playback-" + hex.EncodeToString(digest[:12])
}

func enterTerminal(data *stateData, target contract.Target, causeID, reason string) []Effect {
	switch target.Terminal {
	case contract.TerminalEndGraph:
		data.graphEnded = true
		data.phase = PhaseTerminal
		data.inputWindowOpen = false
		return []Effect{{Kind: EffectEndGraph, RouteID: data.routeID, RouteEntryID: data.routeEntryID, Reason: reason}}
	case contract.TerminalEndSession:
		data.sessionEnded = true
		data.phase = PhaseTerminal
		data.inputWindowOpen = false
		return []Effect{{Kind: EffectEndSession, RouteID: data.routeID, RouteEntryID: data.routeEntryID, Reason: reason}}
	case contract.TerminalReleaseCallOwnership:
		// Release is reachable only from the confirmed-receipt branch below.
		data.phase = PhaseStopped
		data.inputWindowOpen = false
		return []Effect{{Kind: EffectSafeStop, RouteID: data.routeID, RouteEntryID: data.routeEntryID, Reason: "ownership release requires a matching confirmed receipt"}}
	case contract.TerminalAwaitReceipt:
		data.phase = PhaseAwaitingReceipt
		return []Effect{{Kind: EffectAwaitReceipt, RouteID: data.routeID, RouteEntryID: data.routeEntryID}}
	default:
		data.phase = PhaseStopped
		data.inputWindowOpen = false
		return []Effect{{Kind: EffectSafeStop, RouteID: data.routeID, RouteEntryID: data.routeEntryID, Reason: reason}}
	}
}

func reduceEffectReceipt(data *stateData, active contract.Route, event contract.NormalizedEvent, observedAt time.Time) ([]Effect, error) {
	if data.phase != PhaseAwaitingReceipt || data.pendingRequest == nil || data.pendingRequestValidatedAt.IsZero() || data.pendingInput == nil || event.Effect == nil {
		return nil, fmt.Errorf("effect receipt has no reducer-emitted pending intent")
	}
	receipt := event.Effect
	if !reflect.DeepEqual(receipt.Identity, data.pendingRequest.Identity) {
		return nil, fmt.Errorf("effect receipt identity does not match the pending reducer intent")
	}
	if err := receipt.Validate(); err != nil {
		return nil, fmt.Errorf("effect receipt: %w", err)
	}
	// Revalidate against the state at submission time. A protected candidate
	// snapshot can expire while the authoritative provider receipt is in flight;
	// its expiry does not invalidate a receipt for the already submitted intent.
	if err := validateDispatchAction(data, *data.pendingRequest, data.pendingRequestValidatedAt); err != nil {
		return nil, err
	}
	if data.pendingActionReleasesOwnership() {
		if data.ownership.State != contract.OwnershipReceiptPending {
			return nil, fmt.Errorf("call ownership receipt has no admitted pending effect")
		}
		if receipt.TargetOpaqueID != "" && receipt.TargetOpaqueID != data.pendingRequest.TargetOpaqueID {
			return nil, fmt.Errorf("effect receipt target does not match the pending intended target")
		}
		if receipt.Status == contract.EffectConfirmed && (receipt.TargetOpaqueID != data.pendingRequest.TargetOpaqueID || data.ownership.State != contract.OwnershipReceiptPending) {
			return nil, fmt.Errorf("confirmed receipt does not match the pending intended target")
		}
		switch receipt.Status {
		case contract.EffectAccepted, contract.EffectSubmitted, contract.EffectUnknown:
			if data.ownership.State == contract.OwnershipReceiptPending {
				if err := contract.ValidateOwnershipTransition(data.ownership, data.ownership, receipt, data.pendingRequest, data.pendingAction()); err != nil {
					return nil, fmt.Errorf("effect receipt ownership transition: %w", err)
				}
			}
		case contract.EffectFailed:
			if data.ownership.State == contract.OwnershipReceiptPending {
				owned := contract.GraphOwnership{State: contract.OwnershipOwned}
				if err := contract.ValidateOwnershipTransition(data.ownership, owned, receipt, data.pendingRequest, data.pendingAction()); err != nil {
					return nil, fmt.Errorf("failed effect ownership transition: %w", err)
				}
				data.ownership = owned
			}
		case contract.EffectConfirmed:
			released := contract.GraphOwnership{State: contract.OwnershipReleased, ReleaseReceiptID: receipt.ReceiptID}
			if err := contract.ValidateOwnershipTransition(data.ownership, released, receipt, data.pendingRequest, data.pendingAction()); err != nil {
				return nil, fmt.Errorf("confirmed effect ownership transition: %w", err)
			}
			data.ownership = released
		}
	}
	outcome := map[contract.EffectStatus]contract.Outcome{
		contract.EffectSubmitted: contract.OutcomeAccepted,
		contract.EffectAccepted:  contract.OutcomeAccepted,
		contract.EffectConfirmed: contract.OutcomeConfirmed,
		contract.EffectFailed:    contract.OutcomeFailed,
		contract.EffectUnknown:   contract.OutcomeUnknown,
	}[receipt.Status]
	if outcome == "" {
		return nil, fmt.Errorf("unknown effect receipt status %q", receipt.Status)
	}
	target := targetFor(active, contract.SourceEffect, outcome)
	if target.Terminal == contract.TerminalAwaitReceipt {
		// Preserve the reducer-emitted intent whenever the frozen edge requests
		// reconciliation. Call-ownership actions are required by validation to
		// use this edge for accepted/unknown receipts; non-release actions may
		// also explicitly choose it.
		data.phase = PhaseAwaitingReceipt
		return []Effect{{Kind: EffectAwaitReceipt, RouteID: data.routeID, RouteEntryID: data.routeEntryID, InputID: data.pendingInput.ID, ActionID: data.pendingActionID, Request: cloneRequest(data.pendingRequest), ReceiptID: receipt.ReceiptID}}, nil
	}
	if receipt.Status == contract.EffectConfirmed && data.ownership.State == contract.OwnershipReleased {
		if target.Terminal != contract.TerminalReleaseCallOwnership {
			return nil, fmt.Errorf("frozen route does not map this confirmed receipt to ownership release")
		}
		data.phase = PhaseTerminal
		data.graphEnded = true
		request := cloneRequest(data.pendingRequest)
		data.pendingRequest = nil
		return []Effect{{Kind: EffectReleaseOwnership, RouteID: data.routeID, RouteEntryID: data.routeEntryID, InputID: data.pendingInput.ID, ActionID: data.pendingActionID, Request: request, ReceiptID: receipt.ReceiptID, Reason: "adapter must apply the existing owner compare-and-swap"}}, nil
	}
	data.pendingRequest = nil
	data.pendingRequestValidatedAt = time.Time{}
	data.pendingActionID = ""
	data.pendingRequestRoute = ""
	data.pendingRequestEntry = ""
	return routeOutcome(data, active, contract.SourceEffect, outcome, event.ID, "", observedAt)
}

func validateDispatchAction(data *stateData, request contract.EffectRequest, observedAt time.Time) error {
	if data.pendingRequest == nil || !reflect.DeepEqual(request, *data.pendingRequest) || data.pendingRequestRoute != data.routeID || data.pendingRequestEntry != data.routeEntryID {
		return fmt.Errorf("effect does not match the trusted active route-entry intent")
	}
	active, ok := findRoute(data.definition, data.routeID)
	if !ok {
		return fmt.Errorf("active route is missing from the frozen definition")
	}
	action, ok := findAction(active, data.pendingActionID)
	if !ok {
		return fmt.Errorf("pending action is missing from the frozen active route")
	}
	if data.candidateSnapshot == nil {
		return fmt.Errorf("effect has no trusted active candidate snapshot")
	}
	if err := validateBoundSnapshot(data, *data.candidateSnapshot, observedAt); err != nil {
		return err
	}
	return request.ValidateAgainst(action, &data.candidateSnapshot.Snapshot, data.scope.tenantID, data.scope.sessionID, data.scope.generation, observedAt)
}

func validateBoundSnapshot(data *stateData, bound BoundCandidateSnapshot, now time.Time) error {
	active, ok := findRoute(data.definition, data.routeID)
	if !ok || active.Match == nil {
		return fmt.Errorf("candidate snapshot has no frozen active matcher")
	}
	if bound.BindingID != active.Match.BindingID || bound.RouteID != data.routeID || bound.RouteEntryID != data.routeEntryID {
		return fmt.Errorf("candidate snapshot is not bound to the trusted active binding and route entry")
	}
	return bound.Snapshot.ValidateFor(data.scope.tenantID, data.scope.sessionID, data.scope.generation, now)
}

func validateControlSnapshot(data *stateData, result ControlResult) error {
	if !stableIdentifier(result.ID) || result.ReceivedAt.IsZero() {
		return fmt.Errorf("control result requires stable event and receive-time fields")
	}
	active, ok := findRoute(data.definition, data.routeID)
	if !ok || active.Match == nil || data.candidateSnapshot == nil {
		return fmt.Errorf("control result has no active matcher snapshot")
	}
	if result.BindingID != active.Match.BindingID || result.BindingID != data.candidateSnapshot.BindingID || result.SnapshotSHA256 != data.candidateSnapshot.Snapshot.SHA256 || data.candidateSnapshot.RouteEntryID != data.routeEntryID {
		return fmt.Errorf("control result does not match the trusted active binding and route-entry snapshot")
	}
	if err := data.candidateSnapshot.Snapshot.ValidateFor(data.scope.tenantID, data.scope.sessionID, data.scope.generation, result.ReceivedAt); err != nil {
		return err
	}
	return nil
}

func requireOpenWindow(data *stateData, id string) error {
	if !data.inputWindowOpen || id == "" || id != data.inputWindowID {
		return fmt.Errorf("input event does not match the open route-entry window")
	}
	return nil
}

func validateActiveEntry(data *stateData, routeID contract.RouteID, routeEntryID string) error {
	if routeID != data.routeID || routeEntryID == "" || routeEntryID != data.routeEntryID {
		return ErrStaleEvent
	}
	return nil
}

func validateEventShape(event Event) error {
	count := 0
	if event.Normalized != nil {
		count++
	}
	if event.Snapshot != nil {
		count++
	}
	if event.Control != nil {
		count++
	}
	if event.Fallback != nil {
		count++
	}
	if count != 1 {
		return fmt.Errorf("decision event must contain exactly one normalized event, snapshot refresh, or control result")
	}
	if event.Normalized != nil && (event.Snapshot != nil || event.Control != nil || event.Fallback != nil) {
		return fmt.Errorf("decision event union contains multiple variants")
	}
	if event.Snapshot != nil && (event.InputID != "" || event.Control != nil || event.Fallback != nil) {
		return fmt.Errorf("snapshot refresh cannot carry input correlation or another event variant")
	}
	if event.Control != nil && (event.InputID != "" || event.Normalized != nil || event.Fallback != nil) {
		return fmt.Errorf("control result cannot carry normalized input or another event variant")
	}
	if event.Fallback != nil && (event.InputID != "" || event.Normalized != nil || event.Snapshot != nil || event.Control != nil) {
		return fmt.Errorf("full-agent result cannot carry normalized input or another event variant")
	}
	if event.Normalized != nil {
		requiresInput := event.Normalized.Kind == contract.EventMatchCompleted || event.Normalized.Kind == contract.EventDecisionCompleted
		if requiresInput && !stableIdentifier(event.InputID) {
			return fmt.Errorf("match and decision results require a stable accepted-input ID")
		}
		if !requiresInput && event.InputID != "" {
			return fmt.Errorf("this normalized event cannot carry input correlation")
		}
	}
	return nil
}

// ValidateEventStructure checks an event union and each variant's static
// contract shape. It does not authorize the event against reducer state or
// prove that otherwise valid identifiers contain no sensitive semantic data.
func ValidateEventStructure(event Event, now time.Time) error {
	if err := validateEventShape(event); err != nil {
		return err
	}
	switch {
	case event.Normalized != nil:
		return event.Normalized.Validate(now)
	case event.Snapshot != nil:
		refresh := event.Snapshot
		for _, field := range []struct{ name, value string }{
			{"snapshot event ID", refresh.ID}, {"snapshot tenant ID", refresh.TenantID},
			{"snapshot session ID", refresh.SessionID}, {"snapshot route-entry ID", refresh.RouteEntryID},
			{"snapshot binding ID", refresh.Bound.BindingID}, {"bound snapshot route-entry ID", refresh.Bound.RouteEntryID},
		} {
			if err := contract.ValidateIdentifier(field.value); err != nil {
				return fmt.Errorf("%s: %w", field.name, err)
			}
		}
		if refresh.Generation == 0 {
			return fmt.Errorf("snapshot generation must be positive")
		}
		if err := contract.ValidateRouteID(refresh.RouteID); err != nil {
			return fmt.Errorf("snapshot route ID: %w", err)
		}
		if err := contract.ValidateRouteID(refresh.Bound.RouteID); err != nil {
			return fmt.Errorf("bound snapshot route ID: %w", err)
		}
		if refresh.ReceivedAt.IsZero() {
			return fmt.Errorf("candidate snapshot refresh receive time is required")
		}
		return refresh.Bound.Snapshot.ValidateFor(refresh.TenantID, refresh.SessionID, refresh.Generation, now)
	case event.Control != nil:
		result := event.Control
		for _, field := range []struct{ name, value string }{
			{"control event ID", result.ID}, {"control tenant ID", result.TenantID},
			{"control session ID", result.SessionID}, {"control route-entry ID", result.RouteEntryID},
			{"control input ID", result.InputID}, {"control binding ID", result.BindingID},
		} {
			if err := contract.ValidateIdentifier(field.value); err != nil {
				return fmt.Errorf("%s: %w", field.name, err)
			}
		}
		if result.Generation == 0 {
			return fmt.Errorf("control generation must be positive")
		}
		if err := contract.ValidateRouteID(result.RouteID); err != nil {
			return fmt.Errorf("control route ID: %w", err)
		}
		if err := contract.ValidateSHA256(result.SnapshotSHA256); err != nil {
			return fmt.Errorf("control snapshot digest: %w", err)
		}
		if result.ReceivedAt.IsZero() {
			return fmt.Errorf("control result receive time is required")
		}
		switch result.Command {
		case ControlRepeat, ControlHelp, ControlCancel:
			return nil
		default:
			return fmt.Errorf("unknown deterministic control command")
		}
	default:
		result := event.Fallback
		for _, field := range []struct{ name, value string }{
			{"fallback event ID", result.ID}, {"fallback tenant ID", result.TenantID},
			{"fallback session ID", result.SessionID}, {"fallback route-entry ID", result.RouteEntryID},
			{"fallback input ID", result.InputID},
		} {
			if err := contract.ValidateIdentifier(field.value); err != nil {
				return fmt.Errorf("%s: %w", field.name, err)
			}
		}
		if result.Generation == 0 {
			return fmt.Errorf("fallback generation must be positive")
		}
		if err := contract.ValidateRouteID(result.RouteID); err != nil {
			return fmt.Errorf("fallback route ID: %w", err)
		}
		if result.ReceivedAt.IsZero() {
			return fmt.Errorf("fallback result receive time is required")
		}
		switch result.Outcome {
		case contract.OutcomeCompleted, contract.OutcomeNoMatch, contract.OutcomeAmbiguous,
			contract.OutcomeRefusal, contract.OutcomeError, contract.OutcomeUnavailable, contract.OutcomeCancelled:
			return nil
		default:
			return fmt.Errorf("unsupported full-agent outcome")
		}
	}
}

func eventIdentity(event Event) (id string, generation uint64, tenantID, sessionID string, receivedAt time.Time) {
	switch {
	case event.Normalized != nil:
		e := event.Normalized
		return e.ID, e.Generation, e.TenantID, e.SessionID, e.ReceivedAt
	case event.Snapshot != nil:
		e := event.Snapshot
		return e.ID, e.Generation, e.TenantID, e.SessionID, e.ReceivedAt
	case event.Control != nil:
		e := event.Control
		return e.ID, e.Generation, e.TenantID, e.SessionID, e.ReceivedAt
	default:
		e := event.Fallback
		return e.ID, e.Generation, e.TenantID, e.SessionID, e.ReceivedAt
	}
}

func fingerprint(event Event) string {
	encoded, _ := json.Marshal(event)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func cloneState(input *stateData) *stateData {
	copy := *input
	copy.retryCounters = make(map[string]contract.RetryCounter, len(input.retryCounters))
	for id, counter := range input.retryCounters {
		counter.CountedAdmissionEventIDs = append([]string(nil), counter.CountedAdmissionEventIDs...)
		copy.retryCounters[id] = counter
	}
	copy.routeBudgetUsage = make(map[contract.RouteID]routeBudgetUsage, len(input.routeBudgetUsage))
	for id, usage := range input.routeBudgetUsage {
		copy.routeBudgetUsage[id] = usage
	}
	copy.processedEvents = make(map[string]string, len(input.processedEvents))
	for id, fp := range input.processedEvents {
		copy.processedEvents[id] = fp
	}
	copy.candidateSnapshot = cloneBoundSnapshot(input.candidateSnapshot)
	copy.pendingInput = cloneNormalizedEvent(input.pendingInput)
	copy.pendingRequest = cloneRequest(input.pendingRequest)
	if input.policy.FullAgentFallbackLimit != nil {
		limit := *input.policy.FullAgentFallbackLimit
		copy.policy.FullAgentFallbackLimit = &limit
	}
	return &copy
}

func cloneDefinition(input contract.Definition) (contract.Definition, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		return contract.Definition{}, err
	}
	var output contract.Definition
	if err := json.Unmarshal(encoded, &output); err != nil {
		return contract.Definition{}, err
	}
	return output, nil
}

func cloneBoundSnapshot(input *BoundCandidateSnapshot) *BoundCandidateSnapshot {
	if input == nil {
		return nil
	}
	output := *input
	output.Snapshot.CandidateIDs = append([]string(nil), input.Snapshot.CandidateIDs...)
	if input.Snapshot.ProtectedSnapshot != nil {
		protected := *input.Snapshot.ProtectedSnapshot
		output.Snapshot.ProtectedSnapshot = &protected
	}
	return &output
}

func cloneNormalizedEvent(input *contract.NormalizedEvent) *contract.NormalizedEvent {
	if input == nil {
		return nil
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil
	}
	var output contract.NormalizedEvent
	if json.Unmarshal(encoded, &output) != nil {
		return nil
	}
	return &output
}

func cloneRequest(input *contract.EffectRequest) *contract.EffectRequest {
	if input == nil {
		return nil
	}
	output := *input
	if input.Payload != nil {
		payload := *input.Payload
		output.Payload = &payload
	}
	return &output
}

func cloneEffects(input []Effect) []Effect {
	if len(input) == 0 {
		return nil
	}
	output := make([]Effect, len(input))
	copy(output, input)
	for i := range output {
		output[i].MessageFamilies = append([]string(nil), input[i].MessageFamilies...)
		output[i].Accept = append([]contract.InputModality(nil), input[i].Accept...)
		output[i].Request = cloneRequest(input[i].Request)
	}
	return output
}

func findRoute(definition contract.Definition, id contract.RouteID) (contract.Route, bool) {
	for _, route := range definition.Graph.Routes {
		if route.ID == id {
			return route, true
		}
	}
	return contract.Route{}, false
}

func findAction(route contract.Route, id string) (contract.ActStep, bool) {
	for _, action := range route.Act {
		if action.ID == id {
			return action, true
		}
	}
	return contract.ActStep{}, false
}

func findRetryGroup(definition contract.Definition, id string) (contract.RetryGroup, bool) {
	for _, group := range definition.Graph.RetryGroups {
		if group.ID == id {
			return group, true
		}
	}
	return contract.RetryGroup{}, false
}

func (data *stateData) enteredWithinRouteBudget(routeID contract.RouteID) bool {
	usage := data.routeBudgetUsage[routeID]
	return data.budgetHasRoom(routeID, contract.BudgetRouteVisits, data.routeVisits, usage.Visits)
}

// budgetHasRoom applies the inherited session ceiling and any narrower
// per-route override to a reducer-owned counter. Other resource budgets need
// metrics owned by their runtime adapters and are outside this reducer.
func (data *stateData) budgetHasRoom(routeID contract.RouteID, dimension contract.BudgetDimension, sessionUsed, routeUsed uint64) bool {
	inherited, ok := data.definition.Graph.Authority.Budgets[dimension]
	if !ok || inherited.Value == nil || sessionUsed >= *inherited.Value {
		return false
	}
	route, ok := findRoute(data.definition, routeID)
	if !ok {
		return false
	}
	override, ok := route.BudgetOverrides[dimension]
	if !ok {
		return true
	}
	return override.Value != nil && routeUsed < *override.Value
}

func (data *stateData) pendingAction() *contract.ActStep {
	route, ok := findRoute(data.definition, data.pendingRequestRoute)
	if !ok {
		return nil
	}
	action, ok := findAction(route, data.pendingActionID)
	if !ok {
		return nil
	}
	return &action
}

func (data *stateData) pendingActionReleasesOwnership() bool {
	action := data.pendingAction()
	return action != nil && action.ReleasesCallOwnership
}

func canUseFullAgent(data *stateData) bool {
	return data.policy.FullAgentFallbackLimit != nil && data.fullAgentFallbacks < *data.policy.FullAgentFallbackLimit
}

func inputID(data *stateData) string {
	if data.pendingInput == nil {
		return ""
	}
	return data.pendingInput.ID
}

func snapshotDigest(data *stateData) string {
	if data.candidateSnapshot == nil {
		return ""
	}
	return data.candidateSnapshot.Snapshot.SHA256
}

func retryReason(outcome contract.Outcome) string {
	switch outcome {
	case contract.OutcomeNoInput:
		return "no_input"
	case contract.OutcomeNoMatch:
		return "no_match"
	case contract.OutcomeAmbiguous:
		return "ambiguous"
	default:
		return ""
	}
}

func nextRouteEntry(previous, causeID string, route contract.RouteID) string {
	digest := sha256.Sum256([]byte(previous + "\x00" + causeID + "\x00" + string(route)))
	return "entry-" + hex.EncodeToString(digest[:12])
}

func windowID(entry string) string {
	digest := sha256.Sum256([]byte("input-window\x00" + entry))
	return "window-" + hex.EncodeToString(digest[:12])
}

func (effect Effect) String() string {
	parts := []string{string(effect.Kind)}
	if effect.RouteID != "" {
		parts = append(parts, "route="+string(effect.RouteID))
	}
	if effect.RouteEntryID != "" {
		parts = append(parts, "entry="+effect.RouteEntryID)
	}
	if effect.PlaybackOperationID != "" {
		parts = append(parts, "playback="+effect.PlaybackOperationID)
	}
	if effect.BindingID != "" {
		parts = append(parts, "binding="+effect.BindingID)
	}
	if effect.InputID != "" {
		parts = append(parts, "input="+effect.InputID)
	}
	if effect.ServiceID != "" {
		parts = append(parts, "service="+effect.ServiceID)
	}
	if effect.ActionID != "" {
		parts = append(parts, "action="+effect.ActionID)
	}
	if effect.Request != nil && effect.Request.TargetOpaqueID != "" {
		parts = append(parts, "target="+effect.Request.TargetOpaqueID)
	}
	if len(effect.MessageFamilies) > 0 {
		parts = append(parts, "messages="+strings.Join(effect.MessageFamilies, ","))
	}
	if effect.InputWindowID != "" {
		parts = append(parts, "window="+effect.InputWindowID)
	}
	if effect.Reason != "" {
		parts = append(parts, "reason="+effect.Reason)
	}
	return strings.Join(parts, " ")
}

func containsModality(accepted []contract.InputModality, modality contract.InputModality) bool {
	for _, candidate := range accepted {
		if candidate == modality {
			return true
		}
	}
	return false
}

func stableIdentifier(value string) bool {
	return contract.ValidateIdentifier(value) == nil
}
