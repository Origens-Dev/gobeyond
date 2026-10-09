package v1

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// CanonicalizationVersion pins the byte-level rules used for release digests.
const CanonicalizationVersion = "decision.canonical-json/v1"

// CanonicalReleaseDigests computes component digests from the frozen
// Definition content, then hashes their fixed-order envelope into one release
// digest. Stored digest fields are deliberately ignored during computation.
func (definition Definition) CanonicalReleaseDigests() (ReleaseDigestInputs, string, error) {
	graph := canonicalGraph(definition.Graph)
	inputs := ReleaseDigestInputs{}
	var err error
	if inputs.Graph, err = digestCanonical(graphShapeProjection{
		ID: definition.Graph.ID, Entry: definition.Graph.Entry, Fallback: definition.Graph.Fallback,
		Routes: graph.Routes, RetryGroups: graph.RetryGroups,
	}); err != nil {
		return ReleaseDigestInputs{}, "", err
	}
	if inputs.Prompts, err = digestCanonical(graph.Messages); err != nil {
		return ReleaseDigestInputs{}, "", err
	}
	if inputs.Bindings, err = digestCanonical(graph.Authority.Bindings); err != nil {
		return ReleaseDigestInputs{}, "", err
	}
	if inputs.Normalization, err = digestCanonical(graph.NormalizationRulesDigest); err != nil {
		return ReleaseDigestInputs{}, "", err
	}
	if inputs.Authority, err = digestCanonical(authorityDigestProjection{
		ParentManifestSHA256: graph.Authority.ParentManifestSHA256,
		Tools:                graph.Authority.Tools,
		Services:             graph.Authority.Services,
		Budgets:              graph.Authority.Budgets,
	}); err != nil {
		return ReleaseDigestInputs{}, "", err
	}
	if inputs.Policy, err = digestCanonical(canonicalGates(definition.PolicyGates)); err != nil {
		return ReleaseDigestInputs{}, "", err
	}
	if inputs.LocaleAndVoice, err = digestCanonical(localeVoiceProjection{
		Locale:                    graph.Locale,
		RequiredVoiceCapabilities: graph.RequiredVoiceCapabilities,
	}); err != nil {
		return ReleaseDigestInputs{}, "", err
	}
	release, err := digestCanonical(struct {
		Canonicalization string              `json:"canonicalization"`
		SchemaVersion    string              `json:"schemaVersion"`
		Inputs           ReleaseDigestInputs `json:"inputs"`
	}{CanonicalizationVersion, definition.SchemaVersion, inputs})
	if err != nil {
		return ReleaseDigestInputs{}, "", err
	}
	return inputs, release, nil
}

// CanonicalVoiceCapabilitiesSHA256 pins the resolved voice profile and its
// capability evidence in a session. Locale lists are treated as sets; formats
// retain order because a producer may prefer one wire format over another.
func CanonicalVoiceCapabilitiesSHA256(capabilities VoiceCapabilities) (string, error) {
	capabilities = canonicalVoiceCapabilities(capabilities)
	return digestCanonical(capabilities)
}

func digestCanonical(value any) (string, error) {
	bytes, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("canonical decision JSON: %w", err)
	}
	digest := sha256.Sum256(bytes)
	return hex.EncodeToString(digest[:]), nil
}

type graphShapeProjection struct {
	ID          string       `json:"id"`
	Entry       RouteID      `json:"entry"`
	Fallback    RouteID      `json:"fallback"`
	Routes      []Route      `json:"routes"`
	RetryGroups []RetryGroup `json:"retryGroups"`
}

type authorityDigestProjection struct {
	ParentManifestSHA256 string                    `json:"parentManifestSha256"`
	Tools                []ToolGrant               `json:"tools"`
	Services             []ServiceGrant            `json:"services"`
	Budgets              map[BudgetDimension]Bound `json:"budgets"`
}

type localeVoiceProjection struct {
	Locale                    LocalePolicy          `json:"locale"`
	RequiredVoiceCapabilities []VoiceCapabilityName `json:"requiredVoiceCapabilities"`
}

func canonicalGraph(graph Graph) Graph {
	graph.Routes = cloneAndSort(graph.Routes, func(a, b Route) bool { return a.ID < b.ID })
	for index := range graph.Routes {
		route := &graph.Routes[index]
		route.UseTools = cloneAndSortStrings(route.UseTools)
		route.UseServices = cloneAndSortStrings(route.UseServices)
		route.UseBindings = cloneAndSortStrings(route.UseBindings)
		if route.Say != nil {
			say := *route.Say
			say.Families = cloneAndSortStrings(say.Families)
			say.ArgumentBindings = cloneAndSort(say.ArgumentBindings, func(a, b MessageArgumentBinding) bool { return a.Name < b.Name })
			route.Say = &say
		}
		if route.Listen != nil {
			listen := *route.Listen
			listen.Accept = cloneAndSort(listen.Accept, func(a, b InputModality) bool { return a < b })
			route.Listen = &listen
		}
		if route.BudgetOverrides != nil {
			copy := make(map[BudgetDimension]Bound, len(route.BudgetOverrides))
			for dimension, bound := range route.BudgetOverrides {
				copy[dimension] = bound
			}
			route.BudgetOverrides = copy
		}
		// Act order is semantic and is intentionally preserved.
		route.Act = appendPreservingNil(route.Act)
		route.Next = cloneAndSort(route.Next, func(a, b OutcomeTransition) bool {
			if a.Source != b.Source {
				return a.Source < b.Source
			}
			return a.Outcome < b.Outcome
		})
	}
	graph.RetryGroups = cloneAndSort(graph.RetryGroups, func(a, b RetryGroup) bool { return a.ID < b.ID })
	graph.Messages = cloneAndSort(graph.Messages, func(a, b MessageFamily) bool { return a.ID < b.ID })
	for index := range graph.Messages {
		graph.Messages[index].Arguments = cloneAndSort(graph.Messages[index].Arguments, func(a, b MessageArgument) bool { return a.Name < b.Name })
		if graph.Messages[index].Variants != nil {
			copy := make(map[string]string, len(graph.Messages[index].Variants))
			for locale, value := range graph.Messages[index].Variants {
				copy[locale] = value
			}
			graph.Messages[index].Variants = copy
		}
	}
	graph.Authority.Tools = cloneAndSort(graph.Authority.Tools, func(a, b ToolGrant) bool { return a.ID < b.ID })
	graph.Authority.Services = cloneAndSort(graph.Authority.Services, func(a, b ServiceGrant) bool { return a.ID < b.ID })
	graph.Authority.Bindings = cloneAndSort(graph.Authority.Bindings, func(a, b BindingGrant) bool { return a.ID < b.ID })
	graph.Locale.EnabledLocales = cloneAndSortStrings(graph.Locale.EnabledLocales)
	graph.RequiredVoiceCapabilities = cloneAndSort(graph.RequiredVoiceCapabilities, func(a, b VoiceCapabilityName) bool { return a < b })
	return graph
}

func canonicalGates(gates []PolicyGate) []PolicyGate {
	return cloneAndSort(gates, func(a, b PolicyGate) bool { return a.ID < b.ID })
}

func canonicalVoiceCapabilities(capabilities VoiceCapabilities) VoiceCapabilities {
	capabilities.SupportedLocales = cloneAndSortStrings(capabilities.SupportedLocales)
	capabilities.Formats = appendPreservingNil(capabilities.Formats)
	if capabilities.Capabilities != nil {
		copy := make(map[VoiceCapabilityName]CapabilityStatus, len(capabilities.Capabilities))
		for name, status := range capabilities.Capabilities {
			copy[name] = status
		}
		capabilities.Capabilities = copy
	}
	return capabilities
}

func cloneAndSort[T any](values []T, less func(T, T) bool) []T {
	if values == nil {
		return nil
	}
	cloned := append([]T{}, values...)
	sort.Slice(cloned, func(i, j int) bool { return less(cloned[i], cloned[j]) })
	return cloned
}

func cloneAndSortStrings(values []string) []string {
	return cloneAndSort(values, func(a, b string) bool { return a < b })
}

func appendPreservingNil[T any](values []T) []T {
	if values == nil {
		return nil
	}
	return append([]T{}, values...)
}
