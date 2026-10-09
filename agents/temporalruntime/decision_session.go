package temporalruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	DecisionSessionWorkflowName  = "gobeyond.agents.decision_session.v1"
	DecisionSessionAdvanceUpdate = "gobeyond.agents.decision_session.advance.v1"
	DecisionSessionCancelUpdate  = "gobeyond.agents.decision_session.cancel.v1"
	DecisionSessionSnapshotQuery = "gobeyond.agents.decision_session.snapshot.v1"
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
}

// DecisionSessionIdentity is echoed on callbacks so a replaced or stale host
// cannot move a workflow pinned to another graph, snapshot, locale, prompt, or
// ownership generation.
type DecisionSessionIdentity struct {
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
// this workflow validates only their scope and explicit expiry.
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
type DecisionSessionAdvanceResult struct {
	Accepted bool                    `json:"accepted"`
	Stopped  bool                    `json:"stopped,omitempty"`
	Snapshot DecisionSessionSnapshot `json:"snapshot"`
	Effects  []decisions.Effect      `json:"effects,omitempty"`
}

type DecisionSessionResult struct {
	Snapshot DecisionSessionSnapshot `json:"snapshot"`
}

// DecisionSessionAdapter is an opt-in, dormant adapter extension. Its methods
// run in the host process and must return only normalized semantic data and
// authorized opaque references. This package does not supply an implementation
// that resolves owner CAS, protected-payload storage, prompt hydration, or
// provider execution.
type DecisionSessionAdapter interface {
	httpruntime.DecisionAdapter
	PrepareDecisionSession(httpruntime.StartCall) (DecisionSessionInput, error)
	PrepareDecisionResponse(httpruntime.RespondCall) (DecisionSessionUpdate, error)
	PrepareDecisionCancellation(httpruntime.CancelCall) (DecisionSessionCancel, error)
}

type decisionSessionWorkflowState struct {
	identity DecisionSessionIdentity
	reducer  decisions.State
	status   DecisionSessionStatus
}

// DecisionSessionWorkflow applies only deterministic reducer transitions.
// It has no activities and makes no provider, directory, cache, media, storage,
// or external-write calls. Temporal history therefore contains semantic
// events and opaque references, never hydrated personal payloads.
func DecisionSessionWorkflow(ctx workflow.Context, input DecisionSessionInput) (DecisionSessionResult, error) {
	if err := ValidateDecisionSessionInput(input); err != nil {
		return DecisionSessionResult{}, err
	}
	state, _, err := decisions.Start(input.Definition, input.Admission, input.Policy)
	if err != nil {
		return DecisionSessionResult{}, fmt.Errorf("start decision reducer: %w", err)
	}
	session := &decisionSessionWorkflowState{
		identity: decisionSessionIdentity(input.Pin),
		reducer:  state,
		status:   DecisionSessionRunning,
	}
	if err := workflow.SetQueryHandler(ctx, DecisionSessionSnapshotQuery, func() (DecisionSessionSnapshot, error) {
		return session.snapshot(), nil
	}); err != nil {
		return DecisionSessionResult{}, fmt.Errorf("register decision session query: %w", err)
	}
	if err := workflow.SetUpdateHandler(ctx, DecisionSessionAdvanceUpdate,
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
			if nextView.Phase == decisions.PhaseStopped {
				session.status = DecisionSessionStopped
			} else if nextView.GraphEnded || nextView.SessionEnded || nextView.Phase == decisions.PhaseTerminal {
				session.status = DecisionSessionCompleted
			}
			return DecisionSessionAdvanceResult{
				Accepted: true, Snapshot: session.snapshot(), Effects: effects,
			}, nil
		}); err != nil {
		return DecisionSessionResult{}, fmt.Errorf("register decision session update: %w", err)
	}
	if err := workflow.SetUpdateHandler(ctx, DecisionSessionCancelUpdate,
		func(_ workflow.Context, cancel DecisionSessionCancel) (DecisionSessionSnapshot, error) {
			if cancel.Identity != session.identity {
				return DecisionSessionSnapshot{}, errors.New("decision cancellation identity does not match the pinned session")
			}
			if session.status != DecisionSessionRunning {
				return DecisionSessionSnapshot{}, ErrDecisionSessionStopped
			}
			session.status = DecisionSessionCancelled
			return session.snapshot(), nil
		}); err != nil {
		return DecisionSessionResult{}, fmt.Errorf("register decision session cancellation: %w", err)
	}
	if err := workflow.Await(ctx, func() bool { return session.status != DecisionSessionRunning }); err != nil {
		return DecisionSessionResult{}, err
	}
	return DecisionSessionResult{Snapshot: session.snapshot()}, nil
}

func (session *decisionSessionWorkflowState) snapshot() DecisionSessionSnapshot {
	return DecisionSessionSnapshot{Identity: session.identity, View: session.reducer.View(), Status: session.status}
}

// ValidateDecisionSessionInput verifies the frozen manifest/pin and binds the
// admission event to that pin before the data can enter Temporal history.
func ValidateDecisionSessionInput(input DecisionSessionInput) error {
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

func decisionSessionIdentity(pin decisionv1.SessionPin) DecisionSessionIdentity {
	return DecisionSessionIdentity{
		SchemaVersion: pin.SchemaVersion, ReleaseSHA256: pin.ReleaseSHA256,
		GraphSHA256: pin.GraphSHA256, PromptFamiliesSHA256: pin.PromptFamiliesSHA256,
		SnapshotSHA256: pin.SnapshotSHA256, Generation: pin.Generation,
		Locale: pin.Locale, PromptFamily: pin.PromptFamily,
		PromptVariantLocale: pin.PromptVariantLocale,
	}
}

var errDecisionReferenceExpired = errors.New("decision protected reference expired")

func validateDecisionSessionReferences(update DecisionSessionUpdate, identity DecisionSessionIdentity, now time.Time) error {
	if err := validateDecisionSessionEventPrivacy(update.Event); err != nil {
		return err
	}
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

func validateDecisionSessionEventPrivacy(event decisions.Event) error {
	if event.Normalized == nil || event.Normalized.Decision == nil {
		return nil
	}
	for name, raw := range event.Normalized.Decision.ProviderScoreFields {
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return fmt.Errorf("provider score field %q is invalid", name)
		}
		if _, ok := value.(json.Number); !ok {
			return fmt.Errorf("provider score field %q must be numeric semantic data", name)
		}
	}
	return nil
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

func decisionSessionWorkflowID(call httpruntime.StartCall) (string, error) {
	return WorkflowID(call.Session.ID, call.Run.ID)
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
	return id, nil
}
