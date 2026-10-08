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

func refreshDefinitionDigestsForTest(t *testing.T, definition *Definition) {
	t.Helper()
	inputs, release, err := definition.CanonicalReleaseDigests()
	if err != nil {
		t.Fatal(err)
	}
	definition.DigestInputs = inputs
	definition.ReleaseSHA256 = release
}

func sessionPinForTest(t *testing.T, definition Definition) SessionPin {
	t.Helper()
	voice := VoiceCapabilities{
		ProfileRef: "qualified-profile", Revision: "profile-revision-1", VoiceID: "voice-1", Locale: "en",
		SupportedLocales: []string{"en"}, Formats: []AudioFormat{{Codec: "pcm", SampleRateHz: 16000, Channels: 1}},
		Capabilities: map[VoiceCapabilityName]CapabilityStatus{}, EvidenceRef: "voice-qualification-test-only",
	}
	for _, capability := range definition.Graph.RequiredVoiceCapabilities {
		voice.Capabilities[capability] = CapabilitySupported
	}
	voiceDigest, err := CanonicalVoiceCapabilitiesSHA256(voice)
	if err != nil {
		t.Fatal(err)
	}
	return SessionPin{
		SchemaVersion: definition.SchemaVersion, ReleaseSHA256: definition.ReleaseSHA256,
		GraphSHA256: definition.DigestInputs.Graph, PromptFamiliesSHA256: definition.DigestInputs.Prompts,
		BindingsSHA256: definition.DigestInputs.Bindings, NormalizationSHA256: definition.DigestInputs.Normalization,
		AuthoritySHA256: definition.DigestInputs.Authority, PolicySHA256: definition.DigestInputs.Policy,
		LocaleAndVoiceSHA256: definition.DigestInputs.LocaleAndVoice,
		SnapshotSHA256:       strings.Repeat("0", 64), VoiceSHA256: voiceDigest,
		Generation: 1, Locale: "en", PromptFamily: "operator.prompt", PromptVariantLocale: "en", Voice: voice,
	}
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
		refreshDefinitionDigestsForTest(t, &definition)
		if err := definition.ValidateForReview(); err != nil {
			t.Fatalf("independent retry groups may have separate gates of one kind: %v", err)
		}
	})
}

func TestCanonicalReleaseDigestsBindFrozenInputsAndIgnoreSetOrder(t *testing.T) {
	definition := loadReviewDefinition(t)
	inputs, release, err := definition.CanonicalReleaseDigests()
	if err != nil {
		t.Fatal(err)
	}
	if inputs != definition.DigestInputs || release != definition.ReleaseSHA256 {
		t.Fatal("review fixture digests must match the canonical frozen definition")
	}
	permuted := definition
	permuted.Graph = canonicalGraph(permuted.Graph)
	for left, right := 0, len(permuted.Graph.Routes)-1; left < right; left, right = left+1, right-1 {
		permuted.Graph.Routes[left], permuted.Graph.Routes[right] = permuted.Graph.Routes[right], permuted.Graph.Routes[left]
	}
	for index := range permuted.Graph.Routes {
		route := &permuted.Graph.Routes[index]
		for left, right := 0, len(route.Next)-1; left < right; left, right = left+1, right-1 {
			route.Next[left], route.Next[right] = route.Next[right], route.Next[left]
		}
	}
	for left, right := 0, len(permuted.PolicyGates)-1; left < right; left, right = left+1, right-1 {
		permuted.PolicyGates[left], permuted.PolicyGates[right] = permuted.PolicyGates[right], permuted.PolicyGates[left]
	}
	permuted.Graph.Authority.Tools[0], permuted.Graph.Authority.Tools[1] = permuted.Graph.Authority.Tools[1], permuted.Graph.Authority.Tools[0]
	permuted.Graph.Locale.EnabledLocales = []string{"fr", "en"}
	definition.Graph.Locale.EnabledLocales = []string{"en", "fr"}
	inputsAfterPermutation, releaseAfterPermutation, err := permuted.CanonicalReleaseDigests()
	if err != nil {
		t.Fatal(err)
	}
	inputsBeforePermutation, releaseBeforePermutation, err := definition.CanonicalReleaseDigests()
	if err != nil {
		t.Fatal(err)
	}
	if inputsAfterPermutation != inputsBeforePermutation || releaseAfterPermutation != releaseBeforePermutation {
		t.Fatal("set-like route, outcome, grant, locale, and gate order must not change release digests")
	}
	changed := definition
	changed.Graph.Messages = append([]MessageFamily(nil), changed.Graph.Messages...)
	changed.Graph.Messages[0].Variants = map[string]string{"en": "A changed prompt."}
	changedInputs, changedRelease, err := changed.CanonicalReleaseDigests()
	if err != nil {
		t.Fatal(err)
	}
	if changedInputs.Prompts == inputs.Prompts || changedRelease == release {
		t.Fatal("changing frozen prompt content must change component and release digests")
	}
}

func TestRejectsDefinitionDigestMismatch(t *testing.T) {
	definition := loadReviewDefinition(t)
	definition.Graph.Messages[0].Variants["en"] = "Changed after freeze."
	requireValidationError(t, definition.ValidateForReview(), "digestInputs.prompts does not match canonical frozen definition content")
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
	t.Run("act target binding", func(t *testing.T) {
		definition := loadReviewDefinition(t)
		definition.Graph.Routes[0].Act[0].TargetBinding = "ungranted_target"
		requireValidationError(t, definition.ValidateForReview(), "target binding \"ungranted_target\" is outside its inherited binding grants")
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
	t.Run("cycle path bypasses counter inside an SCC", func(t *testing.T) {
		definition := loadReviewDefinition(t)
		var clarify Route
		for _, route := range definition.Graph.Routes {
			if route.ID == "/clarify" {
				clarify = route
				break
			}
		}
		cloneRoute := func(id RouteID) Route {
			copy := clarify
			copy.ID = id
			copy.RetryGroup = ""
			copy.Act = append([]ActStep(nil), clarify.Act...)
			copy.Next = append(OutcomeMap(nil), clarify.Next...)
			return copy
		}
		unboundedA, unboundedB := cloneRoute("/unbounded-a"), cloneRoute("/unbounded-b")
		setTransition := func(route *Route, source ResultSource, outcome Outcome, target RouteID) {
			for index := range route.Next {
				if route.Next[index].Source == source && route.Next[index].Outcome == outcome {
					route.Next[index].Target = Target{Route: target}
					return
				}
			}
			t.Fatalf("missing transition %s/%s in %s", source, outcome, route.ID)
		}
		setTransition(&unboundedA, SourceInput, OutcomeNoInput, unboundedB.ID)
		setTransition(&unboundedB, SourceInput, OutcomeNoInput, unboundedA.ID)
		setTransition(&unboundedA, SourceDecision, OutcomeNoMatch, "/clarify")
		setTransition(&clarify, SourceDecision, OutcomeNoMatch, unboundedA.ID)
		for index := range definition.Graph.Routes {
			if definition.Graph.Routes[index].ID == "/clarify" {
				definition.Graph.Routes[index] = clarify
			}
		}
		definition.Graph.Routes = append(definition.Graph.Routes, unboundedA, unboundedB)
		refreshDefinitionDigestsForTest(t, &definition)
		requireValidationError(t, definition.ValidateForReview(), "cycle path that bypasses every bounded retry counter")
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

func TestEffectRequiresFrozenActionAndAuthorizedTargetSnapshot(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	definition := loadReviewDefinition(t)
	qualifyDefinitionForTest(t, &definition)
	action := definition.Graph.Routes[0].Act[0]
	snapshot := CandidateSetSnapshot{
		CandidateIDs: []string{"candidate-allowed", "candidate-other"},
		ProtectedSnapshot: &ProtectedReference{
			ID: "protected-targets-1", Purpose: "directory-snapshot", TenantID: "tenant-1", SessionID: "session-1",
			Generation: 1, SHA256: strings.Repeat("a", 64), ExpiresAt: now.Add(time.Minute),
		},
	}
	snapshot.SHA256 = snapshot.CanonicalSHA256()
	identity := EffectIdentity{
		TenantID: "tenant-1", SessionID: "session-1", Generation: 1, RouteEntryID: "entry-1",
		InputID: "input-1", ActionID: action.ID, GraphSHA256: definition.DigestInputs.Graph,
	}
	identity.ID = identity.CanonicalID()
	request := EffectRequest{
		Identity: identity, ToolID: action.ToolID, Kind: action.Kind, TargetBinding: action.TargetBinding,
		TargetOpaqueID: "candidate-allowed", CandidateSetSHA256: snapshot.SHA256, ReauthorizeAtExecution: true,
	}
	if err := request.ValidateForDispatch(definition, definition.Graph.Routes[0].ID, &snapshot, "tenant-1", "session-1", 1, now); err != nil {
		t.Fatalf("authorized action target should validate before dispatch: %v", err)
	}
	request.TargetOpaqueID = "candidate-ungranted"
	if err := request.ValidateForDispatch(definition, definition.Graph.Routes[0].ID, &snapshot, "tenant-1", "session-1", 1, now); err == nil || !strings.Contains(err.Error(), "outside the authorized candidate set") {
		t.Fatalf("target outside the protected candidate snapshot should be rejected: %v", err)
	}
	request.TargetOpaqueID = "candidate-allowed"
	request.TargetBinding = "another_binding"
	if err := request.ValidateForDispatch(definition, definition.Graph.Routes[0].ID, &snapshot, "tenant-1", "session-1", 1, now); err == nil || !strings.Contains(err.Error(), "does not match the frozen route action") {
		t.Fatalf("effect target binding must match the frozen action: %v", err)
	}
	request.TargetBinding = action.TargetBinding
	request.Identity.GraphSHA256 = strings.Repeat("c", 64)
	request.Identity.ID = request.Identity.CanonicalID()
	if err := request.ValidateForDispatch(definition, definition.Graph.Routes[0].ID, &snapshot, "tenant-1", "session-1", 1, now); err == nil || !strings.Contains(err.Error(), "graph digest does not match the frozen definition") {
		t.Fatalf("effect must be pinned to the current frozen graph: %v", err)
	}
	request.Identity.GraphSHA256 = definition.DigestInputs.Graph
	request.Identity.SessionID = "another-session"
	request.Identity.ID = request.Identity.CanonicalID()
	if err := request.ValidateForDispatch(definition, definition.Graph.Routes[0].ID, &snapshot, "tenant-1", "session-1", 1, now); err == nil || !strings.Contains(err.Error(), "scope does not match") {
		t.Fatalf("effect scope must match its authorized candidate snapshot: %v", err)
	}
}

func TestSessionPinRequiresQualifiedLocaleAndVoiceCapabilities(t *testing.T) {
	definition := loadReviewDefinition(t)
	qualifyDefinitionForTest(t, &definition)
	if err := definition.ValidateForActivation(); err != nil {
		t.Fatalf("test-only qualified definition should be activation-ready: %v", err)
	}
	pin := sessionPinForTest(t, definition)
	if err := pin.ValidateFor(definition); err != nil {
		t.Fatalf("qualified session pin should validate: %v", err)
	}
	pin.GraphSHA256 = strings.Repeat("a", 64)
	if err := pin.ValidateFor(definition); err == nil || !strings.Contains(err.Error(), "session pin graph digest does not match the frozen definition") {
		t.Fatalf("unrelated component digests must not pass as a session pin: %v", err)
	}
	pin = sessionPinForTest(t, definition)
	pin.ReleaseSHA256 = strings.Repeat("a", 64)
	if err := pin.ValidateFor(definition); err == nil || !strings.Contains(err.Error(), "session pin release digest does not match the frozen definition") {
		t.Fatalf("session pin release digest must match actual frozen inputs: %v", err)
	}
	pin = sessionPinForTest(t, definition)
	pin.Voice.Capabilities[VoiceDTMFInput] = CapabilityUnknown
	pin.VoiceSHA256, _ = CanonicalVoiceCapabilitiesSHA256(pin.Voice)
	if err := pin.ValidateFor(definition); err == nil || !strings.Contains(err.Error(), "not qualified as supported") {
		t.Fatalf("unknown required voice capability should fail closed, got %v", err)
	}
}

func TestSessionPinBindsLocaleAndVoiceRevision(t *testing.T) {
	definition := loadReviewDefinition(t)
	qualifyDefinitionForTest(t, &definition)
	pin := sessionPinForTest(t, definition)
	pin.Locale = "fr"
	if err := pin.ValidateFor(definition); err == nil || !strings.Contains(err.Error(), "locale is not enabled") {
		t.Fatalf("session pin cannot select an unqualified locale: %v", err)
	}
	pin = sessionPinForTest(t, definition)
	pin.Voice.Revision = "profile-revision-2"
	pin.VoiceSHA256, _ = CanonicalVoiceCapabilitiesSHA256(pin.Voice)
	if err := pin.ValidateFor(definition); err == nil || !strings.Contains(err.Error(), "does not match the qualified voice profile gate") {
		t.Fatalf("session pin must use the qualified voice revision: %v", err)
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
	request := EffectRequest{
		Identity: identity, ToolID: "connect", Kind: EffectConnect, TargetBinding: "selected_candidate_id",
		TargetOpaqueID: "candidate-1", CandidateSetSHA256: strings.Repeat("b", 64), ReauthorizeAtExecution: true,
	}
	action := ActStep{ID: identity.ActionID, ToolID: request.ToolID, Kind: request.Kind, TargetBinding: request.TargetBinding, ReauthorizeAtExecution: true, ReleasesCallOwnership: true}
	from := GraphOwnership{
		State: OwnershipReceiptPending, PendingEffectID: identity.ID, PendingTargetBinding: request.TargetBinding,
		PendingTargetOpaqueID: request.TargetOpaqueID, PendingCandidateSetSHA256: request.CandidateSetSHA256,
	}
	accepted := EffectReceipt{
		Identity: identity, Status: EffectAccepted, ReceiptID: "accepted-1", TargetOpaqueID: request.TargetOpaqueID, ObservedAt: now,
	}
	if accepted.ReleasesCallOwnership(true, request.TargetOpaqueID) {
		t.Fatal("accepted effect must not release call ownership")
	}
	if err := ValidateOwnershipTransition(from, GraphOwnership{State: OwnershipReleased, ReleaseReceiptID: "accepted-1"}, &accepted, &request, &action); err == nil {
		t.Fatal("accepted receipt must not release call ownership")
	}
	confirmed := EffectReceipt{
		Identity: identity, Status: EffectConfirmed, ReceiptID: "confirmed-1", ProviderRequestID: "provider-request-1",
		TargetOpaqueID: request.TargetOpaqueID, ObservedAt: now,
	}
	if !confirmed.ReleasesCallOwnership(true, request.TargetOpaqueID) {
		t.Fatal("confirmed receipt should release call ownership for the declared target")
	}
	wrongTarget := confirmed
	wrongTarget.TargetOpaqueID = "candidate-other"
	if wrongTarget.ReleasesCallOwnership(true, request.TargetOpaqueID) {
		t.Fatal("receipt for a different target must not release ownership")
	}
	to := GraphOwnership{State: OwnershipReleased, ReleaseReceiptID: confirmed.ReceiptID}
	if err := ValidateOwnershipTransition(from, to, &confirmed, &request, &action); err != nil {
		t.Fatalf("matching confirmed receipt should release call ownership: %v", err)
	}
	noReleaseAction := action
	noReleaseAction.ReleasesCallOwnership = false
	if err := ValidateOwnershipTransition(from, to, &confirmed, &request, &noReleaseAction); err == nil {
		t.Fatal("an effect not declared to release call ownership must not release it")
	}
	if err := ValidateOwnershipTransition(from, to, &wrongTarget, &request, &action); err == nil {
		t.Fatal("receipt target must match the pending intended target")
	}
	fromWithoutTarget := from
	fromWithoutTarget.PendingTargetOpaqueID = ""
	if err := ValidateOwnershipTransition(fromWithoutTarget, to, &confirmed, &request, &action); err == nil {
		t.Fatal("release must retain the intended target in pending ownership")
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
			TerminalAction    string         `json:"terminalAction"`
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
	if fifth := trace.Steps[4]; fifth.EffectStatus != "accepted" || fifth.ReceiptID == "" || fifth.Ownership != "call_owned" {
		t.Fatalf("accepted handoff must retain call ownership: %#v", fifth)
	}
	if sixth := trace.Steps[5]; sixth.EffectStatus != "confirmed" || sixth.ReceiptID == "" || sixth.Ownership != "call_released" || sixth.TerminalAction != string(TerminalReleaseCallOwnership) {
		t.Fatalf("confirmed handoff receipt should release call ownership: %#v", sixth)
	}
	acceptedStep, confirmedStep := trace.Steps[4], trace.Steps[5]
	if err := acceptedStep.EffectIdentity.Validate(); err != nil {
		t.Fatalf("fixture effect ID must be canonical: %v", err)
	}
	if acceptedStep.EffectIdentity.ID != confirmedStep.EffectIdentity.ID || acceptedStep.TargetOpaqueID != confirmedStep.TargetOpaqueID {
		t.Fatal("accepted and confirmed receipts must refer to the same effect and opaque target")
	}
	request := EffectRequest{
		Identity: acceptedStep.EffectIdentity, ToolID: "connect", Kind: EffectConnect,
		TargetBinding: "selected_candidate_id", TargetOpaqueID: acceptedStep.TargetOpaqueID,
		CandidateSetSHA256: strings.Repeat("b", 64), ReauthorizeAtExecution: true,
	}
	action := ActStep{
		ID: request.Identity.ActionID, ToolID: request.ToolID, Kind: request.Kind,
		TargetBinding: request.TargetBinding, ReauthorizeAtExecution: true, ReleasesCallOwnership: true,
	}
	from := GraphOwnership{
		State: OwnershipReceiptPending, PendingEffectID: acceptedStep.EffectIdentity.ID, PendingTargetBinding: request.TargetBinding,
		PendingTargetOpaqueID: acceptedStep.TargetOpaqueID, PendingCandidateSetSHA256: request.CandidateSetSHA256,
	}
	accepted := EffectReceipt{
		Identity: acceptedStep.EffectIdentity, Status: EffectAccepted, ReceiptID: acceptedStep.ReceiptID,
		ProviderRequestID: acceptedStep.ProviderRequestID, TargetOpaqueID: acceptedStep.TargetOpaqueID, ObservedAt: acceptedStep.ObservedAt,
	}
	confirmed := EffectReceipt{
		Identity: confirmedStep.EffectIdentity, Status: EffectConfirmed, ReceiptID: confirmedStep.ReceiptID,
		ProviderRequestID: confirmedStep.ProviderRequestID, TargetOpaqueID: confirmedStep.TargetOpaqueID, ObservedAt: confirmedStep.ObservedAt,
	}
	if accepted.ReleasesCallOwnership(true, acceptedStep.TargetOpaqueID) {
		t.Fatal("fixture accepted event must retain call ownership")
	}
	if err := ValidateOwnershipTransition(from, GraphOwnership{State: OwnershipReleased, ReleaseReceiptID: accepted.ReceiptID}, &accepted, &request, &action); err == nil {
		t.Fatal("fixture accepted event must not release ownership")
	}
	if err := ValidateOwnershipTransition(from, GraphOwnership{State: OwnershipReleased, ReleaseReceiptID: confirmed.ReceiptID}, &confirmed, &request, &action); err != nil {
		t.Fatalf("fixture confirmed receipt should release call ownership: %v", err)
	}
}

func TestLocalEndGraphDoesNotReleaseCallOwnership(t *testing.T) {
	definition := loadReviewDefinition(t)
	route := &definition.Graph.Routes[0]
	route.Act[0].ReleasesCallOwnership = false
	for index := range route.Next {
		transition := &route.Next[index]
		if transition.Source == SourceEffect && transition.Outcome == OutcomeConfirmed {
			transition.Target = Target{Terminal: TerminalEndGraph}
		}
	}
	refreshDefinitionDigestsForTest(t, &definition)
	if err := definition.ValidateForReview(); err != nil {
		t.Fatalf("local graph termination should remain valid without releasing call ownership: %v", err)
	}
	owned := GraphOwnership{State: OwnershipOwned}
	if err := ValidateOwnershipTransition(owned, owned, nil, nil, nil); err != nil {
		t.Fatalf("local end_graph must preserve parent call ownership: %v", err)
	}
}

func TestRejectsOwnershipReleaseOutsideConfirmedReceiptTransition(t *testing.T) {
	t.Run("input outcome cannot release", func(t *testing.T) {
		definition := loadReviewDefinition(t)
		route := &definition.Graph.Routes[0]
		for index := range route.Next {
			if route.Next[index].Source == SourceInput && route.Next[index].Outcome == OutcomeNoInput {
				route.Next[index].Target = Target{Terminal: TerminalReleaseCallOwnership}
			}
		}
		refreshDefinitionDigestsForTest(t, &definition)
		requireValidationError(t, definition.ValidateForReview(), "cannot release call ownership on input/no_input")
	})
	t.Run("fallback cannot release", func(t *testing.T) {
		definition := loadReviewDefinition(t)
		definition.Graph.Routes[0].Fallback = Target{Terminal: TerminalReleaseCallOwnership}
		refreshDefinitionDigestsForTest(t, &definition)
		requireValidationError(t, definition.ValidateForReview(), "fallback cannot release call ownership")
	})
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

func qualifyDefinitionForTest(t *testing.T, definition *Definition) {
	t.Helper()
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
			gate.Value = "qualified-profile@profile-revision-1"
		}
	}
	definition.Graph.Locale.EnabledLocales = []string{"en"}
	refreshDefinitionDigestsForTest(t, definition)
}
