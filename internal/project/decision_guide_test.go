package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
