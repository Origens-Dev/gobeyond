package project

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverAgentDefinitions(t *testing.T) {
	root := t.TempDir()
	writeSourceTestFile(t, filepath.Join(root, "agents", "support", "agent.go"), `package support

import (
	"context"
	gbagents "github.com/Origens-Dev/gobeyond/agents"
)

type Input struct{ Text string }
type Output struct{ Text string }
func Run(_ context.Context, _ gbagents.Actor, input Input) (Output, error) { return Output{Text: input.Text}, nil }
var Agent = gbagents.Define(gbagents.Config{TaskQueue: "assist", Durable: true, Public: true}, Run, gbagents.Slots{
	Tools: []gbagents.Tool{{ID: "search"}},
	Skills: []gbagents.Skill{{ID: "tone"}},
	Subagents: []gbagents.Subagent{{ID: "research"}},
	Schedules: []gbagents.Schedule{{ID: "daily", Cron: "0 9 * * *"}},
	Channels: []gbagents.Channel{{ID: "web"}},
})
`)
	writeSourceTestFile(t, filepath.Join(root, "agents", "draft", "agent.go"), `package draft

import (
	"context"
	gbagents "github.com/Origens-Dev/gobeyond/agents"
)

func Run(_ context.Context, _ gbagents.Actor, input string) (string, error) { return input, nil }
var Agent = gbagents.Define(gbagents.Config{}, Run)
`)

	definitions, err := DiscoverAgentDefinitions(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 2 {
		t.Fatalf("definitions = %#v", definitions)
	}
	byID := map[string]AgentDefinition{}
	for _, definition := range definitions {
		byID[definition.ID] = definition
	}
	support := byID["support"]
	if support.Mode != AgentModeDurable || !support.Durable || !support.Public || support.TaskQueue != "assist" {
		t.Fatalf("support metadata = %#v", support)
	}
	if support.InputType != "Input" || support.OutputType != "Output" {
		t.Fatalf("handler types = %q -> %q", support.InputType, support.OutputType)
	}
	if got := support.Slots; len(got.Tools) != 1 || got.Tools[0] != "search" || len(got.Skills) != 1 || len(got.Subagents) != 1 || len(got.Schedules) != 1 || len(got.Channels) != 1 {
		t.Fatalf("slots = %#v", got)
	}
	draft := byID["draft"]
	if draft.Mode != AgentModeDirect || draft.Durable || draft.TaskQueue != "" || draft.Public {
		t.Fatalf("direct defaults = %#v", draft)
	}
}

func TestDiscoverAIAgentDefinitionAndRevision(t *testing.T) {
	root := t.TempDir()
	agentFile := filepath.Join(root, "agents", "assistant", "agent.go")
	writeSourceTestFile(t, agentFile, `package assistant
import gbagents "github.com/Origens-Dev/gobeyond/agents"
var Agent = gbagents.DefineAI(gbagents.AIConfig{
  Model: "anthropic/claude-test", MaxSteps: 4, Durable: true, Public: true,
}, gbagents.Slots{Channels: []gbagents.Channel{{ID: "web"}}})
`)
	instructionsFile := filepath.Join(root, "agents", "assistant", "instructions.md")
	writeSourceTestFile(t, instructionsFile, "You are a concise assistant.\n")
	definitions, err := DiscoverAgentDefinitions(root)
	if err != nil || len(definitions) != 1 {
		t.Fatalf("definitions = %#v, err = %v", definitions, err)
	}
	definition := definitions[0]
	if definition.Kind != AgentKindAI || definition.Model != "anthropic/claude-test" || definition.MaxSteps != 4 || definition.TaskQueue != "default" || definition.Revision == "" {
		t.Fatalf("AI definition = %#v", definition)
	}
	firstRevision := definition.Revision
	writeSourceTestFile(t, instructionsFile, "You are a careful assistant.\n")
	definitions, err = DiscoverAgentDefinitions(root)
	if err != nil || definitions[0].Revision == firstRevision {
		t.Fatalf("instructions did not change revision: %#v, err = %v", definitions, err)
	}
}

func TestDiscoverAgentDefinitionsRejectsInvalidLayoutAndMetadata(t *testing.T) {
	tests := []struct{ name, path, source, want string }{
		{"root-go", "agents/agent.go", "package agents\n", "agents/agent.go is not allowed"},
		{"missing-entry", "agents/support/helper.go", "package support\n", "must contain agent.go"},
		{"physical-queue", "agents/support/agent.go", agentSource(`gbagents.Config{TaskQueue: "support__local"}, Run`), "must be logical"},
		{"nonliteral-durable", "agents/support/agent.go", agentSource(`gbagents.Config{Durable: enabled}, Run`), "Durable must be true or false"},
		{"duplicate-slot", "agents/support/agent.go", agentSource(`gbagents.Config{}, Run, gbagents.Slots{Tools: []gbagents.Tool{{ID: "search"}, {ID: "search"}}}`), "duplicates ID"},
		{"ai-missing-instructions", "agents/support/agent.go", `package support
import gbagents "github.com/Origens-Dev/gobeyond/agents"
var Agent = gbagents.DefineAI(gbagents.AIConfig{Model: "anthropic/test"})
`, "must contain instructions.md"},
		{"ai-missing-model", "agents/support/agent.go", `package support
import gbagents "github.com/Origens-Dev/gobeyond/agents"
var Agent = gbagents.DefineAI(gbagents.AIConfig{})
`, "Model is required"},
		{"bad-handler", "agents/support/agent.go", `package support
import gbagents "github.com/Origens-Dev/gobeyond/agents"
func Run(_ gbagents.Actor, input string) (string, error) { return input, nil }
var Agent = gbagents.Define(gbagents.Config{}, Run)
`, "must accept (context.Context, agents.Actor, input)"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeSourceTestFile(t, filepath.Join(root, test.path), test.source)
			_, err := DiscoverAgentDefinitions(root)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func agentSource(defineArgs string) string {
	return `package support
import (
  "context"
  gbagents "github.com/Origens-Dev/gobeyond/agents"
)
func Run(_ context.Context, _ gbagents.Actor, input string) (string, error) { return input, nil }
var Agent = gbagents.Define(` + defineArgs + `)
`
}
