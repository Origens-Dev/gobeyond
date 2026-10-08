package v1

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func loadReviewDefinition(t *testing.T) Definition {
	t.Helper()
	data, err := os.ReadFile("testdata/review-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var definition Definition
	if err := json.Unmarshal(data, &definition); err != nil {
		t.Fatal(err)
	}
	return definition
}

func requireValidationError(t *testing.T, err error, contains string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), contains) {
		t.Fatalf("expected validation error containing %q, got %v", contains, err)
	}
}

func TestReviewFixtureIsValidButCannotActivate(t *testing.T) {
	definition := loadReviewDefinition(t)
	if err := definition.ValidateForReview(); err != nil {
		t.Fatalf("review fixture should validate: %v", err)
	}
	requireValidationError(t, definition.ValidateForActivation(), "activation blocked by unresolved policy gate")
}

func TestRejectsDuplicateAndMissingRoutes(t *testing.T) {
	t.Run("duplicate route ID", func(t *testing.T) {
		definition := loadReviewDefinition(t)
		definition.Graph.Routes = append(definition.Graph.Routes, definition.Graph.Routes[0])
		requireValidationError(t, definition.ValidateForReview(), "duplicate route ID")
	})
	t.Run("missing entry", func(t *testing.T) {
		definition := loadReviewDefinition(t)
		definition.Graph.Entry = "/missing"
		requireValidationError(t, definition.ValidateForReview(), "entry route \"/missing\" is missing")
	})
	t.Run("missing route ID", func(t *testing.T) {
		definition := loadReviewDefinition(t)
		definition.Graph.Routes[0].ID = ""
		requireValidationError(t, definition.ValidateForReview(), "stable absolute route ID")
	})
}

func TestPolicyGateKindsAreUniqueExceptRetryGroups(t *testing.T) {
	t.Run("singleton gate cannot be ambiguous", func(t *testing.T) {
		definition := loadReviewDefinition(t)
		duplicate := definition.PolicyGates[0]
		duplicate.ID = "g-session-duration-duplicate"
		definition.PolicyGates = append(definition.PolicyGates, duplicate)
		requireValidationError(t, definition.ValidateForReview(), `policy gate kind "session_duration_ceiling" must be unique`)
	})
	t.Run("retry gate can qualify separate groups", func(t *testing.T) {
		definition := loadReviewDefinition(t)
		duplicate := definition.PolicyGates[3]
		duplicate.ID = "g-retry-another-group"
		definition.PolicyGates = append(definition.PolicyGates, duplicate)
		if err := definition.ValidateForReview(); err != nil {
			t.Fatalf("independent retry groups may have separate gates of one kind: %v", err)
		}
	})
}

func TestRejectsUnknownAndMissingOutcomeMappings(t *testing.T) {
	t.Run("unknown outcome", func(t *testing.T) {
		definition := loadReviewDefinition(t)
		definition.Graph.Routes[0].Next[0].Outcome = Outcome("mystery")
		requireValidationError(t, definition.ValidateForReview(), "not valid for source")
	})
	t.Run("missing result fallback", func(t *testing.T) {
		definition := loadReviewDefinition(t)
		route := &definition.Graph.Routes[0]
		for index, transition := range route.Next {
			if transition.Source == SourceDecision && transition.Outcome == OutcomeUnavailable {
				route.Next = append(route.Next[:index], route.Next[index+1:]...)
				break
			}
		}
		requireValidationError(t, definition.ValidateForReview(), "missing decision/unavailable")
	})
}

func TestRejectsInvalidICUPlaceholdersAndMissingFallbacks(t *testing.T) {
	t.Run("undeclared named argument", func(t *testing.T) {
		definition := loadReviewDefinition(t)
		definition.Graph.Messages[0].Variants["en"] = "Hi {recipient}, who do you want to call?"
		requireValidationError(t, definition.ValidateForReview(), "placeholder \"recipient\" is not declared")
	})
	t.Run("plural requires other branch", func(t *testing.T) {
		definition := loadReviewDefinition(t)
		definition.Graph.Messages[1].Variants["en"] = "{count, plural, one {one choice}}"
		requireValidationError(t, definition.ValidateForReview(), "requires an other branch")
	})
	t.Run("plural category is typed", func(t *testing.T) {
		definition := loadReviewDefinition(t)
		definition.Graph.Messages[1].Variants["en"] = "{count, plural, singular {one choice} other {# choices}}"
		requireValidationError(t, definition.ValidateForReview(), "invalid branch")
	})
	t.Run("missing base locale", func(t *testing.T) {
		definition := loadReviewDefinition(t)
		delete(definition.Graph.Messages[0].Variants, "en")
		requireValidationError(t, definition.ValidateForReview(), "missing non-empty base message")
	})
	t.Run("missing graph fallback", func(t *testing.T) {
		definition := loadReviewDefinition(t)
		definition.Graph.Fallback = ""
		requireValidationError(t, definition.ValidateForReview(), "graph.fallback must be")
	})
	t.Run("missing route fallback", func(t *testing.T) {
		definition := loadReviewDefinition(t)
		definition.Graph.Routes[0].Fallback = Target{}
		requireValidationError(t, definition.ValidateForReview(), "fallback must name exactly one")
	})
}

func TestRejectsWidenedInheritedAuthority(t *testing.T) {
	t.Run("tool", func(t *testing.T) {
		definition := loadReviewDefinition(t)
		definition.Graph.Routes[0].UseTools = []string{"connect", "ungranted_tool"}
		requireValidationError(t, definition.ValidateForReview(), "widens authority with ungranted tool")
	})
	t.Run("service", func(t *testing.T) {
		definition := loadReviewDefinition(t)
		definition.Graph.Routes[0].UseServices = []string{"jev", "ungranted_service"}
		requireValidationError(t, definition.ValidateForReview(), "widens authority with ungranted service")
	})
	t.Run("typed binding", func(t *testing.T) {
		definition := loadReviewDefinition(t)
		definition.Graph.Routes[0].UseBindings = []string{"recipient_matcher", "ungranted_binding"}
		requireValidationError(t, definition.ValidateForReview(), "widens authority with ungranted binding")
	})
	t.Run("budget", func(t *testing.T) {
		definition := loadReviewDefinition(t)
		qualifyBudgetGatesForTest(&definition)
		parent := definition.Graph.Authority.Budgets[BudgetRouteVisits]
		larger := *parent.Value + 1
		definition.Graph.Routes[0].BudgetOverrides = map[BudgetDimension]Bound{
			BudgetRouteVisits: {GateID: parent.GateID, Value: &larger},
		}
		requireValidationError(t, definition.ValidateForReview(), "widens inherited budget")
	})
}

func TestRejectsMissingLimitsAndUnboundedCycles(t *testing.T) {
	t.Run("missing inherited limit", func(t *testing.T) {
		definition := loadReviewDefinition(t)
		delete(definition.Graph.Authority.Budgets, BudgetDecisionCalls)
		requireValidationError(t, definition.ValidateForReview(), "missing; missing limits fail closed")
	})
	t.Run("cycle without retry budget", func(t *testing.T) {
		definition := loadReviewDefinition(t)
		for index := range definition.Graph.Routes {
			if definition.Graph.Routes[index].ID == "/clarify" {
				definition.Graph.Routes[index].RetryGroup = ""
			}
		}
		requireValidationError(t, definition.ValidateForReview(), "no bounded retry group")
	})
	t.Run("retry limit without gate", func(t *testing.T) {
		definition := loadReviewDefinition(t)
		definition.Graph.RetryGroups[0].MaxReprompts = Bound{}
		requireValidationError(t, definition.ValidateForReview(), "must reference a qualification gate")
	})
}

func TestRetryCounterIsScopedAndReplaySafe(t *testing.T) {
	definition := loadReviewDefinition(t)
	group := definition.Graph.RetryGroups[0]
	if err := (RetryCounter{GroupID: group.ID, LogicalTaskID: "task-1"}).ValidateFor(group); err == nil || !strings.Contains(err.Error(), "limits are unresolved") {
		t.Fatalf("unresolved retry group should fail closed, got %v", err)
	}
	qualifyBudgetGatesForTest(&definition)
	group = definition.Graph.RetryGroups[0]
	counter := RetryCounter{
		GroupID: group.ID, LogicalTaskID: "task-1", Reprompts: 2, NoInputReprompts: 1,
		CountedAdmissionEventIDs: []string{"retry-admitted-1", "retry-admitted-2"},
	}
	if err := counter.ValidateFor(group); err != nil {
		t.Fatalf("qualified retry counter should validate: %v", err)
	}
	counter.CountedAdmissionEventIDs[1] = counter.CountedAdmissionEventIDs[0]
	if err := counter.ValidateFor(group); err == nil {
		t.Fatal("duplicate replay event ID should be rejected")
	}
}

func TestNormalizedInputRequiresScopedProtectedReference(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	event := NormalizedEvent{
		ID: "event-1", Kind: EventInputFinal, TenantID: "tenant-1", SessionID: "session-1", Generation: 1,
		RouteID: "/start", RouteEntryID: "entry-1", InputWindowID: "window-1", Channel: ChannelVoice,
		Modality: ModalitySpeechFinal, Locale: "en", SourceEventIDs: []string{"provider-event-1"}, ReceivedAt: now,
		ProtectedInput: &ProtectedReference{
			ID: "protected-input-1", Purpose: "input-transcript", TenantID: "tenant-1", SessionID: "session-1",
			Generation: 1, SHA256: strings.Repeat("a", 64), ExpiresAt: now.Add(time.Minute),
		},
	}
	if err := event.Validate(now); err != nil {
		t.Fatalf("scoped protected input should validate: %v", err)
	}
	event.ProtectedInput.TenantID = "another-tenant"
	if err := event.Validate(now); err == nil || !strings.Contains(err.Error(), "scope does not match") {
		t.Fatalf("cross-tenant protected reference should be rejected, got %v", err)
	}
}

func TestDecisionCandidateMustBelongToProtectedAuthorizedSnapshot(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	snapshot := CandidateSetSnapshot{
		CandidateIDs: []string{"candidate-allowed"},
		ProtectedSnapshot: &ProtectedReference{
			ID: "protected-candidates-1", Purpose: "directory-snapshot", TenantID: "tenant-1", SessionID: "session-1",
			Generation: 1, SHA256: strings.Repeat("a", 64), ExpiresAt: now.Add(time.Minute),
		},
	}
	snapshot.SHA256 = snapshot.CanonicalSHA256()
	result := DecisionResult{
		Outcome: OutcomeCandidate, CandidateID: "candidate-allowed", ProviderRef: "jev", ModelRef: "jev-model",
		Revision: "rev-1", ResultSchemaSHA256: strings.Repeat("b", 64), CandidateSetSHA256: snapshot.SHA256,
		Usage: UsageRecord{Status: UsageMissing},
	}
	if err := result.ValidateAgainst(snapshot, "tenant-1", "session-1", 1, now); err != nil {
		t.Fatalf("candidate from the authorized snapshot should validate: %v", err)
	}
	result.CandidateID = "candidate-ungranted"
	if err := result.ValidateAgainst(snapshot, "tenant-1", "session-1", 1, now); err == nil || !strings.Contains(err.Error(), "outside the authorized candidate set") {
		t.Fatalf("model-nominated candidate outside the snapshot should be rejected, got %v", err)
	}
}

func TestSessionPinRequiresQualifiedLocaleAndVoiceCapabilities(t *testing.T) {
	definition := loadReviewDefinition(t)
	qualifyDefinitionForTest(&definition)
	if err := definition.ValidateForActivation(); err != nil {
		t.Fatalf("test-only qualified definition should be activation-ready: %v", err)
	}
	pin := SessionPin{
		SchemaVersion: SchemaVersion, GraphSHA256: strings.Repeat("a", 64), PromptFamiliesSHA256: strings.Repeat("b", 64),
		BindingsSHA256: strings.Repeat("c", 64), NormalizationSHA256: strings.Repeat("d", 64), AuthoritySHA256: strings.Repeat("e", 64),
		PolicySHA256: strings.Repeat("f", 64), SnapshotSHA256: strings.Repeat("0", 64), Generation: 1,
		Locale: "en", PromptFamily: "operator.prompt", PromptVariantLocale: "en",
		Voice: VoiceCapabilities{
			ProfileRef: "qualified-profile", Revision: "profile-revision-1", VoiceID: "voice-1", Locale: "en",
			SupportedLocales: []string{"en"}, Formats: []AudioFormat{{Codec: "pcm", SampleRateHz: 16000, Channels: 1}},
			Capabilities: map[VoiceCapabilityName]CapabilityStatus{}, EvidenceRef: "voice-qualification-test-only",
		},
	}
	for _, capability := range definition.Graph.RequiredVoiceCapabilities {
		pin.Voice.Capabilities[capability] = CapabilitySupported
	}
	if err := pin.ValidateFor(definition); err != nil {
		t.Fatalf("qualified session pin should validate: %v", err)
	}
	pin.Voice.Capabilities[VoiceDTMFInput] = CapabilityUnknown
	if err := pin.ValidateFor(definition); err == nil || !strings.Contains(err.Error(), "not qualified as supported") {
		t.Fatalf("unknown required voice capability should fail closed, got %v", err)
	}
}

func TestGraphOwnershipReleasesOnlyOnConfirmedReceipt(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	identity := EffectIdentity{
		TenantID: "tenant-1", SessionID: "session-1", Generation: 2, RouteEntryID: "entry-4",
		InputID: "input-3", ActionID: "connect-recipient", GraphSHA256: strings.Repeat("a", 64),
	}
	identity.ID = identity.CanonicalID()
	if err := identity.Validate(); err != nil {
		t.Fatal(err)
	}
	from := GraphOwnership{State: OwnershipReceiptPending, PendingEffectID: identity.ID}
	accepted := EffectReceipt{
		Identity: identity, Status: EffectAccepted, ReceiptID: "accepted-1", ObservedAt: now,
	}
	if accepted.ReleasesGraphOwnership(true) {
		t.Fatal("accepted effect must not release graph ownership")
	}
	if err := ValidateOwnershipTransition(from, GraphOwnership{State: OwnershipReleased, ReleaseReceiptID: "accepted-1"}, &accepted, true); err == nil {
		t.Fatal("accepted receipt must not release graph ownership")
	}
	confirmed := EffectReceipt{
		Identity: identity, Status: EffectConfirmed, ReceiptID: "confirmed-1", ProviderRequestID: "provider-request-1",
		TargetOpaqueID: "candidate-1", ObservedAt: now,
	}
	if !confirmed.ReleasesGraphOwnership(true) {
		t.Fatal("confirmed receipt should release graph ownership for an ending effect")
	}
	unboundTarget := confirmed
	unboundTarget.TargetOpaqueID = ""
	if unboundTarget.ReleasesGraphOwnership(true) {
		t.Fatal("confirmed receipt without its opaque target must not release ownership")
	}
	to := GraphOwnership{State: OwnershipReleased, ReleaseReceiptID: confirmed.ReceiptID}
	if err := ValidateOwnershipTransition(from, to, &confirmed, true); err != nil {
		t.Fatalf("matching confirmed receipt should release ownership: %v", err)
	}
	if err := ValidateOwnershipTransition(from, to, &confirmed, false); err == nil {
		t.Fatal("an effect not declared to end ownership must not release it")
	}
}

func TestSyntheticDeterministicJevClarifyFallbackTrace(t *testing.T) {
	data, err := os.ReadFile("testdata/synthetic-trace.json")
	if err != nil {
		t.Fatal(err)
	}
	var trace struct {
		SchemaVersion string `json:"schemaVersion"`
		Activation    string `json:"activation"`
		Steps         []struct {
			Sequence          int            `json:"sequence"`
			Route             string         `json:"route"`
			Event             string         `json:"event"`
			MatchOutcome      string         `json:"matchOutcome"`
			DecisionOutcome   string         `json:"decisionOutcome"`
			NextPhase         string         `json:"nextPhase"`
			NextRoute         string         `json:"nextRoute"`
			JevInvocations    int            `json:"jevInvocations"`
			EffectStatus      string         `json:"effectStatus"`
			ReceiptID         string         `json:"receiptId"`
			ProviderRequestID string         `json:"providerRequestId"`
			TargetOpaqueID    string         `json:"targetOpaqueId"`
			ObservedAt        time.Time      `json:"observedAt"`
			EffectIdentity    EffectIdentity `json:"effectIdentity"`
			Ownership         string         `json:"ownership"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(data, &trace); err != nil {
		t.Fatal(err)
	}
	if trace.SchemaVersion != SchemaVersion || trace.Activation != "blocked_by_unresolved_qualification_gates" || len(trace.Steps) != 6 {
		t.Fatalf("unexpected synthetic fixture header or step count: %#v", trace)
	}
	if first := trace.Steps[0]; first.MatchOutcome != "no_match" || first.NextPhase != "decide" || first.JevInvocations != 0 {
		t.Fatalf("deterministic no-match should enter the Jev phase once: %#v", first)
	}
	if second := trace.Steps[1]; second.DecisionOutcome != "ambiguous" || second.JevInvocations != 1 || second.NextRoute != "/clarify" {
		t.Fatalf("Jev ambiguity should clarify: %#v", second)
	}
	if third := trace.Steps[2]; third.Event != "initial_silence" || third.JevInvocations != 0 || third.NextRoute != "/clarify" {
		t.Fatalf("no-input must bypass Jev: %#v", third)
	}
	if fourth := trace.Steps[3]; fourth.Event != "retry_exhausted" || fourth.NextRoute != "/help" {
		t.Fatalf("exhausted clarification should use authored fallback: %#v", fourth)
	}
	if fifth := trace.Steps[4]; fifth.EffectStatus != "accepted" || fifth.ReceiptID == "" || fifth.Ownership != "owned" {
		t.Fatalf("accepted handoff must retain graph ownership: %#v", fifth)
	}
	if sixth := trace.Steps[5]; sixth.EffectStatus != "confirmed" || sixth.ReceiptID == "" || sixth.Ownership != "released" {
		t.Fatalf("confirmed handoff receipt should release graph ownership: %#v", sixth)
	}
	acceptedStep, confirmedStep := trace.Steps[4], trace.Steps[5]
	if err := acceptedStep.EffectIdentity.Validate(); err != nil {
		t.Fatalf("fixture effect ID must be canonical: %v", err)
	}
	if acceptedStep.EffectIdentity.ID != confirmedStep.EffectIdentity.ID || acceptedStep.TargetOpaqueID != confirmedStep.TargetOpaqueID {
		t.Fatal("accepted and confirmed receipts must refer to the same effect and opaque target")
	}
	from := GraphOwnership{State: OwnershipReceiptPending, PendingEffectID: acceptedStep.EffectIdentity.ID}
	accepted := EffectReceipt{
		Identity: acceptedStep.EffectIdentity, Status: EffectAccepted, ReceiptID: acceptedStep.ReceiptID,
		ProviderRequestID: acceptedStep.ProviderRequestID, TargetOpaqueID: acceptedStep.TargetOpaqueID, ObservedAt: acceptedStep.ObservedAt,
	}
	confirmed := EffectReceipt{
		Identity: confirmedStep.EffectIdentity, Status: EffectConfirmed, ReceiptID: confirmedStep.ReceiptID,
		ProviderRequestID: confirmedStep.ProviderRequestID, TargetOpaqueID: confirmedStep.TargetOpaqueID, ObservedAt: confirmedStep.ObservedAt,
	}
	if accepted.ReleasesGraphOwnership(true) {
		t.Fatal("fixture accepted event must retain graph ownership")
	}
	if err := ValidateOwnershipTransition(from, GraphOwnership{State: OwnershipReleased, ReleaseReceiptID: accepted.ReceiptID}, &accepted, true); err == nil {
		t.Fatal("fixture accepted event must not release ownership")
	}
	if err := ValidateOwnershipTransition(from, GraphOwnership{State: OwnershipReleased, ReleaseReceiptID: confirmed.ReceiptID}, &confirmed, true); err != nil {
		t.Fatalf("fixture confirmed receipt should release ownership: %v", err)
	}
}

// The values below exist only to exercise narrowing comparisons in tests. They
// are never part of the review fixture or a proposed runtime default.
func qualifyBudgetGatesForTest(definition *Definition) {
	for index := range definition.PolicyGates {
		gate := &definition.PolicyGates[index]
		for _, dimension := range RequiredBudgetDimensions {
			if gate.Kind == gateForBudget(dimension) {
				gate.Status = GateQualified
				gate.Value = "100"
				gate.EvidenceRef = "synthetic-test-only"
				gate.UnresolvedReason = ""
			}
		}
	}
	value := uint64(100)
	for dimension, bound := range definition.Graph.Authority.Budgets {
		bound.Value = &value
		definition.Graph.Authority.Budgets[dimension] = bound
	}
	for index := range definition.Graph.RetryGroups {
		definition.Graph.RetryGroups[index].MaxReprompts.Value = &value
		definition.Graph.RetryGroups[index].MaxNoInputReprompts.Value = &value
		definition.Graph.RetryGroups[index].MaxNoMatchReprompts.Value = &value
		definition.Graph.RetryGroups[index].MaxAmbiguousReprompts.Value = &value
	}
}

func qualifyDefinitionForTest(definition *Definition) {
	qualifyBudgetGatesForTest(definition)
	for index := range definition.PolicyGates {
		gate := &definition.PolicyGates[index]
		if gate.Status == GateQualified {
			continue
		}
		gate.Status = GateQualified
		gate.Value = "synthetic-test-only"
		gate.EvidenceRef = "synthetic-test-only"
		gate.UnresolvedReason = ""
		if gate.Kind == GateLocaleProfile {
			gate.Value = "en"
		}
		if gate.Kind == GateVoiceProfile {
			gate.Value = "qualified-profile"
		}
	}
	definition.Graph.Locale.EnabledLocales = []string{"en"}
}
