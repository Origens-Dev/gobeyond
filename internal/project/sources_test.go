package project

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestSyncGoSourcesProjectsRoutesAndAPIs(t *testing.T) {
	root := t.TempDir()
	writeTestModule(t, root)
	writeSourceTestFile(t, filepath.Join(root, "app", "page.tsx"), "export default function Page() { return null }\n")
	routeDir := filepath.Join(root, "app", "products", "[slug]")
	writeSourceTestFile(t, filepath.Join(routeDir, "page.tsx"), "export default function Page() { return null }\n")
	writeSourceTestFile(t, filepath.Join(routeDir, "page.go"), "package products_slug\n\nimport \"net/http\"\n\ntype Props struct{}\n\nvar Status = http.StatusOK\n")
	writeSourceTestFile(t, filepath.Join(routeDir, "actions.go"), "package products_slug\n\nimport \"errors\"\n\nvar ErrAction = errors.New(\"action\")\n")
	writeSourceTestFile(t, filepath.Join(root, "app", "api", "time", "route.go"), "package timeapi\n\nfunc GET() {}\n")
	writeSourceTestFile(t, filepath.Join(root, "workflows", "demo", "workflow.go"), `package demo

import (
	gbworkflows "github.com/Origens-Dev/gobeyond/workflows"
	"go.temporal.io/sdk/workflow"
)

func Run(ctx workflow.Context) error { return nil }
var Workflow = gbworkflows.Define(gbworkflows.WorkflowConfig{Name: "demo.echo"}, Run)
`)

	routes, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if routes[1].Mode != "dynamic" || !strings.HasSuffix(routes[1].ServerFile, "/app/products/[slug]/page.go") {
		t.Fatalf("co-located route discovery = %#v", routes[1])
	}
	if err := Write(root, routes, "b_projection", false); err != nil {
		t.Fatal(err)
	}

	projectedPage := filepath.Join(root, GeneratedDir, "routes", routes[1].ID, "page.go")
	assertSourceTestContains(t, projectedPage,
		generatedSourceMarker,
		"//line app/products/[slug]/page.go:1",
		"import \"net/http\"",
	)
	assertSourceTestContains(t,
		filepath.Join(root, GeneratedDir, "routes", routes[1].ID, "actions.go"),
		"//line app/products/[slug]/actions.go:1",
	)
	assertSourceTestContains(t,
		filepath.Join(root, GeneratedDir, "api", "r_api_time_066a4b03", "route.go"),
		"//line app/api/time/route.go:1",
	)
	definitions, err := DiscoverWorkflowDefinitions(root)
	if err != nil || len(definitions) != 1 {
		t.Fatalf("workflow definitions = %#v err=%v", definitions, err)
	}
	assertSourceTestContains(t,
		filepath.Join(root, GeneratedDir, "workflows", definitions[0].Key, "workflow.go"),
		"//line workflows/demo/workflow.go:1",
		"var Workflow = gbworkflows.Define",
	)
	assertSourceTestContains(t, filepath.Join(root, GeneratedDir, "workflows", definitions[0].Key, "gobeyond_register_gen.go"),
		"func GobeyondRegister",
		"RegisterWorkflow(registry, Workflow, \"demo.echo\")",
	)
	assertSourceTestContains(t, filepath.Join(root, GeneratedDir, "cmd", "workflows", "default", "main.go"),
		"definition0.GobeyondRegister(w)",
	)
	assertSourceTestContains(t, filepath.Join(root, GeneratedDir, "workflows", "references", "references_gen.go"),
		"var WorkflowDemo = gbworkflows.WorkflowReference",
		`Name: "demo.echo"`,
		`TaskQueue: "default"`,
	)
	assertSourceTestContains(t, filepath.Join(root, "workflows", "demo", "go.mod"),
		generatedModuleMarker,
		"module example.com/site/internal/gobeyondroute/"+definitions[0].Key,
	)
	assertSourceTestContains(t, filepath.Join(routeDir, "go.mod"),
		generatedModuleMarker,
		"module example.com/site/internal/gobeyondroute/"+routes[1].ID,
		"require github.com/Origens-Dev/gobeyond v0.0.0",
		"replace example.com/site => \"../../..\"",
	)
	assertSourceTestContains(t, filepath.Join(root, GeneratedDir, "routes", "routes_gen.go"), "package routes")
}

func TestGeneratedRouteModulePropagatesLocalReplacements(t *testing.T) {
	root := t.TempDir()
	writeTestModule(t, root)
	moduleFile := filepath.Join(root, "go.mod")
	moduleData, err := os.ReadFile(moduleFile)
	if err != nil {
		t.Fatal(err)
	}
	moduleData = append(moduleData, []byte("\nreplace github.com/Origens-Dev/gobeyond => ../gobeyond\n")...)
	if err := os.WriteFile(moduleFile, moduleData, 0o644); err != nil {
		t.Fatal(err)
	}
	routeDir := filepath.Join(root, "app", "products", "[slug]")
	writeSourceTestFile(t, filepath.Join(routeDir, "page.tsx"), "fixture\n")
	writeSourceTestFile(t, filepath.Join(routeDir, "page.go"), "package products_slug\n\ntype Props struct{}\n")
	routes, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := SyncGoSources(root, routes, false); err != nil {
		t.Fatal(err)
	}
	assertSourceTestContains(t, filepath.Join(routeDir, "go.mod"),
		`replace github.com/Origens-Dev/gobeyond => "../../../../gobeyond"`,
	)
}

func TestSyncGoSourcesBuildsStandaloneActivityWrapper(t *testing.T) {
	root := t.TempDir()
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	moduleRoot, _, _, err := findModule(workingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	writeSourceTestFile(t, filepath.Join(root, "go.mod"), "module example.com/site\n\ngo 1.24.0\n\nrequire github.com/Origens-Dev/gobeyond v0.0.0\n\nreplace github.com/Origens-Dev/gobeyond => "+strconv.Quote(filepath.ToSlash(moduleRoot))+"\n")
	writeSourceTestFile(t, filepath.Join(root, "workflows", "send-receipt", "activity.go"), `package receipt

import (
	"context"
	gbworkflows "github.com/Origens-Dev/gobeyond/workflows"
)

type Input struct { OrderID string }
type Output struct { Sent bool }
func Send(ctx context.Context, input Input) (Output, error) { return Output{Sent: true}, nil }
var Activity = gbworkflows.DefineActivity(gbworkflows.ActivityConfig{Name: "receipt.send"}, Send)
`)
	if err := Write(root, nil, "b_workflow_fixture", false); err != nil {
		t.Fatal(err)
	}
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = root
	if output, err := tidy.CombinedOutput(); err != nil {
		t.Fatalf("tidy generated workflow fixture: %v\n%s", err, output)
	}
	command := exec.Command("go", "test", "./generated/cmd/workflows/default")
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generated standalone activity wrapper does not compile: %v\n%s", err, output)
	}
	definitions, err := DiscoverWorkflowDefinitions(root)
	if err != nil {
		t.Fatal(err)
	}
	assertSourceTestContains(t,
		filepath.Join(root, GeneratedDir, "workflows", definitions[0].Key, "gobeyond_register_gen.go"),
		"RegisterWorkflowWithOptions(gobeyondStandaloneWorkflow",
		`Name: "receipt.send"`,
		"func gobeyondStandaloneWorkflow(ctx temporalworkflow.Context, input Input) (Output, error)",
	)
	assertSourceTestContains(t,
		filepath.Join(root, GeneratedDir, "workflows", "references", "references_gen.go"),
		"var WorkflowSendReceipt = gbworkflows.WorkflowReference",
		"var ActivitySendReceipt = gbworkflows.ActivityReference",
	)
}

func TestSyncGoSourcesBuildsStandaloneActivityWrapperWithAnonymousTypes(t *testing.T) {
	root := t.TempDir()
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	moduleRoot, _, _, err := findModule(workingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	writeSourceTestFile(t, filepath.Join(root, "go.mod"), "module example.com/site\n\ngo 1.24.0\n\nrequire github.com/Origens-Dev/gobeyond v0.0.0\n\nreplace github.com/Origens-Dev/gobeyond => "+strconv.Quote(filepath.ToSlash(moduleRoot))+"\n")
	writeSourceTestFile(t, filepath.Join(root, "workflows", "format-receipt", "activity.go"), `package receipt

import (
	"context"
	gbworkflows "github.com/Origens-Dev/gobeyond/workflows"
)

func Send(ctx context.Context, input struct{ OrderID string }) (struct{ Sent bool }, error) {
	return struct{ Sent bool }{Sent: input.OrderID != ""}, nil
}
var Activity = gbworkflows.DefineActivity(gbworkflows.ActivityConfig{Name: "receipt.format"}, Send)
`)
	if err := Write(root, nil, "b_anonymous_activity", false); err != nil {
		t.Fatal(err)
	}
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = root
	if output, err := tidy.CombinedOutput(); err != nil {
		t.Fatalf("tidy generated anonymous activity fixture: %v\n%s", err, output)
	}
	command := exec.Command("go", "test", "./generated/cmd/workflows/default")
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generated anonymous standalone activity wrapper does not compile: %v\n%s", err, output)
	}
}

func TestGeneratedWorkflowReferencesDisambiguateNormalizedIDs(t *testing.T) {
	root := t.TempDir()
	writeTestModule(t, root)
	writeSourceTestFile(t, filepath.Join(root, "workflows", "foo-bar", "workflow.go"), `package foobar

import gbworkflows "github.com/Origens-Dev/gobeyond/workflows"

func Run() error { return nil }
var Workflow = gbworkflows.Define(gbworkflows.WorkflowConfig{Name: "first"}, Run)
`)
	writeSourceTestFile(t, filepath.Join(root, "workflows", "foo", "workflow.go"), `package foo

import gbworkflows "github.com/Origens-Dev/gobeyond/workflows"

func Run() error { return nil }
var Workflow = gbworkflows.Define(gbworkflows.WorkflowConfig{Name: "parent"}, Run)
`)
	writeSourceTestFile(t, filepath.Join(root, "workflows", "foo", "subworkflows", "bar", "workflow.go"), `package bar

import gbworkflows "github.com/Origens-Dev/gobeyond/workflows"

func Run() error { return nil }
var Workflow = gbworkflows.Define(gbworkflows.WorkflowConfig{Name: "second"}, Run)
`)
	if err := Write(root, nil, "b_reference_collision", false); err != nil {
		t.Fatal(err)
	}
	definitions, err := DiscoverWorkflowDefinitions(root)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, GeneratedDir, "workflows", "references", "references_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, definition := range definitions {
		if !strings.Contains(text, `Name: "`+definition.Name+`"`) {
			t.Fatalf("missing reference for %#v:\n%s", definition, text)
		}
	}
	if strings.Count(text, "var WorkflowFooBar") != 2 {
		t.Fatalf("colliding normalized names were not disambiguated:\n%s", text)
	}
}

func TestSyncGoSourcesProjectsAgentsAndWritesManifest(t *testing.T) {
	root := t.TempDir()
	writeTestModule(t, root)
	writeSourceTestFile(t, filepath.Join(root, "agents", "support", "agent.go"), `package support

import (
	"context"
	gbagents "github.com/Origens-Dev/gobeyond/agents"
)

type Input struct { Text string }
type Output struct { Text string }
func Run(ctx context.Context, actor gbagents.Actor, input Input) (Output, error) { return Output{Text: input.Text}, nil }
var Agent = gbagents.Define(gbagents.Config{Durable: true, Public: true}, Run, gbagents.Slots{
	Tools: []gbagents.Tool{{ID: "search"}},
	Channels: []gbagents.Channel{{ID: "web"}},
})
`)
	if err := Write(root, nil, "b_agent_fixture", false); err != nil {
		t.Fatal(err)
	}
	definitions, err := DiscoverAgentDefinitions(root)
	if err != nil || len(definitions) != 1 {
		t.Fatalf("agent definitions = %#v err=%v", definitions, err)
	}
	definition := definitions[0]
	assertSourceTestContains(t,
		filepath.Join(root, GeneratedDir, "agents", definition.Key, "agent.go"),
		"//line agents/support/agent.go:1",
		"var Agent = gbagents.Define",
	)
	assertSourceTestContains(t,
		filepath.Join(root, GeneratedDir, "agents", definition.Key, "gobeyond_register_gen.go"),
		"func GobeyondRegister(registry httpruntime.Registerer) error",
		"func GobeyondRegisterSIP(registry gbsip.Registerer) error",
		`definition.Config.TaskQueue = "default"`,
		`registry.Register("support", httpruntime.Adapt(definition))`,
		"func GobeyondRegisterTemporal(registry worker.Registry) error",
		`temporalruntime.Register(registry, "support", definition)`,
	)
	assertSourceTestContains(t,
		filepath.Join(root, GeneratedDir, "cmd", "workflows", "default", "main.go"),
		`agent0 "example.com/site/generated/agents/`+definition.Key+`"`,
		"agent0.GobeyondRegisterTemporal(w)",
		`environment = gb.LocalEnvironment`,
		`gb.TaskQueueName("default", environment)`,
	)
	assertSourceTestContains(t, filepath.Join(root, GeneratedDir, "registry", "site.go"),
		`agent0 "example.com/site/generated/agents/`+definition.Key+`"`,
		"agent0.GobeyondRegister(agentRegistry)",
		"agent0.GobeyondRegisterSIP(sipRegistry)",
		`gbsip.NewRegistry()`,
		`os.Getenv("GOBEYOND_SIP_DECISION_TOKEN")`,
		`mux.Handle("/internal/sip/", sipRegistry.Handler(token))`,
		`agentRuntime.Mount(mux, "/api/agents")`,
	)
	assertSourceTestContains(t, filepath.Join(root, GeneratedDir, "cmd", "site", "main.go"),
		`AllowLoopbackAgentActor: os.Getenv("GOBEYOND_AGENT_DEV_LOOPBACK") == "1"`,
		"temporalruntime.NewLazyFromEnv(context.Background())",
		"AgentDispatcher:         dispatcher",
	)
	assertSourceTestContains(t, filepath.Join(root, "agents", "support", "go.mod"),
		generatedModuleMarker,
		"module example.com/site/internal/gobeyondroute/"+definition.Key,
	)
	assertSourceTestContains(t, filepath.Join(root, ".gobeyond", "agents.json"),
		`"apiVersion": "gobeyond.agents/v1alpha4"`,
		`"buildId": "b_agent_fixture"`,
		`"mode": "durable"`,
		`"taskQueue": "default"`,
		`"public": true`,
		`"search"`,
		`"web"`,
	)
}

func TestSyncGoSourcesProjectsAIAgentAndRegistersSDKOnce(t *testing.T) {
	root := t.TempDir()
	writeTestModule(t, root)
	for _, id := range []string{"assistant", "reviewer"} {
		writeSourceTestFile(t, filepath.Join(root, "agents", id, "agent.go"), `package `+id+`
import gbagents "github.com/Origens-Dev/gobeyond/agents"
var Agent = gbagents.DefineAI(gbagents.AIConfig{
  Model: "anthropic/claude-test", Durable: true, Public: true,
})
`)
		writeSourceTestFile(t, filepath.Join(root, "agents", id, "instructions.md"), "You are "+id+".\n")
	}
	if err := Write(root, nil, "b_ai_agent_fixture", false); err != nil {
		t.Fatal(err)
	}
	definitions, err := DiscoverAgentDefinitions(root)
	if err != nil || len(definitions) != 2 {
		t.Fatalf("AI definitions = %#v, err = %v", definitions, err)
	}
	for _, definition := range definitions {
		registration := filepath.Join(root, GeneratedDir, "agents", definition.Key, "gobeyond_register_gen.go")
		assertSourceTestContains(t, registration,
			`httpruntime.RegisterAI(registry, "`+definition.ID+`", definition)`,
			"func GobeyondRegisterTemporalAI(registry worker.Worker, runtimes *temporalruntime.AIRegistry) error",
			`definition.AI.Instructions = "You are `+definition.ID+`."`,
			`definition.AI.Revision = "b_ai_agent_fixture"`,
			`temporalruntime.RegisterAI(registry, runtimes, "`+definition.ID+`", definition)`,
		)
	}
	workerMain := filepath.Join(root, GeneratedDir, "cmd", "workflows", "default", "main.go")
	data, err := os.ReadFile(workerMain)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		"aiRuntimes := temporalruntime.NewAIRegistry()",
		"GobeyondRegisterTemporalAI(w, aiRuntimes)",
		"aiRuntimes.Register(w)",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("generated AI worker missing %q:\n%s", want, text)
		}
	}
	if strings.Count(text, "aiRuntimes.Register(w)") != 1 {
		t.Fatalf("SDK runtime registered more than once:\n%s", text)
	}
	assertSourceTestContains(t, filepath.Join(root, ".gobeyond", "agents.json"),
		`"kind": "ai"`, `"model": "anthropic/claude-test"`, `"revision": "b_ai_agent_fixture"`,
	)
}

func TestSyncGoSourcesUsesSourceBuildIDForAIRevision(t *testing.T) {
	root := t.TempDir()
	writeTestModule(t, root)
	writeSourceTestFile(t, filepath.Join(root, "agents", "assistant", "agent.go"), `package assistant
import gbagents "github.com/Origens-Dev/gobeyond/agents"
var Agent = gbagents.DefineAI(gbagents.AIConfig{Model: "anthropic/claude-test"})
`)
	writeSourceTestFile(t, filepath.Join(root, "agents", "assistant", "instructions.md"), "Help.\n")
	wantRevision, err := BuildID(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := SyncGoSources(root, nil, false); err != nil {
		t.Fatal(err)
	}
	definitions, err := DiscoverAgentDefinitions(root)
	if err != nil || len(definitions) != 1 {
		t.Fatalf("AI definitions = %#v, err = %v", definitions, err)
	}
	assertSourceTestContains(t,
		filepath.Join(root, GeneratedDir, "agents", definitions[0].Key, "gobeyond_register_gen.go"),
		`definition.AI.Revision = "`+wantRevision+`"`,
	)
}

func TestWriteCheckMaterializesIgnoredRouteOutputs(t *testing.T) {
	root := t.TempDir()
	writeTestModule(t, root)
	routeDir := filepath.Join(root, "app", "products", "[slug]")
	writeSourceTestFile(t, filepath.Join(routeDir, "page.tsx"), "fixture\n")
	writeSourceTestFile(t, filepath.Join(routeDir, "page.go"), "package products_slug\n\ntype Props struct{}\n")
	routes, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := Write(root, routes, "b_clean_clone", false); err != nil {
		t.Fatal(err)
	}
	projected := filepath.Join(root, GeneratedDir, "routes", routes[0].ID, "page.go")
	moduleFile := filepath.Join(routeDir, "go.mod")
	manifestFile := filepath.Join(root, ".gobeyond", "routes.json")
	if err := os.Remove(projected); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(moduleFile); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(manifestFile); err != nil {
		t.Fatal(err)
	}
	if err := Write(root, routes, "b_clean_clone", true); err != nil {
		t.Fatalf("check should materialize ignored outputs in a clean clone: %v", err)
	}
	for _, file := range []string{projected, moduleFile, manifestFile} {
		if _, err := os.Stat(file); err != nil {
			t.Fatalf("ignored output %s was not materialized: %v", file, err)
		}
	}
}

func TestSyncGoSourcesProtectsUserModulesAndRejectsRouteImports(t *testing.T) {
	root := t.TempDir()
	writeTestModule(t, root)
	routeDir := filepath.Join(root, "app", "products", "[slug]")
	writeSourceTestFile(t, filepath.Join(routeDir, "page.tsx"), "fixture\n")
	writeSourceTestFile(t, filepath.Join(routeDir, "page.go"), "package products_slug\n\ntype Props struct{}\n")
	writeSourceTestFile(t, filepath.Join(routeDir, "go.mod"), "module example.com/user-owned\n\ngo 1.24.0\n")
	routes, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := SyncGoSources(root, routes, false); err == nil || !strings.Contains(err.Error(), "refusing to overwrite user-owned") {
		t.Fatalf("expected user go.mod protection, got %v", err)
	}

	if err := os.Remove(filepath.Join(routeDir, "go.mod")); err != nil {
		t.Fatal(err)
	}
	writeSourceTestFile(t, filepath.Join(routeDir, "page.go"), "package products_slug\n\nimport _ \"example.com/site/app/account\"\n\ntype Props struct{}\n")
	if err := SyncGoSources(root, routes, false); err == nil || !strings.Contains(err.Error(), "imports framework-owned package") {
		t.Fatalf("expected page-to-page import diagnosis, got %v", err)
	}
}

func TestGeneratedRouteModuleMakesBracketSourceVisibleToGopls(t *testing.T) {
	gopls, err := exec.LookPath("gopls")
	if err != nil {
		t.Skip("gopls is not installed")
	}
	root := t.TempDir()
	writeTestModule(t, root)
	routeDir := filepath.Join(root, "app", "products", "[slug]")
	writeSourceTestFile(t, filepath.Join(routeDir, "page.tsx"), "fixture\n")
	page := filepath.Join(routeDir, "page.go")
	writeSourceTestFile(t, page, "package products_slug\n\nimport \"example.com/site/known-missing\"\n\ntype Props struct{}\n\nvar _ = known_missing.Value\n")
	routes, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := SyncGoSources(root, routes, false); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(gopls, "check", page)
	command.Dir = root
	output, _ := command.CombinedOutput()
	if !strings.Contains(string(output), "could not import example.com/site/known-missing") || !strings.Contains(string(output), "undefined: known_missing") {
		t.Fatalf("gopls did not diagnose the authored bracket route:\n%s", output)
	}
}

func TestGeneratedRouteModuleRespectsNestedWebsiteInternalBoundary(t *testing.T) {
	moduleRoot := t.TempDir()
	writeTestModule(t, moduleRoot)
	website := filepath.Join(moduleRoot, "examples", "site")
	writeSourceTestFile(t, filepath.Join(website, "internal", "shared", "shared.go"), "package shared\n\nconst Value = true\n")
	routeDir := filepath.Join(website, "app", "products", "[slug]")
	writeSourceTestFile(t, filepath.Join(routeDir, "page.tsx"), "fixture\n")
	writeSourceTestFile(t, filepath.Join(routeDir, "page.go"), "package products_slug\n\nimport \"example.com/site/examples/site/internal/shared\"\n\ntype Props struct{}\n\nvar _ = shared.Value\n")
	routes, err := Discover(website)
	if err != nil {
		t.Fatal(err)
	}
	if err := SyncGoSources(website, routes, false); err != nil {
		t.Fatal(err)
	}
	assertSourceTestContains(t, filepath.Join(routeDir, "go.mod"), "module example.com/site/examples/site/internal/gobeyondroute/"+routes[0].ID)
	command := exec.Command("go", "test", "./...")
	command.Dir = routeDir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("nested website route module cannot import its website internal packages: %v\n%s", err, output)
	}
}

func writeSourceTestFile(t *testing.T, file, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertSourceTestContains(t *testing.T, file string, fragments ...string) {
	t.Helper()
	content, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range fragments {
		if !strings.Contains(string(content), fragment) {
			t.Errorf("%s does not contain %q:\n%s", file, fragment, content)
		}
	}
}
