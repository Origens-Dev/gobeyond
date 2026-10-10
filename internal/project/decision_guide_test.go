package project

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	gbagents "github.com/Origens-Dev/gobeyond/agents"
	decisionv1 "github.com/Origens-Dev/gobeyond/agents/decisioncontract/v1"
)

func TestDecisionAgentsGuideRouteCompiles(t *testing.T) {
	guide := readDecisionAgentsGuide(t)
	route := decisionAgentsGuideFence(t, guide, "decision-guide-start-route", "yaml")

	root := writeDecisionCompilerFixture(t, nil)
	writeSourceTestFile(t, filepath.Join(root, "agents", "operator", "routes", "start", "route.yaml"), route)

	definitions, err := DiscoverAgentDefinitions(root)
	if err != nil {
		t.Fatalf("compile documented synthetic route: %v", err)
	}
	if len(definitions) != 1 || definitions[0].Decision == nil {
		t.Fatalf("compiled definitions = %#v, want one decision agent", definitions)
	}
	if got := len(definitions[0].Decision.Graph.Routes); got != 3 {
		t.Fatalf("compiled routes = %d, want start, clarify, and help", got)
	}

	exampleDefinitions, err := DiscoverAgentDefinitions(filepath.Join("..", "..", "docs", "examples", "decision-agents"))
	if err != nil {
		t.Fatalf("compile complete decision-agent example: %v", err)
	}
	guideStart := decisionGuideRoute(t, definitions[0].Decision, "/start")
	exampleStart := decisionGuideRoute(t, decisionAgentByID(t, exampleDefinitions, "operator").Decision, "/start")
	if !reflect.DeepEqual(guideStart, exampleStart) {
		t.Fatal("documented /start route differs from the compiler-validated example source")
	}
}

func TestDecisionAgentsExampleCompilesTwoDistinctOperators(t *testing.T) {
	root := filepath.Join("..", "..", "docs", "examples", "decision-agents")
	definitions, err := DiscoverAgentDefinitions(root)
	if err != nil {
		t.Fatalf("compile complete decision-agent example: %v", err)
	}
	if len(definitions) != 2 {
		t.Fatalf("compiled agents = %d, want two distinct Operators", len(definitions))
	}
	operator := decisionAgentByID(t, definitions, "operator")
	second := decisionAgentByID(t, definitions, "alternate-operator")
	if operator.ID == second.ID || operator.Decision == nil || second.Decision != nil {
		t.Fatalf("operator identities/kinds = (%q, %v), (%q, %v); want a decision Operator and separate direct Operator", operator.ID, operator.Decision != nil, second.ID, second.Decision != nil)
	}
	if operator.Decision.SchemaVersion != decisionv1.SchemaVersion || len(operator.Decision.Graph.Routes) != 3 {
		t.Fatalf("compiled decision schema/routes = %q/%d, want the frozen v1 schema and three routes", operator.Decision.SchemaVersion, len(operator.Decision.Graph.Routes))
	}
	frozenDecision, _, _, err := gbagents.FreezeDecisionManifest(decisionWithParentPlaceholder(*operator.Decision))
	if err != nil {
		t.Fatalf("freeze compiler output before checking activation gates: %v", err)
	}
	if err := frozenDecision.ValidateForActivation(); err == nil || !strings.Contains(err.Error(), "activation blocked by unresolved policy gate") {
		t.Fatalf("synthetic review fixture activation error = %v, want unresolved policy gates", err)
	}

	start := decisionGuideRoute(t, operator.Decision, "/start")
	if start.Say == nil || start.Listen == nil || start.Match == nil || start.Decide == nil || len(start.Act) != 1 {
		t.Fatal("/start does not declare say, listen, match, decide, and one act step")
	}
	if start.Act[0].ToolID != "connect" || start.Act[0].TargetBinding != "selected_candidate_id" {
		t.Fatalf("/start act = %#v, want the declared connect tool and selected-candidate binding", start.Act[0])
	}
	clarify := decisionGuideRoute(t, operator.Decision, "/clarify")
	if clarify.RetryGroup != "recipient_selection" {
		t.Fatalf("clarify retry group = %q, want recipient_selection", clarify.RetryGroup)
	}
	help := decisionGuideRoute(t, operator.Decision, "/help")
	if help.Fallback.Terminal != decisionv1.TerminalSafeStop {
		t.Fatalf("help fallback = %#v, want safe_stop", help.Fallback)
	}

	for _, transition := range []struct {
		route, source, outcome, target string
	}{
		{"/start", "input", "no_input", "/clarify"},
		{"/start", "input", "utterance_limit", "/clarify"},
		{"/start", "input", "error", "/help"},
		{"/start", "match", "error", "/help"},
		{"/start", "decision", "no_match", "/clarify"},
		{"/start", "decision", "error", "/help"},
		{"/clarify", "input", "no_input", "/clarify"},
		{"/clarify", "input", "utterance_limit", "/clarify"},
		{"/clarify", "input", "error", "/help"},
		{"/clarify", "decision", "no_match", "/clarify"},
		{"/clarify", "decision", "error", "/help"},
	} {
		if !decisionGuideHasTransition(decisionGuideRoute(t, operator.Decision, transition.route), transition.source, transition.outcome, transition.target) {
			t.Errorf("%s lacks %s/%s -> %s transition", transition.route, transition.source, transition.outcome, transition.target)
		}
	}

	var retry *decisionv1.RetryGroup
	for index := range operator.Decision.Graph.RetryGroups {
		if operator.Decision.Graph.RetryGroups[index].ID == "recipient_selection" {
			retry = &operator.Decision.Graph.RetryGroups[index]
			break
		}
	}
	if retry == nil || retry.Exhausted.Route != "/help" || !retry.DeduplicateByEventID {
		t.Fatalf("recipient_selection retry group = %#v, want deduplicated exhaustion to /help", retry)
	}
	for _, bound := range []decisionv1.Bound{retry.MaxReprompts, retry.MaxNoInputReprompts, retry.MaxNoMatchReprompts, retry.MaxAmbiguousReprompts} {
		if bound.Value != nil || bound.GateID == "" {
			t.Fatalf("retry bound = %#v, want an unresolved gate and no invented numeric value", bound)
		}
	}
	for _, gateID := range []string{"g-caller-authority", "g-jev-path", "g-locale-profile", "g-voice-profile", "g-session-adapter"} {
		if !decisionGuideGateUnresolved(operator.Decision, gateID) {
			t.Errorf("qualification gate %q is not explicitly unresolved", gateID)
		}
	}
}

func TestDecisionAgentsGuideTraceMatchesFixture(t *testing.T) {
	guide := readDecisionAgentsGuide(t)
	trace := decisionAgentsGuideFence(t, guide, "decision-guide-jev-trace", "text")
	fixture, err := os.ReadFile(filepath.Join("..", "..", "agents", "decisions", "testdata", "jev-clarify.trace"))
	if err != nil {
		t.Fatal(err)
	}
	if trace != string(fixture) {
		t.Fatalf("documented reducer trace differs from test fixture:\n got:\n%s\nwant:\n%s", trace, fixture)
	}
}

func readDecisionAgentsGuide(t *testing.T) string {
	t.Helper()
	guide, err := os.ReadFile(filepath.Join("..", "..", "docs", "guides", "decision-agents.md"))
	if err != nil {
		t.Fatalf("read decision-agent guide: %v", err)
	}
	return string(guide)
}

func decisionAgentByID(t *testing.T, definitions []AgentDefinition, id string) AgentDefinition {
	t.Helper()
	for _, definition := range definitions {
		if definition.ID == id {
			return definition
		}
	}
	t.Fatalf("compiled agents do not contain %q", id)
	return AgentDefinition{}
}

func decisionGuideRoute(t *testing.T, definition *decisionv1.Definition, id string) decisionv1.Route {
	t.Helper()
	if definition == nil {
		t.Fatalf("agent has no compiled decision definition while looking for %s", id)
	}
	for _, route := range definition.Graph.Routes {
		if string(route.ID) == id {
			return route
		}
	}
	t.Fatalf("compiled decision graph has no route %s", id)
	return decisionv1.Route{}
}

func decisionGuideHasTransition(route decisionv1.Route, source, outcome, target string) bool {
	for _, transition := range route.Next {
		if string(transition.Source) == source && string(transition.Outcome) == outcome && string(transition.Target.Route) == target {
			return true
		}
	}
	return false
}

func decisionGuideGateUnresolved(definition *decisionv1.Definition, id string) bool {
	for _, gate := range definition.PolicyGates {
		if gate.ID == id {
			return string(gate.Status) == "unresolved" && gate.Value == "" && gate.EvidenceRef == ""
		}
	}
	return false
}

func decisionAgentsGuideFence(t *testing.T, markdown, marker, language string) string {
	t.Helper()
	startMarker := "<!-- " + marker + " -->"
	markerOffset := strings.Index(markdown, startMarker)
	if markerOffset < 0 {
		t.Fatalf("guide is missing marker %q", marker)
	}
	fence := "~~~" + language + "\n"
	fenceOffset := strings.Index(markdown[markerOffset:], fence)
	if fenceOffset < 0 {
		t.Fatalf("guide marker %q has no %s fence", marker, language)
	}
	contentStart := markerOffset + fenceOffset + len(fence)
	contentEndOffset := strings.Index(markdown[contentStart:], "\n~~~")
	if contentEndOffset < 0 {
		t.Fatalf("guide marker %q has an unterminated fence", marker)
	}
	return markdown[contentStart : contentStart+contentEndOffset+1]
}
