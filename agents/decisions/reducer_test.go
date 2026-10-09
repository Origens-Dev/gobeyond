package decisions

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	contract "github.com/Origens-Dev/gobeyond/agents/decisioncontract/v1"
)

var traceTime = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

func TestGoldenTextTraces(t *testing.T) {
	definition := traceDefinition(t)
	t.Run("no input bypasses Jev", func(t *testing.T) {
		state, effects, err := Start(definition, admissionEvent("entry-no-input"), Policy{})
		if err != nil {
			t.Fatal(err)
		}
		event := normalizedFor(state, "silence-1", contract.EventInitialSilence)
		event.InputWindowID = state.View().InputWindowID
		state, next, err := Reduce(state, Event{Normalized: &event})
		if err != nil {
			t.Fatal(err)
		}
		if state.View().DecisionCalls != 0 || state.View().RouteID != "/clarify" {
			t.Fatalf("no-input did not bypass Jev into clarification: %#v", state.View())
		}
		goldenTrace(t, "no-input-bypass.trace", append(effects, next...))
	})

	t.Run("deterministic to Jev to clarify", func(t *testing.T) {
		state, effects, snapshot := startWithSnapshot(t, definition, "entry-jev", []string{"opaque-a", "opaque-b"})
		trace := append([]Effect(nil), effects...)
		state, effects = acceptTextInput(t, state, "input-jev", "payload-jev")
		trace = append(trace, effects...)
		match := matchEvent(state, "match-jev", snapshot, contract.OutcomeNoMatch, "")
		state, effects = reduceEvent(t, state, Event{Normalized: &match, InputID: "input-jev"})
		trace = append(trace, effects...)
		decision := decisionEvent(state, "decision-jev", snapshot, contract.OutcomeAmbiguous, "")
		state, effects = reduceEvent(t, state, Event{Normalized: &decision, InputID: "input-jev"})
		trace = append(trace, effects...)
		if state.View().RouteID != "/clarify" || retryCount(state, "recipient_selection").AmbiguousReprompts != 1 {
			t.Fatalf("ambiguous result did not admit one bounded clarification: %#v", state.View())
		}
		goldenTrace(t, "jev-clarify.trace", trace)
	})

	t.Run("confirmed receipt gates ownership release", func(t *testing.T) {
		state, effects, snapshot := startWithSnapshot(t, definition, "entry-receipt", []string{"opaque-a"})
		trace := append([]Effect(nil), effects...)
		state, effects = acceptTextInput(t, state, "input-receipt", "payload-receipt")
		trace = append(trace, effects...)
		match := matchEvent(state, "match-receipt", snapshot, contract.OutcomeCandidate, "opaque-a")
		state, effects = reduceEvent(t, state, Event{Normalized: &match, InputID: "input-receipt"})
		trace = append(trace, effects...)
		intent := effects[0]
		if err := state.ValidateDispatchIntent(intent, traceTime.Add(3*time.Second)); err != nil {
			t.Fatalf("trusted frozen action did not validate: %v", err)
		}
		forged := intent
		forged.RouteID = "/help"
		if err := state.ValidateDispatchIntent(forged, traceTime.Add(3*time.Second)); err == nil {
			t.Fatal("caller-supplied route ID authorized dispatch")
		}
		forged = intent
		forged.ActionID = "caller-action"
		if err := state.ValidateDispatchIntent(forged, traceTime.Add(3*time.Second)); err == nil {
			t.Fatal("caller-supplied action authorized dispatch")
		}

		submitted := normalizedFor(state, "submitted-receipt", contract.EventEffectSubmitted)
		submitted.EffectRequest = cloneRequest(intent.Request)
		state, effects = reduceEvent(t, state, Event{Normalized: &submitted})
		trace = append(trace, effects...)
		accepted := receiptEvent(state, "accepted-receipt", intent.Request, contract.EffectAccepted, "receipt-accepted", "opaque-a")
		state, effects = reduceEvent(t, state, Event{Normalized: &accepted})
		trace = append(trace, effects...)
		if state.View().Ownership.State != contract.OwnershipReceiptPending {
			t.Fatal("accepted receipt released call ownership")
		}
		confirmed := receiptEvent(state, "confirmed-receipt", intent.Request, contract.EffectConfirmed, "receipt-confirmed", "opaque-a")
		state, effects = reduceEvent(t, state, Event{Normalized: &confirmed})
		trace = append(trace, effects...)
		if state.View().Ownership.State != contract.OwnershipReleased || !state.View().GraphEnded {
			t.Fatalf("matching confirmed receipt did not release graph ownership: %#v", state.View())
		}
		goldenTrace(t, "receipt-gated-release.trace", trace)
	})

	t.Run("bounded full-agent fallback", func(t *testing.T) {
		limit := uint64(1)
		policy := Policy{FullAgentFallbackLimit: &limit}
		state, effects, snapshot := startWithSnapshotPolicy(t, definition, "entry-full-agent", []string{"opaque-a"}, policy)
		trace := append([]Effect(nil), effects...)
		state, effects = acceptTextInput(t, state, "input-full-agent-1", "payload-full-agent-1")
		trace = append(trace, effects...)
		match := matchEvent(state, "match-full-agent-1", snapshot, contract.OutcomeNoMatch, "")
		state, effects = reduceEvent(t, state, Event{Normalized: &match, InputID: "input-full-agent-1"})
		trace = append(trace, effects...)
		decision := decisionEvent(state, "decision-full-agent-1", snapshot, contract.OutcomeUnavailable, "")
		state, effects = reduceEvent(t, state, Event{Normalized: &decision, InputID: "input-full-agent-1"})
		trace = append(trace, effects...)
		if !hasEffect(effects, EffectFullAgentFallback) || state.View().FullAgentFallbacks != 1 {
			t.Fatalf("first fallback was not bounded by explicit policy: %#v %#v", state.View(), effects)
		}
		full := FullAgentResult{ID: "full-agent-result-1", TenantID: "tenant-1", SessionID: "session-1", Generation: 7, RouteID: state.View().RouteID, RouteEntryID: state.View().RouteEntryID, InputID: "input-full-agent-1", Outcome: contract.OutcomeNoMatch, ReceivedAt: traceTime.Add(4 * time.Second)}
		state, effects = reduceEvent(t, state, Event{Fallback: &full})
		trace = append(trace, effects...)
		state, effects, snapshot = refreshAfterRouteEntry(t, state, []string{"opaque-a"})
		trace = append(trace, effects...)
		state, effects = acceptTextInput(t, state, "input-full-agent-2", "payload-full-agent-2")
		trace = append(trace, effects...)
		match = matchEvent(state, "match-full-agent-2", snapshot, contract.OutcomeNoMatch, "")
		state, effects = reduceEvent(t, state, Event{Normalized: &match, InputID: "input-full-agent-2"})
		trace = append(trace, effects...)
		decision = decisionEvent(state, "decision-full-agent-2", snapshot, contract.OutcomeUnavailable, "")
		state, effects = reduceEvent(t, state, Event{Normalized: &decision, InputID: "input-full-agent-2"})
		trace = append(trace, effects...)
		if hasEffect(effects, EffectFullAgentFallback) || state.View().FullAgentFallbacks != 1 || state.View().RouteID != "/help" {
			t.Fatalf("full-agent fallback exceeded its session limit: %#v %#v", state.View(), effects)
		}
		goldenTrace(t, "bounded-full-agent.trace", trace)
	})
}

func TestPartialSTTAndStaleGenerationOrEntryCannotCreateEffects(t *testing.T) {
	definition := traceDefinition(t)
	state, _, err := Start(definition, admissionEvent("entry-stale"), Policy{})
	if err != nil {
		t.Fatal(err)
	}
	partial := normalizedFor(state, "speech-started", contract.EventSpeechStarted)
	partial.InputWindowID = state.View().InputWindowID
	state, effects, err := Reduce(state, Event{Normalized: &partial})
	if err != nil {
		t.Fatal(err)
	}
	if len(effects) != 0 || state.View().Phase != PhaseListening || !state.View().InputWindowOpen {
		t.Fatalf("partial STT caused work: %#v %#v", state.View(), effects)
	}
	oldGeneration := normalizedFor(state, "old-generation", contract.EventInitialSilence)
	oldGeneration.Generation--
	oldGeneration.InputWindowID = state.View().InputWindowID
	if _, _, err := Reduce(state, Event{Normalized: &oldGeneration}); !errors.Is(err, ErrStaleEvent) {
		t.Fatalf("stale generation was accepted: %v", err)
	}
	silence := normalizedFor(state, "silence-stale", contract.EventInitialSilence)
	silence.InputWindowID = state.View().InputWindowID
	state, _, err = Reduce(state, Event{Normalized: &silence})
	if err != nil {
		t.Fatal(err)
	}
	oldEntryInput := normalizedFor(state, "old-entry-input", contract.EventInputFinal)
	oldEntryInput.RouteID, oldEntryInput.RouteEntryID, oldEntryInput.InputWindowID = "/start", "entry-stale", "old-window"
	oldEntryInput.Modality, oldEntryInput.Locale, oldEntryInput.SourceEventIDs = contract.ModalityText, "en", []string{"source-old-entry"}
	ref := protectedReference("old-entry-payload", "final-input", 7, "f")
	oldEntryInput.ProtectedInput = &ref
	if _, _, err := Reduce(state, Event{Normalized: &oldEntryInput}); !errors.Is(err, ErrStaleEvent) {
		t.Fatalf("stale route-entry input was accepted: %v", err)
	}
}

func TestFirstFinalInputClosesWindowAndStaleResultsAreRejected(t *testing.T) {
	definition := traceDefinition(t)
	state, _, snapshot := startWithSnapshot(t, definition, "entry-stale-result", []string{"opaque-a"})
	state, _ = acceptTextInput(t, state, "input-stale-result", "payload-stale-result")
	duplicate := normalizedFor(state, "input-stale-result-duplicate", contract.EventInputFinal)
	duplicate.InputWindowID, duplicate.Modality, duplicate.Locale = state.View().InputWindowID, contract.ModalityText, "en"
	duplicate.SourceEventIDs = []string{"source-duplicate"}
	ref := protectedReference("payload-duplicate", "final-input", 7, "f")
	duplicate.ProtectedInput = &ref
	if _, _, err := Reduce(state, Event{Normalized: &duplicate}); err == nil {
		t.Fatal("second final input was accepted after the window closed")
	}
	match := matchEvent(state, "match-stale-entry", snapshot, contract.OutcomeNoMatch, "")
	match.RouteEntryID = "entry-old-generation"
	if _, _, err := Reduce(state, Event{Normalized: &match, InputID: "input-stale-result"}); !errors.Is(err, ErrStaleEvent) {
		t.Fatalf("stale matcher result was accepted: %v", err)
	}
	match = matchEvent(state, "match-stale-result", snapshot, contract.OutcomeNoMatch, "")
	state, _, err := Reduce(state, Event{Normalized: &match, InputID: "input-stale-result"})
	if err != nil {
		t.Fatal(err)
	}
	decision := decisionEvent(state, "decision-stale-generation", snapshot, contract.OutcomeAmbiguous, "")
	decision.Generation--
	if _, _, err := Reduce(state, Event{Normalized: &decision, InputID: "input-stale-result"}); !errors.Is(err, ErrStaleEvent) {
		t.Fatalf("stale Jev result generation was accepted: %v", err)
	}
}

func TestCandidateSnapshotsRequireTrustedActiveBindingAndEntry(t *testing.T) {
	definition := traceDefinition(t)
	state, _, err := Start(definition, admissionEvent("entry-bound"), Policy{})
	if err != nil {
		t.Fatal(err)
	}
	state, _ = acceptTextInput(t, state, "input-bound", "payload-bound")
	refresh := snapshotRefresh(state, "snapshot-wrong-binding", []string{"opaque-a"})
	refresh.Bound.BindingID = "untrusted-binding"
	if _, _, err := Reduce(state, Event{Snapshot: &refresh}); err == nil || !strings.Contains(err.Error(), "trusted active binding") {
		t.Fatalf("wrong binding was accepted: %v", err)
	}
	refresh = snapshotRefresh(state, "snapshot-wrong-entry", []string{"opaque-a"})
	refresh.Bound.RouteEntryID = "caller-entry"
	if _, _, err := Reduce(state, Event{Snapshot: &refresh}); err == nil || !strings.Contains(err.Error(), "trusted active binding") {
		t.Fatalf("wrong route entry was accepted: %v", err)
	}
	refresh = snapshotRefresh(state, "snapshot-correct", []string{"opaque-a"})
	state, effects, err := Reduce(state, Event{Snapshot: &refresh})
	if err != nil {
		t.Fatal(err)
	}
	if len(effects) != 1 || effects[0].Kind != EffectRunMatcher || effects[0].BindingID != "recipient_matcher" {
		t.Fatalf("correct snapshot did not resume matcher: %#v", effects)
	}
	match := matchEvent(state, "match-unauthorized-candidate", refresh.Bound, contract.OutcomeCandidate, "outside-snapshot")
	if _, _, err := Reduce(state, Event{Normalized: &match, InputID: "input-bound"}); err == nil {
		t.Fatal("matcher nominated a candidate outside its bound active snapshot")
	}
}

func TestRetryStateSurvivesReplayReconnectAndSnapshotRefresh(t *testing.T) {
	definition := traceDefinition(t)
	state, _, err := Start(definition, admissionEvent("entry-retry"), Policy{})
	if err != nil {
		t.Fatal(err)
	}
	silence := normalizedFor(state, "silence-retry", contract.EventInitialSilence)
	silence.InputWindowID = state.View().InputWindowID
	state, _, err = Reduce(state, Event{Normalized: &silence})
	if err != nil {
		t.Fatal(err)
	}
	if state.View().RouteID != "/clarify" || retryCount(state, "recipient_selection").NoInputReprompts != 1 {
		t.Fatalf("retry was not admitted: %#v", state.View())
	}
	reconnected := state
	reconnected, effects, err := Reduce(reconnected, Event{Normalized: &silence})
	if err != nil {
		t.Fatal(err)
	}
	if len(effects) != 0 || retryCount(reconnected, "recipient_selection").NoInputReprompts != 1 {
		t.Fatalf("replay counted retry twice: %#v", reconnected.View())
	}
	refresh := snapshotRefresh(reconnected, "clarify-snapshot", []string{"opaque-a"})
	reconnected, effects, err = Reduce(reconnected, Event{Snapshot: &refresh})
	if err != nil {
		t.Fatal(err)
	}
	if len(effects) != 0 || reconnected.View().RouteVisits != 2 || retryCount(reconnected, "recipient_selection").NoInputReprompts != 1 {
		t.Fatalf("snapshot refresh reset route/retry counters: %#v %#v", reconnected.View(), effects)
	}
	silence = normalizedFor(reconnected, "silence-retry-exhausted", contract.EventInitialSilence)
	silence.InputWindowID = reconnected.View().InputWindowID
	reconnected, effects, err = Reduce(reconnected, Event{Normalized: &silence})
	if err != nil {
		t.Fatal(err)
	}
	if reconnected.View().RouteID != "/help" || retryCount(reconnected, "recipient_selection").NoInputReprompts != 1 || reconnected.View().RouteVisits != 3 {
		t.Fatalf("exhausted retry looped or lost its session counter: %#v %#v", reconnected.View(), effects)
	}
}

func TestRepeatHelpCancelAndRefusalErrorFallbacks(t *testing.T) {
	definition := traceDefinition(t)
	for _, tc := range []struct {
		name      string
		command   ControlCommand
		wantRoute contract.RouteID
		wantEnded bool
	}{
		{name: "repeat", command: ControlRepeat, wantRoute: "/start"},
		{name: "help", command: ControlHelp, wantRoute: "/help"},
		{name: "cancel", command: ControlCancel, wantRoute: "/start", wantEnded: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, _, snapshot := startWithSnapshot(t, definition, "entry-"+tc.name, []string{"opaque-a"})
			state, _ = acceptTextInput(t, state, "input-"+tc.name, "payload-"+tc.name)
			view := state.View()
			control := ControlResult{ID: "control-" + tc.name, TenantID: view.TenantID, SessionID: view.SessionID, Generation: view.Generation, RouteID: view.RouteID, RouteEntryID: view.RouteEntryID, InputID: "input-" + tc.name, BindingID: snapshot.BindingID, SnapshotSHA256: snapshot.Snapshot.SHA256, Command: tc.command, ReceivedAt: traceTime.Add(3 * time.Second)}
			state, _, err := Reduce(state, Event{Control: &control})
			if err != nil {
				t.Fatal(err)
			}
			if state.View().RouteID != tc.wantRoute || state.View().SessionEnded != tc.wantEnded {
				t.Fatalf("unexpected control transition: %#v", state.View())
			}
		})
	}
	for _, outcome := range []contract.Outcome{contract.OutcomeRefusal, contract.OutcomeError, contract.OutcomeUnavailable} {
		t.Run(string(outcome), func(t *testing.T) {
			state, _, snapshot := startWithSnapshot(t, definition, "entry-"+string(outcome), []string{"opaque-a"})
			state, _ = acceptTextInput(t, state, "input-"+string(outcome), "payload-"+string(outcome))
			match := matchEvent(state, "match-"+string(outcome), snapshot, contract.OutcomeNoMatch, "")
			state, _, err := Reduce(state, Event{Normalized: &match, InputID: "input-" + string(outcome)})
			if err != nil {
				t.Fatal(err)
			}
			decision := decisionEvent(state, "decision-"+string(outcome), snapshot, outcome, "")
			state, effects, err := Reduce(state, Event{Normalized: &decision, InputID: "input-" + string(outcome)})
			if err != nil {
				t.Fatal(err)
			}
			if state.View().RouteID != "/help" || hasEffect(effects, EffectFullAgentFallback) {
				t.Fatalf("refusal/error did not use the frozen fallback: %#v %#v", state.View(), effects)
			}
		})
	}
}

func TestFailedEffectReceiptDoesNotReleaseOwnership(t *testing.T) {
	definition := traceDefinition(t)
	state, _, snapshot := startWithSnapshot(t, definition, "entry-effect-failure", []string{"opaque-a"})
	state, _ = acceptTextInput(t, state, "input-effect-failure", "payload-effect-failure")
	match := matchEvent(state, "match-effect-failure", snapshot, contract.OutcomeCandidate, "opaque-a")
	state, effects, err := Reduce(state, Event{Normalized: &match, InputID: "input-effect-failure"})
	if err != nil {
		t.Fatal(err)
	}
	request := effects[0].Request
	submitted := normalizedFor(state, "submitted-effect-failure", contract.EventEffectSubmitted)
	submitted.EffectRequest = cloneRequest(request)
	state, _, err = Reduce(state, Event{Normalized: &submitted})
	if err != nil {
		t.Fatal(err)
	}
	failed := receiptEvent(state, "failed-effect-receipt", request, contract.EffectFailed, "receipt-failed", "opaque-a")
	failed.Effect.FailureCode = "synthetic-failure"
	state, effects, err = Reduce(state, Event{Normalized: &failed})
	if err != nil {
		t.Fatal(err)
	}
	if state.View().Ownership.State != contract.OwnershipOwned || hasEffect(effects, EffectReleaseOwnership) {
		t.Fatalf("failed receipt released ownership: %#v %#v", state.View(), effects)
	}
}

func TestUnknownReceiptWaitsForReconciliationWithoutRetryOrRelease(t *testing.T) {
	definition := traceDefinition(t)
	state, _, snapshot := startWithSnapshot(t, definition, "entry-unknown", []string{"opaque-a"})
	state, _ = acceptTextInput(t, state, "input-unknown", "payload-unknown")
	match := matchEvent(state, "match-unknown", snapshot, contract.OutcomeCandidate, "opaque-a")
	state, effects, err := Reduce(state, Event{Normalized: &match, InputID: "input-unknown"})
	if err != nil {
		t.Fatal(err)
	}
	request := effects[0].Request
	submitted := normalizedFor(state, "submitted-unknown", contract.EventEffectSubmitted)
	submitted.EffectRequest = cloneRequest(request)
	state, _, err = Reduce(state, Event{Normalized: &submitted})
	if err != nil {
		t.Fatal(err)
	}
	unknown := receiptEvent(state, "unknown-receipt", request, contract.EffectUnknown, "", "opaque-a")
	state, effects, err = Reduce(state, Event{Normalized: &unknown})
	if err != nil {
		t.Fatal(err)
	}
	if state.View().Ownership.State != contract.OwnershipReceiptPending || state.View().Phase != PhaseAwaitingReceipt || len(effects) != 1 || effects[0].Kind != EffectAwaitReceipt {
		t.Fatalf("unknown outcome was retried or released: %#v %#v", state.View(), effects)
	}
}

func TestLocalGraphEndDoesNotReleaseCallOwnership(t *testing.T) {
	definition := traceDefinition(t)
	state, _, err := Start(definition, admissionEvent("entry-local-end"), Policy{})
	if err != nil {
		t.Fatal(err)
	}
	effects := enterTerminal(state.data, contract.Target{Terminal: contract.TerminalEndGraph}, "end-event", "local_end")
	if !state.data.graphEnded || state.data.ownership.State != contract.OwnershipOwned || hasEffect(effects, EffectReleaseOwnership) {
		t.Fatalf("local graph termination changed call ownership: %#v %#v", state.View(), effects)
	}
}

func TestRunTextTraceUsesSemanticEventsWithoutMediaAcknowledgments(t *testing.T) {
	definition := traceDefinition(t)
	admission := admissionEvent("entry-text-harness")
	initial, _, err := Start(definition, admission, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	silence := normalizedFor(initial, "silence-text-harness", contract.EventInitialSilence)
	silence.InputWindowID = initial.View().InputWindowID
	state, trace, err := RunTextTrace(definition, admission, Policy{}, Event{Normalized: &silence})
	if err != nil {
		t.Fatal(err)
	}
	if state.View().RouteID != "/clarify" {
		t.Fatalf("text harness used the wrong route: %#v", state.View())
	}
	if strings.Contains(trace, "playback_completed") || strings.Contains(trace, "dtmf_ack") || strings.Contains(trace, "keypress_ack") {
		t.Fatalf("text harness fabricated a transport acknowledgment: %s", trace)
	}
	if strings.Contains(trace, "jev service=") {
		t.Fatalf("initial silence incorrectly invoked Jev: %s", trace)
	}
}

func TestRepeatHelpCancelAndDecisionFallbackRoutes(t *testing.T) {
	definition := traceDefinition(t)
	for _, tc := range []struct {
		name    string
		command ControlCommand
		route   contract.RouteID
		ended   bool
	}{
		{"repeat", ControlRepeat, "/start", false}, {"help", ControlHelp, "/help", false}, {"cancel", ControlCancel, "/start", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, _, snapshot := startWithSnapshot(t, definition, "entry-"+tc.name, []string{"opaque-a"})
			state, _ = acceptTextInput(t, state, "input-"+tc.name, "payload-"+tc.name)
			view := state.View()
			result := ControlResult{ID: "control-" + tc.name, TenantID: view.TenantID, SessionID: view.SessionID, Generation: view.Generation, RouteID: view.RouteID, RouteEntryID: view.RouteEntryID, InputID: "input-" + tc.name, BindingID: snapshot.BindingID, SnapshotSHA256: snapshot.Snapshot.SHA256, Command: tc.command, ReceivedAt: traceTime.Add(3 * time.Second)}
			state, _, err := Reduce(state, Event{Control: &result})
			if err != nil {
				t.Fatal(err)
			}
			if state.View().RouteID != tc.route || state.View().SessionEnded != tc.ended {
				t.Fatalf("wrong control behavior: %#v", state.View())
			}
		})
	}
	for _, outcome := range []contract.Outcome{contract.OutcomeRefusal, contract.OutcomeError, contract.OutcomeUnavailable} {
		t.Run(string(outcome), func(t *testing.T) {
			state, _, snapshot := startWithSnapshot(t, definition, "entry-"+string(outcome), []string{"opaque-a"})
			state, _ = acceptTextInput(t, state, "input-"+string(outcome), "payload-"+string(outcome))
			match := matchEvent(state, "match-"+string(outcome), snapshot, contract.OutcomeNoMatch, "")
			state, _, err := Reduce(state, Event{Normalized: &match, InputID: "input-" + string(outcome)})
			if err != nil {
				t.Fatal(err)
			}
			decision := decisionEvent(state, "decision-"+string(outcome), snapshot, outcome, "")
			state, effects, err := Reduce(state, Event{Normalized: &decision, InputID: "input-" + string(outcome)})
			if err != nil {
				t.Fatal(err)
			}
			if state.View().RouteID != "/help" || hasEffect(effects, EffectFullAgentFallback) {
				t.Fatalf("decision outcome did not follow frozen fallback: %#v %#v", state.View(), effects)
			}
		})
	}
}

func TestFullAgentFallbackNeedsExplicitLimitAndCannotNameTarget(t *testing.T) {
	definition := traceDefinition(t)
	limit := uint64(1)
	policy := Policy{FullAgentFallbackLimit: &limit}
	state, effects, err := Start(definition, admissionEvent("entry-full-agent"), policy)
	if err != nil {
		t.Fatal(err)
	}
	refresh := snapshotRefresh(state, "snapshot-full-agent", []string{"opaque-a"})
	snapshot := refresh.Bound
	state, _, err = Reduce(state, Event{Snapshot: &refresh})
	if err != nil {
		t.Fatal(err)
	}
	state, _ = acceptTextInput(t, state, "input-full-agent-1", "payload-full-agent-1")
	match := matchEvent(state, "match-full-agent-1", snapshot, contract.OutcomeNoMatch, "")
	state, _, err = Reduce(state, Event{Normalized: &match, InputID: "input-full-agent-1"})
	if err != nil {
		t.Fatal(err)
	}
	decision := decisionEvent(state, "decision-full-agent-1", snapshot, contract.OutcomeError, "")
	state, effects, err = Reduce(state, Event{Normalized: &decision, InputID: "input-full-agent-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasEffect(effects, EffectFullAgentFallback) || state.View().FullAgentFallbacks != 1 {
		t.Fatalf("explicit fallback allowance was not used: %#v %#v", state.View(), effects)
	}
	full := FullAgentResult{ID: "full-agent-result", TenantID: "tenant-1", SessionID: "session-1", Generation: 7, RouteID: state.View().RouteID, RouteEntryID: state.View().RouteEntryID, InputID: "input-full-agent-1", Outcome: contract.OutcomeNoMatch, ReceivedAt: traceTime.Add(4 * time.Second)}
	state, _, err = Reduce(state, Event{Fallback: &full})
	if err != nil {
		t.Fatal(err)
	}
	if state.View().RouteID != "/clarify" {
		t.Fatalf("full-agent no-match did not use frozen clarify route: %#v", state.View())
	}
	full.ID, full.RouteID, full.RouteEntryID = "full-agent-forged", "/clarify", state.View().RouteEntryID
	full.InputID, full.Outcome = "input-full-agent-1", contract.OutcomeCandidate
	if _, _, err := Reduce(state, Event{Fallback: &full}); err == nil {
		t.Fatal("full-agent result nominated an opaque connect target")
	}
	if effects[0].Kind != EffectFullAgentFallback {
		t.Fatal("full-agent escalation effect missing")
	}
	_ = effects // The configured limit is one; a nil limit disables escalation.
}

func TestFailedEffectDoesNotReleaseCallOwnership(t *testing.T) {
	definition := traceDefinition(t)
	state, _, snapshot := startWithSnapshot(t, definition, "entry-failed-effect", []string{"opaque-a"})
	state, _ = acceptTextInput(t, state, "input-failed-effect", "payload-failed-effect")
	match := matchEvent(state, "match-failed-effect", snapshot, contract.OutcomeCandidate, "opaque-a")
	state, effects, err := Reduce(state, Event{Normalized: &match, InputID: "input-failed-effect"})
	if err != nil {
		t.Fatal(err)
	}
	request := effects[0].Request
	submitted := normalizedFor(state, "submitted-failed-effect", contract.EventEffectSubmitted)
	submitted.EffectRequest = cloneRequest(request)
	state, _, err = Reduce(state, Event{Normalized: &submitted})
	if err != nil {
		t.Fatal(err)
	}
	failed := receiptEvent(state, "failed-effect", request, contract.EffectFailed, "receipt-failed", "opaque-a")
	failed.Effect.FailureCode = "test-failure"
	state, effects, err = Reduce(state, Event{Normalized: &failed})
	if err != nil {
		t.Fatal(err)
	}
	if state.View().Ownership.State != contract.OwnershipOwned || hasEffect(effects, EffectReleaseOwnership) {
		t.Fatalf("failed effect released call ownership: %#v %#v", state.View(), effects)
	}
}

func TestConfirmedReceiptMayArriveAfterCandidateSnapshotExpiry(t *testing.T) {
	definition := traceDefinition(t)
	state, _, err := Start(definition, admissionEvent("entry-late-receipt"), Policy{})
	if err != nil {
		t.Fatal(err)
	}
	refresh := snapshotRefresh(state, "snapshot-late-receipt", []string{"opaque-a"})
	refresh.Bound.Snapshot.ProtectedSnapshot.ExpiresAt = traceTime.Add(4 * time.Second)
	refresh.Bound.Snapshot.SHA256 = refresh.Bound.Snapshot.CanonicalSHA256()
	state, _, err = Reduce(state, Event{Snapshot: &refresh})
	if err != nil {
		t.Fatal(err)
	}
	state, _ = acceptTextInput(t, state, "input-late-receipt", "payload-late-receipt")
	match := matchEvent(state, "match-late-receipt", refresh.Bound, contract.OutcomeCandidate, "opaque-a")
	state, effects, err := Reduce(state, Event{Normalized: &match, InputID: "input-late-receipt"})
	if err != nil {
		t.Fatal(err)
	}
	request := effects[0].Request
	submitted := normalizedFor(state, "submitted-late-receipt", contract.EventEffectSubmitted)
	submitted.ReceivedAt = traceTime.Add(3 * time.Second)
	submitted.EffectRequest = cloneRequest(request)
	state, _, err = Reduce(state, Event{Normalized: &submitted})
	if err != nil {
		t.Fatal(err)
	}
	receipt := receiptEvent(state, "confirmed-late-receipt", request, contract.EffectConfirmed, "receipt-late", "opaque-a")
	receipt.ReceivedAt = traceTime.Add(10 * time.Second)
	receipt.Effect.ObservedAt = receipt.ReceivedAt
	state, effects, err = Reduce(state, Event{Normalized: &receipt})
	if err != nil {
		t.Fatalf("authoritative receipt was rejected after snapshot expiry: %v", err)
	}
	if state.View().Ownership.State != contract.OwnershipReleased || !hasEffect(effects, EffectReleaseOwnership) {
		t.Fatalf("confirmed receipt did not request the existing owner CAS: %#v %#v", state.View(), effects)
	}
}

func TestTestOnlyDefinitionPreservesActivationGate(t *testing.T) {
	definition := traceDefinition(t)
	if err := definition.ValidateForActivation(); err == nil {
		t.Fatal("fixture unexpectedly became activation-ready")
	}
}

func traceDefinition(t *testing.T) contract.Definition {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "decisioncontract", "v1", "testdata", "review-contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	var definition contract.Definition
	if err := json.Unmarshal(data, &definition); err != nil {
		t.Fatal(err)
	}
	// Explicit limits below exist only in this synthetic trace. The frozen
	// ORI-66 review fixture remains unresolved and activation-blocked.
	values := map[string]uint64{"g-route-visits": 12, "g-decision-calls": 4, "g-effect-attempts": 4, "g-reprompts": 2, "g-no-input-reprompts": 1, "g-no-match-reprompts": 2, "g-ambiguous-reprompts": 2}
	for i := range definition.PolicyGates {
		gate := &definition.PolicyGates[i]
		if value, ok := values[gate.ID]; ok {
			gate.Status, gate.Value = contract.GateQualified, strconv.FormatUint(value, 10)
			gate.EvidenceRef, gate.UnresolvedReason = "test-only:agents/decisions/reducer.trace", ""
		}
	}
	for dimension, bound := range definition.Graph.Authority.Budgets {
		if value, ok := values[bound.GateID]; ok {
			v := value
			bound.Value = &v
			definition.Graph.Authority.Budgets[dimension] = bound
		}
	}
	for i := range definition.Graph.RetryGroups {
		group := &definition.Graph.RetryGroups[i]
		group.MaxReprompts = setTestBound(group.MaxReprompts, values)
		group.MaxNoInputReprompts = setTestBound(group.MaxNoInputReprompts, values)
		group.MaxNoMatchReprompts = setTestBound(group.MaxNoMatchReprompts, values)
		group.MaxAmbiguousReprompts = setTestBound(group.MaxAmbiguousReprompts, values)
	}
	inputs, release, err := definition.CanonicalReleaseDigests()
	if err != nil {
		t.Fatal(err)
	}
	definition.DigestInputs, definition.ReleaseSHA256 = inputs, release
	if err := definition.ValidateForReview(); err != nil {
		t.Fatalf("synthetic reducer fixture: %v", err)
	}
	return definition
}

func setTestBound(bound contract.Bound, values map[string]uint64) contract.Bound {
	if value, ok := values[bound.GateID]; ok {
		v := value
		bound.Value = &v
	}
	return bound
}

func admissionEvent(entry string) contract.NormalizedEvent {
	return contract.NormalizedEvent{ID: "admit-" + entry, Kind: contract.EventSessionAdmitted, TenantID: "tenant-1", SessionID: "session-1", Generation: 7, RouteEntryID: entry, Channel: contract.ChannelText, Locale: "en", ReceivedAt: traceTime}
}

func startWithSnapshot(t *testing.T, definition contract.Definition, entry string, candidates []string) (State, []Effect, BoundCandidateSnapshot) {
	return startWithSnapshotPolicy(t, definition, entry, candidates, Policy{})
}

func startWithSnapshotPolicy(t *testing.T, definition contract.Definition, entry string, candidates []string, policy Policy) (State, []Effect, BoundCandidateSnapshot) {
	t.Helper()
	state, effects, err := Start(definition, admissionEvent(entry), policy)
	if err != nil {
		t.Fatal(err)
	}
	refresh := snapshotRefresh(state, "snapshot-"+entry, candidates)
	state, _, err = Reduce(state, Event{Snapshot: &refresh})
	if err != nil {
		t.Fatal(err)
	}
	return state, effects, refresh.Bound
}

func refreshAfterRouteEntry(t *testing.T, state State, candidates []string) (State, []Effect, BoundCandidateSnapshot) {
	t.Helper()
	refresh := snapshotRefresh(state, "snapshot-"+state.View().RouteEntryID, candidates)
	state, effects, err := Reduce(state, Event{Snapshot: &refresh})
	if err != nil {
		t.Fatal(err)
	}
	return state, effects, refresh.Bound
}

func snapshotRefresh(state State, id string, candidates []string) SnapshotRefresh {
	view := state.View()
	ref := protectedReference("directory-"+id, "directory-snapshot", view.Generation, "e")
	snapshot := contract.CandidateSetSnapshot{CandidateIDs: append([]string(nil), candidates...), ProtectedSnapshot: &ref}
	snapshot.SHA256 = snapshot.CanonicalSHA256()
	bound := BoundCandidateSnapshot{BindingID: "recipient_matcher", RouteID: view.RouteID, RouteEntryID: view.RouteEntryID, Snapshot: snapshot}
	return SnapshotRefresh{ID: id, TenantID: view.TenantID, SessionID: view.SessionID, Generation: view.Generation, RouteID: view.RouteID, RouteEntryID: view.RouteEntryID, ReceivedAt: traceTime.Add(time.Second), Bound: bound}
}

func protectedReference(id, purpose string, generation uint64, digestByte string) contract.ProtectedReference {
	return contract.ProtectedReference{ID: id, Purpose: purpose, TenantID: "tenant-1", SessionID: "session-1", Generation: generation, SHA256: strings.Repeat(digestByte, 64), ExpiresAt: traceTime.Add(time.Hour)}
}

func acceptTextInput(t *testing.T, state State, id, payloadID string) (State, []Effect) {
	t.Helper()
	event := normalizedFor(state, id, contract.EventInputFinal)
	event.InputWindowID, event.Modality, event.Locale = state.View().InputWindowID, contract.ModalityText, "en"
	event.SourceEventIDs = []string{"source-" + id}
	ref := protectedReference(payloadID, "final-input", state.View().Generation, "f")
	event.ProtectedInput = &ref
	return reduceEvent(t, state, Event{Normalized: &event})
}

func normalizedFor(state State, id string, kind contract.EventKind) contract.NormalizedEvent {
	view := state.View()
	return contract.NormalizedEvent{ID: id, Kind: kind, TenantID: view.TenantID, SessionID: view.SessionID, Generation: view.Generation, RouteID: view.RouteID, RouteEntryID: view.RouteEntryID, Channel: contract.ChannelText, ReceivedAt: traceTime.Add(2 * time.Second)}
}

func matchEvent(state State, id string, snapshot BoundCandidateSnapshot, outcome contract.Outcome, candidateID string) contract.NormalizedEvent {
	event := normalizedFor(state, id, contract.EventMatchCompleted)
	event.Match = &contract.MatchResult{Outcome: outcome, CandidateID: candidateID, SnapshotSHA256: snapshot.Snapshot.SHA256, BindingID: snapshot.BindingID}
	return event
}

func decisionEvent(state State, id string, snapshot BoundCandidateSnapshot, outcome contract.Outcome, candidateID string) contract.NormalizedEvent {
	event := normalizedFor(state, id, contract.EventDecisionCompleted)
	event.Decision = &contract.DecisionResult{Outcome: outcome, CandidateID: candidateID, ProviderRef: "jev-test", ModelRef: "decision-test", Revision: "fixture-1", ResultSchemaSHA256: strings.Repeat("a", 64), CandidateSetSHA256: snapshot.Snapshot.SHA256, Usage: contract.UsageRecord{Status: contract.UsageMissing}}
	return event
}

func receiptEvent(state State, id string, request *contract.EffectRequest, status contract.EffectStatus, receiptID, targetID string) contract.NormalizedEvent {
	event := normalizedFor(state, id, contract.EventEffectReceipt)
	event.Effect = &contract.EffectReceipt{Identity: request.Identity, Status: status, ReceiptID: receiptID, ProviderRequestID: "provider-test-request", TargetOpaqueID: targetID, ObservedAt: traceTime.Add(5 * time.Second)}
	return event
}

func reduceEvent(t *testing.T, state State, event Event) (State, []Effect) {
	t.Helper()
	state, effects, err := Reduce(state, event)
	if err != nil {
		t.Fatal(err)
	}
	return state, effects
}

func retryCount(state State, group string) contract.RetryCounter {
	for _, item := range state.View().RetryCounters {
		if item.GroupID == group {
			return item.Counter
		}
	}
	return contract.RetryCounter{}
}

func hasEffect(effects []Effect, kind EffectKind) bool {
	for _, effect := range effects {
		if effect.Kind == kind {
			return true
		}
	}
	return false
}

func goldenTrace(t *testing.T, name string, effects []Effect) {
	t.Helper()
	got := FormatTextTrace(effects)
	want, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != got {
		t.Fatalf("trace mismatch for %s\n--- want ---\n%s\n--- got ---\n%s", name, want, got)
	}
}
