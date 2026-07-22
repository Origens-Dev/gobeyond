package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAddPageCreatesSchemaAndPreservesExistingWork(t *testing.T) {
	root := newAddTestProject(t)
	if err := add(root, []string{"page", "articles/[slug]"}); err != nil {
		t.Fatal(err)
	}

	pagePath := filepath.Join(root, "app", "articles", "[slug]", "page.tsx")
	schemaPath := filepath.Join(root, "app", "articles", "[slug]", "page.schema.ts")
	assertAddFileContains(t, pagePath, "export default function Page()", "<h1>articles/[slug]</h1>")
	assertAddFileContains(t, schemaPath, "definePage", "schema.object({})")

	if err := add(root, []string{"page", "articles/[slug]"}); err != nil {
		t.Fatalf("unchanged page scaffold should be idempotent: %v", err)
	}
	if err := os.WriteFile(pagePath, []byte("// user-owned page\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := add(root, []string{"page", "articles/[slug]"}); err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("expected no-overwrite refusal, got %v", err)
	}
	assertAddFileContains(t, pagePath, "// user-owned page")
}

func TestAddDynamicCreatesTypedLoaderWithGeneratedContractPath(t *testing.T) {
	root := newAddTestProject(t)
	pagePath := filepath.Join(root, "app", "products", "[slug]", "page.tsx")
	if err := os.MkdirAll(filepath.Dir(pagePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pagePath, []byte("// existing React page\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := add(root, []string{"dynamic", "products/[slug]"}); err != nil {
		t.Fatal(err)
	}

	assertAddFileContains(t,
		pagePath,
		"// existing React page",
	)
	assertAddFileContains(t,
		filepath.Join(root, "app", "products", "[slug]", "page.schema.ts"),
		"definePage",
	)
	loaderPath := filepath.Join(root, "server", "pages", "products_slug", "page.go")
	assertAddFileContains(t, loaderPath,
		"contract \"example.com/add-test/server/internal/gobeyondgen/contracts/routes/r_products_slug_3e2e8eb9\"",
		"func Page(_ *gb.PageContext) (gbruntime.LoadedPage, error)",
		"generatedroutes.RouteProductsSlug",
		"Props:  contract.Props{}",
	)
	writeAddTestFile(t,
		filepath.Join(root, "server", "internal", "gobeyondgen", "contracts", "routes", "r_products_slug_3e2e8eb9", "types.gobeyond_gen.go"),
		"package r_products_slug_3e2e8eb9\n\ntype Props struct{}\n",
	)
	assertAddGoPackageCompiles(t, root, "./server/pages/products_slug")

	if err := add(root, []string{"dynamic", "products/[slug]"}); err != nil {
		t.Fatalf("unchanged dynamic scaffold should be idempotent: %v", err)
	}
}

func TestAddActionMergesOnlyMarkedScaffoldsAndCreatesTypedHandler(t *testing.T) {
	root := newAddTestProject(t)
	if err := add(root, []string{"page", "orders"}); err != nil {
		t.Fatal(err)
	}
	if err := add(root, []string{"action", "orders", "submitOrder"}); err != nil {
		t.Fatal(err)
	}
	if err := add(root, []string{"action", "orders", "cancelOrder"}); err != nil {
		t.Fatal(err)
	}

	actionsPath := filepath.Join(root, "app", "orders", "actions.ts")
	assertAddFileContains(t, actionsPath,
		"import { defineAction, schema } from '@gobeyond/schema'",
		actionInsertionMarker,
		"export const submitOrder = defineAction(",
		"export const cancelOrder = defineAction(",
	)
	before, err := os.ReadFile(actionsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := add(root, []string{"action", "orders", "submitOrder"}); err != nil {
		t.Fatalf("existing action scaffold should be idempotent: %v", err)
	}
	after, err := os.ReadFile(actionsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("adding an existing action changed actions.ts")
	}

	handlerPath := filepath.Join(root, "server", "actions", "orders", "submitOrder.go")
	assertAddFileContains(t, handlerPath,
		"contract \"example.com/add-test/server/internal/gobeyondgen/contracts/actions/r_orders_fc7d0552_submit_order\"",
		"func SubmitOrder(_ *gb.ActionContext, _ contract.Input) (contract.Output, error)",
		"contract.Register(SubmitOrder)",
	)
	writeAddTestFile(t,
		filepath.Join(root, "server", "internal", "gobeyondgen", "contracts", "actions", "r_orders_fc7d0552_submit_order", "types.gobeyond_gen.go"),
		"package r_orders_fc7d0552_submit_order\n\ntype Input struct{}\ntype Output struct{}\n",
	)
	writeAddTestFile(t,
		filepath.Join(root, "server", "internal", "gobeyondgen", "contracts", "actions", "r_orders_fc7d0552_cancel_order", "types.gobeyond_gen.go"),
		"package r_orders_fc7d0552_cancel_order\n\ntype Input struct{}\ntype Output struct{}\n",
	)
	assertAddGoPackageCompiles(t, root, "./server/actions/orders")

	manualPath := filepath.Join(root, "app", "orders", "actions.ts")
	manual := "import { defineAction, schema } from '@gobeyond/schema'\n\nexport const manual = defineAction({ input: schema.object({}), output: schema.object({}) })\n"
	if err := os.WriteFile(manualPath, []byte(manual), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := add(root, []string{"action", "orders", "archiveOrder"}); err == nil || !strings.Contains(err.Error(), "cannot safely update") {
		t.Fatalf("expected safe-merge refusal, got %v", err)
	}
	current, err := os.ReadFile(manualPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != manual {
		t.Fatal("refused action merge modified user-owned actions.ts")
	}
}

func TestAddAPICreatesGETHandler(t *testing.T) {
	root := newAddTestProject(t)
	if err := add(root, []string{"api", "status"}); err != nil {
		t.Fatal(err)
	}
	assertAddFileContains(t,
		filepath.Join(root, "server", "api", "status", "route.go"),
		"func GET(ctx *gb.RequestContext) (gb.Response, error)",
		"Status: http.StatusOK",
		"\"Content-Type\": {\"application/json\"}",
	)
}

func newAddTestProject(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, directory := range []string{"app"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repositoryRoot := filepath.Clean(filepath.Join(workingDirectory, "..", ".."))
	writeAddTestFile(t, filepath.Join(root, "go.mod"), "module example.com/add-test\n\ngo 1.24.0\n\nrequire github.com/gobeyond-dev/gobeyond v0.0.0\n\nreplace github.com/gobeyond-dev/gobeyond => "+repositoryRoot+"\n")
	return root
}

func assertAddGoPackageCompiles(t *testing.T, root, packagePath string) {
	t.Helper()
	command := exec.Command("go", "test", packagePath)
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("compile %s: %v\n%s", packagePath, err, output)
	}
}

func writeAddTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertAddFileContains(t *testing.T, path string, fragments ...string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	for _, fragment := range fragments {
		if !strings.Contains(string(data), fragment) {
			t.Errorf("%s does not contain %q:\n%s", path, fragment, data)
		}
	}
}
