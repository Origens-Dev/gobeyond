package temporalruntime

import (
	"testing"

	"github.com/Origens-Dev/go-ai/packages/ai"
	"github.com/Origens-Dev/gobeyond/agents"
)

func TestGrokSessionToolsUsesNativeWebSearch(t *testing.T) {
	definition := agents.DefineAI(agents.AIConfig{
		Tools: map[string]ai.Tool{
			"web-search": {Name: "web-search", Description: "Search public web facts."},
			"lookup":     {Name: "lookup", Description: "Look up an internal record."},
		},
	})
	tools := grokSessionTools(voiceToolsFromDefinition(definition, []string{"web_search", "lookup"}))
	if len(tools) != 2 {
		t.Fatalf("Grok session tools = %#v", tools)
	}
	if got, _ := tools[0]["type"].(string); got != "web_search" {
		t.Fatalf("native web search declaration = %#v", tools[0])
	}
	if got, _ := tools[1]["type"].(string); got != "function" {
		t.Fatalf("custom declaration = %#v", tools[1])
	}
	if got, _ := tools[1]["name"].(string); got != "lookup" {
		t.Fatalf("custom tool name = %q", got)
	}
}

func TestGrokSessionToolsDoesNotEnableWebSearchWhenNotAllowed(t *testing.T) {
	definition := agents.DefineAI(agents.AIConfig{
		Tools: map[string]ai.Tool{
			"web-search": {Name: "web-search", Description: "Search public web facts."},
			"lookup":     {Name: "lookup", Description: "Look up an internal record."},
		},
	})
	tools := grokSessionTools(voiceToolsFromDefinition(definition, []string{"lookup"}))
	if len(tools) != 1 || tools[0]["type"] != "function" || tools[0]["name"] != "lookup" {
		t.Fatalf("Grok session tools = %#v", tools)
	}
}

func TestGrokOpeningResponseUsesDefault(t *testing.T) {
	t.Setenv("GOBEYOND_LIVE_OPENING_TURN", "")

	message, ok := grokOpeningResponse()
	if !ok {
		t.Fatal("default opening response disabled")
	}
	if got := message["type"]; got != "response.create" {
		t.Fatalf("opening event type = %#v", got)
	}
	response, ok := message["response"].(map[string]any)
	if !ok || response["instructions"] != defaultGrokOpeningTurn {
		t.Fatalf("opening response = %#v", message["response"])
	}
}

func TestGrokOpeningResponseCanBeOverriddenOrDisabled(t *testing.T) {
	t.Setenv("GOBEYOND_LIVE_OPENING_TURN", "Say hello to Andrew.")
	message, ok := grokOpeningResponse()
	if !ok || message["response"].(map[string]any)["instructions"] != "Say hello to Andrew." {
		t.Fatalf("override opening response = %#v", message)
	}

	t.Setenv("GOBEYOND_LIVE_OPENING_TURN", "-")
	if message, ok := grokOpeningResponse(); ok || message != nil {
		t.Fatalf("disabled opening response = %#v, enabled=%t", message, ok)
	}
}

func TestGrokReasoningEffortDefaultsToNone(t *testing.T) {
	t.Setenv("GOBEYOND_GROK_REASONING_EFFORT", "")
	if got := grokReasoningEffort(); got != "none" {
		t.Fatalf("default reasoning effort = %q, want none", got)
	}
}

func TestGrokReasoningEffortAllowsHighOverride(t *testing.T) {
	t.Setenv("GOBEYOND_GROK_REASONING_EFFORT", "high")
	if got := grokReasoningEffort(); got != "high" {
		t.Fatalf("high reasoning effort = %q, want high", got)
	}
}

func TestGrokReasoningEffortRejectsUnknownValues(t *testing.T) {
	t.Setenv("GOBEYOND_GROK_REASONING_EFFORT", "balanced")
	if got := grokReasoningEffort(); got != "none" {
		t.Fatalf("unknown reasoning effort = %q, want none", got)
	}
}

func TestGrokResponseCompletedOnlyAcceptsCompletedOrMissingStatus(t *testing.T) {
	for _, status := range []string{"", "completed", " COMPLETED "} {
		if !grokResponseCompleted(status) {
			t.Errorf("status %q treated as incomplete", status)
		}
	}
	for _, status := range []string{"cancelled", "failed", "incomplete"} {
		if grokResponseCompleted(status) {
			t.Errorf("status %q treated as complete", status)
		}
	}
}
