package temporalruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Origens-Dev/go-ai/packages/ai"
	"github.com/Origens-Dev/gobeyond/agents"
	decisionv1 "github.com/Origens-Dev/gobeyond/agents/decisioncontract/v1"
	"github.com/Origens-Dev/gobeyond/agents/decisions"
	"github.com/Origens-Dev/gobeyond/agents/httpruntime"
	"github.com/Origens-Dev/gobeyond/agents/voicecontract"
	"go.temporal.io/sdk/client"
)

type fakeDecisionEffectAdapter struct {
	*decisionSessionAdapterFake
	reconciliations []DecisionEffectReconciliation
	reconcileErrors []error
	reconcileCalls  int
	reconciledIDs   []string
	prepareErr      error
	authorization   DecisionEffectAuthorization
	prepareCalls    int
	ceilingCalls    int
	verifyCalls     int
	casCalls        int
	casReceiptIDs   []string
	casErr          error
}

func (adapter *fakeDecisionEffectAdapter) ReconcileDecisionEffect(_ context.Context, _ httpruntime.RespondCall, effect decisions.Effect) (DecisionEffectReconciliation, error) {
	adapter.reconcileCalls++
	if effect.Request != nil {
		adapter.reconciledIDs = append(adapter.reconciledIDs, effect.Request.Identity.ID)
	}
	index := adapter.reconcileCalls - 1
	if index >= 0 && index < len(adapter.reconcileErrors) && adapter.reconcileErrors[index] != nil {
		return DecisionEffectReconciliation{}, adapter.reconcileErrors[index]
	}
	if len(adapter.reconciliations) == 0 {
		return DecisionEffectReconciliation{State: DecisionEffectNotAttempted}, nil
	}
	if index >= len(adapter.reconciliations) {
		index = len(adapter.reconciliations) - 1
	}
	return adapter.reconciliations[index], nil
}

func (adapter *fakeDecisionEffectAdapter) PrepareDecisionEffect(context.Context, httpruntime.RespondCall, DecisionSessionIdentity, decisions.Effect) (DecisionEffectAuthorization, error) {
	adapter.prepareCalls++
	return adapter.authorization, adapter.prepareErr
}

func (adapter *fakeDecisionEffectAdapter) VerifyDecisionEffectToolCeiling(context.Context, httpruntime.RespondCall, decisions.Effect, VoiceSessionExecuteToolInput) error {
	adapter.ceilingCalls++
	return nil
}

func (adapter *fakeDecisionEffectAdapter) VerifyDecisionEffectReceipt(_ context.Context, _ httpruntime.RespondCall, receipt decisionv1.EffectReceipt) error {
	adapter.verifyCalls++
	return receipt.Validate()
}

func (adapter *fakeDecisionEffectAdapter) ApplyDecisionOwnershipCAS(_ context.Context, _ httpruntime.RespondCall, request decisionv1.EffectRequest, receipt decisionv1.EffectReceipt) error {
	adapter.casCalls++
	adapter.casReceiptIDs = append(adapter.casReceiptIDs, receipt.ReceiptID)
	if adapter.casErr != nil {
		return adapter.casErr
	}
	if request.Identity != receipt.Identity || request.TargetOpaqueID != receipt.TargetOpaqueID {
		return errors.New("fake CAS received mismatched effect receipt")
	}
	return nil
}

func newDecisionEffectTestAdapter(input DecisionSessionInput) *fakeDecisionEffectAdapter {
	return &fakeDecisionEffectAdapter{decisionSessionAdapterFake: &decisionSessionAdapterFake{
		config: agents.Config{Durable: true, TaskQueue: "decision"}, definition: input.Definition,
	}}
}

func decisionEffectTestFixture(t *testing.T, sessionID string, generation uint64) (DecisionSessionInput, decisions.Effect, decisionv1.CandidateSetSnapshot) {
	t.Helper()
	input := decisionSessionInputFixture(t, sessionID, generation)
	var route decisionv1.Route
	for _, candidate := range input.Definition.Graph.Routes {
		if candidate.ID == input.Definition.Graph.Entry {
			route = candidate
			break
		}
	}
	if len(route.Act) == 0 {
		t.Fatal("synthetic entry route has no action")
	}
	action := route.Act[0]
	now := time.Now().UTC()
	snapshot := decisionv1.CandidateSetSnapshot{
		CandidateIDs: []string{"target_1"},
		ProtectedSnapshot: &decisionv1.ProtectedReference{
			ID: "target_snapshot", Purpose: "directory-snapshot", TenantID: input.Admission.TenantID,
			SessionID: input.Admission.SessionID, Generation: input.Pin.Generation,
			SHA256:    "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			ExpiresAt: now.Add(time.Hour),
		},
	}
	snapshot.SHA256 = snapshot.CanonicalSHA256()
	identity := decisionv1.EffectIdentity{
		TenantID: input.Admission.TenantID, SessionID: input.Admission.SessionID,
		Generation: input.Pin.Generation, RouteEntryID: input.Admission.RouteEntryID,
		InputID: "input_dispatch", ActionID: action.ID, GraphSHA256: input.Definition.DigestInputs.Graph,
	}
	identity.ID = identity.CanonicalID()
	request := &decisionv1.EffectRequest{
		Identity: identity, ToolID: action.ToolID, Kind: action.Kind, TargetBinding: action.TargetBinding,
		TargetOpaqueID: "target_1", CandidateSetSHA256: snapshot.SHA256,
		ReauthorizeAtExecution: action.ReauthorizeAtExecution,
	}
	effect := decisions.Effect{
		Kind: decisions.EffectDispatchIntent, RouteID: route.ID, RouteEntryID: input.Admission.RouteEntryID,
		InputID: identity.InputID, ActionID: action.ID, Request: request,
	}
	if err := request.ValidateForDispatch(input.Definition, route.ID, &snapshot, identity.TenantID, identity.SessionID, identity.Generation, now); err != nil {
		t.Fatalf("synthetic reducer intent should validate: %v", err)
	}
	return input, effect, snapshot
}

func decisionEffectTestCall(input DecisionSessionInput) httpruntime.RespondCall {
	return httpruntime.RespondCall{
		Session: agents.Session{ID: input.Admission.SessionID, AgentID: "operator"},
		Run:     agents.Run{ID: input.RunID, SessionID: input.Admission.SessionID, AgentID: "operator", Mode: agents.DurableMode},
		Actor:   agents.Actor{ID: "actor_1", Kind: "user"},
	}
}

func newDecisionEffectTestDispatcher(t *testing.T, fake *fakeClient) *Dispatcher {
	t.Helper()
	dispatcher, err := New(context.Background(), Options{Client: fake, Environment: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dispatcher.Close)
	return dispatcher
}

func TestDecisionEffectCommittedReceiptReconcilesWithoutProviderRetry(t *testing.T) {
	input, effect, _ := decisionEffectTestFixture(t, "ses_effect_committed", 101)
	receipt := decisionv1.EffectReceipt{
		Identity: effect.Request.Identity, Status: decisionv1.EffectConfirmed,
		ReceiptID: "receipt_1", ProviderRequestID: "provider_req_1", TargetOpaqueID: effect.Request.TargetOpaqueID,
		ObservedAt: time.Now().UTC(),
	}
	authority := newDecisionEffectTestAdapter(input)
	authority.reconciliations = []DecisionEffectReconciliation{{State: DecisionEffectHasReceipt, Receipt: &receipt}}
	fake := &fakeClient{}
	fake.updateFunc = func(_ context.Context, options client.UpdateWorkflowOptions) (interface{}, error) {
		switch options.UpdateName {
		case DecisionSessionSubmissionUpdate:
			return DecisionSessionAdvanceResult{Accepted: true, Snapshot: DecisionSessionSnapshot{Identity: decisionSessionIdentity(input), Status: DecisionSessionRunning}}, nil
		case DecisionSessionDispatchCommitUpdate:
			return DecisionSessionSnapshot{Identity: decisionSessionIdentity(input), Status: DecisionSessionRunning}, nil
		case DecisionSessionReceiptUpdate:
			return DecisionSessionAdvanceResult{Accepted: true, Snapshot: DecisionSessionSnapshot{Identity: decisionSessionIdentity(input), Status: DecisionSessionCompleted}}, nil
		default:
			return nil, fmt.Errorf("unexpected committed receipt update %q", options.UpdateName)
		}
	}
	dispatcher := newDecisionEffectTestDispatcher(t, fake)
	if err := dispatcher.dispatchDecisionTransition(context.Background(), authority, decisionEffectTestCall(input), DecisionSessionAdvanceResult{
		Accepted: true, Snapshot: DecisionSessionSnapshot{Identity: decisionSessionIdentity(input), Status: DecisionSessionRunning},
		Effects: []decisions.Effect{effect},
	}, nil); err != nil {
		t.Fatalf("reconcile committed receipt: %v", err)
	}
	if fake.updateCalls != 3 || fake.updateHistory[0].UpdateName != DecisionSessionSubmissionUpdate ||
		fake.updateHistory[1].UpdateName != DecisionSessionDispatchCommitUpdate ||
		fake.updateHistory[2].UpdateName != DecisionSessionReceiptUpdate {
		t.Fatalf("committed receipt recovery did not record submission before receipt: %#v", fake.updateHistory)
	}
	if authority.prepareCalls != 0 || authority.ceilingCalls != 0 || authority.verifyCalls != 1 {
		t.Fatalf("committed receipt authority calls: prepare=%d ceiling=%d verify=%d", authority.prepareCalls, authority.ceilingCalls, authority.verifyCalls)
	}
}

func TestDecisionEffectUnknownOutcomeReconcilesBeforeRetry(t *testing.T) {
	input, effect, _ := decisionEffectTestFixture(t, "ses_effect_unknown", 102)
	authority := newDecisionEffectTestAdapter(input)
	authority.reconciliations = []DecisionEffectReconciliation{{State: DecisionEffectPending}}
	fake := &fakeClient{}
	fake.updateFunc = func(_ context.Context, options client.UpdateWorkflowOptions) (interface{}, error) {
		switch options.UpdateName {
		case DecisionSessionSubmissionUpdate:
			return DecisionSessionAdvanceResult{Accepted: true, Snapshot: DecisionSessionSnapshot{Identity: decisionSessionIdentity(input), Status: DecisionSessionRunning}}, nil
		case DecisionSessionDispatchCommitUpdate:
			return DecisionSessionSnapshot{Identity: decisionSessionIdentity(input), Status: DecisionSessionRunning}, nil
		default:
			return nil, fmt.Errorf("unexpected unknown effect update %q", options.UpdateName)
		}
	}
	dispatcher := newDecisionEffectTestDispatcher(t, fake)
	emitter := &recordingDecisionEmitter{}
	if err := dispatcher.dispatchDecisionTransition(context.Background(), authority, decisionEffectTestCall(input), DecisionSessionAdvanceResult{
		Accepted: true, Snapshot: DecisionSessionSnapshot{Identity: decisionSessionIdentity(input), Status: DecisionSessionRunning},
		Effects: []decisions.Effect{effect},
	}, emitter); err != nil {
		t.Fatalf("pending effect should be held for reconciliation: %v", err)
	}
	if fake.updateCalls != 2 || fake.updateHistory[0].UpdateName != DecisionSessionSubmissionUpdate ||
		fake.updateHistory[1].UpdateName != DecisionSessionDispatchCommitUpdate || authority.prepareCalls != 0 || authority.ceilingCalls != 0 {
		t.Fatalf("unknown provider outcome was not durably held before retry: updates=%#v prepare=%d ceiling=%d", fake.updateHistory, authority.prepareCalls, authority.ceilingCalls)
	}
	if len(emitter.events) != 1 || emitter.events[0].Type != "agent.decision.effect_pending" {
		t.Fatalf("pending effect event = %#v", emitter.events)
	}
}

type syntheticDecisionWriteFixture struct {
	input             DecisionSessionInput
	effect            decisions.Effect
	candidateSnapshot decisionv1.CandidateSetSnapshot
	call              httpruntime.RespondCall
	authority         *fakeDecisionEffectAdapter
	receipt           decisionv1.EffectReceipt
	providerCalls     *int
}

func newSyntheticDecisionWriteFixture(t *testing.T, sessionID string, generation uint64, requiresApproval bool) syntheticDecisionWriteFixture {
	t.Helper()
	input, effect, candidateSnapshot := decisionEffectTestFixture(t, sessionID, generation)
	schema, outputSchema := voiceWriteClosedSchemas()
	providerCalls := 0
	tool := agents.DefineTool(agents.ToolConfig{
		Name: "synthetic_lookup", Description: "Synthetic test provider", InputSchema: schema,
		OutputSchema: outputSchema, VoiceWrite: true, RequiresApproval: requiresApproval,
	}, func(context.Context, agents.Actor, map[string]any) (any, error) {
		providerCalls++
		return map[string]any{"ok": true}, nil
	})
	voiceDefinition := agents.DefineAI(agents.AIConfig{Revision: "synthetic-revision", Tools: map[string]agents.AITool{
		"synthetic_lookup": tool,
	}})
	_, rawManifest, manifestDigest, err := voiceDefinition.CompileVoiceManifest()
	if err != nil {
		t.Fatal(err)
	}
	var manifest voicecontract.Manifest
	if err := voicecontract.Decode(rawManifest, voicecontract.MaxManifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Tools) != 1 {
		t.Fatalf("synthetic voice manifest tools = %d, want one", len(manifest.Tools))
	}
	toolSpec := manifest.Tools[0]
	registry := NewVoiceRegistry()
	registry.mu.Lock()
	registry.definitions["operator"] = voiceDefinition
	registry.manifests = map[string][]byte{"operator": rawManifest}
	registry.manifestDigests = map[string]string{"operator": manifestDigest}
	registry.mu.Unlock()
	previousRegistry := ProcessVoiceRegistry()
	RetainVoiceRegistry(registry)
	t.Cleanup(func() { RetainVoiceRegistry(previousRegistry) })
	resetVoiceWriteLedgerWithAuthority(t.TempDir(), newMemoryVoiceWriteStore())
	t.Cleanup(resetVoiceWriteLedger)

	definition := input.Definition
	for routeIndex := range definition.Graph.Routes {
		for actionIndex := range definition.Graph.Routes[routeIndex].Act {
			action := &definition.Graph.Routes[routeIndex].Act[actionIndex]
			if action.ToolID == effect.Request.ToolID {
				action.ToolID = toolSpec.ID
				action.Kind = decisionv1.EffectWrite
				action.ReleasesCallOwnership = false
			}
		}
		for transitionIndex := range definition.Graph.Routes[routeIndex].Next {
			transition := &definition.Graph.Routes[routeIndex].Next[transitionIndex]
			if transition.Source == decisionv1.SourceEffect && transition.Outcome == decisionv1.OutcomeConfirmed &&
				transition.Target.Terminal == decisionv1.TerminalReleaseCallOwnership {
				transition.Target.Terminal = decisionv1.TerminalEndSession
			}
		}
	}
	grant := decisionv1.ToolGrant{ID: toolSpec.ID, SchemaSHA256: strings.TrimPrefix(toolSpec.SchemaDigest, "sha256:")}
	if requiresApproval {
		grant.ApprovalSHA256 = strings.Repeat("d", 64)
	}
	definition.Graph.Authority.Tools = []decisionv1.ToolGrant{grant}
	frozen, _, _, err := agents.FreezeDecisionManifest(definition)
	if err != nil {
		t.Fatalf("freeze synthetic write grant: %v", err)
	}
	input.Definition = frozen
	input.Pin.SchemaVersion = frozen.SchemaVersion
	input.Pin.ReleaseSHA256 = frozen.ReleaseSHA256
	input.Pin.GraphSHA256 = frozen.DigestInputs.Graph
	input.Pin.PromptFamiliesSHA256 = frozen.DigestInputs.Prompts
	input.Pin.BindingsSHA256 = frozen.DigestInputs.Bindings
	input.Pin.NormalizationSHA256 = frozen.DigestInputs.Normalization
	input.Pin.AuthoritySHA256 = frozen.DigestInputs.Authority
	input.Pin.PolicySHA256 = frozen.DigestInputs.Policy
	input.Pin.LocaleAndVoiceSHA256 = frozen.DigestInputs.LocaleAndVoice
	if err := ValidateDecisionSessionInput(input); err != nil {
		t.Fatalf("validate synthetic write decision: %v", err)
	}
	identity := effect.Request.Identity
	identity.GraphSHA256 = frozen.DigestInputs.Graph
	identity.ID = identity.CanonicalID()
	effect.Request.Identity = identity
	effect.Request.ToolID = toolSpec.ID
	effect.Request.Kind = decisionv1.EffectWrite
	effect.Request.ReauthorizeAtExecution = true
	effect.ActionID = identity.ActionID
	toolInput, err := json.Marshal(map[string]any{"q": "synthetic lookup"})
	if err != nil {
		t.Fatal(err)
	}
	call := decisionEffectTestCall(input)
	call.Actor.Metadata = map[string]string{"network_id": "synthetic-network"}
	authorization := DecisionEffectAuthorization{
		VoiceExecutionID: "voice_execution_synthetic", CandidateSnapshot: candidateSnapshot,
		TargetOpaqueID: effect.Request.TargetOpaqueID, TargetSnapshotSHA256: effect.Request.CandidateSetSHA256,
		OriginalTargetSHA256: strings.Repeat("c", 64), CurrentTargetSHA256: strings.Repeat("c", 64),
		PermissionExpiresAt: time.Now().UTC().Add(time.Hour),
		ToolRequest: VoiceSessionExecuteToolInput{
			AgentID: call.Run.AgentID, SessionID: call.Session.ID, CallID: "synthetic-call",
			ToolCallID: identity.ID, Input: toolInput, ActorID: call.Actor.ID, ActorKind: call.Actor.Kind,
			NetworkID: "synthetic-network", ManifestDigest: manifestDigest, AgentRevision: "synthetic-revision",
			AllowedToolIDs: []string{toolSpec.ID},
			ResourceBinding: agents.ResourceBinding{Kind: "agent", ResourceID: "line_approval"},
		},
	}
	receipt := decisionv1.EffectReceipt{
		Identity: identity, Status: decisionv1.EffectConfirmed, ReceiptID: "synthetic-receipt",
		ProviderRequestID: "synthetic-provider-request", TargetOpaqueID: effect.Request.TargetOpaqueID, ObservedAt: time.Now().UTC(),
	}
	authority := newDecisionEffectTestAdapter(input)
	authority.authorization = authorization
	return syntheticDecisionWriteFixture{
		input: input, effect: effect, candidateSnapshot: candidateSnapshot, call: call,
		authority: authority, receipt: receipt, providerCalls: &providerCalls,
	}
}

func TestDecisionEffectUsesExistingVoiceWriteUpdateAndStableReceipt(t *testing.T) {
	fixture := newSyntheticDecisionWriteFixture(t, "ses_effect_voice_write", 109, false)
	input, effect, call, authority := fixture.input, fixture.effect, fixture.call, fixture.authority
	receipt, providerCalls := fixture.receipt, fixture.providerCalls
	identity := effect.Request.Identity
	authority.reconciliations = []DecisionEffectReconciliation{
		{State: DecisionEffectNotAttempted}, {State: DecisionEffectHasReceipt, Receipt: &receipt},
		{State: DecisionEffectHasReceipt, Receipt: &receipt},
	}
	fake := &fakeClient{}
	fake.updateFunc = func(ctx context.Context, options client.UpdateWorkflowOptions) (interface{}, error) {
		switch options.UpdateName {
		case DecisionSessionSubmissionUpdate:
			return DecisionSessionAdvanceResult{Accepted: true, Snapshot: DecisionSessionSnapshot{
				Identity: decisionSessionIdentity(input), Status: DecisionSessionRunning,
			}}, nil
		case DecisionSessionDispatchCommitUpdate:
			return DecisionSessionSnapshot{Identity: decisionSessionIdentity(input), Status: DecisionSessionRunning}, nil
		case VoiceSessionExecuteToolUpdate:
			if options.WorkflowID != "gobeyond-agent-run/"+call.Session.ID+"/voice_execution_synthetic" {
				return nil, errors.New("dispatcher did not target the existing voice workflow")
			}
			request, ok := options.Args[0].(VoiceSessionExecuteToolInput)
			if !ok {
				return nil, errors.New("voice update did not carry the typed voice tool request")
			}
			return VoiceSessionExecuteToolActivity(ctx, request)
		case DecisionSessionReceiptUpdate:
			return DecisionSessionAdvanceResult{Accepted: true, Snapshot: DecisionSessionSnapshot{
				Identity: decisionSessionIdentity(input), Status: DecisionSessionCompleted,
			}}, nil
		default:
			return nil, errors.New("unexpected update in synthetic decision dispatch")
		}
	}
	dispatcher := newDecisionEffectTestDispatcher(t, fake)
	transition := DecisionSessionAdvanceResult{Accepted: true,
		Snapshot: DecisionSessionSnapshot{Identity: decisionSessionIdentity(input), Status: DecisionSessionRunning},
		Effects:  []decisions.Effect{effect},
	}
	if err := dispatcher.dispatchDecisionTransition(context.Background(), authority, call, transition, &recordingDecisionEmitter{}); err != nil {
		t.Fatalf("dispatch through existing voice tool update: %v", err)
	}
	firstReceiptUpdateID := fake.updateHistory[len(fake.updateHistory)-1].UpdateID
	if err := dispatcher.dispatchDecisionTransition(context.Background(), authority, call, transition, &recordingDecisionEmitter{}); err != nil {
		t.Fatalf("reconcile committed synthetic receipt: %v", err)
	}
	voiceUpdates := 0
	for _, update := range fake.updateHistory {
		if update.UpdateName == VoiceSessionExecuteToolUpdate {
			voiceUpdates++
			if update.UpdateID != "decision-effect-"+identity.ID {
				t.Fatalf("voice tool update ID = %q, want stable effect identity", update.UpdateID)
			}
		}
	}
	if *providerCalls != 1 || voiceUpdates != 1 {
		t.Fatalf("synthetic provider calls=%d voice updates=%d; committed effect must not repeat", *providerCalls, voiceUpdates)
	}
	if fake.updateHistory[len(fake.updateHistory)-1].UpdateName != DecisionSessionReceiptUpdate ||
		fake.updateHistory[len(fake.updateHistory)-1].UpdateID != firstReceiptUpdateID {
		t.Fatalf("duplicate receipt did not reuse its durable receipt update ID: first=%q last=%#v", firstReceiptUpdateID, fake.updateHistory[len(fake.updateHistory)-1])
	}
}

func TestDecisionEffectCancelBetweenSubmissionAndCommitPreventsVoiceUpdate(t *testing.T) {
	fixture := newSyntheticDecisionWriteFixture(t, "ses_effect_cancel_race", 110, false)
	input, effect, call, authority := fixture.input, fixture.effect, fixture.call, fixture.authority
	authority.reconciliations = []DecisionEffectReconciliation{{State: DecisionEffectNotAttempted}}
	authority.cancellation = DecisionSessionCancel{Identity: decisionSessionIdentity(input)}
	pending := DecisionSessionPendingEffect{
		Identity: decisionSessionIdentity(input), Effect: &effect, Submitted: true,
		DispatchCommitted: false, Status: DecisionSessionCancelled,
		Snapshot: DecisionSessionSnapshot{Identity: decisionSessionIdentity(input), Status: DecisionSessionCancelled},
	}
	fake := &fakeClient{queryOutput: pending}
	dispatcher := newDecisionEffectTestDispatcher(t, fake)
	cancelAccepted := false
	fake.updateFunc = func(_ context.Context, options client.UpdateWorkflowOptions) (interface{}, error) {
		switch options.UpdateName {
		case DecisionSessionSubmissionUpdate:
			// Model a deterministic interleaving after the submission update has
			// committed in workflow history but before the dispatcher receives its
			// result and requests the dispatch commit.
			if err := dispatcher.Cancel(context.Background(), authority, httpruntime.CancelCall{Session: call.Session, Run: call.Run}, nil); err != nil {
				return nil, err
			}
			cancelAccepted = true
			return DecisionSessionAdvanceResult{Accepted: true, Snapshot: DecisionSessionSnapshot{
				Identity: decisionSessionIdentity(input), Status: DecisionSessionRunning,
			}}, nil
		case DecisionSessionCancelUpdate:
			return DecisionSessionSnapshot{Identity: decisionSessionIdentity(input), Status: DecisionSessionCancelled}, nil
		case DecisionSessionAbandonEffectUpdate:
			return DecisionSessionSnapshot{Identity: decisionSessionIdentity(input), Status: DecisionSessionCancelled}, nil
		case DecisionSessionDispatchCommitUpdate:
			if !cancelAccepted {
				return nil, errors.New("dispatch commit arrived before cancellation interleaving")
			}
			return nil, ErrDecisionSessionStopped
		case VoiceSessionExecuteToolUpdate:
			return nil, errors.New("voice tool update must not start after cancellation won the commit race")
		default:
			return nil, errors.New("unexpected update in cancellation race test")
		}
	}
	err := dispatcher.dispatchDecisionTransition(context.Background(), authority, call, DecisionSessionAdvanceResult{
		Accepted: true, Snapshot: DecisionSessionSnapshot{Identity: decisionSessionIdentity(input), Status: DecisionSessionRunning},
		Effects: []decisions.Effect{effect},
	}, nil)
	if err == nil || !cancelAccepted {
		t.Fatalf("dispatch error=%v cancellation accepted=%v", err, cancelAccepted)
	}
	for _, update := range fake.updateHistory {
		if update.UpdateName == VoiceSessionExecuteToolUpdate {
			t.Fatalf("provider update was sent after cancel won: %#v", update)
		}
	}
	// The transition reconciles before submission, then Cancel performs its own
	// reconciliation after observing the durable submitted state.
	if authority.reconcileCalls != 2 || len(fake.queryHistory) != 1 || fake.queryHistory[0] != DecisionSessionPendingEffectQuery {
		t.Fatalf("cancel did not reconcile the submitted effect: reconciliations=%d queries=%#v", authority.reconcileCalls, fake.queryHistory)
	}
	if len(fake.updateHistory) != 4 || fake.updateHistory[0].UpdateName != DecisionSessionSubmissionUpdate ||
		fake.updateHistory[1].UpdateName != DecisionSessionCancelUpdate || fake.updateHistory[2].UpdateName != DecisionSessionAbandonEffectUpdate ||
		fake.updateHistory[3].UpdateName != DecisionSessionDispatchCommitUpdate {
		t.Fatalf("unexpected submission/cancel/commit ordering: %#v", fake.updateHistory)
	}
}

func TestDecisionEffectCommittedBeforeCancelResumesAfterProvenNoAttempt(t *testing.T) {
	fixture := newSyntheticDecisionWriteFixture(t, "ses_effect_committed_cancel", 112, false)
	input, call, authority := fixture.input, fixture.call, fixture.authority
	effect := cloneDecisionEffect(fixture.effect)
	effect.Kind = decisions.EffectAwaitReceipt
	identity := effect.Request.Identity
	receipt := fixture.receipt
	authority.reconciliations = []DecisionEffectReconciliation{
		{State: DecisionEffectNotAttempted}, {State: DecisionEffectHasReceipt, Receipt: &receipt},
	}
	pending := DecisionSessionPendingEffect{
		Identity: decisionSessionIdentity(input), Effect: &effect, Submitted: true, DispatchCommitted: true,
		Status:   DecisionSessionCancelled,
		Snapshot: DecisionSessionSnapshot{Identity: decisionSessionIdentity(input), Status: DecisionSessionCancelled},
	}
	fake := &fakeClient{queryOutputs: map[string]interface{}{
		DecisionSessionPendingEffectQuery: pending,
	}}
	fake.updateFunc = func(ctx context.Context, options client.UpdateWorkflowOptions) (interface{}, error) {
		switch options.UpdateName {
		case VoiceSessionExecuteToolUpdate:
			request, ok := options.Args[0].(VoiceSessionExecuteToolInput)
			if !ok || request.ToolCallID != identity.ID || request.DecisionEffectIdentity == nil || *request.DecisionEffectIdentity != identity {
				return nil, errors.New("committed recovery lost the exact effect binding")
			}
			return VoiceSessionExecuteToolActivity(ctx, request)
		case DecisionSessionReceiptUpdate:
			fake.mu.Lock()
			fake.queryOutputs[DecisionSessionPendingEffectQuery] = DecisionSessionPendingEffect{
				Identity: pending.Identity, Status: DecisionSessionCompleted,
				Snapshot: DecisionSessionSnapshot{Identity: pending.Identity, Status: DecisionSessionCompleted},
			}
			fake.mu.Unlock()
			return DecisionSessionAdvanceResult{Accepted: true, Snapshot: DecisionSessionSnapshot{
				Identity: pending.Identity, Status: DecisionSessionCompleted,
			}}, nil
		default:
			return nil, fmt.Errorf("unexpected committed cancellation recovery update %q", options.UpdateName)
		}
	}
	dispatcher := newDecisionEffectTestDispatcher(t, fake)
	retry, err := dispatcher.recoverPendingDecisionEffect(context.Background(), authority, call, &recordingDecisionEmitter{})
	if err != nil || retry {
		t.Fatalf("committed cancelled no-attempt recovery = retry %v err %v", retry, err)
	}
	if *fixture.providerCalls != 1 || len(fake.updateHistory) != 2 ||
		fake.updateHistory[0].UpdateName != VoiceSessionExecuteToolUpdate ||
		fake.updateHistory[0].UpdateID != "decision-effect-"+identity.ID ||
		fake.updateHistory[1].UpdateName != DecisionSessionReceiptUpdate {
		t.Fatalf("committed recovery did not resume exactly once: provider calls=%d updates=%#v", *fixture.providerCalls, fake.updateHistory)
	}
	if authority.prepareCalls != 1 || authority.ceilingCalls != 1 {
		t.Fatalf("committed recovery skipped current authorization: prepare=%d signed ceiling=%d", authority.prepareCalls, authority.ceilingCalls)
	}
}

func TestDecisionEffectApprovalCallsExistingVoiceApprovalUpdateAndReconciles(t *testing.T) {
	for _, approved := range []bool{true, false} {
		name := "denied"
		if approved {
			name = "approved"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newSyntheticDecisionWriteFixture(t, "ses_effect_approval_"+name, 111, true)
			identity := fixture.effect.Request.Identity
			voiceRequest, err := validateDecisionEffectAuthorization(fixture.input.Definition,
				decisionSessionIdentity(fixture.input), fixture.effect, fixture.authority.authorization, fixture.call)
			if err != nil {
				t.Fatalf("prepare approved voice request: %v", err)
			}
			var input map[string]any
			if err := json.Unmarshal(voiceRequest.Input, &input); err != nil {
				t.Fatal(err)
			}
			voiceApproval, err := newVoiceSessionToolApproval(voiceRequest, ai.ToolCall{
				ToolCallID: voiceRequest.ToolCallID, ToolName: voiceRequest.ToolName, Input: input,
			})
			if err != nil {
				t.Fatalf("derive synthetic approval identity: %v", err)
			}
			voiceApproval.ExpiresAt = time.Now().UTC().Add(time.Minute)
			pendingVoiceApproval := VoiceSessionDecisionApprovalSnapshot{
				Found: true, InteractionID: voiceApproval.InteractionID, ActorID: voiceApproval.ActorID,
				ActorKind: voiceApproval.ActorKind, ToolCallID: voiceApproval.ToolCallID, ToolName: voiceApproval.ToolName,
				InputHash: voiceApproval.InputHash, ExpiresAt: voiceApproval.ExpiresAt,
				DecisionEffectIdentity: &identity,
			}
			pendingEffect := cloneDecisionEffect(fixture.effect)
			pendingEffect.Kind = decisions.EffectAwaitReceipt
			pending := DecisionSessionPendingEffect{
				Identity: decisionSessionIdentity(fixture.input), Effect: &pendingEffect, Submitted: true,
				DispatchCommitted: true, Status: DecisionSessionRunning,
				Snapshot: DecisionSessionSnapshot{Identity: decisionSessionIdentity(fixture.input), Status: DecisionSessionRunning},
			}
			receipt := fixture.receipt
			if !approved {
				receipt.Status = decisionv1.EffectFailed
				receipt.ReceiptID = "approval-denied-receipt"
				receipt.ProviderRequestID = ""
				receipt.FailureCode = "approval_denied"
			}
			receipt.ObservedAt = time.Now().UTC()
			fixture.authority.reconciliations = []DecisionEffectReconciliation{
				{State: DecisionEffectPending}, {State: DecisionEffectPending},
			}
			if approved {
				fixture.authority.reconciliations = append(fixture.authority.reconciliations,
					DecisionEffectReconciliation{State: DecisionEffectHasReceipt, Receipt: &receipt},
					DecisionEffectReconciliation{State: DecisionEffectHasReceipt, Receipt: &receipt})
			} else {
				fixture.authority.reconciliations = append(fixture.authority.reconciliations,
					DecisionEffectReconciliation{State: DecisionEffectNotAttempted},
					DecisionEffectReconciliation{State: DecisionEffectNotAttempted})
			}
			fake := &fakeClient{queryOutputs: map[string]interface{}{
				DecisionSessionPendingEffectQuery:              pending,
				VoiceSessionPendingDecisionEffectApprovalQuery: pendingVoiceApproval,
			}}
			voiceApprovalUpdates := 0
			var voiceApprovalUpdateIDs []string
			var receiptUpdateIDs []string
			fake.updateFunc = func(_ context.Context, options client.UpdateWorkflowOptions) (interface{}, error) {
				switch options.UpdateName {
				case DecisionSessionApprovalCommitUpdate:
					return DecisionSessionSnapshot{Identity: pending.Identity, Status: DecisionSessionRunning}, nil
				case VoiceSessionApproveToolUpdate:
					voiceApprovalUpdates++
					voiceApprovalUpdateIDs = append(voiceApprovalUpdateIDs, options.UpdateID)
					if options.WorkflowID != "gobeyond-agent-run/"+voiceRequest.SessionID+"/voice_execution_synthetic" {
						return nil, errors.New("approval update did not target the existing voice workflow")
					}
					response, ok := options.Args[0].(VoiceSessionApprovalResponse)
					if !ok || response.InteractionID != voiceApproval.InteractionID || response.Approved != approved ||
						response.ToolCallID != identity.ID || response.InputHash != voiceApproval.InputHash ||
						response.DecisionEffectIdentity == nil || *response.DecisionEffectIdentity != identity {
						return nil, errors.New("approval update did not preserve exact decision-effect binding")
					}
					if approved {
						return VoiceSessionExecuteToolResult{Result: json.RawMessage(`{"ok":true}`)}, nil
					}
					return VoiceSessionExecuteToolResult{Error: "tool approval denied"}, nil
				case DecisionSessionReceiptUpdate:
					receiptUpdateIDs = append(receiptUpdateIDs, options.UpdateID)
					update, ok := options.Args[0].(DecisionSessionUpdate)
					if !ok || update.Event.Normalized == nil || update.Event.Normalized.Effect == nil ||
						update.Event.Normalized.Effect.Identity != identity {
						return nil, errors.New("decision receipt update lost the exact effect receipt")
					}
					if !approved && (update.Event.Normalized.Effect.Status != decisionv1.EffectFailed ||
						update.Event.Normalized.Effect.FailureCode != "approval_denied") {
						return nil, errors.New("explicit approval denial was not recorded as a failed effect receipt")
					}
					return DecisionSessionAdvanceResult{Accepted: true, Snapshot: DecisionSessionSnapshot{
						Identity: pending.Identity, Status: DecisionSessionCompleted,
					}}, nil
				default:
					return nil, fmt.Errorf("unexpected decision approval update %q", options.UpdateName)
				}
			}
			dispatcher := newDecisionEffectTestDispatcher(t, fake)
			approvalCommit := &DecisionSessionApprovalCommit{
				Identity: pending.Identity, EffectID: identity.ID, Effect: identity,
				ApprovalID: voiceApproval.InteractionID, ToolCallID: voiceApproval.ToolCallID,
				InputHash: voiceApproval.InputHash, ActorID: voiceApproval.ActorID, ActorKind: voiceApproval.ActorKind,
				Approved: approved,
			}
			payload, err := json.Marshal(map[string]any{
				"interactionId": voiceApproval.InteractionID, "approved": approved, "toolCallId": voiceApproval.ToolCallID,
				"inputHash": voiceApproval.InputHash, "effectIdentity": identity,
			})
			if err != nil {
				t.Fatal(err)
			}
			call := fixture.call
			call.Response = payload
			if err := dispatcher.Respond(context.Background(), fixture.authority, call, &recordingDecisionEmitter{}); err != nil {
				t.Fatalf("respond to pending decision approval: %v", err)
			}
			pending.ApprovalCommit = approvalCommit
			fake.mu.Lock()
			fake.queryOutputs[DecisionSessionPendingEffectQuery] = pending
			fake.mu.Unlock()
			if err := dispatcher.Respond(context.Background(), fixture.authority, call, &recordingDecisionEmitter{}); err != nil {
				t.Fatalf("reconcile duplicate decision approval: %v", err)
			}
			if voiceApprovalUpdates < 1 || voiceApprovalUpdateIDs[0] != voiceApprovalUpdateIDs[len(voiceApprovalUpdateIDs)-1] {
				t.Fatalf("voice approval retries did not use a stable update ID: %#v", voiceApprovalUpdateIDs)
			}
			if len(receiptUpdateIDs) != 2 || receiptUpdateIDs[0] != receiptUpdateIDs[1] {
				t.Fatalf("receipt replay did not use its stable ID: %#v", receiptUpdateIDs)
			}
			if !approved && receipt.Status != decisionv1.EffectFailed {
				t.Fatalf("denied approval failed to reconcile as an authoritative failure: %#v", receipt)
			}
		})
	}
}

func TestDecisionEffectReplacementHostDoesNotRetryCancelledUnknownOutcome(t *testing.T) {
	input, intent, _ := decisionEffectTestFixture(t, "ses_effect_replacement", 108)
	awaiting := cloneDecisionEffect(intent)
	awaiting.Kind = decisions.EffectAwaitReceipt
	pending := DecisionSessionPendingEffect{
		Identity: decisionSessionIdentity(input), Effect: &awaiting, Submitted: true,
		Status:   DecisionSessionCancelled,
		Snapshot: DecisionSessionSnapshot{Identity: decisionSessionIdentity(input), Status: DecisionSessionCancelled},
	}
	authority := newDecisionEffectTestAdapter(input)
	authority.reconciliations = []DecisionEffectReconciliation{
		{State: DecisionEffectPending},
		{State: DecisionEffectNotAttempted},
	}
	fake := &fakeClient{queryOutput: pending, updateOutput: DecisionSessionSnapshot{Identity: pending.Identity, Status: DecisionSessionCancelled}}
	dispatcher := newDecisionEffectTestDispatcher(t, fake)
	emitter := &recordingDecisionEmitter{}
	call := decisionEffectTestCall(input)
	if retry, err := dispatcher.recoverPendingDecisionEffect(context.Background(), authority, call, emitter); err != nil || !retry {
		t.Fatalf("unknown cancelled effect recovery = retry %v, err %v", retry, err)
	}
	if fake.updateCalls != 0 || authority.prepareCalls != 0 {
		t.Fatalf("unknown cancelled outcome attempted a write: updates=%d prepare=%d", fake.updateCalls, authority.prepareCalls)
	}
	// A replacement host still does not retry. It closes the stranded reducer
	// intent only after the existing receipt authority proves no reservation
	// or provider outcome exists.
	if retry, err := dispatcher.recoverPendingDecisionEffect(context.Background(), authority, call, emitter); err != nil || retry {
		t.Fatalf("proven no-attempt cancellation recovery = retry %v, err %v", retry, err)
	}
	if fake.updateCalls != 1 || fake.updateOptions.UpdateName != DecisionSessionAbandonEffectUpdate || authority.prepareCalls != 0 {
		t.Fatalf("cancelled recovery update = %d %#v prepare=%d", fake.updateCalls, fake.updateOptions, authority.prepareCalls)
	}
}

func TestDecisionEffectPermissionRevocationAndTargetRemapFailClosed(t *testing.T) {
	t.Run("permission revoked", func(t *testing.T) {
		input, effect, _ := decisionEffectTestFixture(t, "ses_effect_revoked", 103)
		authority := newDecisionEffectTestAdapter(input)
		authority.reconciliations = []DecisionEffectReconciliation{{State: DecisionEffectNotAttempted}}
		authority.prepareErr = errors.New("current product permission revoked")
		fake := &fakeClient{}
		dispatcher := newDecisionEffectTestDispatcher(t, fake)
		err := dispatcher.dispatchDecisionTransition(context.Background(), authority, decisionEffectTestCall(input), DecisionSessionAdvanceResult{
			Accepted: true, Snapshot: DecisionSessionSnapshot{Identity: decisionSessionIdentity(input), Status: DecisionSessionRunning}, Effects: []decisions.Effect{effect},
		}, nil)
		if err == nil || fake.updateCalls != 0 || authority.prepareCalls != 1 {
			t.Fatalf("revoked permission path err=%v updates=%d prepare=%d", err, fake.updateCalls, authority.prepareCalls)
		}
	})

	t.Run("target changed", func(t *testing.T) {
		input, effect, snapshot := decisionEffectTestFixture(t, "ses_effect_remapped", 104)
		call := decisionEffectTestCall(input)
		authority := newDecisionEffectTestAdapter(input)
		authority.reconciliations = []DecisionEffectReconciliation{{State: DecisionEffectNotAttempted}}
		authority.authorization = DecisionEffectAuthorization{
			VoiceExecutionID: "voice_exec_1", CandidateSnapshot: snapshot,
			TargetOpaqueID: effect.Request.TargetOpaqueID, TargetSnapshotSHA256: effect.Request.CandidateSetSHA256,
			OriginalTargetSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			CurrentTargetSHA256:  "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
			PermissionExpiresAt:  time.Now().UTC().Add(time.Minute),
			ToolRequest: VoiceSessionExecuteToolInput{
				AgentID: call.Run.AgentID, SessionID: call.Session.ID, ToolCallID: effect.Request.Identity.ID,
				ActorID: call.Actor.ID, ActorKind: call.Actor.Kind,
			},
		}
		fake := &fakeClient{}
		dispatcher := newDecisionEffectTestDispatcher(t, fake)
		err := dispatcher.dispatchDecisionTransition(context.Background(), authority, call, DecisionSessionAdvanceResult{
			Accepted: true, Snapshot: DecisionSessionSnapshot{Identity: decisionSessionIdentity(input), Status: DecisionSessionRunning}, Effects: []decisions.Effect{effect},
		}, nil)
		if err == nil || fake.updateCalls != 0 || authority.ceilingCalls != 0 {
			t.Fatalf("remapped target path err=%v updates=%d ceiling=%d", err, fake.updateCalls, authority.ceilingCalls)
		}
	})
}

func TestDecisionEffectStaleGenerationAndInternalStepsNeverReachProvider(t *testing.T) {
	input, effect, _ := decisionEffectTestFixture(t, "ses_effect_stale", 105)
	stale := cloneDecisionEffect(effect)
	stale.Request.Identity.Generation++
	stale.Request.Identity.ID = stale.Request.Identity.CanonicalID()
	authority := newDecisionEffectTestAdapter(input)
	fake := &fakeClient{}
	dispatcher := newDecisionEffectTestDispatcher(t, fake)
	err := dispatcher.dispatchDecisionTransition(context.Background(), authority, decisionEffectTestCall(input), DecisionSessionAdvanceResult{
		Accepted: true, Snapshot: DecisionSessionSnapshot{Identity: decisionSessionIdentity(input), Status: DecisionSessionRunning}, Effects: []decisions.Effect{stale},
	}, nil)
	if err == nil || authority.reconcileCalls != 0 || fake.updateCalls != 0 {
		t.Fatalf("stale generation path err=%v reconcile=%d updates=%d", err, authority.reconcileCalls, fake.updateCalls)
	}
	for _, kind := range []decisions.EffectKind{decisions.EffectEnterRoute, decisions.EffectSay, decisions.EffectListen, decisions.EffectRunMatcher, decisions.EffectRunDecision} {
		if hasDecisionEffectWork([]decisions.Effect{{Kind: kind}}) {
			t.Fatalf("internal graph effect %q was treated as an external transfer/tool hop", kind)
		}
	}
}

func TestDecisionEffectCannotTargetDecisionWorkflowAsVoiceWorkflow(t *testing.T) {
	input, effect, snapshot := decisionEffectTestFixture(t, "ses_effect_workflow_ids", 109)
	call := decisionEffectTestCall(input)
	authorization := DecisionEffectAuthorization{
		VoiceExecutionID:  input.RunID,
		CandidateSnapshot: snapshot,
		TargetOpaqueID:    effect.Request.TargetOpaqueID, TargetSnapshotSHA256: effect.Request.CandidateSetSHA256,
		OriginalTargetSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		CurrentTargetSHA256:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		PermissionExpiresAt:  time.Now().UTC().Add(time.Minute),
		ToolRequest: VoiceSessionExecuteToolInput{
			AgentID: call.Run.AgentID, SessionID: call.Session.ID, ToolCallID: effect.Request.Identity.ID,
			ActorID: call.Actor.ID, ActorKind: call.Actor.Kind,
		},
	}
	if _, err := validateDecisionEffectAuthorization(input.Definition, decisionSessionIdentity(input), effect, authorization, call); err == nil {
		t.Fatal("voice tool update was allowed to target the decision workflow ID")
	}
}

func TestDecisionEffectOwnerCASUsesConfirmedReceiptAndStableAck(t *testing.T) {
	input, dispatch, _ := decisionEffectTestFixture(t, "ses_effect_owner_cas", 106)
	receipt := decisionv1.EffectReceipt{
		Identity: dispatch.Request.Identity, Status: decisionv1.EffectConfirmed,
		ReceiptID: "receipt_confirmed", ProviderRequestID: "provider_req_confirmed", TargetOpaqueID: dispatch.Request.TargetOpaqueID,
		ObservedAt: time.Now().UTC(),
	}
	effect := decisions.Effect{Kind: decisions.EffectReleaseOwnership, RouteID: dispatch.RouteID,
		RouteEntryID: dispatch.RouteEntryID, InputID: dispatch.InputID, ActionID: dispatch.ActionID,
		Request: cloneDecisionEffectRequest(dispatch.Request), ReceiptID: receipt.ReceiptID}
	authority := newDecisionEffectTestAdapter(input)
	authority.reconciliations = []DecisionEffectReconciliation{{State: DecisionEffectHasReceipt, Receipt: &receipt}}
	fake := &fakeClient{updateOutput: DecisionSessionSnapshot{Identity: decisionSessionIdentity(input), Status: DecisionSessionCompleted}}
	dispatcher := newDecisionEffectTestDispatcher(t, fake)
	transition := DecisionSessionAdvanceResult{
		Accepted: true,
		Snapshot: DecisionSessionSnapshot{Identity: decisionSessionIdentity(input), Status: DecisionSessionCompleted,
			View: decisions.View{Ownership: decisionv1.GraphOwnership{State: decisionv1.OwnershipReleased, ReleaseReceiptID: receipt.ReceiptID}}},
		Effects: []decisions.Effect{effect},
	}
	if err := dispatcher.dispatchDecisionTransition(context.Background(), authority, decisionEffectTestCall(input), transition, nil); err != nil {
		t.Fatalf("owner CAS failed: %v", err)
	}
	if authority.casCalls != 1 || authority.verifyCalls != 1 || fake.updateCalls != 1 || fake.updateOptions.UpdateName != DecisionSessionOwnershipCASUpdate {
		t.Fatalf("owner CAS recovery calls cas=%d verify=%d update=%d last=%#v", authority.casCalls, authority.verifyCalls, fake.updateCalls, fake.updateOptions)
	}
}

func TestDecisionReceiptEventIDIgnoresObservationTimeForDuplicateDelivery(t *testing.T) {
	input, effect, _ := decisionEffectTestFixture(t, "ses_effect_duplicate_receipt", 107)
	receipt := decisionv1.EffectReceipt{
		Identity: effect.Request.Identity, Status: decisionv1.EffectConfirmed,
		ReceiptID: "receipt_repeat", ProviderRequestID: "provider_req_repeat", TargetOpaqueID: effect.Request.TargetOpaqueID,
		ObservedAt: time.Now().UTC(),
	}
	first, firstID, err := decisionReceiptUpdate(httpruntime.RespondCall{
		Session: agents.Session{ID: input.Admission.SessionID}, Run: agents.Run{ID: input.RunID},
	}, decisionSessionIdentity(input), effect, receipt)
	if err != nil {
		t.Fatal(err)
	}
	receipt.ObservedAt = receipt.ObservedAt.Add(time.Minute)
	second, secondID, err := decisionReceiptUpdate(httpruntime.RespondCall{
		Session: agents.Session{ID: input.Admission.SessionID}, Run: agents.Run{ID: input.RunID},
	}, decisionSessionIdentity(input), effect, receipt)
	if err != nil {
		t.Fatal(err)
	}
	if firstID != secondID || first.Event.Normalized.ID != second.Event.Normalized.ID {
		t.Fatalf("duplicate receipt IDs diverged: %q/%q, %q/%q", firstID, secondID, first.Event.Normalized.ID, second.Event.Normalized.ID)
	}
}
