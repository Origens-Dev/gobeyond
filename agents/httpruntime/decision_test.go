package httpruntime

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Origens-Dev/gobeyond/agents"
	decisionv1 "github.com/Origens-Dev/gobeyond/agents/decisioncontract/v1"
)

type decisionAdapterFake struct {
	config     agents.Config
	definition decisionv1.Definition
	starts     []StartCall
	responds   []RespondCall
	cancels    []CancelCall
	contexts   []context.Context
}

func (adapter *decisionAdapterFake) Config() agents.Config { return adapter.config }

func (adapter *decisionAdapterFake) DecisionDefinition() decisionv1.Definition {
	return adapter.definition
}

func (adapter *decisionAdapterFake) Start(ctx context.Context, call StartCall, _ EventEmitter) error {
	adapter.contexts = append(adapter.contexts, ctx)
	adapter.starts = append(adapter.starts, call)
	return nil
}

func (adapter *decisionAdapterFake) Respond(ctx context.Context, call RespondCall, _ EventEmitter) error {
	adapter.contexts = append(adapter.contexts, ctx)
	adapter.responds = append(adapter.responds, call)
	return nil
}

func (adapter *decisionAdapterFake) Cancel(ctx context.Context, call CancelCall, _ EventEmitter) error {
	adapter.contexts = append(adapter.contexts, ctx)
	adapter.cancels = append(adapter.cancels, call)
	return nil
}

var _ DecisionAdapter = (*decisionAdapterFake)(nil)

func TestRegisterDecisionPreservesFrozenPromptLocaleAndLifecycleCalls(t *testing.T) {
	definition := decisionDefinitionFixture(t, true)
	registry := NewRegistry()
	var factoryDefinition agents.DecisionDefinition
	factoryCalls := 0
	err := RegisterDecision(registry, " operator ", definition, func(got agents.DecisionDefinition) (DecisionAdapter, error) {
		factoryCalls++
		factoryDefinition = got
		return &decisionAdapterFake{config: got.Config, definition: got.Decision.Definition}, nil
	})
	if err != nil {
		t.Fatalf("RegisterDecision error = %v", err)
	}
	if factoryCalls != 1 {
		t.Fatalf("factory calls = %d, want 1", factoryCalls)
	}
	if factoryDefinition.Decision.Definition.ReleaseSHA256 != definition.Decision.Definition.ReleaseSHA256 {
		t.Fatal("factory did not receive the compiled frozen release")
	}
	if !reflect.DeepEqual(factoryDefinition.Decision.Definition.Graph.Locale, definition.Decision.Definition.Graph.Locale) {
		t.Fatal("factory locale policy differs from the compiled manifest")
	}
	if !reflect.DeepEqual(factoryDefinition.Decision.Definition.Graph.Messages, definition.Decision.Definition.Graph.Messages) {
		t.Fatal("factory prompt bytes or message identities differ from the compiled manifest")
	}

	registered, ok := registry.Lookup("operator")
	if !ok {
		t.Fatal("decision adapter was not registered under the trimmed agent ID")
	}
	adapter, ok := registered.(DecisionAdapter)
	if !ok {
		t.Fatalf("registered adapter type %T does not implement DecisionAdapter", registered)
	}
	fake := registered.(*decisionAdapterFake)
	ctx := context.WithValue(context.Background(), struct{}{}, "call-context")
	start := StartCall{
		Session: agents.Session{ID: "session-1", AgentID: "operator", Metadata: map[string]string{"locale": "en"}},
		Run:     agents.Run{ID: "run-1", SessionID: "session-1", AgentID: "operator"},
		Actor:   agents.Actor{ID: "actor-1", Kind: "user"},
		Input:   json.RawMessage(`{"text":"hello"}`),
	}
	respond := RespondCall{
		Session:  start.Session,
		Run:      start.Run,
		Actor:    start.Actor,
		Response: json.RawMessage(`{"input":"yes"}`),
	}
	cancel := CancelCall{Session: start.Session, Run: start.Run, Actor: start.Actor, Reason: "caller ended"}
	if err := adapter.Start(ctx, start, nil); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Respond(ctx, respond, nil); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Cancel(ctx, cancel, nil); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fake.starts, []StartCall{start}) || !reflect.DeepEqual(fake.responds, []RespondCall{respond}) || !reflect.DeepEqual(fake.cancels, []CancelCall{cancel}) {
		t.Fatalf("lifecycle calls were changed: starts=%#v responds=%#v cancels=%#v", fake.starts, fake.responds, fake.cancels)
	}
	if len(fake.contexts) != 3 {
		t.Fatalf("context calls = %d, want 3", len(fake.contexts))
	}
	for _, got := range fake.contexts {
		if got != ctx || got.Value(struct{}{}) != "call-context" {
			t.Fatal("adapter lifecycle call did not retain the caller context")
		}
	}
}

func TestRegisterDecisionRejectsUnresolvedAuthorityBeforeFactory(t *testing.T) {
	definition := decisionDefinitionFixture(t, false)
	registry := NewRegistry()
	factoryCalls := 0
	err := RegisterDecision(registry, "operator", definition, func(agents.DecisionDefinition) (DecisionAdapter, error) {
		factoryCalls++
		return nil, nil
	})
	if err == nil || !strings.Contains(err.Error(), "not activation-ready") {
		t.Fatalf("RegisterDecision error = %v, want activation gate failure", err)
	}
	for _, gate := range []decisionv1.GateKind{
		decisionv1.GateCallerAuthority,
		decisionv1.GateJevServicePath,
		decisionv1.GateSessionAdapter,
		decisionv1.GatePromptRuntimeParity,
	} {
		if !strings.Contains(err.Error(), string(gate)) {
			t.Errorf("activation error %q does not retain unresolved gate %q", err, gate)
		}
	}
	if factoryCalls != 0 {
		t.Fatalf("factory calls = %d, want 0 while gates are unresolved", factoryCalls)
	}
	if _, ok := registry.Lookup("operator"); ok {
		t.Fatal("unqualified decision agent was registered")
	}
}

func TestRegisterDecisionRejectsPromptAndLocaleDrift(t *testing.T) {
	base := decisionDefinitionFixture(t, true)
	tests := []struct {
		name   string
		change func(*decisionv1.Definition)
	}{
		{
			name: "prompt bytes",
			change: func(definition *decisionv1.Definition) {
				definition.Graph.Messages[0].Variants["en"] += " "
			},
		},
		{
			name: "enabled locale",
			change: func(definition *decisionv1.Definition) {
				definition.Graph.Locale.EnabledLocales = []string{"fr"}
				for index := range definition.PolicyGates {
					if definition.PolicyGates[index].Kind == decisionv1.GateLocaleProfile {
						definition.PolicyGates[index].Value = "fr"
					}
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var altered decisionv1.Definition
			encoded, err := json.Marshal(base.Decision.Definition)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(encoded, &altered); err != nil {
				t.Fatal(err)
			}
			test.change(&altered)
			altered, _, _, err = agents.FreezeDecisionManifest(altered)
			if err != nil {
				t.Fatalf("freeze altered test definition: %v", err)
			}
			registry := NewRegistry()
			err = RegisterDecision(registry, "operator", base, func(got agents.DecisionDefinition) (DecisionAdapter, error) {
				return &decisionAdapterFake{config: got.Config, definition: altered}, nil
			})
			if err == nil || !strings.Contains(err.Error(), "does not match the compiled release") {
				t.Fatalf("RegisterDecision error = %v, want frozen release mismatch", err)
			}
			if _, ok := registry.Lookup("operator"); ok {
				t.Fatal("adapter with changed frozen content was registered")
			}
		})
	}
}

func TestRegisterDecisionConcurrentRegistrationIsSingleAndSafe(t *testing.T) {
	definition := decisionDefinitionFixture(t, true)
	registry := NewRegistry()
	const attempts = 20
	var successes atomic.Int32
	var failures atomic.Int32
	var wait sync.WaitGroup
	start := make(chan struct{})
	for range attempts {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			err := RegisterDecision(registry, "operator", definition, func(got agents.DecisionDefinition) (DecisionAdapter, error) {
				return &decisionAdapterFake{config: got.Config, definition: got.Decision.Definition}, nil
			})
			if err == nil {
				successes.Add(1)
			} else {
				failures.Add(1)
			}
		}()
	}
	close(start)
	wait.Wait()
	if successes.Load() != 1 || failures.Load() != attempts-1 {
		t.Fatalf("concurrent registrations = %d successes, %d failures; want 1 and %d", successes.Load(), failures.Load(), attempts-1)
	}
	if _, ok := registry.Lookup("operator"); !ok {
		t.Fatal("successful concurrent registration is missing")
	}
}

func decisionDefinitionFixture(t *testing.T, qualified bool) agents.DecisionDefinition {
	t.Helper()
	data, err := os.ReadFile("../decisioncontract/v1/testdata/review-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	var contract decisionv1.Definition
	if err := json.Unmarshal(data, &contract); err != nil {
		t.Fatal(err)
	}
	if qualified {
		qualifyDecisionDefinitionForTest(&contract)
	}
	frozen, _, _, err := agents.FreezeDecisionManifest(contract)
	if err != nil {
		t.Fatalf("freeze decision test fixture: %v", err)
	}
	config := agents.Config{TaskQueue: "decision", Durable: true, Public: true}
	return agents.DecisionDefinition{
		Config: config,
		Decision: agents.DecisionConfig{
			Config:     config,
			Definition: frozen,
		},
	}
}

// qualifyDecisionDefinitionForTest creates only synthetic test evidence so the
// registration success path can be exercised. Compiler-produced definitions
// keep these gates and budget ceilings unresolved until their owners qualify
// the missing authority and runtime parity.
func qualifyDecisionDefinitionForTest(definition *decisionv1.Definition) {
	const ceiling = uint64(100)
	budgetGateIDs := map[string]bool{}
	for dimension, bound := range definition.Graph.Authority.Budgets {
		bound.Value = new(uint64)
		*bound.Value = ceiling
		budgetGateIDs[bound.GateID] = true
		definition.Graph.Authority.Budgets[dimension] = bound
	}
	for index := range definition.Graph.RetryGroups {
		group := &definition.Graph.RetryGroups[index]
		for _, bound := range []*decisionv1.Bound{&group.MaxReprompts, &group.MaxNoInputReprompts, &group.MaxNoMatchReprompts, &group.MaxAmbiguousReprompts} {
			bound.Value = new(uint64)
			*bound.Value = ceiling
			budgetGateIDs[bound.GateID] = true
		}
	}
	for index := range definition.PolicyGates {
		gate := &definition.PolicyGates[index]
		gate.Status = decisionv1.GateQualified
		gate.EvidenceRef = "synthetic-test-only"
		gate.UnresolvedReason = ""
		switch {
		case budgetGateIDs[gate.ID]:
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
