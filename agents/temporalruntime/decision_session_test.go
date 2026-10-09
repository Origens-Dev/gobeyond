package temporalruntime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Origens-Dev/gobeyond/agents"
	decisionv1 "github.com/Origens-Dev/gobeyond/agents/decisioncontract/v1"
	"github.com/Origens-Dev/gobeyond/agents/decisions"
	"github.com/Origens-Dev/gobeyond/agents/httpruntime"
	"go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

var decisionSessionTestStart = time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)

func TestDecisionSessionDispatcherStartRespondCancelAndHistoryBoundary(t *testing.T) {
	input := decisionSessionInputFixture(t, "ses_1", 7)
	input.RunID = "run_1"
	identity := decisionSessionIdentity(input)
	adapter := &decisionSessionAdapterFake{
		config:     agents.Config{Durable: true, TaskQueue: "decision"},
		definition: input.Definition,
		input:      input,
		response: DecisionSessionUpdate{
			Identity: identity, ExpectedRouteEntryID: input.Admission.RouteEntryID,
			Event: decisions.Event{Normalized: &decisionv1.NormalizedEvent{
				ID: "cancel-run_1", Kind: decisionv1.EventSpeechStarted, TenantID: input.Admission.TenantID,
				SessionID: input.Admission.SessionID, Generation: input.Pin.Generation,
				RouteID: input.Definition.Graph.Entry, RouteEntryID: input.Admission.RouteEntryID,
				InputWindowID: "window_1", Channel: decisionv1.ChannelVoice, ReceivedAt: decisionSessionTestStart.Add(time.Second),
			}},
		},
		cancellation: DecisionSessionCancel{Identity: identity},
	}
	fake := &fakeClient{run: &fakeRun{output: DecisionSessionResult{Snapshot: DecisionSessionSnapshot{Status: DecisionSessionCancelled}}}}
	dispatcher, err := New(context.Background(), Options{Client: fake, Environment: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dispatcher.Close)
	call := httpruntime.StartCall{
		Session: agents.Session{ID: "ses_1", AgentID: "operator"},
		Run:     agents.Run{ID: "run_1", SessionID: "ses_1", AgentID: "operator", Mode: agents.DurableMode},
		Input:   json.RawMessage(`{"audio":"RAW_AUDIO_SENTINEL","transcript":"RAW_TRANSCRIPT_SENTINEL","rendered":"RENDERED_PERSONAL_SENTINEL"}`),
	}
	emitter := &recordingDecisionEmitter{}
	if err := dispatcher.Start(context.Background(), adapter, call, emitter); err != nil {
		t.Fatalf("dispatcher Start: %v", err)
	}
	fake.mu.Lock()
	workflowName, workflowOptions, args := fake.workflow, fake.options, append([]interface{}(nil), fake.args...)
	fake.mu.Unlock()
	if workflowName != DecisionSessionWorkflowName {
		t.Fatalf("workflow = %v, want %q", workflowName, DecisionSessionWorkflowName)
	}
	if workflowOptions.ID != "gobeyond-agent-run/ses_1/run_1" || workflowOptions.TaskQueue != "decision__test" {
		t.Fatalf("workflow start options = %#v", workflowOptions)
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"RAW_AUDIO_SENTINEL", "RAW_TRANSCRIPT_SENTINEL", "RENDERED_PERSONAL_SENTINEL"} {
		if strings.Contains(string(encoded), raw) {
			t.Fatalf("workflow input retained %q: %s", raw, encoded)
		}
	}
	if !strings.Contains(string(encoded), input.Pin.PromptFamiliesSHA256) || !strings.Contains(string(encoded), input.Pin.SnapshotSHA256) {
		t.Fatalf("workflow input lost frozen prompt or snapshot identity: %s", encoded)
	}
	if len(emitter.events) < 2 || emitter.events[0].Type != "agent.decision.transition" || emitter.events[len(emitter.events)-1].Type != "agent.decision.completed" {
		t.Fatalf("start events = %#v", emitter.events)
	}

	fake.updateOutput = DecisionSessionAdvanceResult{Accepted: true, Snapshot: DecisionSessionSnapshot{Identity: identity}}
	if err := dispatcher.Respond(context.Background(), adapter, httpruntime.RespondCall{
		Session: call.Session, Run: call.Run, Response: json.RawMessage(`{"text":"RAW_TRANSCRIPT_SENTINEL"}`),
	}, emitter); err != nil {
		t.Fatalf("dispatcher Respond: %v", err)
	}
	fake.mu.Lock()
	respondOptions := fake.updateOptions
	fake.mu.Unlock()
	if respondOptions.UpdateName != DecisionSessionAdvanceUpdate || respondOptions.UpdateID != "advance-cancel-run_1" || len(respondOptions.Args) != 1 {
		t.Fatalf("respond update options = %#v", respondOptions)
	}
	updateJSON, err := json.Marshal(respondOptions.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(updateJSON), "RAW_TRANSCRIPT_SENTINEL") {
		t.Fatalf("respond update retained raw transcript: %s", updateJSON)
	}

	fake.updateOutput = DecisionSessionSnapshot{Identity: identity, Status: DecisionSessionCancelled}
	if err := dispatcher.Cancel(context.Background(), adapter, httpruntime.CancelCall{Session: call.Session, Run: call.Run}, emitter); err != nil {
		t.Fatalf("dispatcher Cancel: %v", err)
	}
	fake.mu.Lock()
	cancelOptions := fake.updateOptions
	fake.mu.Unlock()
	if cancelOptions.UpdateName != DecisionSessionCancelUpdate || cancelOptions.UpdateID != "cancel-run_1" {
		t.Fatalf("cancel update options = %#v", cancelOptions)
	}
	if respondOptions.UpdateID == cancelOptions.UpdateID {
		t.Fatalf("advance and cancellation update IDs collide: %#v / %#v", respondOptions, cancelOptions)
	}
	workflowID, err := WorkflowID(call.Session.ID, call.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cancelOptions.WorkflowID != workflowID {
		t.Fatalf("cancel target = %q, want session/run workflow %q", cancelOptions.WorkflowID, workflowID)
	}
	cancelArg, ok := cancelOptions.Args[0].(DecisionSessionCancel)
	if !ok || cancelArg.Identity.TenantID != input.Admission.TenantID || cancelArg.Identity.SessionID != call.Session.ID || cancelArg.Identity.RunID != call.Run.ID {
		t.Fatalf("cancel identity is not bound to the authorized tenant/session/run: %#v", cancelOptions.Args)
	}
}

func TestDecisionSessionDispatcherRejectsMismatchedCancelTargetBeforeTemporalUpdate(t *testing.T) {
	input := decisionSessionInputFixture(t, "ses_cancel", 9)
	adapter := &decisionSessionAdapterFake{
		config: agents.Config{Durable: true, TaskQueue: "decision"}, definition: input.Definition,
		cancellation: DecisionSessionCancel{Identity: decisionSessionIdentity(input)},
	}
	fake := &fakeClient{}
	dispatcher, err := New(context.Background(), Options{Client: fake})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dispatcher.Close)
	err = dispatcher.Cancel(context.Background(), adapter, httpruntime.CancelCall{
		Session: agents.Session{ID: input.Admission.SessionID, AgentID: "operator"},
		Run:     agents.Run{ID: input.RunID, SessionID: "different-session", AgentID: "operator", Mode: agents.DurableMode},
	}, &recordingDecisionEmitter{})
	if err == nil || !strings.Contains(err.Error(), "does not belong to the requested session") {
		t.Fatalf("mismatched cancellation target error = %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.updateOptions.UpdateName != "" {
		t.Fatalf("mismatched cancellation reached Temporal: %#v", fake.updateOptions)
	}
}

func TestDecisionSessionRespondMayReemitCachedIntentForReceiptDeduplication(t *testing.T) {
	input, effect, _ := decisionEffectTestFixture(t, "ses_duplicate", 13)
	identity := effect.Request.Identity
	result := DecisionSessionAdvanceResult{
		Accepted: true,
		Snapshot: DecisionSessionSnapshot{Identity: decisionSessionIdentity(input), Status: DecisionSessionRunning},
		Effects:  []decisions.Effect{effect},
	}
	base := &decisionSessionAdapterFake{
		config: agents.Config{Durable: true, TaskQueue: "decision"}, definition: input.Definition,
		response: sessionUpdate(input, input.Admission.RouteEntryID, decisions.Event{Normalized: &decisionv1.NormalizedEvent{
			ID: "retry_response", Kind: decisionv1.EventSpeechStarted,
			TenantID: input.Admission.TenantID, SessionID: input.Admission.SessionID,
			Generation: input.Pin.Generation, RouteID: input.Definition.Graph.Entry,
			RouteEntryID: input.Admission.RouteEntryID, InputWindowID: "window_duplicate",
			Channel: decisionv1.ChannelVoice, ReceivedAt: decisionSessionTestStart.Add(time.Second),
		}}),
	}
	adapter := &fakeDecisionEffectAdapter{
		decisionSessionAdapterFake: base,
		reconciliations:            []DecisionEffectReconciliation{{State: DecisionEffectPending}},
	}
	fake := &fakeClient{queryOutput: DecisionSessionPendingEffect{Identity: decisionSessionIdentity(input), Status: DecisionSessionRunning}}
	fake.updateFunc = func(_ context.Context, options client.UpdateWorkflowOptions) (interface{}, error) {
		switch options.UpdateName {
		case DecisionSessionAdvanceUpdate:
			return result, nil
		case DecisionSessionSubmissionUpdate:
			return DecisionSessionAdvanceResult{Accepted: true, Snapshot: DecisionSessionSnapshot{
				Identity: decisionSessionIdentity(input), Status: DecisionSessionRunning,
			}}, nil
		case DecisionSessionDispatchCommitUpdate:
			return DecisionSessionSnapshot{Identity: decisionSessionIdentity(input), Status: DecisionSessionRunning}, nil
		default:
			return nil, errors.New("unexpected duplicate-intent update")
		}
	}
	dispatcher, err := New(context.Background(), Options{Client: fake})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dispatcher.Close)
	emitter := &recordingDecisionEmitter{}
	call := httpruntime.RespondCall{
		Session:  agents.Session{ID: input.Admission.SessionID, AgentID: "operator"},
		Run:      agents.Run{ID: input.RunID, SessionID: input.Admission.SessionID, AgentID: "operator", Mode: agents.DurableMode},
		Response: json.RawMessage(`{"answer":"synthetic"}`),
	}
	for range 2 {
		if err := dispatcher.Respond(context.Background(), adapter, call, emitter); err != nil {
			t.Fatalf("dispatcher Respond: %v", err)
		}
	}
	fake.mu.Lock()
	updateCalls, updateOptions := fake.updateCalls, fake.updateOptions
	fake.mu.Unlock()
	emitter.mu.Lock()
	events := append([]struct {
		Type string
		Data interface{}
	}(nil), emitter.events...)
	emitter.mu.Unlock()
	if updateCalls != 6 || updateOptions.UpdateName != DecisionSessionDispatchCommitUpdate || len(events) != 4 {
		t.Fatalf("duplicate response routing = calls %d, options %#v, emitted %#v", updateCalls, updateOptions, events)
	}
	for _, event := range events {
		if event.Type == "agent.decision.effect_pending" {
			continue
		}
		advance, ok := event.Data.(DecisionSessionAdvanceResult)
		if event.Type != "agent.decision.transition" || !ok || len(advance.Effects) != 1 || advance.Effects[0].Request == nil {
			t.Fatalf("unexpected duplicate response intent: %#v", event)
		}
		if advance.Effects[0].Request.Identity.ID != identity.ID {
			t.Fatalf("effect identity changed across cached response: got %q, want %q", advance.Effects[0].Request.Identity.ID, identity.ID)
		}
	}
}

func TestDecisionSessionUnqualifiedDefinitionRequiresActivationReadyPin(t *testing.T) {
	input := decisionSessionInputFixtureWithQualification(t, "ses_unqualified", 15, false)
	err := ValidateDecisionSessionInput(input)
	if err == nil || !strings.Contains(err.Error(), "activation-ready") {
		t.Fatalf("unqualified session definition error = %v", err)
	}
}

func TestDecisionSessionExhaustedInitialRouteCompletesImmediatelyWithSafeStop(t *testing.T) {
	input := decisionSessionInputFixture(t, "ses_initial_stop", 17)
	definition := input.Definition
	bound := definition.Graph.Authority.Budgets[decisionv1.BudgetRouteVisits]
	zero := uint64(0)
	bound.Value = &zero
	definition.Graph.Authority.Budgets[decisionv1.BudgetRouteVisits] = bound
	for index := range definition.PolicyGates {
		if definition.PolicyGates[index].ID == bound.GateID {
			definition.PolicyGates[index].Value = "0"
		}
	}
	frozen, _, _, err := agents.FreezeDecisionManifest(definition)
	if err != nil {
		t.Fatalf("freeze zero-ceiling test definition: %v", err)
	}
	input.Definition = frozen
	input.Pin.ReleaseSHA256 = frozen.ReleaseSHA256
	input.Pin.GraphSHA256 = frozen.DigestInputs.Graph
	input.Pin.PromptFamiliesSHA256 = frozen.DigestInputs.Prompts
	input.Pin.BindingsSHA256 = frozen.DigestInputs.Bindings
	input.Pin.NormalizationSHA256 = frozen.DigestInputs.Normalization
	input.Pin.AuthoritySHA256 = frozen.DigestInputs.Authority
	input.Pin.PolicySHA256 = frozen.DigestInputs.Policy
	input.Pin.LocaleAndVoiceSHA256 = frozen.DigestInputs.LocaleAndVoice
	if err := ValidateDecisionSessionInput(input); err != nil {
		t.Fatalf("qualified zero-ceiling session input invalid: %v", err)
	}
	env, _ := decisionSessionTestEnvironment(input)
	env.ExecuteWorkflow(DecisionSessionWorkflow, input)
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	var result DecisionSessionResult
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatal(err)
	}
	if result.Snapshot.Status != DecisionSessionStopped || result.Snapshot.View.Phase != decisions.PhaseStopped {
		t.Fatalf("initial exhausted route status = %#v", result.Snapshot)
	}
	if len(result.InitialEffects) != 1 || result.InitialEffects[0].Kind != decisions.EffectSafeStop {
		t.Fatalf("initial exhausted route effects = %#v", result.InitialEffects)
	}
}

func TestDecisionSessionWorkflowUsesRecordedResultsAndOpaqueReferences(t *testing.T) {
	input := decisionSessionInputFixture(t, "ses_2", 11)
	env, payloads := decisionSessionTestEnvironment(input)
	var accepted, stale decisionSessionUpdateOutcome
	var cancelSnapshot DecisionSessionSnapshot
	env.RegisterDelayedCallback(func() {
		snapshot := queryDecisionSession(t, env)
		event := decisionv1.NormalizedEvent{
			ID: "input_1", Kind: decisionv1.EventInputFinal, TenantID: input.Admission.TenantID,
			SessionID: input.Admission.SessionID, Generation: input.Pin.Generation,
			RouteID: input.Definition.Graph.Entry, RouteEntryID: snapshot.View.RouteEntryID,
			InputWindowID: snapshot.View.InputWindowID, Channel: decisionv1.ChannelVoice,
			Modality: decisionv1.ModalitySpeechFinal, Locale: input.Pin.Locale,
			ProtectedInput: decisionRef("transcript_ref_1", "final-transcript", "b", input, decisionSessionTestStart.Add(time.Hour)),
			SourceEventIDs: []string{"speech_source_1"}, ReceivedAt: decisionSessionTestStart.Add(time.Second),
		}
		sendDecisionSessionUpdate(env, "input_1", sessionUpdate(input, snapshot.View.RouteEntryID, decisions.Event{Normalized: &event}), func(result decisionSessionUpdateOutcome) { accepted = result })
	}, time.Second)
	env.RegisterDelayedCallback(func() {
		snapshot := queryDecisionSession(t, env)
		event := decisionv1.NormalizedEvent{
			ID: "input_stale", Kind: decisionv1.EventInputFinal, TenantID: input.Admission.TenantID,
			SessionID: input.Admission.SessionID, Generation: input.Pin.Generation,
			RouteID: input.Definition.Graph.Entry, RouteEntryID: input.Admission.RouteEntryID,
			InputWindowID: "stale_input_window", Channel: decisionv1.ChannelVoice,
			Modality: decisionv1.ModalitySpeechFinal, Locale: input.Pin.Locale,
			ProtectedInput: decisionRef("transcript_stale", "final-transcript", "b", input, decisionSessionTestStart.Add(time.Hour)),
			SourceEventIDs: []string{"speech_stale"}, ReceivedAt: decisionSessionTestStart.Add(2 * time.Second),
		}
		sendDecisionSessionUpdate(env, "input_stale", sessionUpdate(input, snapshot.View.RouteEntryID, decisions.Event{Normalized: &event}), func(result decisionSessionUpdateOutcome) { stale = result })
	}, 2*time.Second)
	env.RegisterDelayedCallback(func() {
		cancelDecisionSessionTest(env, input, "cancel_run_2", &cancelSnapshot, t)
	}, 3*time.Second)
	env.ExecuteWorkflow(DecisionSessionWorkflow, input)
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("decision workflow: %v", err)
	}
	if !accepted.advance.Accepted || len(accepted.advance.Effects) != 1 || accepted.advance.Effects[0].Kind != decisions.EffectRefreshCandidates {
		t.Fatalf("accepted protected input = %#v", accepted)
	}
	if stale.advance.Accepted || stale.err == nil || len(stale.advance.Effects) != 0 {
		t.Fatalf("stale callback result = %#v", stale)
	}
	if cancelSnapshot.Status != DecisionSessionCancelled {
		t.Fatalf("cancel snapshot = %#v", cancelSnapshot)
	}
	serialized := strings.Join(payloads.snapshot(), "\n")
	for _, raw := range []string{"RAW_AUDIO_SENTINEL", "RAW_TRANSCRIPT_SENTINEL", "RENDERED_PERSONAL_SENTINEL"} {
		if strings.Contains(serialized, raw) {
			t.Fatalf("Temporal serialized personal sentinel %q", raw)
		}
	}
	if !strings.Contains(serialized, "transcript_ref_1") {
		t.Fatal("Temporal serialization omitted the opaque protected reference")
	}
}

func TestDecisionSessionCancelledAfterSubmissionAcceptsLateReceiptAndOwnerCAS(t *testing.T) {
	input := decisionSessionInputFixture(t, "ses_cancel_late_receipt", 12)
	env, _ := decisionSessionTestEnvironment(input)
	var intent decisions.Effect
	var submission DecisionSessionAdvanceResult
	var dispatchCommit DecisionSessionSnapshot
	var dispatchCommitErr error
	var cancelSnapshot DecisionSessionSnapshot
	var receiptAdvance DecisionSessionAdvanceResult
	var receiptErr error
	var casSnapshot DecisionSessionSnapshot
	var casErr error
	route := decisionRouteByID(t, input.Definition, input.Definition.Graph.Entry)
	candidateRef := decisionRef("snapshot_late", "directory-snapshot", "a", input, decisionSessionTestStart.Add(time.Hour))
	candidateSet := decisionv1.CandidateSetSnapshot{CandidateIDs: []string{"candidate_late"}, ProtectedSnapshot: candidateRef}
	candidateSet.SHA256 = candidateSet.CanonicalSHA256()

	env.RegisterDelayedCallback(func() {
		snapshot := queryDecisionSession(t, env)
		event := decisionv1.NormalizedEvent{
			ID: "input_late", Kind: decisionv1.EventInputFinal, TenantID: input.Admission.TenantID,
			SessionID: input.Admission.SessionID, Generation: input.Pin.Generation,
			RouteID: input.Definition.Graph.Entry, RouteEntryID: snapshot.View.RouteEntryID,
			InputWindowID: snapshot.View.InputWindowID, Channel: decisionv1.ChannelVoice,
			Modality: decisionv1.ModalitySpeechFinal, Locale: input.Pin.Locale,
			ProtectedInput: decisionRef("transcript_late", "final-transcript", "b", input, decisionSessionTestStart.Add(time.Hour)),
			SourceEventIDs: []string{"speech_late"}, ReceivedAt: decisionSessionTestStart.Add(time.Second),
		}
		sendDecisionSessionUpdate(env, "input_late", sessionUpdate(input, snapshot.View.RouteEntryID, decisions.Event{Normalized: &event}), func(result decisionSessionUpdateOutcome) {
			if !result.advance.Accepted {
				t.Errorf("late-receipt input rejected: %#v", result)
			}
		})
	}, time.Second)
	env.RegisterDelayedCallback(func() {
		snapshot := queryDecisionSession(t, env)
		refresh := decisions.SnapshotRefresh{
			ID: "snapshot_late", TenantID: input.Admission.TenantID, SessionID: input.Admission.SessionID,
			Generation: input.Pin.Generation, RouteID: input.Definition.Graph.Entry, RouteEntryID: snapshot.View.RouteEntryID,
			ReceivedAt: decisionSessionTestStart.Add(2 * time.Second),
			Bound: decisions.BoundCandidateSnapshot{BindingID: route.Match.BindingID, RouteID: input.Definition.Graph.Entry,
				RouteEntryID: snapshot.View.RouteEntryID, Snapshot: candidateSet},
		}
		update := sessionUpdate(input, snapshot.View.RouteEntryID, decisions.Event{Snapshot: &refresh})
		update.AuthorizedReferences = []decisionv1.ProtectedReference{*candidateRef}
		sendDecisionSessionUpdate(env, "snapshot_late", update, func(result decisionSessionUpdateOutcome) {
			if !result.advance.Accepted {
				t.Errorf("late-receipt snapshot rejected: %#v", result)
			}
		})
	}, 2*time.Second)
	env.RegisterDelayedCallback(func() {
		snapshot := queryDecisionSession(t, env)
		match := decisionv1.NormalizedEvent{
			ID: "match_late", Kind: decisionv1.EventMatchCompleted, TenantID: input.Admission.TenantID,
			SessionID: input.Admission.SessionID, Generation: input.Pin.Generation,
			RouteID: input.Definition.Graph.Entry, RouteEntryID: snapshot.View.RouteEntryID,
			Channel: decisionv1.ChannelVoice, ReceivedAt: decisionSessionTestStart.Add(3 * time.Second),
			Match: &decisionv1.MatchResult{Outcome: decisionv1.OutcomeCandidate, CandidateID: "candidate_late",
				SnapshotSHA256: candidateSet.SHA256, BindingID: route.Match.BindingID},
		}
		update := sessionUpdate(input, snapshot.View.RouteEntryID, decisions.Event{Normalized: &match, InputID: "input_late"})
		update.AuthorizedReferences = []decisionv1.ProtectedReference{*candidateRef}
		sendDecisionSessionUpdate(env, "match_late", update, func(result decisionSessionUpdateOutcome) {
			if !result.advance.Accepted || len(result.advance.Effects) != 1 || result.advance.Effects[0].Kind != decisions.EffectDispatchIntent {
				t.Errorf("match did not produce reducer dispatch intent: %#v", result)
				return
			}
			intent = cloneDecisionEffect(result.advance.Effects[0])
		})
	}, 3*time.Second)
	env.RegisterDelayedCallback(func() {
		if intent.Request == nil {
			t.Fatal("missing reducer dispatch request before submission")
		}
		env.UpdateWorkflow(DecisionSessionSubmissionUpdate, "submit_late_effect", &testsuite.TestUpdateCallback{
			OnReject: func(err error) { t.Errorf("effect submission rejected: %v", err) },
			OnComplete: func(value interface{}, err error) {
				if err != nil {
					t.Errorf("effect submission failed: %v", err)
					return
				}
				submission, _ = value.(DecisionSessionAdvanceResult)
			},
		}, DecisionSessionEffectSubmission{Identity: decisionSessionIdentity(input), Effect: intent})
	}, 4*time.Second)
	env.RegisterDelayedCallback(func() {
		if intent.Request == nil || !submission.Accepted {
			t.Fatal("missing submitted effect before dispatch commit")
		}
		env.UpdateWorkflow(DecisionSessionDispatchCommitUpdate, "dispatch_commit_late_effect", &testsuite.TestUpdateCallback{
			OnReject: func(err error) { dispatchCommitErr = err },
			OnComplete: func(value interface{}, err error) {
				dispatchCommitErr = err
				if err == nil {
					dispatchCommit, _ = value.(DecisionSessionSnapshot)
				}
			},
		}, DecisionSessionEffectDispatchCommit{Identity: decisionSessionIdentity(input), EffectID: intent.Request.Identity.ID})
	}, 4*time.Second+500*time.Millisecond)
	env.RegisterDelayedCallback(func() {
		cancelDecisionSessionTest(env, input, "cancel_after_submit", &cancelSnapshot, t)
	}, 5*time.Second)
	env.RegisterDelayedCallback(func() {
		receipt := decisionv1.EffectReceipt{
			Identity: intent.Request.Identity, Status: decisionv1.EffectConfirmed,
			ReceiptID: "receipt_late", ProviderRequestID: "provider_late", TargetOpaqueID: intent.Request.TargetOpaqueID,
			ObservedAt: decisionSessionTestStart.Add(6 * time.Second),
		}
		update, updateID, err := decisionReceiptUpdate(httpruntime.RespondCall{
			Session: agents.Session{ID: input.Admission.SessionID}, Run: agents.Run{ID: input.RunID},
		}, decisionSessionIdentity(input), intent, receipt)
		if err != nil {
			t.Fatal(err)
		}
		env.UpdateWorkflow(DecisionSessionReceiptUpdate, updateID, &testsuite.TestUpdateCallback{
			OnReject: func(err error) { receiptErr = err },
			OnComplete: func(value interface{}, err error) {
				receiptErr = err
				if err == nil {
					receiptAdvance, _ = value.(DecisionSessionAdvanceResult)
				}
			},
		}, update)
	}, 6*time.Second)
	env.RegisterDelayedCallback(func() {
		if len(receiptAdvance.Effects) != 1 || receiptAdvance.Effects[0].Kind != decisions.EffectReleaseOwnership {
			t.Errorf("confirmed late receipt did not request owner CAS: %#v", receiptAdvance)
			return
		}
		ack := DecisionSessionOwnershipCAS{Identity: decisionSessionIdentity(input), EffectID: intent.Request.Identity.ID, ReceiptID: "receipt_late"}
		env.UpdateWorkflow(DecisionSessionOwnershipCASUpdate, "owner_cas_late", &testsuite.TestUpdateCallback{
			OnReject: func(err error) { casErr = err },
			OnComplete: func(value interface{}, err error) {
				casErr = err
				if err == nil {
					casSnapshot, _ = value.(DecisionSessionSnapshot)
				}
			},
		}, ack)
	}, 7*time.Second)

	env.ExecuteWorkflow(DecisionSessionWorkflow, input)
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("cancelled decision workflow failed: %v", err)
	}
	var result DecisionSessionResult
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatal(err)
	}
	if dispatchCommitErr != nil || dispatchCommit.Status != DecisionSessionRunning {
		t.Fatalf("dispatch commit = %#v err=%v", dispatchCommit, dispatchCommitErr)
	}
	if cancelSnapshot.Status != DecisionSessionCancelled || submission.Snapshot.View.Phase != decisions.PhaseAwaitingReceipt {
		t.Fatalf("submission/cancellation state = %#v / %#v", submission, cancelSnapshot)
	}
	if receiptErr != nil || len(receiptAdvance.Effects) != 1 || receiptAdvance.Effects[0].Kind != decisions.EffectReleaseOwnership {
		t.Fatalf("late receipt outcome = %v, %#v", receiptErr, receiptAdvance)
	}
	if casErr != nil || casSnapshot.View.Ownership.State != decisionv1.OwnershipReleased {
		t.Fatalf("owner CAS acknowledgement = %#v / %v", casSnapshot, casErr)
	}
	if result.Snapshot.Status != DecisionSessionCancelled || result.Snapshot.View.Ownership.State != decisionv1.OwnershipReleased {
		t.Fatalf("late confirmed effect changed cancellation or lost ownership receipt: %#v", result.Snapshot)
	}
}

func TestDecisionApprovalFailureReceiptRequiresExactDurableChoice(t *testing.T) {
	input, effect, _ := decisionEffectTestFixture(t, "ses_approval_failure_receipt", 17)
	identity := decisionSessionIdentity(input)
	commit := &DecisionSessionApprovalCommit{
		Identity: identity, EffectID: effect.Request.Identity.ID, Effect: effect.Request.Identity,
		ApprovalID: "approval_1", ToolCallID: effect.Request.Identity.ID,
		InputHash: strings.Repeat("a", 64), ActorID: "actor_1", ActorKind: "user", Approved: false,
	}
	receipt := decisionv1.EffectReceipt{
		Identity: effect.Request.Identity, Status: decisionv1.EffectFailed, ReceiptID: "approval_failure_1",
		TargetOpaqueID: effect.Request.TargetOpaqueID, FailureCode: "approval_denied", ObservedAt: decisionSessionTestStart,
	}
	if err := validateDecisionApprovalFailureCommit(identity, commit, receipt); err != nil {
		t.Fatalf("exact durable denial rejected: %v", err)
	}
	if err := validateDecisionApprovalFailureCommit(identity, nil, receipt); err == nil {
		t.Fatal("approval denial receipt without a durable decision was accepted")
	}
	wrong := *commit
	wrong.EffectID = "different_effect"
	if err := validateDecisionApprovalFailureCommit(identity, &wrong, receipt); err == nil {
		t.Fatal("approval denial receipt for another effect was accepted")
	}
	approved := *commit
	approved.Approved = true
	if err := validateDecisionApprovalFailureCommit(identity, &approved, receipt); err == nil {
		t.Fatal("denial receipt contradicted the committed approval")
	}
	expired := receipt
	expired.FailureCode = "approval_expired"
	if err := validateDecisionApprovalFailureCommit(identity, &approved, expired); err != nil {
		t.Fatalf("expired receipt after an authenticated late approval choice rejected: %v", err)
	}
	unrelated := receipt
	unrelated.FailureCode = "provider_rejected"
	if err := validateDecisionApprovalFailureCommit(identity, nil, unrelated); err != nil {
		t.Fatalf("provider failure incorrectly required an approval record: %v", err)
	}
}

func TestDecisionSessionConcurrentResponsesAcceptOnlyPinnedCurrentGeneration(t *testing.T) {
	input := decisionSessionInputFixture(t, "ses_3", 19)
	env, _ := decisionSessionTestEnvironment(input)
	var first, second, replaced decisionSessionUpdateOutcome
	var cancelSnapshot DecisionSessionSnapshot
	env.RegisterDelayedCallback(func() {
		snapshot := queryDecisionSession(t, env)
		makeInput := func(id string) DecisionSessionUpdate {
			event := decisionv1.NormalizedEvent{
				ID: id, Kind: decisionv1.EventInputFinal, TenantID: input.Admission.TenantID, SessionID: input.Admission.SessionID,
				Generation: input.Pin.Generation, RouteID: input.Definition.Graph.Entry, RouteEntryID: snapshot.View.RouteEntryID,
				InputWindowID: snapshot.View.InputWindowID, Channel: decisionv1.ChannelVoice,
				Modality: decisionv1.ModalitySpeechFinal, Locale: input.Pin.Locale,
				ProtectedInput: decisionRef("input_ref_"+id, "final-transcript", "b", input, decisionSessionTestStart.Add(time.Hour)),
				SourceEventIDs: []string{"source_" + id}, ReceivedAt: decisionSessionTestStart.Add(time.Second),
			}
			return sessionUpdate(input, snapshot.View.RouteEntryID, decisions.Event{Normalized: &event})
		}
		stale := makeInput("input_replaced")
		stale.Identity.Generation++
		stale.Event.Normalized.Generation++
		remaining := 3
		done := func(target *decisionSessionUpdateOutcome) func(decisionSessionUpdateOutcome) {
			return func(result decisionSessionUpdateOutcome) {
				*target = result
				remaining--
				if remaining == 0 {
					cancelDecisionSessionTest(env, input, "cancel_run_3", &cancelSnapshot, t)
				}
			}
		}
		sendDecisionSessionUpdate(env, "input_first", makeInput("input_first"), done(&first))
		sendDecisionSessionUpdate(env, "input_second", makeInput("input_second"), done(&second))
		sendDecisionSessionUpdate(env, "input_replaced", stale, done(&replaced))
	}, time.Second)
	env.ExecuteWorkflow(DecisionSessionWorkflow, input)
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	accepted := 0
	if first.advance.Accepted {
		accepted++
	}
	if second.advance.Accepted {
		accepted++
	}
	if accepted != 1 {
		t.Fatalf("same-generation concurrent inputs accepted %d times: first=%#v second=%#v", accepted, first, second)
	}
	if replaced.advance.Accepted || replaced.err == nil || len(replaced.advance.Effects) != 0 {
		t.Fatalf("replaced-generation callback = %#v", replaced)
	}
	if cancelSnapshot.Status != DecisionSessionCancelled {
		t.Fatalf("cancel snapshot = %#v", cancelSnapshot)
	}
}

func TestDecisionSessionExpiredProtectedReferenceStopsWithoutEffects(t *testing.T) {
	input := decisionSessionInputFixture(t, "ses_4", 23)
	env, _ := decisionSessionTestEnvironment(input)
	var resultUpdate decisionSessionUpdateOutcome
	env.RegisterDelayedCallback(func() {
		snapshot := queryDecisionSession(t, env)
		event := decisionv1.NormalizedEvent{
			ID: "expired_input", Kind: decisionv1.EventInputFinal, TenantID: input.Admission.TenantID,
			SessionID: input.Admission.SessionID, Generation: input.Pin.Generation,
			RouteID: input.Definition.Graph.Entry, RouteEntryID: snapshot.View.RouteEntryID,
			InputWindowID: snapshot.View.InputWindowID, Channel: decisionv1.ChannelVoice,
			Modality: decisionv1.ModalitySpeechFinal, Locale: input.Pin.Locale,
			ProtectedInput: decisionRef("expired_transcript", "final-transcript", "b", input, decisionSessionTestStart),
			SourceEventIDs: []string{"expired_source"}, ReceivedAt: decisionSessionTestStart.Add(time.Second),
		}
		sendDecisionSessionUpdate(env, "expired_input", sessionUpdate(input, snapshot.View.RouteEntryID, decisions.Event{Normalized: &event}), func(result decisionSessionUpdateOutcome) { resultUpdate = result })
	}, time.Second)
	env.ExecuteWorkflow(DecisionSessionWorkflow, input)
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	var result DecisionSessionResult
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatal(err)
	}
	if resultUpdate.err != nil || resultUpdate.advance.Accepted || !resultUpdate.advance.Stopped || len(resultUpdate.advance.Effects) != 0 {
		t.Fatalf("expired reference update = %#v", resultUpdate)
	}
	if result.Snapshot.Status != DecisionSessionStopped {
		t.Fatalf("workflow status = %q, want stopped", result.Snapshot.Status)
	}
}

func TestDecisionSessionRejectsPinnedIdentityDrift(t *testing.T) {
	changes := []struct {
		name   string
		change func(*DecisionSessionIdentity)
	}{
		{"schema", func(i *DecisionSessionIdentity) { i.SchemaVersion = "decision.graph/v2" }},
		{"release", func(i *DecisionSessionIdentity) { i.ReleaseSHA256 = strings.Repeat("0", 64) }},
		{"graph", func(i *DecisionSessionIdentity) { i.GraphSHA256 = strings.Repeat("0", 64) }},
		{"prompt hash", func(i *DecisionSessionIdentity) { i.PromptFamiliesSHA256 = strings.Repeat("0", 64) }},
		{"snapshot", func(i *DecisionSessionIdentity) { i.SnapshotSHA256 = strings.Repeat("0", 64) }},
		{"generation", func(i *DecisionSessionIdentity) { i.Generation++ }},
		{"locale", func(i *DecisionSessionIdentity) { i.Locale = "fr" }},
		{"prompt family", func(i *DecisionSessionIdentity) { i.PromptFamily = "other.prompt" }},
		{"prompt locale", func(i *DecisionSessionIdentity) { i.PromptVariantLocale = "fr" }},
	}
	for _, change := range changes {
		t.Run(change.name, func(t *testing.T) {
			input := decisionSessionInputFixture(t, "ses_5", 29)
			env, _ := decisionSessionTestEnvironment(input)
			var outcome decisionSessionUpdateOutcome
			var cancelSnapshot DecisionSessionSnapshot
			env.RegisterDelayedCallback(func() {
				snapshot := queryDecisionSession(t, env)
				event := decisionv1.NormalizedEvent{
					ID: "identity_event", Kind: decisionv1.EventSpeechStarted,
					TenantID: input.Admission.TenantID, SessionID: input.Admission.SessionID,
					Generation: input.Pin.Generation, RouteID: input.Definition.Graph.Entry,
					RouteEntryID: snapshot.View.RouteEntryID, InputWindowID: snapshot.View.InputWindowID,
					Channel: decisionv1.ChannelVoice, ReceivedAt: decisionSessionTestStart.Add(time.Second),
				}
				update := sessionUpdate(input, snapshot.View.RouteEntryID, decisions.Event{Normalized: &event})
				change.change(&update.Identity)
				sendDecisionSessionUpdate(env, "identity_event", update, func(result decisionSessionUpdateOutcome) { outcome = result })
			}, time.Second)
			env.RegisterDelayedCallback(func() { cancelDecisionSessionTest(env, input, "cancel_identity", &cancelSnapshot, t) }, 2*time.Second)
			env.ExecuteWorkflow(DecisionSessionWorkflow, input)
			if err := env.GetWorkflowError(); err != nil {
				t.Fatal(err)
			}
			if outcome.advance.Accepted || outcome.err == nil || len(outcome.advance.Effects) != 0 {
				t.Fatalf("identity drift result = %#v", outcome)
			}
			if cancelSnapshot.Status != DecisionSessionCancelled {
				t.Fatalf("cancel snapshot = %#v", cancelSnapshot)
			}
		})
	}
}

type decisionSessionUpdateOutcome struct {
	advance  DecisionSessionAdvanceResult
	accepted bool
	err      error
}

func sendDecisionSessionUpdate(env *testsuite.TestWorkflowEnvironment, updateID string, update DecisionSessionUpdate, done func(decisionSessionUpdateOutcome)) {
	var outcome decisionSessionUpdateOutcome
	env.UpdateWorkflow(DecisionSessionAdvanceUpdate, updateID, &testsuite.TestUpdateCallback{
		OnReject: func(err error) { outcome.err = err; done(outcome) },
		OnComplete: func(value interface{}, err error) {
			if err != nil {
				outcome.err = err
			} else {
				outcome.advance, _ = value.(DecisionSessionAdvanceResult)
				outcome.accepted = outcome.advance.Accepted
			}
			done(outcome)
		},
	}, update)
}

func cancelDecisionSessionTest(env *testsuite.TestWorkflowEnvironment, input DecisionSessionInput, updateID string, snapshot *DecisionSessionSnapshot, t *testing.T) {
	t.Helper()
	env.UpdateWorkflow(DecisionSessionCancelUpdate, updateID, &testsuite.TestUpdateCallback{
		OnReject: func(err error) { t.Errorf("cancel update rejected: %v", err) },
		OnComplete: func(value interface{}, err error) {
			if err != nil {
				t.Errorf("cancel update failed: %v", err)
				return
			}
			*snapshot, _ = value.(DecisionSessionSnapshot)
		},
	}, DecisionSessionCancel{Identity: decisionSessionIdentity(input)})
}

func TestDecisionSessionUpdateFailureDoesNotEmitEffects(t *testing.T) {
	input := decisionSessionInputFixture(t, "ses_9", 43)
	identity := decisionSessionIdentity(input)
	adapter := &decisionSessionAdapterFake{
		config: agents.Config{Durable: true, TaskQueue: "decision"}, definition: input.Definition,
		response: DecisionSessionUpdate{Identity: identity, ExpectedRouteEntryID: "stale_entry", Event: decisions.Event{Normalized: &decisionv1.NormalizedEvent{
			ID: "stale_event", Kind: decisionv1.EventSpeechStarted, TenantID: input.Admission.TenantID,
			SessionID: input.Admission.SessionID, Generation: input.Pin.Generation, RouteID: input.Definition.Graph.Entry,
			RouteEntryID: input.Admission.RouteEntryID, InputWindowID: "window_stale",
			Channel: decisionv1.ChannelVoice, ReceivedAt: decisionSessionTestStart.Add(time.Second),
		}}},
	}
	fake := &fakeClient{updateErr: errors.New("update rejected")}
	dispatcher, err := New(context.Background(), Options{Client: fake})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dispatcher.Close)
	emitter := &recordingDecisionEmitter{}
	err = dispatcher.Respond(context.Background(), adapter, httpruntime.RespondCall{
		Session:  agents.Session{ID: "ses_9", AgentID: "operator"},
		Run:      agents.Run{ID: input.RunID, SessionID: "ses_9", AgentID: "operator", Mode: agents.DurableMode},
		Response: json.RawMessage(`{"text":"private"}`),
	}, emitter)
	if err == nil {
		t.Fatal("stale update unexpectedly succeeded")
	}
	if len(emitter.events) != 0 {
		t.Fatalf("stale update emitted effects: %#v", emitter.events)
	}
}

func TestDecisionSessionDispatcherRejectsRawProviderScoreTextBeforeUpdate(t *testing.T) {
	input := decisionSessionInputFixture(t, "ses_12", 59)
	identity := decisionSessionIdentity(input)
	adapter := &decisionSessionAdapterFake{
		config: agents.Config{Durable: true, TaskQueue: "decision"}, definition: input.Definition,
		response: DecisionSessionUpdate{
			Identity: identity, ExpectedRouteEntryID: input.Admission.RouteEntryID,
			Event: decisions.Event{Normalized: &decisionv1.NormalizedEvent{
				ID: "raw_score_event", Kind: decisionv1.EventDecisionCompleted,
				Decision: &decisionv1.DecisionResult{ProviderScoreFields: map[string]json.RawMessage{
					"transcript": json.RawMessage(`"RAW_TRANSCRIPT_SENTINEL"`),
				}},
			}},
		},
	}
	fake := &fakeClient{}
	dispatcher, err := New(context.Background(), Options{Client: fake})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dispatcher.Close)
	emitter := &recordingDecisionEmitter{}
	err = dispatcher.Respond(context.Background(), adapter, httpruntime.RespondCall{
		Session:  agents.Session{ID: input.Admission.SessionID, AgentID: "operator"},
		Run:      agents.Run{ID: input.RunID, SessionID: input.Admission.SessionID, AgentID: "operator", Mode: agents.DurableMode},
		Response: json.RawMessage(`{"text":"RAW_TRANSCRIPT_SENTINEL"}`),
	}, emitter)
	if err == nil || !strings.Contains(err.Error(), "must be numeric semantic data") {
		t.Fatalf("raw score response error = %v", err)
	}
	fake.mu.Lock()
	updateSent := fake.updateOptions.UpdateName != ""
	fake.mu.Unlock()
	if updateSent || len(emitter.events) != 0 {
		t.Fatalf("raw provider score reached workflow or emitter: update=%v events=%#v", updateSent, emitter.events)
	}
}

func TestDecisionSessionDispatcherRejectsMalformedProviderScoreKeyBeforeUpdate(t *testing.T) {
	input := decisionSessionInputFixture(t, "ses_bad_score_key", 60)
	const sensitiveKey = "raw transcript 555-12-3456"
	adapter := &decisionSessionAdapterFake{
		config: agents.Config{Durable: true, TaskQueue: "decision"}, definition: input.Definition,
		response: DecisionSessionUpdate{
			Identity: decisionSessionIdentity(input), ExpectedRouteEntryID: input.Admission.RouteEntryID,
			Event: decisions.Event{Normalized: &decisionv1.NormalizedEvent{
				ID: "valid_score_event", Kind: decisionv1.EventDecisionCompleted,
				TenantID: input.Admission.TenantID, SessionID: input.Admission.SessionID,
				Generation: input.Pin.Generation, RouteID: input.Definition.Graph.Entry,
				RouteEntryID: input.Admission.RouteEntryID, Channel: decisionv1.ChannelVoice,
				ReceivedAt: decisionSessionTestStart.Add(time.Second),
				Decision: &decisionv1.DecisionResult{
					Outcome: decisionv1.OutcomeNoMatch, ProviderRef: "provider_1", ModelRef: "model_1", Revision: "rev_1",
					ResultSchemaSHA256: strings.Repeat("b", 64), CandidateSetSHA256: input.Pin.SnapshotSHA256,
					ProviderScoreFields: map[string]json.RawMessage{sensitiveKey: json.RawMessage(`0.5`)},
					Usage:               decisionv1.UsageRecord{Status: decisionv1.UsageMissing},
				},
			}, InputID: "accepted_input"},
		},
	}
	fake := &fakeClient{}
	dispatcher, err := New(context.Background(), Options{Client: fake})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dispatcher.Close)
	emitter := &recordingDecisionEmitter{}
	err = dispatcher.Respond(context.Background(), adapter, httpruntime.RespondCall{
		Session:  agents.Session{ID: input.Admission.SessionID, AgentID: "operator"},
		Run:      agents.Run{ID: input.RunID, SessionID: input.Admission.SessionID, AgentID: "operator", Mode: agents.DurableMode},
		Response: json.RawMessage(`{"score":0.5}`),
	}, emitter)
	if err == nil || !strings.Contains(err.Error(), "provider score fields must have stable names") {
		t.Fatalf("malformed provider score key error = %v", err)
	}
	fake.mu.Lock()
	updateCalls, options := fake.updateCalls, fake.updateOptions
	fake.mu.Unlock()
	encoded, err := json.Marshal(options.Args)
	if err != nil {
		t.Fatal(err)
	}
	if updateCalls != 0 || options.UpdateName != "" || strings.Contains(string(encoded), sensitiveKey) || len(emitter.events) != 0 {
		t.Fatalf("malformed score key reached Temporal or emitter: calls=%d options=%#v payload=%s events=%#v", updateCalls, options, encoded, emitter.events)
	}
}

func TestDecisionSessionDispatcherRejectsMalformedEventIDBeforeUpdate(t *testing.T) {
	input := decisionSessionInputFixture(t, "ses_bad_event_id", 62)
	const sensitiveID = "raw transcript 555-12-3456"
	adapter := &decisionSessionAdapterFake{
		config: agents.Config{Durable: true, TaskQueue: "decision"}, definition: input.Definition,
		response: DecisionSessionUpdate{
			Identity: decisionSessionIdentity(input), ExpectedRouteEntryID: input.Admission.RouteEntryID,
			Event: decisions.Event{Normalized: &decisionv1.NormalizedEvent{
				ID: sensitiveID, Kind: decisionv1.EventSpeechStarted,
				TenantID: input.Admission.TenantID, SessionID: input.Admission.SessionID,
				Generation: input.Pin.Generation, RouteID: input.Definition.Graph.Entry,
				RouteEntryID: input.Admission.RouteEntryID, InputWindowID: "window_bad_event_id",
				Channel: decisionv1.ChannelVoice, ReceivedAt: decisionSessionTestStart.Add(time.Second),
			}},
		},
	}
	fake := &fakeClient{}
	dispatcher, err := New(context.Background(), Options{Client: fake})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dispatcher.Close)
	emitter := &recordingDecisionEmitter{}
	err = dispatcher.Respond(context.Background(), adapter, httpruntime.RespondCall{
		Session:  agents.Session{ID: input.Admission.SessionID, AgentID: "operator"},
		Run:      agents.Run{ID: input.RunID, SessionID: input.Admission.SessionID, AgentID: "operator", Mode: agents.DurableMode},
		Response: json.RawMessage(`{"text":"synthetic"}`),
	}, emitter)
	if err == nil || !strings.Contains(err.Error(), "event, tenant, and session IDs are required") {
		t.Fatalf("malformed event ID error = %v", err)
	}
	fake.mu.Lock()
	updateCalls, options := fake.updateCalls, fake.updateOptions
	fake.mu.Unlock()
	encoded, err := json.Marshal(options.Args)
	if err != nil {
		t.Fatal(err)
	}
	if updateCalls != 0 || options.UpdateName != "" || strings.Contains(string(encoded), sensitiveID) || len(emitter.events) != 0 {
		t.Fatalf("malformed event ID reached Temporal or emitter: calls=%d options=%#v payload=%s events=%#v", updateCalls, options, encoded, emitter.events)
	}
}

func TestDecisionSessionDispatcherRejectsInlineDTMFPINBeforeTemporalUpdate(t *testing.T) {
	input := decisionSessionInputFixture(t, "ses_pin", 61)
	const pinSentinel = "746281"
	update := DecisionSessionUpdate{
		Identity: decisionSessionIdentity(input), ExpectedRouteEntryID: input.Admission.RouteEntryID,
		Event: decisions.Event{Normalized: &decisionv1.NormalizedEvent{
			ID: "dtmf_pin", Kind: decisionv1.EventInputFinal, TenantID: input.Admission.TenantID,
			SessionID: input.Admission.SessionID, Generation: input.Pin.Generation,
			RouteID: input.Definition.Graph.Entry, RouteEntryID: input.Admission.RouteEntryID,
			InputWindowID: "window_pin", Channel: decisionv1.ChannelVoice,
			Modality: decisionv1.ModalityDTMFComplete, Digits: pinSentinel,
		}},
	}
	adapter := &decisionSessionAdapterFake{
		config: agents.Config{Durable: true, TaskQueue: "decision"}, definition: input.Definition, response: update,
	}
	fake := &fakeClient{}
	dispatcher, err := New(context.Background(), Options{Client: fake})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dispatcher.Close)
	emitter := &recordingDecisionEmitter{}
	err = dispatcher.Respond(context.Background(), adapter, httpruntime.RespondCall{
		Session:  agents.Session{ID: input.Admission.SessionID, AgentID: "operator"},
		Run:      agents.Run{ID: input.RunID, SessionID: input.Admission.SessionID, AgentID: "operator", Mode: agents.DurableMode},
		Response: json.RawMessage(`{"digits":"746281"}`),
	}, emitter)
	if err == nil || !strings.Contains(err.Error(), "inline DTMF input is unsupported") {
		t.Fatalf("inline DTMF response error = %v", err)
	}
	if err := validateDecisionSessionReferences(update, decisionSessionIdentity(input), decisionSessionTestStart); err == nil {
		t.Fatal("workflow-side validation accepted inline DTMF")
	}
	fake.mu.Lock()
	options := fake.updateOptions
	fake.mu.Unlock()
	encoded, err := json.Marshal(options.Args)
	if err != nil {
		t.Fatal(err)
	}
	if options.UpdateName != "" || strings.Contains(string(encoded), pinSentinel) || len(emitter.events) != 0 {
		t.Fatalf("inline PIN reached Temporal or emitter: update=%#v payload=%s events=%#v", options, encoded, emitter.events)
	}
}

func TestDecisionSessionPrivacyRejectsTrailingProviderScoreContentBeforeTemporalUpdate(t *testing.T) {
	input := decisionSessionInputFixture(t, "ses_score_tail", 63)
	adapter := &decisionSessionAdapterFake{
		config: agents.Config{Durable: true, TaskQueue: "decision"}, definition: input.Definition,
		response: DecisionSessionUpdate{
			Identity: decisionSessionIdentity(input), ExpectedRouteEntryID: input.Admission.RouteEntryID,
			Event: decisions.Event{Normalized: &decisionv1.NormalizedEvent{
				ID: "score_tail", Kind: decisionv1.EventDecisionCompleted,
				Decision: &decisionv1.DecisionResult{ProviderScoreFields: map[string]json.RawMessage{
					"score": json.RawMessage(`123 "RAW_TRAILING_SCORE_SENTINEL"`),
				}},
			}},
		},
	}
	fake := &fakeClient{}
	dispatcher, err := New(context.Background(), Options{Client: fake})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dispatcher.Close)
	emitter := &recordingDecisionEmitter{}
	err = dispatcher.Respond(context.Background(), adapter, httpruntime.RespondCall{
		Session:  agents.Session{ID: input.Admission.SessionID, AgentID: "operator"},
		Run:      agents.Run{ID: input.RunID, SessionID: input.Admission.SessionID, AgentID: "operator", Mode: agents.DurableMode},
		Response: json.RawMessage(`{"score":0.5}`),
	}, emitter)
	if err == nil || !strings.Contains(err.Error(), "exactly one JSON value") {
		t.Fatalf("trailing provider score content error = %v", err)
	}
	fake.mu.Lock()
	updateSent := fake.updateOptions.UpdateName != ""
	fake.mu.Unlock()
	if updateSent || len(emitter.events) != 0 {
		t.Fatalf("malformed score reached Temporal or emitter: update=%v events=%#v", updateSent, emitter.events)
	}
}

func TestDecisionSessionSDKValidatorRejectsDirectInlineDTMFBeforeAcceptance(t *testing.T) {
	input := decisionSessionInputFixture(t, "ses_direct_pin", 67)
	env, _ := decisionSessionTestEnvironment(input)
	var rejected error
	var accepted bool
	var cancelSnapshot DecisionSessionSnapshot
	env.RegisterDelayedCallback(func() {
		snapshot := queryDecisionSession(t, env)
		event := decisionv1.NormalizedEvent{
			ID: "direct_dtmf", Kind: decisionv1.EventInputFinal, TenantID: input.Admission.TenantID,
			SessionID: input.Admission.SessionID, Generation: input.Pin.Generation,
			RouteID: input.Definition.Graph.Entry, RouteEntryID: snapshot.View.RouteEntryID,
			InputWindowID: snapshot.View.InputWindowID, Channel: decisionv1.ChannelVoice,
			Modality: decisionv1.ModalityDTMFComplete, Digits: "746281",
		}
		update := sessionUpdate(input, snapshot.View.RouteEntryID, decisions.Event{Normalized: &event})
		env.UpdateWorkflow(DecisionSessionAdvanceUpdate, "direct-pin", &testsuite.TestUpdateCallback{
			OnReject: func(err error) { rejected = err },
			OnAccept: func() { accepted = true },
			OnComplete: func(_ interface{}, err error) {
				if err != nil {
					t.Errorf("unexpected completion error after acceptance: %v", err)
				}
			},
		}, update)
	}, time.Second)
	env.RegisterDelayedCallback(func() {
		cancelDecisionSessionTest(env, input, "cancel_direct_pin", &cancelSnapshot, t)
	}, 2*time.Second)
	// The direct test client serializes this request for transport. The SDK
	// validator rejects it before acceptance/history; application requests are
	// rejected earlier by dispatcher preflight. Direct clients remain trusted.
	env.ExecuteWorkflow(DecisionSessionWorkflow, input)
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if rejected == nil || accepted {
		t.Fatalf("direct Temporal update validation: rejected=%v accepted=%v", rejected, accepted)
	}
	if cancelSnapshot.Status != DecisionSessionCancelled {
		t.Fatalf("post-rejection cancellation snapshot = %#v", cancelSnapshot)
	}
}

func TestDecisionSessionSDKValidatorRejectsMalformedScoreKeysAndEventIDs(t *testing.T) {
	input := decisionSessionInputFixture(t, "ses_direct_shape", 69)
	env, _ := decisionSessionTestEnvironment(input)
	rejected := map[string]error{}
	accepted := 0
	var cancelSnapshot DecisionSessionSnapshot
	env.RegisterDelayedCallback(func() {
		snapshot := queryDecisionSession(t, env)
		baseEvent := func(id string) decisionv1.NormalizedEvent {
			return decisionv1.NormalizedEvent{
				ID: id, TenantID: input.Admission.TenantID, SessionID: input.Admission.SessionID,
				Generation: input.Pin.Generation, RouteID: input.Definition.Graph.Entry,
				RouteEntryID: snapshot.View.RouteEntryID, InputWindowID: snapshot.View.InputWindowID,
				Channel: decisionv1.ChannelVoice, ReceivedAt: decisionSessionTestStart.Add(time.Second),
			}
		}
		invalidID := baseEvent("raw transcript 555-12-3456")
		invalidID.Kind = decisionv1.EventSpeechStarted
		invalidScore := baseEvent("valid_score_event")
		invalidScore.Kind = decisionv1.EventDecisionCompleted
		invalidScore.Decision = &decisionv1.DecisionResult{
			Outcome: decisionv1.OutcomeNoMatch, ProviderRef: "provider_1", ModelRef: "model_1", Revision: "rev_1",
			ResultSchemaSHA256: strings.Repeat("b", 64), CandidateSetSHA256: input.Pin.SnapshotSHA256,
			ProviderScoreFields: map[string]json.RawMessage{
				"raw transcript 555-12-3456": json.RawMessage(`0.5`),
			}, Usage: decisionv1.UsageRecord{Status: decisionv1.UsageMissing},
		}
		send := func(key string, event decisions.Event) {
			update := sessionUpdate(input, snapshot.View.RouteEntryID, event)
			env.UpdateWorkflow(DecisionSessionAdvanceUpdate, "direct-shape-"+key, &testsuite.TestUpdateCallback{
				OnReject: func(err error) { rejected[key] = err },
				OnAccept: func() { accepted++ },
				OnComplete: func(_ interface{}, err error) {
					if err == nil {
						t.Errorf("malformed %s update completed", key)
					} else {
						t.Errorf("malformed %s update passed validation then failed in handler: %v", key, err)
					}
				},
			}, update)
		}
		send("event-id", decisions.Event{Normalized: &invalidID})
		send("score-key", decisions.Event{Normalized: &invalidScore, InputID: "accepted_input"})
	}, time.Second)
	env.RegisterDelayedCallback(func() {
		cancelDecisionSessionTest(env, input, "cancel_direct_shape", &cancelSnapshot, t)
	}, 2*time.Second)
	env.ExecuteWorkflow(DecisionSessionWorkflow, input)
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if rejected["event-id"] == nil || rejected["score-key"] == nil || accepted != 0 {
		t.Fatalf("direct SDK validation: rejected=%#v accepted=%d", rejected, accepted)
	}
	if !strings.Contains(rejected["event-id"].Error(), "event, tenant, and session IDs are required") || !strings.Contains(rejected["score-key"].Error(), "provider score fields must have stable names") {
		t.Fatalf("direct SDK validation errors = %#v", rejected)
	}
	if cancelSnapshot.Status != DecisionSessionCancelled {
		t.Fatalf("post-rejection cancellation snapshot = %#v", cancelSnapshot)
	}
}

func TestDecisionSessionCandidatePinMismatchRejected(t *testing.T) {
	input := decisionSessionInputFixture(t, "ses_10", 47)
	badRef := decisionRef("snapshot_bad", "directory-snapshot", "b", input, decisionSessionTestStart.Add(time.Hour))
	badSnapshot := decisionv1.CandidateSetSnapshot{CandidateIDs: []string{"candidate_opaque_1"}, ProtectedSnapshot: badRef}
	badSnapshot.SHA256 = badSnapshot.CanonicalSHA256()
	update := DecisionSessionUpdate{
		Identity: decisionSessionIdentity(input), ExpectedRouteEntryID: input.Admission.RouteEntryID,
		AuthorizedReferences: []decisionv1.ProtectedReference{*badRef},
		Event: decisions.Event{Snapshot: &decisions.SnapshotRefresh{
			ID: "snapshot_bad", TenantID: input.Admission.TenantID, SessionID: input.Admission.SessionID,
			Generation: input.Pin.Generation, RouteID: input.Definition.Graph.Entry, RouteEntryID: input.Admission.RouteEntryID,
			ReceivedAt: decisionSessionTestStart.Add(time.Second),
			Bound: decisions.BoundCandidateSnapshot{BindingID: decisionRouteByID(t, input.Definition, input.Definition.Graph.Entry).Match.BindingID,
				RouteID: input.Definition.Graph.Entry, RouteEntryID: input.Admission.RouteEntryID,
				Snapshot: badSnapshot},
		}},
	}
	if err := validateDecisionSessionReferences(update, decisionSessionIdentity(input), decisionSessionTestStart); err == nil || !strings.Contains(err.Error(), "pinned snapshot") {
		t.Fatalf("candidate pin mismatch error = %v", err)
	}
}

// This is a workflow test with previously recorded outcomes, not a captured-
// history WorkflowReplayer test.
func TestDecisionSessionRecordedOutcomesRunWithoutProviderOrDirectoryRequery(t *testing.T) {
	input := decisionSessionInputFixture(t, "ses_8", 41)
	env, payloads := decisionSessionTestEnvironment(input)
	outcomes := map[string]decisionSessionUpdateOutcome{}
	var cancelSnapshot DecisionSessionSnapshot
	candidateRef := decisionRef("snapshot_ref_replay", "directory-snapshot", "a", input, decisionSessionTestStart.Add(time.Hour))
	candidateSet := decisionv1.CandidateSetSnapshot{CandidateIDs: []string{"candidate_opaque_1"}, ProtectedSnapshot: candidateRef}
	candidateSet.SHA256 = candidateSet.CanonicalSHA256()

	env.RegisterDelayedCallback(func() {
		snapshot := queryDecisionSession(t, env)
		event := decisionv1.NormalizedEvent{
			ID: "input_replay", Kind: decisionv1.EventInputFinal, TenantID: input.Admission.TenantID,
			SessionID: input.Admission.SessionID, Generation: input.Pin.Generation,
			RouteID: input.Definition.Graph.Entry, RouteEntryID: snapshot.View.RouteEntryID,
			InputWindowID: snapshot.View.InputWindowID, Channel: decisionv1.ChannelVoice,
			Modality: decisionv1.ModalitySpeechFinal, Locale: input.Pin.Locale,
			ProtectedInput: decisionRef("transcript_replay", "final-transcript", "b", input, decisionSessionTestStart.Add(time.Hour)),
			SourceEventIDs: []string{"source_replay"}, ReceivedAt: decisionSessionTestStart.Add(time.Second),
		}
		sendDecisionSessionUpdate(env, "input_replay", sessionUpdate(input, snapshot.View.RouteEntryID, decisions.Event{Normalized: &event}), func(result decisionSessionUpdateOutcome) { outcomes["input"] = result })
	}, time.Second)
	env.RegisterDelayedCallback(func() {
		snapshot := queryDecisionSession(t, env)
		refresh := decisions.SnapshotRefresh{
			ID: "snapshot_replay", TenantID: input.Admission.TenantID, SessionID: input.Admission.SessionID,
			Generation: input.Pin.Generation, RouteID: input.Definition.Graph.Entry, RouteEntryID: snapshot.View.RouteEntryID,
			ReceivedAt: decisionSessionTestStart.Add(2 * time.Second),
			Bound: decisions.BoundCandidateSnapshot{
				BindingID: decisionRouteByID(t, input.Definition, input.Definition.Graph.Entry).Match.BindingID,
				RouteID:   input.Definition.Graph.Entry, RouteEntryID: snapshot.View.RouteEntryID,
				Snapshot: candidateSet,
			},
		}
		update := sessionUpdate(input, snapshot.View.RouteEntryID, decisions.Event{Snapshot: &refresh})
		update.AuthorizedReferences = []decisionv1.ProtectedReference{*candidateRef}
		sendDecisionSessionUpdate(env, "snapshot_replay", update, func(result decisionSessionUpdateOutcome) { outcomes["snapshot"] = result })
	}, 2*time.Second)
	env.RegisterDelayedCallback(func() {
		snapshot := queryDecisionSession(t, env)
		match := decisionv1.NormalizedEvent{
			ID: "match_replay", Kind: decisionv1.EventMatchCompleted, TenantID: input.Admission.TenantID,
			SessionID: input.Admission.SessionID, Generation: input.Pin.Generation,
			RouteID: input.Definition.Graph.Entry, RouteEntryID: snapshot.View.RouteEntryID,
			Channel: decisionv1.ChannelVoice, ReceivedAt: decisionSessionTestStart.Add(3 * time.Second),
			Match: &decisionv1.MatchResult{
				Outcome: decisionv1.OutcomeNoMatch, SnapshotSHA256: candidateSet.SHA256,
				BindingID: decisionRouteByID(t, input.Definition, input.Definition.Graph.Entry).Match.BindingID,
			},
		}
		update := sessionUpdate(input, snapshot.View.RouteEntryID, decisions.Event{Normalized: &match, InputID: "input_replay"})
		update.AuthorizedReferences = []decisionv1.ProtectedReference{*candidateRef}
		sendDecisionSessionUpdate(env, "match_replay", update, func(result decisionSessionUpdateOutcome) { outcomes["match"] = result })
	}, 3*time.Second)
	env.RegisterDelayedCallback(func() {
		snapshot := queryDecisionSession(t, env)
		recorded := &decisionv1.DecisionResult{
			Outcome: decisionv1.OutcomeNoMatch, ProviderRef: "recorded-provider", ModelRef: "recorded-model",
			Revision: "recorded-revision", ResultSchemaSHA256: strings.Repeat("d", 64),
			CandidateSetSHA256: candidateSet.SHA256, Usage: decisionv1.UsageRecord{Status: decisionv1.UsageMissing},
		}
		decision := decisionv1.NormalizedEvent{
			ID: "decision_replay", Kind: decisionv1.EventDecisionCompleted, TenantID: input.Admission.TenantID,
			SessionID: input.Admission.SessionID, Generation: input.Pin.Generation,
			RouteID: input.Definition.Graph.Entry, RouteEntryID: snapshot.View.RouteEntryID,
			Channel: decisionv1.ChannelVoice, ReceivedAt: decisionSessionTestStart.Add(4 * time.Second), Decision: recorded,
		}
		update := sessionUpdate(input, snapshot.View.RouteEntryID, decisions.Event{Normalized: &decision, InputID: "input_replay"})
		update.AuthorizedReferences = []decisionv1.ProtectedReference{*candidateRef}
		sendDecisionSessionUpdate(env, "decision_replay", update, func(result decisionSessionUpdateOutcome) { outcomes["decision"] = result })
	}, 4*time.Second)
	env.RegisterDelayedCallback(func() {
		cancelDecisionSessionTest(env, input, "cancel_replay", &cancelSnapshot, t)
	}, 5*time.Second)

	env.ExecuteWorkflow(DecisionSessionWorkflow, input)
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("decision replay: %v", err)
	}
	var result DecisionSessionResult
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"input", "snapshot", "match", "decision"} {
		if !outcomes[name].advance.Accepted {
			t.Fatalf("%s replay update rejected: %#v", name, outcomes[name])
		}
	}
	if outcomes["decision"].advance.Snapshot.View.DecisionCalls != 1 || outcomes["decision"].advance.Snapshot.View.RouteID != "/clarify" {
		t.Fatalf("recorded decision was not applied by replay: %#v", outcomes["decision"])
	}
	if result.Snapshot.Status != DecisionSessionCancelled || cancelSnapshot.Status != DecisionSessionCancelled {
		t.Fatalf("replay cancellation result = %#v / %#v", cancelSnapshot, result.Snapshot)
	}
	serialized := strings.Join(payloads.snapshot(), "\n")
	for _, raw := range []string{"RAW_AUDIO_SENTINEL", "RAW_TRANSCRIPT_SENTINEL", "RENDERED_PERSONAL_SENTINEL"} {
		if strings.Contains(serialized, raw) {
			t.Fatalf("Temporal replay serialized personal sentinel %q", raw)
		}
	}
	if !strings.Contains(serialized, "snapshot_ref_replay") || !strings.Contains(serialized, "recorded-revision") {
		t.Fatal("Temporal replay history omitted recorded decision data or the authorized opaque reference")
	}
	// No activities are registered in this environment, so any provider or directory requery would fail the workflow.
}

func decisionSessionInputFixture(t *testing.T, sessionID string, generation uint64) DecisionSessionInput {
	return decisionSessionInputFixtureWithQualification(t, sessionID, generation, true)
}

func decisionSessionInputFixtureWithQualification(t *testing.T, sessionID string, generation uint64, qualified bool) DecisionSessionInput {
	t.Helper()
	data, err := os.ReadFile("../decisioncontract/v1/testdata/review-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var definition decisionv1.Definition
	if err := json.Unmarshal(data, &definition); err != nil {
		t.Fatal(err)
	}
	if qualified {
		qualifyDecisionSessionDefinition(&definition)
	}
	frozen, _, _, err := agents.FreezeDecisionManifest(definition)
	if err != nil {
		t.Fatalf("freeze synthetic decision definition: %v", err)
	}
	voice := decisionv1.VoiceCapabilities{
		ProfileRef: "qualified-profile", Revision: "profile-revision-1", VoiceID: "voice-1", Locale: "en",
		SupportedLocales: []string{"en"}, Formats: []decisionv1.AudioFormat{{Codec: "pcm", SampleRateHz: 16000, Channels: 1}},
		Capabilities: map[decisionv1.VoiceCapabilityName]decisionv1.CapabilityStatus{}, EvidenceRef: "synthetic-test-only",
	}
	for _, capability := range frozen.Graph.RequiredVoiceCapabilities {
		voice.Capabilities[capability] = decisionv1.CapabilitySupported
	}
	voiceDigest, err := decisionv1.CanonicalVoiceCapabilitiesSHA256(voice)
	if err != nil {
		t.Fatal(err)
	}
	pin := decisionv1.SessionPin{
		SchemaVersion: frozen.SchemaVersion, ReleaseSHA256: frozen.ReleaseSHA256,
		GraphSHA256: frozen.DigestInputs.Graph, PromptFamiliesSHA256: frozen.DigestInputs.Prompts,
		BindingsSHA256: frozen.DigestInputs.Bindings, NormalizationSHA256: frozen.DigestInputs.Normalization,
		AuthoritySHA256: frozen.DigestInputs.Authority, PolicySHA256: frozen.DigestInputs.Policy,
		LocaleAndVoiceSHA256: frozen.DigestInputs.LocaleAndVoice, SnapshotSHA256: strings.Repeat("a", 64),
		VoiceSHA256: voiceDigest, Generation: generation, Locale: "en", PromptFamily: "operator.prompt",
		PromptVariantLocale: "en", Voice: voice,
	}
	input := DecisionSessionInput{Definition: frozen, Pin: pin, RunID: "run_" + sessionID, Admission: decisionv1.NormalizedEvent{
		ID: "admission_1", Kind: decisionv1.EventSessionAdmitted, TenantID: "tenant_1", SessionID: sessionID,
		Generation: generation, RouteID: frozen.Graph.Entry, RouteEntryID: "entry_1",
		Channel: decisionv1.ChannelVoice, Locale: "en", ReceivedAt: decisionSessionTestStart,
	}}
	if qualified {
		if err := ValidateDecisionSessionInput(input); err != nil {
			t.Fatalf("synthetic decision session input invalid: %v", err)
		}
	}
	return input
}

func qualifyDecisionSessionDefinition(definition *decisionv1.Definition) {
	const ceiling = uint64(100)
	budgetGates := map[string]bool{}
	for dimension, bound := range definition.Graph.Authority.Budgets {
		bound.Value = new(uint64)
		*bound.Value = ceiling
		budgetGates[bound.GateID] = true
		definition.Graph.Authority.Budgets[dimension] = bound
	}
	for index := range definition.Graph.RetryGroups {
		group := &definition.Graph.RetryGroups[index]
		for _, bound := range []*decisionv1.Bound{&group.MaxReprompts, &group.MaxNoInputReprompts, &group.MaxNoMatchReprompts, &group.MaxAmbiguousReprompts} {
			bound.Value = new(uint64)
			*bound.Value = ceiling
			budgetGates[bound.GateID] = true
		}
	}
	for index := range definition.PolicyGates {
		gate := &definition.PolicyGates[index]
		gate.Status = decisionv1.GateQualified
		gate.EvidenceRef = "synthetic-test-only"
		gate.UnresolvedReason = ""
		switch {
		case budgetGates[gate.ID]:
			gate.Value = strconv.FormatUint(ceiling, 10)
		case gate.Kind == decisionv1.GateLocaleProfile:
			gate.Value = "en"
		case gate.Kind == decisionv1.GateVoiceProfile:
			gate.Value = "qualified-profile@profile-revision-1"
		default:
			gate.Value = "synthetic-test-only"
		}
	}
	definition.Graph.Locale.EnabledLocales = []string{"en"}
}

func decisionRef(id, purpose, digestChar string, input DecisionSessionInput, expiry time.Time) *decisionv1.ProtectedReference {
	return &decisionv1.ProtectedReference{ID: id, Purpose: purpose, TenantID: input.Admission.TenantID,
		SessionID: input.Admission.SessionID, Generation: input.Pin.Generation,
		SHA256: strings.Repeat(digestChar, 64), ExpiresAt: expiry}
}

func sessionUpdate(input DecisionSessionInput, routeEntry string, event decisions.Event) DecisionSessionUpdate {
	return DecisionSessionUpdate{Identity: decisionSessionIdentity(input), ExpectedRouteEntryID: routeEntry, Event: event}
}

func decisionRouteByID(t *testing.T, definition decisionv1.Definition, id decisionv1.RouteID) decisionv1.Route {
	t.Helper()
	for _, route := range definition.Graph.Routes {
		if route.ID == id {
			return route
		}
	}
	t.Fatalf("route %q missing", id)
	return decisionv1.Route{}
}

type decisionSessionAdapterFake struct {
	config       agents.Config
	definition   decisionv1.Definition
	input        DecisionSessionInput
	response     DecisionSessionUpdate
	cancellation DecisionSessionCancel
}

func (adapter *decisionSessionAdapterFake) Config() agents.Config { return adapter.config }
func (adapter *decisionSessionAdapterFake) DecisionDefinition() decisionv1.Definition {
	return adapter.definition
}
func (*decisionSessionAdapterFake) Start(context.Context, httpruntime.StartCall, httpruntime.EventEmitter) error {
	return nil
}
func (*decisionSessionAdapterFake) Respond(context.Context, httpruntime.RespondCall, httpruntime.EventEmitter) error {
	return nil
}
func (*decisionSessionAdapterFake) Cancel(context.Context, httpruntime.CancelCall, httpruntime.EventEmitter) error {
	return nil
}
func (adapter *decisionSessionAdapterFake) PrepareDecisionSession(httpruntime.StartCall) (DecisionSessionInput, error) {
	return adapter.input, nil
}
func (adapter *decisionSessionAdapterFake) PrepareDecisionResponse(httpruntime.RespondCall) (DecisionSessionUpdate, error) {
	return adapter.response, nil
}
func (adapter *decisionSessionAdapterFake) PrepareDecisionCancellation(httpruntime.CancelCall) (DecisionSessionCancel, error) {
	return adapter.cancellation, nil
}

var _ httpruntime.DecisionAdapter = (*decisionSessionAdapterFake)(nil)
var _ DecisionSessionAdapter = (*decisionSessionAdapterFake)(nil)

type recordingDecisionEmitter struct {
	mu     sync.Mutex
	events []struct {
		Type string
		Data interface{}
	}
}

func (emitter *recordingDecisionEmitter) Emit(_ context.Context, eventType string, data interface{}) error {
	emitter.mu.Lock()
	defer emitter.mu.Unlock()
	emitter.events = append(emitter.events, struct {
		Type string
		Data interface{}
	}{Type: eventType, Data: data})
	return nil
}

type decisionSessionCaptureConverter struct {
	inner converter.DataConverter
	mu    sync.Mutex
	data  []string
}

func (capture *decisionSessionCaptureConverter) record(payload *common.Payload) {
	if payload == nil {
		return
	}
	capture.mu.Lock()
	capture.data = append(capture.data, string(payload.Data))
	capture.mu.Unlock()
}
func (capture *decisionSessionCaptureConverter) ToPayload(value interface{}) (*common.Payload, error) {
	payload, err := capture.inner.ToPayload(value)
	if err == nil {
		capture.record(payload)
	}
	return payload, err
}
func (capture *decisionSessionCaptureConverter) FromPayload(payload *common.Payload, value interface{}) error {
	return capture.inner.FromPayload(payload, value)
}
func (capture *decisionSessionCaptureConverter) ToPayloads(values ...interface{}) (*common.Payloads, error) {
	payloads, err := capture.inner.ToPayloads(values...)
	if err == nil && payloads != nil {
		for _, payload := range payloads.Payloads {
			capture.record(payload)
		}
	}
	return payloads, err
}
func (capture *decisionSessionCaptureConverter) FromPayloads(payloads *common.Payloads, values ...interface{}) error {
	return capture.inner.FromPayloads(payloads, values...)
}
func (capture *decisionSessionCaptureConverter) ToString(payload *common.Payload) string {
	return capture.inner.ToString(payload)
}
func (capture *decisionSessionCaptureConverter) ToStrings(payloads *common.Payloads) []string {
	return capture.inner.ToStrings(payloads)
}
func (capture *decisionSessionCaptureConverter) snapshot() []string {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return append([]string(nil), capture.data...)
}

var _ converter.DataConverter = (*decisionSessionCaptureConverter)(nil)

func queryDecisionSession(t *testing.T, env *testsuite.TestWorkflowEnvironment) DecisionSessionSnapshot {
	t.Helper()
	value, err := env.QueryWorkflow(DecisionSessionSnapshotQuery)
	if err != nil {
		t.Fatalf("query decision workflow: %v", err)
	}
	var snapshot DecisionSessionSnapshot
	if err := value.Get(&snapshot); err != nil {
		t.Fatalf("decode decision workflow query: %v", err)
	}
	return snapshot
}

func decisionSessionTestEnvironment(input DecisionSessionInput) (*testsuite.TestWorkflowEnvironment, *decisionSessionCaptureConverter) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.SetStartTime(decisionSessionTestStart)
	capture := &decisionSessionCaptureConverter{inner: converter.GetDefaultDataConverter()}
	env.SetDataConverter(capture)
	env.RegisterWorkflowWithOptions(DecisionSessionWorkflow, workflow.RegisterOptions{Name: DecisionSessionWorkflowName})
	return env, capture
}
