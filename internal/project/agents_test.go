package project

import (
	"encoding/json"
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
	writeSourceTestFile(t, filepath.Join(root, "agents", "research", "agent.go"), `package research

import (
	"context"
	gbagents "github.com/Origens-Dev/gobeyond/agents"
)

func Run(_ context.Context, _ gbagents.Actor, input string) (string, error) { return input, nil }
var Agent = gbagents.Define(gbagents.Config{Durable: true}, Run)
`)

	definitions, err := DiscoverAgentDefinitions(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 3 {
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
	if research := byID["research"]; research.TaskQueue != "assist" {
		t.Fatalf("subagent did not inherit root queue: %#v", research)
	}
}

func TestDiscoverAIAgentAllowsInferenceWithoutManifestingIt(t *testing.T) {
	root := t.TempDir()
	writeSourceTestFile(t, filepath.Join(root, "agents", "assistant", "agent.go"), `package assistant
import gbagents "github.com/Origens-Dev/gobeyond/agents"
var Agent = gbagents.DefineAI(gbagents.AIConfig{
  Model: "openai/gpt-4o-mini", Inference: "openrouter", Durable: true,
})
`)
	writeSourceTestFile(t, filepath.Join(root, "agents", "assistant", "instructions.md"), "You are a concise assistant.\n")
	definitions, err := DiscoverAgentDefinitions(root)
	if err != nil || len(definitions) != 1 {
		t.Fatalf("definitions = %#v, err = %v", definitions, err)
	}
	definition := definitions[0]
	if definition.Model != "openai/gpt-4o-mini" || definition.Kind != AgentKindAI {
		t.Fatalf("AI definition = %#v", definition)
	}
	encoded, err := json.Marshal(portableAgentsManifest(definitions, "build-test"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(string(encoded)), "inference") {
		t.Fatalf("Inference leaked into compiler manifest: %s", encoded)
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

func TestDiscoverAgentGraphResolvesToolSubagentAndRealtimeQueues(t *testing.T) {
	root := t.TempDir()
	writeSourceTestFile(t, filepath.Join(root, "agents", "support", "agent.go"), `package support
import (
  "context"
  gbagents "github.com/Origens-Dev/gobeyond/agents"
)
var lookup = gbagents.DefineTool(gbagents.ToolConfig{TaskQueue: "support-tools"}, func(context.Context, gbagents.Actor, string) (string, error) { return "", nil })
var Agent = gbagents.DefineAI(gbagents.AIConfig{
  Model: "anthropic/test", Durable: true, TaskQueue: "support",
  Tools: map[string]gbagents.AITool{"lookup": lookup},
}, gbagents.Slots{Subagents: []gbagents.Subagent{{ID: "research"}}})
`)
	writeSourceTestFile(t, filepath.Join(root, "agents", "support", "instructions.md"), "Support.")
	writeSourceTestFile(t, filepath.Join(root, "agents", "research", "agent.go"), agentSource(`gbagents.Config{Durable: true}, Run`))
	writeSourceTestFile(t, filepath.Join(root, "agents", "realtime", "agent.go"), `package realtime
import (
  "context"
  gbagents "github.com/Origens-Dev/gobeyond/agents"
)
var local = gbagents.DefineTool(gbagents.ToolConfig{}, func(context.Context, gbagents.Actor, string) (string, error) { return "", nil })
var Agent = gbagents.DefineAI(gbagents.AIConfig{
  Model: "anthropic/test", Durable: true, Realtime: true,
  Tools: map[string]gbagents.AITool{"local": local},
})
`)
	writeSourceTestFile(t, filepath.Join(root, "agents", "realtime", "instructions.md"), "Realtime.")

	definitions, err := DiscoverAgentDefinitions(root)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]AgentDefinition{}
	for _, definition := range definitions {
		byID[definition.ID] = definition
	}
	if got := byID["research"].TaskQueue; got != "support" {
		t.Fatalf("research queue = %q", got)
	}
	if tools := byID["support"].Tools; len(tools) != 1 || tools[0].ID != "lookup" || tools[0].TaskQueue != "support-tools" {
		t.Fatalf("support tools = %#v", tools)
	}
	realtime := byID["realtime"]
	if !realtime.Realtime || !strings.HasPrefix(realtime.TaskQueue, "realtime-") || len(realtime.Tools) != 1 || realtime.Tools[0].TaskQueue != realtime.TaskQueue {
		t.Fatalf("realtime definition = %#v", realtime)
	}
	queues := GroupWorkerQueues(nil, definitions)
	queueIDs := make([]string, 0, len(queues))
	for _, queue := range queues {
		queueIDs = append(queueIDs, queue.ID)
	}
	for _, expected := range []string{"support", "support-tools", realtime.TaskQueue} {
		if !containsString(queueIDs, expected) {
			t.Fatalf("worker queues %v missing %q", queueIDs, expected)
		}
	}
}

func TestDiscoverAgentGraphRejectsInvalidExecutionCombinations(t *testing.T) {
	tests := []struct {
		name    string
		sources map[string]string
		want    string
	}{
		{
			name:    "direct queue",
			sources: map[string]string{"agents/a/agent.go": agentSource(`gbagents.Config{TaskQueue: "a"}, Run`)},
			want:    "direct agents cannot set TaskQueue",
		},
		{
			name:    "realtime explicit queue",
			sources: map[string]string{"agents/a/agent.go": agentSource(`gbagents.Config{Durable: true, Realtime: true, TaskQueue: "a"}, Run`)},
			want:    "compiler-derived unique TaskQueue",
		},
		{
			name:    "missing subagent",
			sources: map[string]string{"agents/a/agent.go": agentSource(`gbagents.Config{Durable: true}, Run, gbagents.Slots{Subagents: []gbagents.Subagent{{ID: "missing"}}}`)},
			want:    "does not exist",
		},
		{
			name: "cycle",
			sources: map[string]string{
				"agents/a/agent.go": agentSource(`gbagents.Config{Durable: true}, Run, gbagents.Slots{Subagents: []gbagents.Subagent{{ID: "b"}}}`),
				"agents/b/agent.go": strings.Replace(agentSource(`gbagents.Config{Durable: true}, Run, gbagents.Slots{Subagents: []gbagents.Subagent{{ID: "a"}}}`), "package support", "package b", 1),
			},
			want: "contains a cycle",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			for path, source := range test.sources {
				writeSourceTestFile(t, filepath.Join(root, path), source)
			}
			_, err := DiscoverAgentDefinitions(root)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
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
		{"ai-inference-grok", "agents/support/agent.go", `package support
import gbagents "github.com/Origens-Dev/gobeyond/agents"
var Agent = gbagents.DefineAI(gbagents.AIConfig{Model: "x-ai/grok-4.6", Inference: "grok"})
`, "Inference \"grok\" is not supported"},
		{"ai-inference-openai", "agents/support/agent.go", `package support
import gbagents "github.com/Origens-Dev/gobeyond/agents"
var Agent = gbagents.DefineAI(gbagents.AIConfig{Model: "openai/gpt-4o-mini", Inference: "openai"})
`, "Inference \"openai\" is not supported"},
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
