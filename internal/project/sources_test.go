package project

import (
	"os"
	"os/exec"
	"path/filepath"
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

	projectedPage := filepath.Join(root, "internal", "gobeyondgen", "routes", routes[1].ID, "page.go")
	assertSourceTestContains(t, projectedPage,
		generatedSourceMarker,
		"//line app/products/[slug]/page.go:1",
		"import \"net/http\"",
	)
	assertSourceTestContains(t,
		filepath.Join(root, "internal", "gobeyondgen", "routes", routes[1].ID, "actions.go"),
		"//line app/products/[slug]/actions.go:1",
	)
	assertSourceTestContains(t,
		filepath.Join(root, "internal", "gobeyondgen", "api", "r_api_time_066a4b03", "route.go"),
		"//line app/api/time/route.go:1",
	)
	assertSourceTestContains(t, filepath.Join(routeDir, "go.mod"),
		generatedModuleMarker,
		"module example.com/site/internal/gobeyondroute/"+routes[1].ID,
		"require github.com/Origens-Dev/gobeyond v0.0.0",
		"replace example.com/site => \"../../..\"",
	)
	assertSourceTestContains(t, filepath.Join(root, "internal", "gobeyondgen", "routes", "routes_gen.go"), "package routes")
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
	projected := filepath.Join(root, "internal", "gobeyondgen", "routes", routes[0].ID, "page.go")
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
	if err := SyncGoSources(root, routes, false); err == nil || !strings.Contains(err.Error(), "imports route-owned package") {
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
