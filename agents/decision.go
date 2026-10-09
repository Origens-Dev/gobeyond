package agents

import (
	"encoding/json"
	"fmt"
	"sort"

	decisionv1 "github.com/Origens-Dev/gobeyond/agents/decisioncontract/v1"
)

// DecisionConfig is the compiler-visible configuration for a file-based
// decision agent. The graph contract supplies shared authority, policy,
// locale, message-family metadata, and normalization inputs. Routes and prompt
// bytes are discovered from RoutesDir by the project compiler.
//
// Tools deliberately reuse the same authored AITool declarations as DefineAI;
// this type does not register or execute them.
type DecisionConfig struct {
	Config     Config
	Tools      map[string]AITool
	RoutesDir  string
	Definition decisionv1.Definition
}

// DecisionDefinition is a static definition marker. It has no Invoke method
// and does not install a runtime adapter.
type DecisionDefinition struct {
	Config   Config
	Decision DecisionConfig
	Slots    Slots
}

// DefineDecision declares a compiler-visible decision graph. The compiler
// reads this literal and the route-local files without executing it.
func DefineDecision(config DecisionConfig, slots ...Slots) DecisionDefinition {
	definition := DecisionDefinition{Config: config.Config, Decision: config}
	if len(slots) > 0 {
		definition.Slots = slots[0]
	}
	return definition
}

// FreezeDecisionManifest canonicalizes and validates a review-ready frozen
// decision definition, then returns its stable JSON bytes and release digest.
// Both the project compiler and future runtime adapters use this same boundary
// so they cannot disagree about manifest bytes or digest inputs.
func FreezeDecisionManifest(definition decisionv1.Definition) (decisionv1.Definition, []byte, string, error) {
	definition = canonicalDecisionDefinition(definition)
	if definition.SchemaVersion == "" {
		definition.SchemaVersion = decisionv1.SchemaVersion
	}
	if definition.SchemaVersion != decisionv1.SchemaVersion {
		return decisionv1.Definition{}, nil, "", fmt.Errorf("unsupported decision schema version %q", definition.SchemaVersion)
	}
	inputs, release, err := definition.CanonicalReleaseDigests()
	if err != nil {
		return decisionv1.Definition{}, nil, "", err
	}
	definition.DigestInputs = inputs
	definition.ReleaseSHA256 = release
	if err := definition.ValidateForReview(); err != nil {
		return decisionv1.Definition{}, nil, "", err
	}
	raw, err := json.MarshalIndent(definition, "", "  ")
	if err != nil {
		return decisionv1.Definition{}, nil, "", fmt.Errorf("marshal frozen decision manifest: %w", err)
	}
	raw = append(raw, '\n')
	return definition, raw, release, nil
}

func canonicalDecisionDefinition(definition decisionv1.Definition) decisionv1.Definition {
	definition.PolicyGates = append([]decisionv1.PolicyGate(nil), definition.PolicyGates...)
	sort.Slice(definition.PolicyGates, func(i, j int) bool { return definition.PolicyGates[i].ID < definition.PolicyGates[j].ID })

	graph := &definition.Graph
	graph.Routes = append([]decisionv1.Route(nil), graph.Routes...)
	sort.Slice(graph.Routes, func(i, j int) bool { return graph.Routes[i].ID < graph.Routes[j].ID })
	for i := range graph.Routes {
		route := &graph.Routes[i]
		route.UseTools = sortedStrings(route.UseTools)
		route.UseServices = sortedStrings(route.UseServices)
		route.UseBindings = sortedStrings(route.UseBindings)
		if route.Say != nil {
			say := *route.Say
			say.Families = sortedStrings(say.Families)
			say.ArgumentBindings = append([]decisionv1.MessageArgumentBinding(nil), say.ArgumentBindings...)
			sort.Slice(say.ArgumentBindings, func(i, j int) bool { return say.ArgumentBindings[i].Name < say.ArgumentBindings[j].Name })
			route.Say = &say
		}
		if route.Listen != nil {
			listen := *route.Listen
			listen.Accept = append([]decisionv1.InputModality(nil), listen.Accept...)
			sort.Slice(listen.Accept, func(i, j int) bool { return listen.Accept[i] < listen.Accept[j] })
			route.Listen = &listen
		}
		route.Act = append([]decisionv1.ActStep(nil), route.Act...)
		route.Next = append([]decisionv1.OutcomeTransition(nil), route.Next...)
		sort.Slice(route.Next, func(i, j int) bool {
			if route.Next[i].Source != route.Next[j].Source {
				return route.Next[i].Source < route.Next[j].Source
			}
			return route.Next[i].Outcome < route.Next[j].Outcome
		})
	}
	graph.RetryGroups = append([]decisionv1.RetryGroup(nil), graph.RetryGroups...)
	sort.Slice(graph.RetryGroups, func(i, j int) bool { return graph.RetryGroups[i].ID < graph.RetryGroups[j].ID })
	graph.Messages = append([]decisionv1.MessageFamily(nil), graph.Messages...)
	sort.Slice(graph.Messages, func(i, j int) bool { return graph.Messages[i].ID < graph.Messages[j].ID })
	for i := range graph.Messages {
		graph.Messages[i].Arguments = append([]decisionv1.MessageArgument(nil), graph.Messages[i].Arguments...)
		sort.Slice(graph.Messages[i].Arguments, func(a, b int) bool { return graph.Messages[i].Arguments[a].Name < graph.Messages[i].Arguments[b].Name })
	}
	graph.Authority.Tools = append([]decisionv1.ToolGrant(nil), graph.Authority.Tools...)
	sort.Slice(graph.Authority.Tools, func(i, j int) bool { return graph.Authority.Tools[i].ID < graph.Authority.Tools[j].ID })
	graph.Authority.Services = append([]decisionv1.ServiceGrant(nil), graph.Authority.Services...)
	sort.Slice(graph.Authority.Services, func(i, j int) bool { return graph.Authority.Services[i].ID < graph.Authority.Services[j].ID })
	graph.Authority.Bindings = append([]decisionv1.BindingGrant(nil), graph.Authority.Bindings...)
	sort.Slice(graph.Authority.Bindings, func(i, j int) bool { return graph.Authority.Bindings[i].ID < graph.Authority.Bindings[j].ID })
	graph.Locale.EnabledLocales = sortedStrings(graph.Locale.EnabledLocales)
	graph.RequiredVoiceCapabilities = append([]decisionv1.VoiceCapabilityName(nil), graph.RequiredVoiceCapabilities...)
	sort.Slice(graph.RequiredVoiceCapabilities, func(i, j int) bool { return graph.RequiredVoiceCapabilities[i] < graph.RequiredVoiceCapabilities[j] })
	return definition
}

func sortedStrings(values []string) []string {
	if values == nil {
		return nil
	}
	result := append([]string{}, values...)
	sort.Strings(result)
	return result
}
