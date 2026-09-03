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
