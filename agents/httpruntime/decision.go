package httpruntime

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/Origens-Dev/gobeyond/agents"
	decisionv1 "github.com/Origens-Dev/gobeyond/agents/decisioncontract/v1"
)

// DecisionAdapter is the stateful Start/Respond/Cancel seam for a frozen
// decision definition. Implementations use the existing session calls and
// ordered event emitter; graph effects and IDs do not grant mutation authority.
type DecisionAdapter interface {
	Adapter
	DecisionDefinition() decisionv1.Definition
}

// DecisionAdapterFactory constructs a dormant adapter shell from the
// compiler-frozen agent definition. It is called only after activation-gate
// validation and an agent-ID reservation have succeeded. It must not start
// goroutines, invoke providers, open external resources, or acquire mutation
// authority. If it returns an error, it must release any temporary resources
// before returning. Runtime resources belong to later lifecycle calls.
type DecisionAdapterFactory func(agents.DecisionDefinition) (DecisionAdapter, error)

// DecisionRegisterer atomically reserves a decision agent ID before invoking
// its constructor, then installs the returned adapter on success. Conflicting
// registrations must fail without invoking construct. Implementations must
// not hold registry-wide locks while calling construct.
//
// The built-in MemoryRegistry implements this contract. RegisterDecision
// rejects registries that only implement Registerer because they cannot
// guarantee that concurrent duplicates avoid constructing losing adapters.
type DecisionRegisterer interface {
	RegisterDecisionAdapter(agentID string, construct func() (DecisionAdapter, error)) error
}

// RegisterDecision is an explicit opt-in registration seam for a decision
// definition. Compiler-generated site registries intentionally do not call it.
// It validates the frozen manifest and activation gates before constructing an
// adapter, then checks that the adapter keeps the exact prompt, locale and
// graph release. It does not resolve external authority, create policy, or
// implement reducer, speech, service, effect, or receipt behavior.
func RegisterDecision(registry Registerer, agentID string, definition agents.DecisionDefinition, factory DecisionAdapterFactory) error {
	if isNilInterfaceValue(registry) {
		return errors.New("agent registry is required")
	}
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return errors.New("agent ID is required")
	}
	if factory == nil {
		return errors.New("decision adapter factory is required")
	}
	decisionRegistry, ok := registry.(DecisionRegisterer)
	if !ok || isNilInterfaceValue(decisionRegistry) {
		return errors.New("agent registry does not support reserved decision registration")
	}
	if definition.Decision.Config != definition.Config {
		return fmt.Errorf("decision agent %q has mismatched definition config", agentID)
	}
	if err := definition.Decision.Definition.ValidateForReview(); err != nil {
		return fmt.Errorf("decision agent %q has an invalid frozen definition: %w", agentID, err)
	}
	frozen, _, release, err := agents.FreezeDecisionManifest(definition.Decision.Definition)
	if err != nil {
		return fmt.Errorf("decision agent %q: freeze definition: %w", agentID, err)
	}
	if err := frozen.ValidateForActivation(); err != nil {
		return fmt.Errorf("decision agent %q is not activation-ready: %w", agentID, err)
	}
	definition.Decision.Definition = frozen

	return decisionRegistry.RegisterDecisionAdapter(agentID, func() (DecisionAdapter, error) {
		adapter, err := factory(definition)
		if err != nil {
			return nil, fmt.Errorf("decision agent %q: construct adapter: %w", agentID, err)
		}
		if isNilInterfaceValue(adapter) {
			return nil, fmt.Errorf("decision agent %q: adapter factory returned nil", agentID)
		}
		if adapter.Config() != definition.Config {
			return nil, fmt.Errorf("decision agent %q adapter config does not match its frozen definition", agentID)
		}
		adapterDefinition := adapter.DecisionDefinition()
		if err := adapterDefinition.ValidateForReview(); err != nil {
			return nil, fmt.Errorf("decision agent %q adapter has an invalid frozen definition: %w", agentID, err)
		}
		_, _, adapterRelease, err := agents.FreezeDecisionManifest(adapterDefinition)
		if err != nil {
			return nil, fmt.Errorf("decision agent %q: freeze adapter definition: %w", agentID, err)
		}
		if adapterRelease != release {
			return nil, fmt.Errorf("decision agent %q adapter definition does not match the compiled release", agentID)
		}
		return adapter, nil
	})
}

func isNilInterfaceValue(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
