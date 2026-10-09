package v1

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	sha256Pattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]*$`)
	routePattern      = regexp.MustCompile(`^/[A-Za-z0-9][A-Za-z0-9._/-]*$`)
	argumentPattern   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	localePattern     = regexp.MustCompile(`^[a-z]{2,3}(?:-[a-z0-9]{2,8})*$`)
)

type ValidationErrors []string

func (errs ValidationErrors) Error() string {
	return "decision contract invalid: " + strings.Join(errs, "; ")
}

// DecodeNormalizedEvent strictly decodes one event object. Unknown fields are
// rejected so raw user or provider error data cannot be silently accepted as
// part of the v1 event wire shape.
func DecodeNormalizedEvent(data []byte) (NormalizedEvent, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var event NormalizedEvent
	if err := decoder.Decode(&event); err != nil {
		return NormalizedEvent{}, fmt.Errorf("decode normalized event: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return NormalizedEvent{}, fmt.Errorf("decode normalized event: expected exactly one JSON value")
		}
		return NormalizedEvent{}, fmt.Errorf("decode normalized event trailer: %w", err)
	}
	return event, nil
}

type collector struct{ issues []string }

func (c *collector) add(format string, args ...any) {
	c.issues = append(c.issues, fmt.Sprintf(format, args...))
}

func (c *collector) err() error {
	if len(c.issues) == 0 {
		return nil
	}
	return ValidationErrors(c.issues)
}

func (definition Definition) ValidateForReview() error {
	c := &collector{}
	if definition.SchemaVersion != SchemaVersion {
		c.add("schemaVersion must be %q", SchemaVersion)
	}
	validateDigestInputs(c, definition)
	validateGates(c, definition.PolicyGates)
	validateGraph(c, definition)
	return c.err()
}

// ValidateIdentifier checks the stable identifier syntax shared by decision
// contract fields. A valid identifier is a syntax check only; callers must
// still keep sensitive semantic data out of identifier values.
func ValidateIdentifier(value string) error {
	if !isIdentifier(value) {
		return fmt.Errorf("value must be a stable identifier")
	}
	return nil
}

// ValidateRouteID checks the canonical absolute route-ID syntax.
func ValidateRouteID(value RouteID) error {
	if !isRouteID(value) {
		return fmt.Errorf("value must be a stable absolute route ID")
	}
	return nil
}

// ValidateSHA256 checks for a lowercase hexadecimal SHA-256 digest.
func ValidateSHA256(value string) error {
	if !isSHA256(value) {
		return fmt.Errorf("value must be a lowercase SHA-256 digest")
	}
	return nil
}

// ValidateLocale checks for the canonical locale syntax used by session pins.
func ValidateLocale(value string) error {
	if !isLocale(value) {
		return fmt.Errorf("value must be a canonical locale")
	}
	return nil
}

// ValidateForActivation additionally fails closed while any policy or
// qualification gate remains unresolved. It does not activate a runtime.
func (definition Definition) ValidateForActivation() error {
	if err := definition.ValidateForReview(); err != nil {
		return err
	}
	c := &collector{}
	for _, gate := range definition.PolicyGates {
		if gate.Status == GateUnresolved {
			c.add("activation blocked by unresolved policy gate %q (%s)", gate.ID, gate.Kind)
		}
	}
	if len(definition.Graph.Locale.EnabledLocales) != 1 {
		c.add("V0 activation requires exactly one qualified enabled locale")
	}
	for _, dimension := range RequiredBudgetDimensions {
		bound := definition.Graph.Authority.Budgets[dimension]
		if bound.Value == nil {
			c.add("activation blocked by unresolved budget %q", dimension)
		}
	}
	return c.err()
}

func (pin SessionPin) ValidateFor(definition Definition) error {
	if err := definition.ValidateForActivation(); err != nil {
		return fmt.Errorf("session pin requires an activation-ready definition: %w", err)
	}
	if pin.SchemaVersion != definition.SchemaVersion || pin.SchemaVersion != SchemaVersion {
		return fmt.Errorf("session pin schema version does not match the frozen definition")
	}
	if pin.Generation == 0 || !isLocale(pin.Locale) || !isLocale(pin.PromptVariantLocale) {
		return fmt.Errorf("session pin requires positive generation and canonical locale fields")
	}
	for name, digest := range map[string]string{
		"release":          pin.ReleaseSHA256,
		"graph":            pin.GraphSHA256,
		"prompt families":  pin.PromptFamiliesSHA256,
		"bindings":         pin.BindingsSHA256,
		"normalization":    pin.NormalizationSHA256,
		"authority":        pin.AuthoritySHA256,
		"policy":           pin.PolicySHA256,
		"locale and voice": pin.LocaleAndVoiceSHA256,
		"snapshot":         pin.SnapshotSHA256,
		"voice":            pin.VoiceSHA256,
	} {
		if !isSHA256(digest) {
			return fmt.Errorf("session pin %s digest is invalid", name)
		}
	}
	for name, pair := range map[string][2]string{
		"release":          {pin.ReleaseSHA256, definition.ReleaseSHA256},
		"graph":            {pin.GraphSHA256, definition.DigestInputs.Graph},
		"prompt families":  {pin.PromptFamiliesSHA256, definition.DigestInputs.Prompts},
		"bindings":         {pin.BindingsSHA256, definition.DigestInputs.Bindings},
		"normalization":    {pin.NormalizationSHA256, definition.DigestInputs.Normalization},
		"authority":        {pin.AuthoritySHA256, definition.DigestInputs.Authority},
		"policy":           {pin.PolicySHA256, definition.DigestInputs.Policy},
		"locale and voice": {pin.LocaleAndVoiceSHA256, definition.DigestInputs.LocaleAndVoice},
	} {
		if pair[0] != pair[1] {
			return fmt.Errorf("session pin %s digest does not match the frozen definition", name)
		}
	}
	voiceDigest, err := CanonicalVoiceCapabilitiesSHA256(pin.Voice)
	if err != nil || pin.VoiceSHA256 != voiceDigest {
		return fmt.Errorf("session pin voice digest does not match its capability snapshot")
	}
	enabled := false
	for _, locale := range definition.Graph.Locale.EnabledLocales {
		enabled = enabled || locale == pin.Locale
	}
	if !enabled {
		return fmt.Errorf("session pin locale is not enabled by the graph")
	}
	if gate, exists := policyGateByID(definition.PolicyGates, definition.Graph.Locale.QualificationGateID); !exists || gate.Value != pin.Locale {
		return fmt.Errorf("session pin locale does not match the qualified locale gate")
	}
	var family *MessageFamily
	for index := range definition.Graph.Messages {
		if definition.Graph.Messages[index].ID == pin.PromptFamily {
			family = &definition.Graph.Messages[index]
			break
		}
	}
	if family == nil {
		return fmt.Errorf("session pin references missing prompt family %q", pin.PromptFamily)
	}
	if _, exists := family.Variants[pin.PromptVariantLocale]; !exists {
		return fmt.Errorf("session pin prompt variant locale is missing from its message family")
	}
	if pin.PromptVariantLocale != pin.Locale && pin.PromptVariantLocale != family.FallbackLocale {
		return fmt.Errorf("session pin may use only its requested locale or the whole-message fallback")
	}
	if pin.PromptVariantLocale != pin.Locale && !containsString(pin.Voice.SupportedLocales, pin.PromptVariantLocale) {
		return fmt.Errorf("resolved voice profile does not advertise the selected fallback locale")
	}
	if gate, exists := uniquePolicyGate(definition.PolicyGates, GateVoiceProfile); !exists || gate.Value != pin.Voice.QualificationKey() {
		return fmt.Errorf("session pin voice profile does not match the qualified voice profile gate")
	}
	if err := pin.Voice.ValidateFor(definition.Graph.RequiredVoiceCapabilities, pin.Locale); err != nil {
		return fmt.Errorf("session voice capability snapshot: %w", err)
	}
	return nil
}

func validateDigestInputs(c *collector, definition Definition) {
	inputs := definition.DigestInputs
	for _, component := range []struct{ name, value string }{
		{"graph", inputs.Graph}, {"prompts", inputs.Prompts}, {"bindings", inputs.Bindings},
		{"normalization", inputs.Normalization}, {"authority", inputs.Authority},
		{"policy", inputs.Policy}, {"localeAndVoice", inputs.LocaleAndVoice},
	} {
		name, value := component.name, component.value
		if !isSHA256(value) {
			c.add("digestInputs.%s must be a lowercase SHA-256 digest", name)
		}
	}
	actual, release, err := definition.CanonicalReleaseDigests()
	if err != nil {
		c.add("canonical release digest computation failed: %v", err)
		return
	}
	for _, component := range []struct{ name, got, want string }{
		{"graph", inputs.Graph, actual.Graph}, {"prompts", inputs.Prompts, actual.Prompts},
		{"bindings", inputs.Bindings, actual.Bindings}, {"normalization", inputs.Normalization, actual.Normalization},
		{"authority", inputs.Authority, actual.Authority}, {"policy", inputs.Policy, actual.Policy},
		{"localeAndVoice", inputs.LocaleAndVoice, actual.LocaleAndVoice},
	} {
		if component.got != component.want {
			c.add("digestInputs.%s does not match canonical frozen definition content", component.name)
		}
	}
	if !isSHA256(definition.ReleaseSHA256) {
		c.add("releaseSha256 must be a lowercase SHA-256 digest")
	} else if definition.ReleaseSHA256 != release {
		c.add("releaseSha256 does not match canonical frozen definition content")
	}
}

func validateGates(c *collector, gates []PolicyGate) map[string]PolicyGate {
	byID := make(map[string]PolicyGate, len(gates))
	kinds := make(map[GateKind]int, len(gates))
	for i, gate := range gates {
		path := fmt.Sprintf("policyGates[%d]", i)
		if !isIdentifier(gate.ID) {
			c.add("%s.id is required and must be a stable identifier", path)
		}
		if _, exists := byID[gate.ID]; exists && gate.ID != "" {
			c.add("duplicate policy gate ID %q", gate.ID)
		}
		if gate.ID != "" {
			byID[gate.ID] = gate
		}
		if !isKnownGateKind(gate.Kind) {
			c.add("%s.kind %q is not recognized", path, gate.Kind)
		} else {
			kinds[gate.Kind]++
			if kinds[gate.Kind] > 1 && !isRepeatableGateKind(gate.Kind) {
				c.add("policy gate kind %q must be unique", gate.Kind)
			}
		}
		switch gate.Status {
		case GateUnresolved:
			if strings.TrimSpace(gate.UnresolvedReason) == "" {
				c.add("%s.unresolvedReason is required for an unresolved gate", path)
			}
			if gate.Value != "" || gate.EvidenceRef != "" {
				c.add("%s unresolved gate cannot carry a qualified value or evidence", path)
			}
		case GateQualified:
			if strings.TrimSpace(gate.Value) == "" || strings.TrimSpace(gate.EvidenceRef) == "" {
				c.add("%s qualified gate requires value and evidenceRef", path)
			}
			if gate.UnresolvedReason != "" {
				c.add("%s qualified gate cannot carry unresolvedReason", path)
			}
		default:
			c.add("%s.status %q is not recognized", path, gate.Status)
		}
	}
	for _, kind := range requiredGateKinds() {
		if kinds[kind] == 0 {
			c.add("required policy/qualification gate %q is missing", kind)
		}
	}
	return byID
}

func isRepeatableGateKind(kind GateKind) bool {
	switch kind {
	case GateReprompts, GateNoInputReprompts, GateNoMatchReprompts, GateAmbiguousReprompts:
		return true
	default:
		return false
	}
}

func policyGateByID(gates []PolicyGate, id string) (PolicyGate, bool) {
	for _, gate := range gates {
		if gate.ID == id {
			return gate, true
		}
	}
	return PolicyGate{}, false
}

func uniquePolicyGate(gates []PolicyGate, kind GateKind) (PolicyGate, bool) {
	for _, gate := range gates {
		if gate.Kind == kind {
			return gate, true
		}
	}
	return PolicyGate{}, false
}

func validateGraph(c *collector, definition Definition) {
	graph := definition.Graph
	if !isIdentifier(graph.ID) {
		c.add("graph.id is required and must be a stable identifier")
	}
	if !isSHA256(graph.NormalizationRulesDigest) {
		c.add("graph.normalizationRulesDigest must be a lowercase SHA-256 digest")
	}
	gates := make(map[string]PolicyGate, len(definition.PolicyGates))
	for _, gate := range definition.PolicyGates {
		if gate.ID != "" {
			gates[gate.ID] = gate
		}
	}
	validateAuthority(c, graph.Authority, gates)
	validateLocale(c, graph.Locale, gates)
	validateMessages(c, graph.Messages, graph.Locale)
	validateVoiceRequirements(c, graph.RequiredVoiceCapabilities, gates)

	routes := make(map[RouteID]Route, len(graph.Routes))
	for i, route := range graph.Routes {
		if !isRouteID(route.ID) {
			c.add("routes[%d].id must be a stable absolute route ID such as /start", i)
			continue
		}
		if previous, exists := routes[route.ID]; exists {
			c.add("duplicate route ID %q", route.ID)
			_ = previous
			continue
		}
		routes[route.ID] = route
	}
	if len(graph.Routes) == 0 {
		c.add("graph requires at least one route")
	}
	if !isRouteID(graph.Entry) {
		c.add("graph.entry must be a stable absolute route ID")
	} else if _, exists := routes[graph.Entry]; !exists {
		c.add("entry route %q is missing", graph.Entry)
	}
	if !isRouteID(graph.Fallback) {
		c.add("graph.fallback must be a stable absolute route ID")
	} else if _, exists := routes[graph.Fallback]; !exists {
		c.add("fallback route %q is missing", graph.Fallback)
	}

	tools := toolIndex(c, graph.Authority.Tools)
	services := serviceIndex(c, graph.Authority.Services)
	bindings := bindingIndex(c, graph.Authority.Bindings)
	messageFamilies := messageIndex(graph.Messages)
	retryGroups := retryIndex(c, graph.RetryGroups, routes, gates)
	for _, route := range graph.Routes {
		validateRoute(c, route, routes, tools, services, bindings, messageFamilies, retryGroups, graph.Authority, gates, graph.RequiredVoiceCapabilities)
	}
	validateCycles(c, routes, retryGroups)
	if fallback, exists := routes[graph.Fallback]; exists {
		if fallback.Fallback.Route == graph.Fallback {
			c.add("graph fallback route %q cannot fall back to itself", graph.Fallback)
		}
	}
}

func validateAuthority(c *collector, authority AuthorityEnvelope, gates map[string]PolicyGate) {
	if !isSHA256(authority.ParentManifestSHA256) {
		c.add("authority.parentManifestSha256 must pin the inherited parent manifest")
	}
	toolIDs := make(map[string]struct{}, len(authority.Tools))
	for i, tool := range authority.Tools {
		path := fmt.Sprintf("authority.tools[%d]", i)
		if !isIdentifier(tool.ID) {
			c.add("%s.id is required and must be a stable identifier", path)
		}
		if _, exists := toolIDs[tool.ID]; exists && tool.ID != "" {
			c.add("duplicate inherited tool ID %q", tool.ID)
		}
		toolIDs[tool.ID] = struct{}{}
		if !isSHA256(tool.SchemaSHA256) {
			c.add("%s.schemaSha256 must be a lowercase SHA-256 digest", path)
		}
		if tool.ApprovalSHA256 != "" && !isSHA256(tool.ApprovalSHA256) {
			c.add("%s.approvalSha256 must be a lowercase SHA-256 digest", path)
		}
	}
	serviceIDs := make(map[string]struct{}, len(authority.Services))
	for i, service := range authority.Services {
		path := fmt.Sprintf("authority.services[%d]", i)
		if !isIdentifier(service.ID) {
			c.add("%s.id is required and must be a stable identifier", path)
		}
		if _, exists := serviceIDs[service.ID]; exists && service.ID != "" {
			c.add("duplicate inherited service ID %q", service.ID)
		}
		serviceIDs[service.ID] = struct{}{}
		if !isSHA256(service.ContractSHA256) || !isSHA256(service.UsageSchemaSHA256) {
			c.add("%s contract and usage schema digests are required SHA-256 values", path)
		}
	}
	bindingIDs := make(map[string]struct{}, len(authority.Bindings))
	for i, binding := range authority.Bindings {
		path := fmt.Sprintf("authority.bindings[%d]", i)
		if !isIdentifier(binding.ID) {
			c.add("%s.id is required and must be a stable identifier", path)
		}
		if _, exists := bindingIDs[binding.ID]; exists && binding.ID != "" {
			c.add("duplicate inherited binding ID %q", binding.ID)
		}
		bindingIDs[binding.ID] = struct{}{}
		if !isSHA256(binding.ContractSHA256) {
			c.add("%s.contractSha256 must be a lowercase SHA-256 digest", path)
		}
	}

	for _, dimension := range RequiredBudgetDimensions {
		bound, exists := authority.Budgets[dimension]
		if !exists {
			c.add("inherited budget %q is missing; missing limits fail closed", dimension)
			continue
		}
		validateBound(c, "authority.budgets."+string(dimension), dimension, bound, gates, nil)
	}
	known := make(map[BudgetDimension]struct{}, len(RequiredBudgetDimensions))
	for _, dimension := range RequiredBudgetDimensions {
		known[dimension] = struct{}{}
	}
	for dimension := range authority.Budgets {
		if _, exists := known[dimension]; !exists {
			c.add("authority contains unknown budget dimension %q", dimension)
		}
	}
}

func validateBound(c *collector, path string, dimension BudgetDimension, bound Bound, gates map[string]PolicyGate, parent *Bound) {
	if bound.GateID == "" {
		c.add("%s must reference a qualification gate", path)
		return
	}
	gate, exists := gates[bound.GateID]
	if !exists {
		c.add("%s references missing policy gate %q", path, bound.GateID)
		return
	}
	if expected := gateForBudget(dimension); expected == "" || gate.Kind != expected {
		c.add("%s references gate %q with incompatible kind %q", path, bound.GateID, gate.Kind)
	}
	if gate.Status == GateUnresolved {
		if bound.Value != nil {
			c.add("%s cannot guess a value while gate %q is unresolved", path, gate.ID)
		}
	} else if gate.Status == GateQualified {
		if bound.Value == nil {
			c.add("%s omits the qualified value from gate %q", path, gate.ID)
		} else if parsed, err := strconv.ParseUint(gate.Value, 10, 64); err != nil {
			c.add("%s references a non-numeric budget gate %q", path, gate.ID)
		} else if parent == nil && parsed != *bound.Value {
			c.add("%s value must match the qualified value recorded by gate %q", path, gate.ID)
		} else if parent != nil && *bound.Value > parsed {
			c.add("%s widens the qualified value recorded by gate %q", path, gate.ID)
		}
	}
	if bound.Value != nil && parent == nil && gate.Status != GateQualified {
		c.add("%s numeric ceiling requires a qualified gate", path)
	}
	if parent != nil {
		if parent.Value == nil {
			c.add("%s cannot override unresolved inherited budget %q", path, dimension)
			return
		}
		if bound.GateID != parent.GateID {
			c.add("%s must retain inherited budget gate %q", path, parent.GateID)
		}
		if bound.Value == nil {
			c.add("%s cannot remove a finite inherited ceiling", path)
		} else if *bound.Value > *parent.Value {
			c.add("%s widens inherited budget %q", path, dimension)
		}
	}
}

func gateForBudget(dimension BudgetDimension) GateKind {
	switch dimension {
	case BudgetSessionDuration:
		return GateSessionDuration
	case BudgetRouteVisits:
		return GateRouteVisits
	case BudgetDecisionCalls:
		return GateDecisionCalls
	case BudgetReprompts:
		return GateReprompts
	case BudgetNoInputReprompts:
		return GateNoInputReprompts
	case BudgetNoMatchReprompts:
		return GateNoMatchReprompts
	case BudgetAmbiguousReprompts:
		return GateAmbiguousReprompts
	case BudgetInputDuration:
		return GateInputDuration
	case BudgetTTSCharacters:
		return GateTTSCharacters
	case BudgetQueuedAudioBytes:
		return GateQueuedAudioBytes
	case BudgetArtifactBytes:
		return GateArtifactBytes
	case BudgetEffectAttempts:
		return GateEffectAttempts
	case BudgetSessionSpendMicrounits:
		return GateSessionSpend
	case BudgetFirstAudioLatency:
		return GateFirstAudioLatency
	case BudgetFirstResponseLatency:
		return GateFirstResponseLatency
	case BudgetProtectedPayloadTTL:
		return GateProtectedPayloadTTL
	default:
		return ""
	}
}

func validateLocale(c *collector, policy LocalePolicy, gates map[string]PolicyGate) {
	if !isLocale(policy.BaseLocale) {
		c.add("locale.baseLocale must be a canonical lowercase language tag")
	}
	if !isLocale(policy.FallbackLocale) || policy.FallbackLocale != policy.BaseLocale {
		c.add("locale fallback must be the whole-message base locale")
	}
	if !policy.WholeMessageFallback {
		c.add("locale fallback must replace the whole message")
	}
	gate, exists := gates[policy.QualificationGateID]
	if policy.QualificationGateID == "" || !exists || gate.Kind != GateLocaleProfile {
		c.add("locale.qualificationGateId must reference the locale qualification gate")
	}
	seen := map[string]bool{}
	for _, locale := range policy.EnabledLocales {
		if !isLocale(locale) {
			c.add("enabled locale %q is not canonical lowercase", locale)
		}
		if seen[locale] {
			c.add("duplicate enabled locale %q", locale)
		}
		seen[locale] = true
	}
	if exists && gate.Status == GateQualified && len(policy.EnabledLocales) == 1 && gate.Value != policy.EnabledLocales[0] {
		c.add("qualified locale gate value must match the sole enabled locale")
	}
}

func validateMessages(c *collector, families []MessageFamily, locale LocalePolicy) {
	seenFamilies := map[string]bool{}
	for i, family := range families {
		path := fmt.Sprintf("messages[%d]", i)
		if !isIdentifier(family.ID) {
			c.add("%s.id is required and must be stable", path)
		}
		if seenFamilies[family.ID] && family.ID != "" {
			c.add("duplicate message family ID %q", family.ID)
		}
		seenFamilies[family.ID] = true
		if family.BaseLocale != locale.BaseLocale || family.FallbackLocale != locale.FallbackLocale {
			c.add("%s must use the graph base and whole-message fallback locales", path)
		}
		arguments := make(map[string]ArgumentType, len(family.Arguments))
		for j, argument := range family.Arguments {
			if !isArgumentName(argument.Name) {
				c.add("%s.arguments[%d].name is invalid", path, j)
			}
			if _, exists := arguments[argument.Name]; exists && argument.Name != "" {
				c.add("%s declares duplicate argument %q", path, argument.Name)
			}
			if !isArgumentType(argument.Type) {
				c.add("%s argument %q has unknown type %q", path, argument.Name, argument.Type)
			}
			arguments[argument.Name] = argument.Type
		}
		if len(family.Variants) == 0 {
			c.add("%s has no localized variants", path)
		}
		base, exists := family.Variants[family.BaseLocale]
		if !exists || strings.TrimSpace(base) == "" {
			c.add("%s is missing non-empty base message %q", path, family.BaseLocale)
		}
		fallback, exists := family.Variants[family.FallbackLocale]
		if !exists || strings.TrimSpace(fallback) == "" {
			c.add("%s is missing non-empty fallback message %q", path, family.FallbackLocale)
		}
		locales := make([]string, 0, len(family.Variants))
		for variantLocale := range family.Variants {
			locales = append(locales, variantLocale)
		}
		sort.Strings(locales)
		for _, variantLocale := range locales {
			message := family.Variants[variantLocale]
			if !isLocale(variantLocale) {
				c.add("%s contains non-canonical locale %q", path, variantLocale)
			}
			if strings.TrimSpace(message) == "" {
				c.add("%s variant %q is empty", path, variantLocale)
				continue
			}
			if err := ValidateICUMessage(message, family.Arguments); err != nil {
				c.add("%s variant %q: %v", path, variantLocale, err)
			}
		}
	}
}

func validateVoiceRequirements(c *collector, required []VoiceCapabilityName, gates map[string]PolicyGate) {
	if len(required) == 0 {
		c.add("requiredVoiceCapabilities must declare the contract's speech capabilities")
	}
	seen := map[VoiceCapabilityName]bool{}
	for _, capability := range required {
		if !isKnownVoiceCapability(capability) {
			c.add("unknown required voice capability %q", capability)
		}
		if seen[capability] {
			c.add("duplicate required voice capability %q", capability)
		}
		seen[capability] = true
	}
	for _, kind := range []GateKind{GateVoiceProfile, GateVoiceFallback} {
		found := false
		for _, gate := range gates {
			if gate.Kind == kind {
				found = true
				break
			}
		}
		if !found {
			c.add("required voice gate %q is missing", kind)
		}
	}
}

func validateRoute(
	c *collector,
	route Route,
	routes map[RouteID]Route,
	tools map[string]ToolGrant,
	services map[string]ServiceGrant,
	bindings map[string]BindingGrant,
	messages map[string]MessageFamily,
	retryGroups map[string]RetryGroup,
	authority AuthorityEnvelope,
	gates map[string]PolicyGate,
	requiredVoice []VoiceCapabilityName,
) {
	path := "route " + string(route.ID)
	effectiveTools := subsetSelection(c, path+".useTools", route.UseTools, tools)
	effectiveServices := subsetSelectionServices(c, path+".useServices", route.UseServices, services)
	effectiveBindings := subsetSelectionBindings(c, path+".useBindings", route.UseBindings, bindings)
	for dimension, override := range route.BudgetOverrides {
		parent, exists := authority.Budgets[dimension]
		if !exists {
			c.add("%s widens authority with unknown budget %q", path, dimension)
			continue
		}
		validateBound(c, path+".budgetOverrides."+string(dimension), dimension, override, gates, &parent)
	}
	if route.Say != nil {
		if !containsVoiceCapability(requiredVoice, VoiceExactTextSynthesis) {
			c.add("%s.say requires exact-text synthesis in requiredVoiceCapabilities", path)
		}
		validateSay(c, path, *route.Say, messages)
	}
	if route.Listen != nil {
		validateListen(c, path, *route.Listen, requiredVoice)
	}
	if route.Match != nil {
		if !isIdentifier(route.Match.BindingID) {
			c.add("%s.match.bindingId is required", path)
		} else if _, ok := effectiveBindings[route.Match.BindingID]; !ok {
			c.add("%s uses matcher binding %q outside its inherited binding grants", path, route.Match.BindingID)
		}
		switch route.Match.OnNoMatch {
		case NoMatchToDecision:
			if route.Decide == nil {
				c.add("%s sends no-match to Jev but has no decide step", path)
			}
		case NoMatchToRoute:
		default:
			c.add("%s.match.onNoMatch %q is not recognized", path, route.Match.OnNoMatch)
		}
	}
	if route.Decide != nil {
		if !isIdentifier(route.Decide.ServiceID) {
			c.add("%s.decide.serviceId is required", path)
		} else if _, ok := effectiveServices[route.Decide.ServiceID]; !ok {
			c.add("%s uses decision service %q outside its inherited service grants", path, route.Decide.ServiceID)
		}
		if !isIdentifier(route.Decide.ResultPolicy) {
			c.add("%s.decide.resultPolicy is required", path)
		}
	}
	actIDs := map[string]bool{}
	releaseCallOwnershipActions := 0
	for i, action := range route.Act {
		actionPath := fmt.Sprintf("%s.act[%d]", path, i)
		if !isIdentifier(action.ID) {
			c.add("%s.id is required", actionPath)
		}
		if actIDs[action.ID] && action.ID != "" {
			c.add("%s has duplicate action ID %q", path, action.ID)
		}
		actIDs[action.ID] = true
		if _, ok := effectiveTools[action.ToolID]; !ok {
			c.add("%s uses tool %q outside its inherited tool grants", actionPath, action.ToolID)
		}
		if !isKnownEffect(action.Kind) {
			c.add("%s.kind %q is not recognized", actionPath, action.Kind)
		}
		if action.TargetBinding != "" {
			if !isIdentifier(action.TargetBinding) {
				c.add("%s.targetBinding must be a stable identifier", actionPath)
			} else if _, ok := effectiveBindings[action.TargetBinding]; !ok {
				c.add("%s target binding %q is outside its inherited binding grants", actionPath, action.TargetBinding)
			}
		}
		if (action.Kind == EffectConnect || action.Kind == EffectHandoff) && !isIdentifier(action.TargetBinding) {
			c.add("%s call-control effect requires an authorized targetBinding", actionPath)
		}
		if (action.Kind == EffectConnect || action.Kind == EffectHandoff) && !action.ReauthorizeAtExecution {
			c.add("%s must reauthorize call-control effects at execution", actionPath)
		}
		if action.ReleasesCallOwnership {
			releaseCallOwnershipActions++
		}
		if action.ReleasesCallOwnership && action.Kind != EffectConnect && action.Kind != EffectHandoff {
			c.add("%s cannot release call ownership for this effect kind", actionPath)
		}
	}
	if releaseCallOwnershipActions > 1 {
		c.add("%s may declare only one call-ownership-releasing action", path)
	}
	if route.Say == nil && route.Listen == nil && route.Match == nil && route.Decide == nil && len(route.Act) == 0 {
		c.add("%s must declare at least one route-local phase", path)
	}
	if route.RetryGroup != "" {
		if _, ok := retryGroups[route.RetryGroup]; !ok {
			c.add("%s references missing retry group %q", path, route.RetryGroup)
		}
	}
	validateTarget(c, path+".fallback", route.Fallback, routes)
	if route.Fallback.Phase != "" {
		c.add("%s fallback cannot target an internal route phase", path)
	}
	if route.Fallback.Route == route.ID {
		c.add("%s fallback cannot target itself", path)
	}
	if route.Fallback.Terminal == TerminalReleaseCallOwnership {
		c.add("%s fallback cannot release call ownership without a matching confirmed effect receipt", path)
	}
	transitions := transitionIndex(c, route, routes)
	requireTransition := func(source ResultSource, outcome Outcome) {
		if _, exists := transitions[transitionKey{source: source, outcome: outcome}]; !exists {
			c.add("%s.next is missing %s/%s", path, source, outcome)
		}
	}
	if route.Say != nil {
		for _, outcome := range []Outcome{OutcomeCompleted, OutcomeCleared, OutcomeFailed} {
			requireTransition(SourcePlayback, outcome)
		}
	}
	if route.Listen != nil {
		for _, outcome := range []Outcome{OutcomeNoInput, OutcomeUtteranceLimit, OutcomeError} {
			requireTransition(SourceInput, outcome)
		}
	}
	if route.Match != nil {
		requireTransition(SourceMatch, OutcomeCandidate)
		requireTransition(SourceMatch, OutcomeAmbiguous)
		requireTransition(SourceMatch, OutcomeError)
		if route.Match.OnNoMatch == NoMatchToRoute {
			requireTransition(SourceMatch, OutcomeNoMatch)
		}
	}
	if route.Decide != nil {
		for _, outcome := range []Outcome{OutcomeCandidate, OutcomeNoMatch, OutcomeAmbiguous, OutcomeRefusal, OutcomeError, OutcomeUnavailable} {
			requireTransition(SourceDecision, outcome)
		}
	}
	if len(route.Act) > 0 {
		for _, outcome := range []Outcome{OutcomeAccepted, OutcomeConfirmed, OutcomeFailed, OutcomeUnknown} {
			requireTransition(SourceEffect, outcome)
		}
	}
	for _, outcome := range []Outcome{OutcomeCancelled, OutcomeDisconnected} {
		requireTransition(SourceControl, outcome)
	}
	for key, target := range transitions {
		if target.Terminal == TerminalReleaseCallOwnership && !(key.source == SourceEffect && key.outcome == OutcomeConfirmed && releaseCallOwnershipActions == 1) {
			c.add("%s cannot release call ownership on %s/%s without its declared confirmed effect", path, key.source, key.outcome)
		}
	}
	if releaseCallOwnershipActions > 0 {
		confirmed, ok := transitions[transitionKey{source: SourceEffect, outcome: OutcomeConfirmed}]
		if !ok || confirmed.Terminal != TerminalReleaseCallOwnership {
			c.add("%s must release call ownership only on effect/confirmed", path)
		}
		for _, outcome := range []Outcome{OutcomeAccepted, OutcomeFailed, OutcomeUnknown} {
			target, ok := transitions[transitionKey{source: SourceEffect, outcome: outcome}]
			if ok && target.Terminal == TerminalReleaseCallOwnership {
				c.add("%s cannot release call ownership on effect/%s", path, outcome)
			}
			if ok && (outcome == OutcomeAccepted || outcome == OutcomeUnknown) && target != (Target{Terminal: TerminalAwaitReceipt}) {
				c.add("%s must reconcile effect/%s while retaining call ownership", path, outcome)
			}
		}
	}
}

func validateSay(c *collector, path string, say SayStep, families map[string]MessageFamily) {
	if len(say.Families) == 0 {
		c.add("%s.say.families must not be empty", path)
	}
	if say.LocaleSource != "session" {
		c.add("%s.say.localeSource must be the pinned session locale", path)
	}
	argumentNames := map[string]bool{}
	familyIDs := map[string]bool{}
	for _, familyID := range say.Families {
		if familyIDs[familyID] {
			c.add("%s.say repeats message family %q", path, familyID)
		}
		familyIDs[familyID] = true
		family, ok := families[familyID]
		if !ok {
			c.add("%s.say references missing message family %q", path, familyID)
			continue
		}
		for _, argument := range family.Arguments {
			argumentNames[argument.Name] = true
		}
	}
	bindings := map[string]bool{}
	for _, binding := range say.ArgumentBindings {
		if !isArgumentName(binding.Name) || strings.TrimSpace(binding.Source) == "" {
			c.add("%s.say argument bindings require a valid name and source", path)
		}
		if bindings[binding.Name] && binding.Name != "" {
			c.add("%s.say binds argument %q more than once", path, binding.Name)
		}
		bindings[binding.Name] = true
		if !argumentNames[binding.Name] {
			c.add("%s.say binds undeclared message argument %q", path, binding.Name)
		}
	}
	for name := range argumentNames {
		if !bindings[name] {
			c.add("%s.say is missing a runtime binding for argument %q", path, name)
		}
	}
}

func validateListen(c *collector, path string, listen ListenStep, requiredVoice []VoiceCapabilityName) {
	if len(listen.Accept) == 0 {
		c.add("%s.listen.accept must not be empty", path)
	}
	seen := map[InputModality]bool{}
	for _, modality := range listen.Accept {
		if !isKnownModality(modality) {
			c.add("%s.listen has unknown modality %q", path, modality)
		}
		if seen[modality] {
			c.add("%s.listen repeats modality %q", path, modality)
		}
		seen[modality] = true
		capability := VoiceCapabilityName("")
		switch modality {
		case ModalitySpeechFinal:
			capability = VoiceFinalSpeechText
		case ModalityDTMFComplete:
			capability = VoiceDTMFInput
		case ModalityText:
			capability = VoiceTextInput
		}
		if capability != "" && !containsVoiceCapability(requiredVoice, capability) {
			c.add("%s.listen modality %q lacks required voice/input capability %q", path, modality, capability)
		}
	}
	if listen.BargeIn && !containsVoiceCapability(requiredVoice, VoiceBargeIn) {
		c.add("%s.listen enables barge-in without declaring that capability", path)
	}
}

type transitionKey struct {
	source  ResultSource
	outcome Outcome
}

func transitionIndex(c *collector, route Route, routes map[RouteID]Route) map[transitionKey]Target {
	index := make(map[transitionKey]Target, len(route.Next))
	for i, transition := range route.Next {
		path := fmt.Sprintf("route %s.next[%d]", route.ID, i)
		if !isKnownSource(transition.Source) {
			c.add("%s.source %q is not recognized", path, transition.Source)
		}
		if !isOutcomeAllowed(transition.Source, transition.Outcome) {
			c.add("%s outcome %q is not valid for source %q", path, transition.Outcome, transition.Source)
		}
		key := transitionKey{source: transition.Source, outcome: transition.Outcome}
		if _, exists := index[key]; exists {
			c.add("%s duplicates outcome mapping %s/%s", path, transition.Source, transition.Outcome)
		}
		index[key] = transition.Target
		validateTarget(c, path+".target", transition.Target, routes)
		if transition.Target.Phase != "" && !routeHasPhase(route, transition.Target.Phase) {
			c.add("%s targets phase %q that the route does not declare", path, transition.Target.Phase)
		}
		if transition.Target.Phase != "" && !isAllowedPhaseTransition(transition.Source, transition.Outcome, transition.Target.Phase) {
			c.add("%s uses an unsupported route-phase transition", path)
		}
		if transition.Source == SourceInput && transition.Outcome == OutcomeNoInput && transition.Target.Phase == PhaseDecide {
			c.add("%s cannot invoke Jev on no-input", path)
		}
	}
	return index
}

func isAllowedPhaseTransition(source ResultSource, outcome Outcome, target RoutePhase) bool {
	return source == SourcePlayback && outcome == OutcomeCompleted && target == PhaseListen ||
		source == SourceMatch && outcome == OutcomeCandidate && target == PhaseAct ||
		source == SourceDecision && outcome == OutcomeCandidate && target == PhaseAct
}

func validateTarget(c *collector, path string, target Target, routes map[RouteID]Route) {
	hasRoute := target.Route != ""
	hasPhase := target.Phase != ""
	hasTerminal := target.Terminal != ""
	if btoi(hasRoute)+btoi(hasPhase)+btoi(hasTerminal) != 1 {
		c.add("%s must name exactly one route, route phase, or terminal action", path)
		return
	}
	if hasRoute {
		if _, exists := routes[target.Route]; !exists {
			c.add("%s references missing route %q", path, target.Route)
		}
	} else if hasPhase && !isKnownPhase(target.Phase) {
		c.add("%s route phase %q is not recognized", path, target.Phase)
	} else if hasTerminal && !isKnownTerminal(target.Terminal) {
		c.add("%s terminal action %q is not recognized", path, target.Terminal)
	}
}

func routeHasPhase(route Route, phase RoutePhase) bool {
	switch phase {
	case PhaseSay:
		return route.Say != nil
	case PhaseListen:
		return route.Listen != nil
	case PhaseMatch:
		return route.Match != nil
	case PhaseDecide:
		return route.Decide != nil
	case PhaseAct:
		return len(route.Act) > 0
	default:
		return false
	}
}

func isKnownPhase(phase RoutePhase) bool {
	switch phase {
	case PhaseSay, PhaseListen, PhaseMatch, PhaseDecide, PhaseAct:
		return true
	default:
		return false
	}
}

func btoi(value bool) int {
	if value {
		return 1
	}
	return 0
}

func retryIndex(c *collector, groups []RetryGroup, routes map[RouteID]Route, gates map[string]PolicyGate) map[string]RetryGroup {
	byID := make(map[string]RetryGroup, len(groups))
	for i, group := range groups {
		path := fmt.Sprintf("retryGroups[%d]", i)
		if !isIdentifier(group.ID) {
			c.add("%s.id is required and must be stable", path)
		}
		if _, exists := byID[group.ID]; exists && group.ID != "" {
			c.add("duplicate retry group ID %q", group.ID)
		}
		byID[group.ID] = group
		if group.CounterScope != "logical_task" {
			c.add("%s.counterScope must preserve a logical-task counter", path)
		}
		if group.CountOn != "retry_turn_admitted" {
			c.add("%s.countOn must count once when the retry turn is admitted", path)
		}
		if !group.DeduplicateByEventID {
			c.add("%s must deduplicate replay by event ID", path)
		}
		validateBound(c, path+".maxReprompts", BudgetReprompts, group.MaxReprompts, gates, nil)
		validateBound(c, path+".maxNoInputReprompts", BudgetNoInputReprompts, group.MaxNoInputReprompts, gates, nil)
		validateBound(c, path+".maxNoMatchReprompts", BudgetNoMatchReprompts, group.MaxNoMatchReprompts, gates, nil)
		validateBound(c, path+".maxAmbiguousReprompts", BudgetAmbiguousReprompts, group.MaxAmbiguousReprompts, gates, nil)
		if group.MaxReprompts.Value != nil {
			for name, bound := range map[string]Bound{
				"maxNoInputReprompts":   group.MaxNoInputReprompts,
				"maxNoMatchReprompts":   group.MaxNoMatchReprompts,
				"maxAmbiguousReprompts": group.MaxAmbiguousReprompts,
			} {
				if bound.Value != nil && *bound.Value > *group.MaxReprompts.Value {
					c.add("%s.%s exceeds maxReprompts", path, name)
				}
			}
		}
		validateTarget(c, path+".exhausted", group.Exhausted, routes)
		if group.Exhausted.Phase != "" {
			c.add("%s.exhausted cannot target an internal route phase", path)
		}
		if group.Exhausted.Terminal == TerminalReleaseCallOwnership {
			c.add("%s.exhausted cannot release call ownership without a matching confirmed effect receipt", path)
		}
	}
	return byID
}

func validateCycles(c *collector, routes map[RouteID]Route, groups map[string]RetryGroup) {
	ids := make([]RouteID, 0, len(routes))
	for id := range routes {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	index := 0
	indices := map[RouteID]int{}
	lowlink := map[RouteID]int{}
	stack := []RouteID{}
	onStack := map[RouteID]bool{}
	var visit func(RouteID)
	visit = func(id RouteID) {
		index++
		indices[id] = index
		lowlink[id] = index
		stack = append(stack, id)
		onStack[id] = true
		for _, next := range routeEdges(routes[id]) {
			if _, exists := routes[next]; !exists {
				continue
			}
			if indices[next] == 0 {
				visit(next)
				if lowlink[next] < lowlink[id] {
					lowlink[id] = lowlink[next]
				}
			} else if onStack[next] && indices[next] < lowlink[id] {
				lowlink[id] = indices[next]
			}
		}
		if lowlink[id] != indices[id] {
			return
		}
		component := []RouteID{}
		for len(stack) > 0 {
			last := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			onStack[last] = false
			component = append(component, last)
			if last == id {
				break
			}
		}
		cyclic := len(component) > 1
		if len(component) == 1 {
			for _, next := range routeEdges(routes[component[0]]) {
				if next == component[0] {
					cyclic = true
				}
			}
		}
		if !cyclic {
			return
		}
		members := make(map[RouteID]bool, len(component))
		for _, routeID := range component {
			members[routeID] = true
		}
		boundedRoutes := make(map[RouteID]bool, len(component))
		for _, routeID := range component {
			route := routes[routeID]
			if route.RetryGroup == "" {
				continue
			}
			group, exists := groups[route.RetryGroup]
			if !exists {
				continue
			}
			exhaustionOutside := group.Exhausted.Route != "" && !members[group.Exhausted.Route] ||
				group.Exhausted.Terminal == TerminalEndSession || group.Exhausted.Terminal == TerminalSafeStop
			if exhaustionOutside {
				boundedRoutes[routeID] = true
			}
		}
		if len(boundedRoutes) == 0 {
			parts := make([]string, 0, len(component))
			for _, routeID := range component {
				parts = append(parts, string(routeID))
			}
			sort.Strings(parts)
			c.add("route cycle [%s] has no bounded retry group with an exhaustion path outside the cycle", strings.Join(parts, ", "))
			return
		}
		if hasCycleWithoutRetryCounter(component, members, boundedRoutes, routes) {
			parts := make([]string, 0, len(component))
			for _, routeID := range component {
				parts = append(parts, string(routeID))
			}
			sort.Strings(parts)
			c.add("route cycle [%s] contains a cycle path that bypasses every bounded retry counter", strings.Join(parts, ", "))
		}
	}
	for _, id := range ids {
		if indices[id] == 0 {
			visit(id)
		}
	}
}

func hasCycleWithoutRetryCounter(component []RouteID, members, boundedRoutes map[RouteID]bool, routes map[RouteID]Route) bool {
	colors := make(map[RouteID]uint8, len(component))
	var visit func(RouteID) bool
	visit = func(id RouteID) bool {
		if boundedRoutes[id] {
			return false
		}
		if colors[id] == 1 {
			return true
		}
		if colors[id] == 2 {
			return false
		}
		colors[id] = 1
		for _, next := range routeEdges(routes[id]) {
			if members[next] && !boundedRoutes[next] && visit(next) {
				return true
			}
		}
		colors[id] = 2
		return false
	}
	for _, id := range component {
		if !boundedRoutes[id] && colors[id] == 0 && visit(id) {
			return true
		}
	}
	return false
}

func routeEdges(route Route) []RouteID {
	var edges []RouteID
	for _, transition := range route.Next {
		if transition.Target.Route != "" {
			edges = append(edges, transition.Target.Route)
		}
	}
	if route.Fallback.Route != "" {
		edges = append(edges, route.Fallback.Route)
	}
	return edges
}

func subsetSelection(c *collector, path string, selected []string, inherited map[string]ToolGrant) map[string]ToolGrant {
	if selected == nil {
		return inherited
	}
	result := make(map[string]ToolGrant, len(selected))
	for _, id := range selected {
		if _, duplicate := result[id]; duplicate {
			c.add("%s repeats inherited tool %q", path, id)
		}
		tool, exists := inherited[id]
		if !exists {
			c.add("%s widens authority with ungranted tool %q", path, id)
			continue
		}
		result[id] = tool
	}
	return result
}

func subsetSelectionServices(c *collector, path string, selected []string, inherited map[string]ServiceGrant) map[string]ServiceGrant {
	if selected == nil {
		return inherited
	}
	result := make(map[string]ServiceGrant, len(selected))
	for _, id := range selected {
		if _, duplicate := result[id]; duplicate {
			c.add("%s repeats inherited service %q", path, id)
		}
		service, exists := inherited[id]
		if !exists {
			c.add("%s widens authority with ungranted service %q", path, id)
			continue
		}
		result[id] = service
	}
	return result
}

func subsetSelectionBindings(c *collector, path string, selected []string, inherited map[string]BindingGrant) map[string]BindingGrant {
	if selected == nil {
		return inherited
	}
	result := make(map[string]BindingGrant, len(selected))
	for _, id := range selected {
		if _, duplicate := result[id]; duplicate {
			c.add("%s repeats inherited binding %q", path, id)
		}
		binding, exists := inherited[id]
		if !exists {
			c.add("%s widens authority with ungranted binding %q", path, id)
			continue
		}
		result[id] = binding
	}
	return result
}

func toolIndex(c *collector, grants []ToolGrant) map[string]ToolGrant {
	result := make(map[string]ToolGrant, len(grants))
	for _, grant := range grants {
		if _, exists := result[grant.ID]; exists {
			continue
		}
		result[grant.ID] = grant
	}
	return result
}

func serviceIndex(c *collector, grants []ServiceGrant) map[string]ServiceGrant {
	result := make(map[string]ServiceGrant, len(grants))
	for _, grant := range grants {
		if _, exists := result[grant.ID]; exists {
			continue
		}
		result[grant.ID] = grant
	}
	return result
}

func bindingIndex(c *collector, grants []BindingGrant) map[string]BindingGrant {
	result := make(map[string]BindingGrant, len(grants))
	for _, grant := range grants {
		if _, exists := result[grant.ID]; exists {
			continue
		}
		result[grant.ID] = grant
	}
	return result
}

func messageIndex(families []MessageFamily) map[string]MessageFamily {
	result := make(map[string]MessageFamily, len(families))
	for _, family := range families {
		if _, exists := result[family.ID]; !exists {
			result[family.ID] = family
		}
	}
	return result
}

func isKnownGateKind(kind GateKind) bool {
	for _, required := range requiredGateKinds() {
		if kind == required {
			return true
		}
	}
	return false
}

func requiredGateKinds() []GateKind {
	budgetKinds := make([]GateKind, 0, len(RequiredBudgetDimensions))
	for _, dimension := range RequiredBudgetDimensions {
		budgetKinds = append(budgetKinds, gateForBudget(dimension))
	}
	return append(budgetKinds, RequiredNonBudgetGateKinds...)
}

func isKnownSource(source ResultSource) bool {
	switch source {
	case SourceInput, SourceMatch, SourceDecision, SourceEffect, SourcePlayback, SourceControl:
		return true
	default:
		return false
	}
}

func isOutcomeAllowed(source ResultSource, outcome Outcome) bool {
	switch source {
	case SourceInput:
		return outcome == OutcomeNoInput || outcome == OutcomeUtteranceLimit || outcome == OutcomeError
	case SourceMatch:
		return outcome == OutcomeCandidate || outcome == OutcomeNoMatch || outcome == OutcomeAmbiguous || outcome == OutcomeError
	case SourceDecision:
		return outcome == OutcomeCandidate || outcome == OutcomeNoMatch || outcome == OutcomeAmbiguous || outcome == OutcomeRefusal || outcome == OutcomeError || outcome == OutcomeUnavailable
	case SourceEffect:
		return outcome == OutcomeAccepted || outcome == OutcomeConfirmed || outcome == OutcomeFailed || outcome == OutcomeUnknown
	case SourcePlayback:
		return outcome == OutcomeCompleted || outcome == OutcomeCleared || outcome == OutcomeFailed
	case SourceControl:
		return outcome == OutcomeCancelled || outcome == OutcomeDisconnected
	default:
		return false
	}
}

func isKnownTerminal(terminal TerminalAction) bool {
	switch terminal {
	case TerminalEndSession, TerminalEndGraph, TerminalReleaseCallOwnership, TerminalAwaitReceipt, TerminalSafeStop:
		return true
	default:
		return false
	}
}

func isKnownEffect(kind EffectKind) bool {
	switch kind {
	case EffectConnect, EffectHandoff, EffectWrite, EffectCustom:
		return true
	default:
		return false
	}
}

func isKnownModality(modality InputModality) bool {
	switch modality {
	case ModalitySpeechFinal, ModalityDTMFComplete, ModalityText:
		return true
	default:
		return false
	}
}

func isKnownVoiceCapability(capability VoiceCapabilityName) bool {
	switch capability {
	case VoiceExactTextSynthesis, VoiceFinalSpeechText, VoiceDTMFInput, VoiceTextInput, VoiceBargeIn, VoiceStreaming, VoiceCancellation:
		return true
	default:
		return false
	}
}

func containsVoiceCapability(capabilities []VoiceCapabilityName, target VoiceCapabilityName) bool {
	for _, capability := range capabilities {
		if capability == target {
			return true
		}
	}
	return false
}

func isArgumentType(argumentType ArgumentType) bool {
	switch argumentType {
	case ArgumentString, ArgumentNumber, ArgumentDate, ArgumentTime:
		return true
	default:
		return false
	}
}

func isIdentifier(value string) bool { return identifierPattern.MatchString(value) }
func isRouteID(value RouteID) bool {
	text := string(value)
	return routePattern.MatchString(text) && !strings.Contains(text, "//") && !strings.HasSuffix(text, "/")
}
func isArgumentName(value string) bool { return argumentPattern.MatchString(value) }
func isLocale(value string) bool       { return localePattern.MatchString(value) }
func isSHA256(value string) bool       { return sha256Pattern.MatchString(value) }

// ValidateICUMessage validates the deliberately bounded ICU subset used by
// message families: named arguments and nested plural arguments with `other`.
// Unsupported formatter styles fail closed until their syntax and typing are
// part of a later schema version.
func ValidateICUMessage(message string, arguments []MessageArgument) error {
	argumentTypes := make(map[string]ArgumentType, len(arguments))
	for _, argument := range arguments {
		argumentTypes[argument.Name] = argument.Type
	}
	parser := icuParser{message: message, arguments: argumentTypes}
	if err := parser.parseMessage(false); err != nil {
		return err
	}
	if parser.position != len(message) {
		return fmt.Errorf("unexpected content after message")
	}
	return nil
}

type icuParser struct {
	message   string
	arguments map[string]ArgumentType
	position  int
}

func (p *icuParser) parseMessage(expectClose bool) error {
	for p.position < len(p.message) {
		switch p.message[p.position] {
		case '{':
			if err := p.parseArgument(); err != nil {
				return err
			}
		case '}':
			if !expectClose {
				return fmt.Errorf("unexpected closing brace at byte %d", p.position)
			}
			p.position++
			return nil
		default:
			p.position++
		}
	}
	if expectClose {
		return fmt.Errorf("unclosed ICU branch")
	}
	return nil
}

func (p *icuParser) parseArgument() error {
	start := p.position
	p.position++ // opening brace
	name := p.readToken(',', '}')
	name = strings.TrimSpace(name)
	if !isArgumentName(name) {
		return fmt.Errorf("invalid or empty argument name at byte %d", start)
	}
	argumentType, declared := p.arguments[name]
	if !declared {
		return fmt.Errorf("placeholder %q is not declared", name)
	}
	if p.position >= len(p.message) {
		return fmt.Errorf("unclosed placeholder %q", name)
	}
	if p.message[p.position] == '}' {
		p.position++
		return nil
	}
	p.position++ // comma after argument name
	formatter := strings.TrimSpace(p.readToken(',', '}'))
	if formatter != "plural" {
		return fmt.Errorf("placeholder %q uses unsupported formatter %q", name, formatter)
	}
	if argumentType != ArgumentNumber {
		return fmt.Errorf("plural placeholder %q must be declared as number", name)
	}
	if p.position >= len(p.message) || p.message[p.position] != ',' {
		return fmt.Errorf("plural placeholder %q requires selector branches", name)
	}
	p.position++
	seenOther := false
	seenBranches := map[string]bool{}
	for {
		p.skipSpace()
		if p.position >= len(p.message) {
			return fmt.Errorf("unclosed plural placeholder %q", name)
		}
		if p.message[p.position] == '}' {
			p.position++
			break
		}
		selector := p.readSelector()
		p.skipSpace()
		if !isValidPluralSelector(selector) || p.position >= len(p.message) || p.message[p.position] != '{' {
			return fmt.Errorf("plural placeholder %q has an invalid branch", name)
		}
		if seenBranches[selector] {
			return fmt.Errorf("plural placeholder %q repeats branch %q", name, selector)
		}
		seenBranches[selector] = true
		seenOther = seenOther || selector == "other"
		p.position++ // branch opening brace
		if err := p.parseMessage(true); err != nil {
			return err
		}
	}
	if !seenOther {
		return fmt.Errorf("plural placeholder %q requires an other branch", name)
	}
	return nil
}

func isValidPluralSelector(selector string) bool {
	switch selector {
	case "zero", "one", "two", "few", "many", "other":
		return true
	}
	if !strings.HasPrefix(selector, "=") || len(selector) == 1 {
		return false
	}
	value, err := strconv.ParseFloat(selector[1:], 64)
	return err == nil && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func (p *icuParser) readToken(delimiters ...byte) string {
	start := p.position
	for p.position < len(p.message) {
		for _, delimiter := range delimiters {
			if p.message[p.position] == delimiter {
				return p.message[start:p.position]
			}
		}
		p.position++
	}
	return p.message[start:p.position]
}

func (p *icuParser) readSelector() string {
	start := p.position
	for p.position < len(p.message) {
		b := p.message[p.position]
		if b == '{' || b == '}' || b == ' ' || b == '\t' || b == '\n' || b == '\r' {
			break
		}
		p.position++
	}
	return p.message[start:p.position]
}

func (p *icuParser) skipSpace() {
	for p.position < len(p.message) {
		switch p.message[p.position] {
		case ' ', '\t', '\n', '\r':
			p.position++
		default:
			return
		}
	}
}

func (identity EffectIdentity) Validate() error {
	if !isIdentifier(identity.TenantID) || !isIdentifier(identity.SessionID) || !isIdentifier(identity.RouteEntryID) || !isIdentifier(identity.InputID) || !isIdentifier(identity.ActionID) {
		return fmt.Errorf("effect identity is missing a stable tenant/session/turn/input/action field")
	}
	if identity.Generation == 0 {
		return fmt.Errorf("effect identity generation must be positive")
	}
	if !isSHA256(identity.GraphSHA256) {
		return fmt.Errorf("effect identity graphSha256 must be a lowercase SHA-256 digest")
	}
	if identity.ID != identity.CanonicalID() {
		return fmt.Errorf("effect identity ID does not match its stable idempotency fields")
	}
	return nil
}

func (receipt EffectReceipt) Validate() error {
	if err := receipt.Identity.Validate(); err != nil {
		return err
	}
	if receipt.ObservedAt.IsZero() {
		return fmt.Errorf("effect receipt observedAt is required")
	}
	switch receipt.Status {
	case EffectSubmitted, EffectAccepted:
		if receipt.ReceiptID == "" {
			return fmt.Errorf("submitted/accepted effect status requires its receipt ID")
		}
	case EffectConfirmed:
		if receipt.ReceiptID == "" || receipt.ProviderRequestID == "" {
			return fmt.Errorf("confirmed effect requires receipt and provider request IDs")
		}
	case EffectFailed:
		if receipt.FailureCode == "" || receipt.ReceiptID == "" {
			return fmt.Errorf("failed effect requires receipt ID and failure code")
		}
	case EffectUnknown:
		// Unknown is an explicit reconciliation state, not a retry instruction.
	default:
		return fmt.Errorf("unknown effect status %q", receipt.Status)
	}
	return nil
}

func (receipt PlaybackReceipt) Validate() error {
	if !isIdentifier(receipt.OperationID) || !isIdentifier(receipt.ReceiptID) || receipt.ObservedAt.IsZero() {
		return fmt.Errorf("playback receipt requires operation, receipt, and observed-time fields")
	}
	switch receipt.Outcome {
	case OutcomeCompleted, OutcomeCleared, OutcomeFailed:
		return nil
	default:
		return fmt.Errorf("unknown playback receipt outcome %q", receipt.Outcome)
	}
}

func (event NormalizedEvent) Validate(now time.Time) error {
	if !isIdentifier(event.ID) || !isIdentifier(event.TenantID) || !isIdentifier(event.SessionID) {
		return fmt.Errorf("event, tenant, and session IDs are required")
	}
	if event.Generation == 0 {
		return fmt.Errorf("event generation must be positive")
	}
	if event.ReceivedAt.IsZero() {
		return fmt.Errorf("event receivedAt is required")
	}
	switch event.Channel {
	case ChannelVoice, ChannelText:
	default:
		return fmt.Errorf("unknown event channel %q", event.Channel)
	}
	switch event.Kind {
	case EventInputError:
		if event.ProtectedInput != nil || event.Digits != "" || event.Modality != "" || event.Locale != "" || len(event.SourceEventIDs) != 0 || event.Match != nil || event.Decision != nil || event.EffectRequest != nil || event.Effect != nil || event.Playback != nil {
			return fmt.Errorf("input_error event cannot carry user input, result, effect, or playback payload")
		}
		if !isRouteID(event.RouteID) || !isIdentifier(event.RouteEntryID) || !isIdentifier(event.InputWindowID) {
			return fmt.Errorf("input_error requires route, route-entry, and input-window identity")
		}
	case EventInputFinal:
		if event.Match != nil || event.Decision != nil || event.EffectRequest != nil || event.Effect != nil || event.Playback != nil {
			return fmt.Errorf("input_final event cannot also carry a result, effect, or playback receipt")
		}
		if !isRouteID(event.RouteID) || !isIdentifier(event.RouteEntryID) || !isIdentifier(event.InputWindowID) {
			return fmt.Errorf("final input requires route, route-entry, and input-window identity")
		}
		if !isLocale(event.Locale) {
			return fmt.Errorf("final input requires a pinned canonical locale")
		}
		if len(event.SourceEventIDs) == 0 {
			return fmt.Errorf("final input requires source event IDs")
		}
		seen := map[string]bool{}
		for _, sourceID := range event.SourceEventIDs {
			if !isIdentifier(sourceID) || seen[sourceID] {
				return fmt.Errorf("source event IDs must be stable and unique")
			}
			seen[sourceID] = true
		}
		switch event.Modality {
		case ModalityDTMFComplete:
			if event.Channel != ChannelVoice {
				return fmt.Errorf("DTMF input must use the voice channel")
			}
			if event.Digits == "" || event.ProtectedInput != nil {
				return fmt.Errorf("DTMF final input requires digits and no protected transcript payload")
			}
		case ModalitySpeechFinal, ModalityText:
			if event.Modality == ModalitySpeechFinal && event.Channel != ChannelVoice || event.Modality == ModalityText && event.Channel != ChannelText {
				return fmt.Errorf("input channel does not match its modality")
			}
			if event.ProtectedInput == nil || event.Digits != "" {
				return fmt.Errorf("speech/text final input requires a protected payload reference, not inline text")
			}
			if err := event.ProtectedInput.ValidateFor(event.TenantID, event.SessionID, event.Generation, now); err != nil {
				return fmt.Errorf("protected input: %w", err)
			}
		default:
			return fmt.Errorf("unknown final input modality %q", event.Modality)
		}
	case EventMatchCompleted:
		if event.ProtectedInput != nil || event.Digits != "" || event.Decision != nil || event.EffectRequest != nil || event.Effect != nil || event.Playback != nil {
			return fmt.Errorf("match_completed event carries an incompatible payload")
		}
		if event.Match == nil {
			return fmt.Errorf("match_completed event requires a typed match result")
		}
		if err := event.Match.Validate(); err != nil {
			return fmt.Errorf("match result: %w", err)
		}
	case EventDecisionCompleted:
		if event.ProtectedInput != nil || event.Digits != "" || event.Match != nil || event.EffectRequest != nil || event.Effect != nil || event.Playback != nil {
			return fmt.Errorf("decision_completed event carries an incompatible payload")
		}
		if event.Decision == nil {
			return fmt.Errorf("decision_completed event requires a typed decision result")
		}
		if err := event.Decision.Validate(); err != nil {
			return fmt.Errorf("decision result: %w", err)
		}
	case EventEffectSubmitted:
		if event.ProtectedInput != nil || event.Digits != "" || event.Match != nil || event.Decision != nil || event.Effect != nil || event.Playback != nil {
			return fmt.Errorf("effect_submitted event carries an incompatible payload")
		}
		if event.EffectRequest == nil {
			return fmt.Errorf("effect_submitted event requires a typed effect request")
		}
		if err := event.EffectRequest.Validate(now); err != nil {
			return fmt.Errorf("effect request: %w", err)
		}
		identity := event.EffectRequest.Identity
		if identity.TenantID != event.TenantID || identity.SessionID != event.SessionID || identity.Generation != event.Generation {
			return fmt.Errorf("effect request scope does not match the event")
		}
	case EventEffectReceipt:
		if event.ProtectedInput != nil || event.Digits != "" || event.Match != nil || event.Decision != nil || event.EffectRequest != nil || event.Playback != nil {
			return fmt.Errorf("effect_receipt event carries an incompatible payload")
		}
		if event.Effect == nil {
			return fmt.Errorf("effect_receipt event requires a typed effect receipt")
		}
		if err := event.Effect.Validate(); err != nil {
			return fmt.Errorf("effect receipt: %w", err)
		}
	case EventPlaybackCompleted, EventPlaybackCleared, EventPlaybackFailed:
		if event.ProtectedInput != nil || event.Digits != "" || event.Match != nil || event.Decision != nil || event.EffectRequest != nil || event.Effect != nil || event.Playback == nil {
			return fmt.Errorf("playback completion event requires only a playback receipt")
		}
		if err := event.Playback.Validate(); err != nil {
			return fmt.Errorf("playback receipt: %w", err)
		}
		expected := map[EventKind]Outcome{
			EventPlaybackCompleted: OutcomeCompleted,
			EventPlaybackCleared:   OutcomeCleared,
			EventPlaybackFailed:    OutcomeFailed,
		}[event.Kind]
		if event.Playback.Outcome != expected {
			return fmt.Errorf("playback event kind and receipt outcome do not match")
		}
	case EventSessionAdmitted, EventRouteEntered, EventSpeechStarted, EventInitialSilence, EventUtteranceLimit, EventRetryExhausted,
		EventPlaybackStarted, EventHandoffAccepted, EventDisconnected, EventCancelled:
		if event.ProtectedInput != nil || event.Digits != "" || event.Match != nil || event.Decision != nil || event.EffectRequest != nil || event.Effect != nil {
			return fmt.Errorf("event kind %q cannot carry input, decision, or effect payload", event.Kind)
		}
		if event.Playback != nil {
			return fmt.Errorf("event kind %q cannot carry a playback completion receipt", event.Kind)
		}
	default:
		return fmt.Errorf("unknown event kind %q", event.Kind)
	}
	if event.Kind == EventEffectReceipt && event.Effect != nil {
		identity := event.Effect.Identity
		if identity.TenantID != event.TenantID || identity.SessionID != event.SessionID || identity.Generation != event.Generation {
			return fmt.Errorf("effect receipt scope does not match the event")
		}
	}
	return nil
}

func (identity InputWindowIdentity) Validate() error {
	if !isIdentifier(identity.TenantID) || !isIdentifier(identity.SessionID) || !isIdentifier(identity.RouteEntryID) || !isIdentifier(identity.InputWindowID) {
		return fmt.Errorf("active input window requires stable tenant, session, route-entry, and input-window IDs")
	}
	if identity.Generation == 0 {
		return fmt.Errorf("active input window generation must be positive")
	}
	if !isRouteID(identity.RouteID) {
		return fmt.Errorf("active input window requires a stable absolute route ID")
	}
	switch identity.Channel {
	case ChannelVoice, ChannelText:
	default:
		return fmt.Errorf("active input window has unknown channel %q", identity.Channel)
	}
	return nil
}

// ValidateForActiveInputWindow validates an input_error event and binds it to
// the exact currently active session generation, route entry, input window,
// and channel. A delayed error from an earlier window is rejected.
func (event NormalizedEvent) ValidateForActiveInputWindow(active InputWindowIdentity, now time.Time) error {
	if event.Kind != EventInputError {
		return fmt.Errorf("active input window validation requires an input_error event")
	}
	if err := active.Validate(); err != nil {
		return err
	}
	if err := event.Validate(now); err != nil {
		return err
	}
	if event.TenantID != active.TenantID || event.SessionID != active.SessionID || event.Generation != active.Generation || event.RouteID != active.RouteID || event.RouteEntryID != active.RouteEntryID || event.InputWindowID != active.InputWindowID || event.Channel != active.Channel {
		return fmt.Errorf("input_error identity does not match the active input window")
	}
	return nil
}

func (reference ProtectedReference) ValidateFor(tenantID, sessionID string, generation uint64, now time.Time) error {
	if !isIdentifier(reference.ID) || !isIdentifier(reference.Purpose) {
		return fmt.Errorf("protected reference ID and purpose are required")
	}
	if reference.TenantID != tenantID || reference.SessionID != sessionID || reference.Generation != generation {
		return fmt.Errorf("protected reference scope does not match the event")
	}
	if !isSHA256(reference.SHA256) {
		return fmt.Errorf("protected reference requires a lowercase SHA-256 digest")
	}
	if reference.ExpiresAt.IsZero() || !now.IsZero() && !reference.ExpiresAt.After(now) {
		return fmt.Errorf("protected reference is expired or has no expiry")
	}
	return nil
}

func (result DecisionResult) Validate() error {
	switch result.Outcome {
	case OutcomeCandidate:
		if !isIdentifier(result.CandidateID) {
			return fmt.Errorf("candidate result requires an opaque candidate ID")
		}
	case OutcomeNoMatch, OutcomeAmbiguous, OutcomeRefusal, OutcomeError, OutcomeUnavailable:
		if result.CandidateID != "" {
			return fmt.Errorf("non-candidate result cannot carry a candidate ID")
		}
	default:
		return fmt.Errorf("unknown decision outcome %q", result.Outcome)
	}
	if !isIdentifier(result.ProviderRef) || !isIdentifier(result.ModelRef) || !isIdentifier(result.Revision) {
		return fmt.Errorf("decision result requires provider/model/revision identifiers")
	}
	if !isSHA256(result.ResultSchemaSHA256) || !isSHA256(result.CandidateSetSHA256) {
		return fmt.Errorf("decision result schema and candidate set digests are required")
	}
	if !finiteProbability(result.ASRConfidence) || !finiteProbability(result.OptionProbability) {
		return fmt.Errorf("ASR confidence and option probability must be separate finite values in [0,1]")
	}
	for name, raw := range result.ProviderScoreFields {
		if !isIdentifier(name) || !json.Valid(raw) {
			return fmt.Errorf("provider score fields must have stable names and valid JSON values")
		}
	}
	if err := result.Usage.Validate(); err != nil {
		return err
	}
	return nil
}

func (counter RetryCounter) ValidateFor(group RetryGroup) error {
	if counter.GroupID != group.ID || !isIdentifier(counter.LogicalTaskID) {
		return fmt.Errorf("retry counter requires the matching group and stable logical task ID")
	}
	if group.MaxReprompts.Value == nil || group.MaxNoInputReprompts.Value == nil || group.MaxNoMatchReprompts.Value == nil || group.MaxAmbiguousReprompts.Value == nil {
		return fmt.Errorf("retry counter cannot run while its limits are unresolved")
	}
	if counter.Reprompts > *group.MaxReprompts.Value ||
		counter.NoInputReprompts > *group.MaxNoInputReprompts.Value ||
		counter.NoMatchReprompts > *group.MaxNoMatchReprompts.Value ||
		counter.AmbiguousReprompts > *group.MaxAmbiguousReprompts.Value {
		return fmt.Errorf("retry counter exceeds its qualified group limits")
	}
	if counter.NoInputReprompts > counter.Reprompts || counter.NoMatchReprompts > counter.Reprompts || counter.AmbiguousReprompts > counter.Reprompts || uint64(len(counter.CountedAdmissionEventIDs)) != counter.Reprompts {
		return fmt.Errorf("retry counter admissions must align with unique reprompt event IDs")
	}
	reasonTotal := counter.NoInputReprompts
	for _, count := range []uint64{counter.NoMatchReprompts, counter.AmbiguousReprompts} {
		if count > counter.Reprompts-reasonTotal {
			return fmt.Errorf("per-reason retry counters exceed the total reprompt count")
		}
		reasonTotal += count
	}
	seen := map[string]bool{}
	for _, eventID := range counter.CountedAdmissionEventIDs {
		if !isIdentifier(eventID) || seen[eventID] {
			return fmt.Errorf("retry counter admission event IDs must be stable and unique")
		}
		seen[eventID] = true
	}
	return nil
}

func (result MatchResult) Validate() error {
	switch result.Outcome {
	case OutcomeCandidate:
		if !isIdentifier(result.CandidateID) {
			return fmt.Errorf("candidate match requires an opaque candidate ID")
		}
	case OutcomeNoMatch, OutcomeAmbiguous, OutcomeError:
		if result.CandidateID != "" {
			return fmt.Errorf("non-candidate match cannot carry a candidate ID")
		}
	default:
		return fmt.Errorf("unknown match outcome %q", result.Outcome)
	}
	if !isIdentifier(result.BindingID) || !isSHA256(result.SnapshotSHA256) {
		return fmt.Errorf("match result requires binding ID and snapshot digest")
	}
	return nil
}

func (snapshot CandidateSetSnapshot) ValidateFor(tenantID, sessionID string, generation uint64, now time.Time) error {
	if snapshot.ProtectedSnapshot == nil {
		return fmt.Errorf("candidate set requires a protected authorized snapshot reference")
	}
	if err := snapshot.ProtectedSnapshot.ValidateFor(tenantID, sessionID, generation, now); err != nil {
		return fmt.Errorf("candidate snapshot: %w", err)
	}
	if snapshot.ProtectedSnapshot.Purpose != "directory-snapshot" {
		return fmt.Errorf("candidate snapshot reference must have directory-snapshot purpose")
	}
	seen := map[string]bool{}
	for _, candidateID := range snapshot.CandidateIDs {
		if !isIdentifier(candidateID) || seen[candidateID] {
			return fmt.Errorf("authorized candidate IDs must be stable and unique")
		}
		seen[candidateID] = true
	}
	if !isSHA256(snapshot.SHA256) || snapshot.SHA256 != snapshot.CanonicalSHA256() {
		return fmt.Errorf("candidate set digest does not match its protected snapshot and opaque IDs")
	}
	return nil
}

func (result MatchResult) ValidateAgainst(snapshot CandidateSetSnapshot, tenantID, sessionID string, generation uint64, now time.Time) error {
	if err := result.Validate(); err != nil {
		return err
	}
	if err := snapshot.ValidateFor(tenantID, sessionID, generation, now); err != nil {
		return err
	}
	if result.SnapshotSHA256 != snapshot.SHA256 {
		return fmt.Errorf("match result snapshot digest does not match the authorized candidate set")
	}
	if result.Outcome == OutcomeCandidate && !containsString(snapshot.CandidateIDs, result.CandidateID) {
		return fmt.Errorf("match result candidate is outside the authorized candidate set")
	}
	return nil
}

func (result DecisionResult) ValidateAgainst(snapshot CandidateSetSnapshot, tenantID, sessionID string, generation uint64, now time.Time) error {
	if err := result.Validate(); err != nil {
		return err
	}
	if err := snapshot.ValidateFor(tenantID, sessionID, generation, now); err != nil {
		return err
	}
	if result.CandidateSetSHA256 != snapshot.SHA256 {
		return fmt.Errorf("decision result candidate set digest does not match the authorized snapshot")
	}
	if result.Outcome == OutcomeCandidate && !containsString(snapshot.CandidateIDs, result.CandidateID) {
		return fmt.Errorf("decision result candidate is outside the authorized candidate set")
	}
	return nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func (request EffectRequest) Validate(now time.Time) error {
	if err := request.Identity.Validate(); err != nil {
		return err
	}
	if !isIdentifier(request.ToolID) || !isKnownEffect(request.Kind) {
		return fmt.Errorf("effect request requires an inherited tool ID and known effect kind")
	}
	if request.TargetBinding != "" && !isIdentifier(request.TargetBinding) {
		return fmt.Errorf("effect request target binding must be a stable identifier")
	}
	if (request.Kind == EffectConnect || request.Kind == EffectHandoff) && !request.ReauthorizeAtExecution {
		return fmt.Errorf("call-control effects require current authorization at execution")
	}
	if (request.Kind == EffectConnect || request.Kind == EffectHandoff) && !isIdentifier(request.TargetOpaqueID) {
		return fmt.Errorf("call-control effects require an opaque authorized target ID")
	}
	if (request.Kind == EffectConnect || request.Kind == EffectHandoff) && (!isIdentifier(request.TargetBinding) || !isSHA256(request.CandidateSetSHA256)) {
		return fmt.Errorf("call-control effects require an authorized target binding and candidate-set digest")
	}
	if request.CandidateSetSHA256 != "" && !isSHA256(request.CandidateSetSHA256) {
		return fmt.Errorf("effect request candidate-set digest must be a lowercase SHA-256 digest")
	}
	if request.Payload != nil {
		if err := request.Payload.ValidateFor(request.Identity.TenantID, request.Identity.SessionID, request.Identity.Generation, now); err != nil {
			return fmt.Errorf("protected effect payload: %w", err)
		}
	}
	return nil
}

// ValidateAgainst is the mandatory pre-dispatch check for an effect. It binds
// the request to the exact route ActStep, inherited target binding, and scoped
// protected candidate snapshot before a call-control side effect can start.
func (request EffectRequest) ValidateAgainst(action ActStep, snapshot *CandidateSetSnapshot, tenantID, sessionID string, generation uint64, now time.Time) error {
	if err := request.Validate(now); err != nil {
		return err
	}
	if request.Identity.TenantID != tenantID || request.Identity.SessionID != sessionID || request.Identity.Generation != generation {
		return fmt.Errorf("effect request scope does not match the authorized candidate snapshot")
	}
	if request.Identity.ActionID != action.ID || request.ToolID != action.ToolID || request.Kind != action.Kind || request.TargetBinding != action.TargetBinding {
		return fmt.Errorf("effect request does not match the frozen route action and target binding")
	}
	if action.ReleasesCallOwnership && (!action.ReauthorizeAtExecution || request.Kind != EffectConnect && request.Kind != EffectHandoff) {
		return fmt.Errorf("call ownership release requires a reauthorized call-control action")
	}
	if request.Kind != EffectConnect && request.Kind != EffectHandoff {
		return nil
	}
	if snapshot == nil {
		return fmt.Errorf("call-control effect requires the authorized candidate snapshot before dispatch")
	}
	if err := snapshot.ValidateFor(tenantID, sessionID, generation, now); err != nil {
		return err
	}
	if request.CandidateSetSHA256 != snapshot.SHA256 {
		return fmt.Errorf("effect request candidate-set digest does not match the authorized snapshot")
	}
	if !containsString(snapshot.CandidateIDs, request.TargetOpaqueID) {
		return fmt.Errorf("effect target is outside the authorized candidate set")
	}
	return nil
}

// ValidateForDispatch resolves the exact action from an activation-ready
// frozen definition and checks the target against its scoped protected
// snapshot. Effect executors must pass this before creating a call-control
// operation; Validate alone is structural and is not dispatch authority.
func (request EffectRequest) ValidateForDispatch(definition Definition, routeID RouteID, snapshot *CandidateSetSnapshot, tenantID, sessionID string, generation uint64, now time.Time) error {
	if err := definition.ValidateForActivation(); err != nil {
		return fmt.Errorf("effect dispatch requires an activation-ready definition: %w", err)
	}
	if request.Identity.GraphSHA256 != definition.DigestInputs.Graph {
		return fmt.Errorf("effect request graph digest does not match the frozen definition")
	}
	var action *ActStep
	for _, route := range definition.Graph.Routes {
		if route.ID != routeID {
			continue
		}
		for index := range route.Act {
			if route.Act[index].ID == request.Identity.ActionID {
				candidate := route.Act[index]
				action = &candidate
				break
			}
		}
		break
	}
	if action == nil {
		return fmt.Errorf("effect action does not exist in the frozen route")
	}
	return request.ValidateAgainst(*action, snapshot, tenantID, sessionID, generation, now)
}

func (usage UsageRecord) Validate() error {
	switch usage.Status {
	case UsageMeasured, UsageProviderReported, UsageEstimated:
		if usage.CostMicros == nil {
			return fmt.Errorf("usage status %q requires a cost value", usage.Status)
		}
	case UsageMissing:
		if usage.CostMicros != nil {
			return fmt.Errorf("missing usage cannot claim a cost value")
		}
	default:
		return fmt.Errorf("unknown usage status %q", usage.Status)
	}
	for _, unit := range usage.Units {
		if !isIdentifier(unit.Kind) || math.IsNaN(unit.Amount) || math.IsInf(unit.Amount, 0) || unit.Amount < 0 {
			return fmt.Errorf("usage units require a stable kind and finite non-negative amount")
		}
	}
	return nil
}

func (capabilities VoiceCapabilities) ValidateFor(required []VoiceCapabilityName, locale string) error {
	if !isIdentifier(capabilities.ProfileRef) || !isIdentifier(capabilities.Revision) || !isIdentifier(capabilities.VoiceID) {
		return fmt.Errorf("resolved voice profile, revision, and voice IDs are required")
	}
	if !isLocale(locale) || capabilities.Locale != locale {
		return fmt.Errorf("resolved voice locale must match the pinned session locale")
	}
	if capabilities.EvidenceRef == "" {
		return fmt.Errorf("voice capabilities require qualification evidence")
	}
	localeFound := false
	for _, supported := range capabilities.SupportedLocales {
		localeFound = localeFound || supported == locale
	}
	if !localeFound {
		return fmt.Errorf("voice profile does not advertise the pinned locale")
	}
	if len(capabilities.Formats) == 0 {
		return fmt.Errorf("voice profile must advertise at least one audio format")
	}
	for _, format := range capabilities.Formats {
		if strings.TrimSpace(format.Codec) == "" || format.SampleRateHz == 0 || format.Channels == 0 {
			return fmt.Errorf("voice audio formats require codec, sample rate, and channel count")
		}
	}
	for _, capability := range required {
		if capabilities.Capabilities[capability] != CapabilitySupported {
			return fmt.Errorf("required voice capability %q is not qualified as supported", capability)
		}
	}
	return nil
}
