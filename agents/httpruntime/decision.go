package httpruntime

import (
	"errors"
	"fmt"
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

// DecisionAdapterFactory constructs an adapter from the compiler-frozen agent
// definition. It is called only after the definition passes activation-gate
// validation.
type DecisionAdapterFactory func(agents.DecisionDefinition) (DecisionAdapter, error)

// RegisterDecision is an explicit opt-in registration seam for a decision
// definition. Compiler-generated site registries intentionally do not call it.
// It validates the frozen manifest and activation gates before constructing an
// adapter, then checks that the adapter keeps the exact prompt, locale and
// graph release. It does not resolve external authority, create policy, or
// implement reducer, speech, service, effect, or receipt behavior.
func RegisterDecision(registry Registerer, agentID string, definition agents.DecisionDefinition, factory DecisionAdapterFactory) error {
	if registry == nil {
		return errors.New("agent registry is required")
	}
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return errors.New("agent ID is required")
	}
	if factory == nil {
		return errors.New("decision adapter factory is required")
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

	adapter, err := factory(definition)
	if err != nil {
		return fmt.Errorf("decision agent %q: construct adapter: %w", agentID, err)
	}
	if adapter == nil {
		return fmt.Errorf("decision agent %q: adapter factory returned nil", agentID)
	}
	if adapter.Config() != definition.Config {
		return fmt.Errorf("decision agent %q adapter config does not match its frozen definition", agentID)
	}
	adapterDefinition := adapter.DecisionDefinition()
	if err := adapterDefinition.ValidateForReview(); err != nil {
		return fmt.Errorf("decision agent %q adapter has an invalid frozen definition: %w", agentID, err)
	}
	_, _, adapterRelease, err := agents.FreezeDecisionManifest(adapterDefinition)
	if err != nil {
		return fmt.Errorf("decision agent %q: freeze adapter definition: %w", agentID, err)
	}
	if adapterRelease != release {
		return fmt.Errorf("decision agent %q adapter definition does not match the compiled release", agentID)
	}
	return registry.Register(agentID, adapter)
}
