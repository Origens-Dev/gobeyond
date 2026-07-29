package project

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverAndGenerate(t *testing.T) {
	root := t.TempDir()
	writeTestModule(t, root)
	writeTestFile(t, filepath.Join(root, "app", "page.tsx"))
	writeTestFile(t, filepath.Join(root, "app", "(shop)", "products", "[slug]", "page.tsx"))
	writeSourceTestFile(t, filepath.Join(root, "server", "pages", "products_slug", "page.go"), "package products_slug\n\ntype Props struct{}\n")

	routes, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 2 {
		t.Fatalf("expected two routes, got %d", len(routes))
	}
	if routes[1].Pattern != "/products/[slug]" || routes[1].Mode != "dynamic" {
		t.Fatalf("unexpected dynamic route: %#v", routes[1])
	}
	if err := Generate(root, root, false); err != nil {
		t.Fatal(err)
	}
	if err := Generate(root, root, true); err != nil {
		t.Fatalf("fresh generation should pass check: %v", err)
	}
}

func TestGeneratedManifestIsPortableAcrossProjectDirectories(t *testing.T) {
	generated := make([][]byte, 0, 2)
	for _, parent := range []string{"first", "second"} {
		root := filepath.Join(t.TempDir(), parent)
		writeTestModule(t, root)
		writeTestFile(t, filepath.Join(root, "app", "page.tsx"))
		writeTestFile(t, filepath.Join(root, "app", "page.schema.ts"))
		writeSourceTestFile(t, filepath.Join(root, "server", "pages", "root", "page.go"), "package root\n\ntype Props struct{}\n")
		routes, err := Discover(root)
		if err != nil {
			t.Fatal(err)
		}
		if err := Write(root, routes, "b_reproducible", false); err != nil {
			t.Fatal(err)
		}
		manifest, err := os.ReadFile(filepath.Join(root, ".gobeyond", "routes.json"))
		if err != nil {
			t.Fatal(err)
		}
		generated = append(generated, manifest)
	}
	if !bytes.Equal(generated[0], generated[1]) {
		t.Fatalf("route manifests differ across directories:\n%s\n%s", generated[0], generated[1])
	}
}

func TestBuildIDChangesWithRuntimeAndWebsiteInputs(t *testing.T) {
	root := t.TempDir()
	page := filepath.Join(root, "app", "page.tsx")
	writeTestFile(t, page)
	routes, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	first, err := BuildID(root, routes)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(page, []byte("changed page"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := BuildID(root, routes)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("page content must contribute to the build ID")
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/site\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	third, err := BuildID(root, routes)
	if err != nil {
		t.Fatal(err)
	}
	if second == third {
		t.Fatal("dependency manifests must contribute to the build ID")
	}
}

func TestBuildIDIgnoresGeneratedAndSecretFiles(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "app", "page.tsx"))
	routes, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	first, err := BuildID(root, routes)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(root, GeneratedDir, "routes", "routes_gen.go"))
	if err := os.WriteFile(filepath.Join(root, ".env.local"), []byte("SECRET=rotated"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := BuildID(root, routes)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("generated outputs and local secrets must not perturb the build ID")
	}
}

func TestBuildSnapshotTracksInputsAndIgnoresGeneratedFiles(t *testing.T) {
	root := t.TempDir()
	page := filepath.Join(root, "app", "page.tsx")
	writeTestFile(t, page)
	writeTestFile(t, filepath.Join(root, "server", "pages", "root", "page.go"))
	writeTestFile(t, filepath.Join(root, GeneratedDir, "routes", "routes_gen.go"))

	first, err := BuildSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	if first["app/page.tsx"] == "" || first["server/pages/root/page.go"] == "" {
		t.Fatalf("snapshot is missing build inputs: %#v", first)
	}
	if _, exists := first[".generated/routes/routes_gen.go"]; exists {
		t.Fatal("generated files must not enter the build snapshot")
	}

	if err := os.WriteFile(page, []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := BuildSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	if first["app/page.tsx"] == second["app/page.tsx"] {
		t.Fatal("source edits must change their snapshot digest")
	}
}

func writeTestModule(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/site\n\ngo 1.24.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMiddlewareMakesStaticRoutesDynamic(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "app", "page.tsx"))
	writeTestFile(t, filepath.Join(root, "server", "middleware", "middleware.go"))
	routes, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 || routes[0].Mode != "dynamic" {
		t.Fatalf("middleware route classification = %#v", routes)
	}
}

func TestDiscoverRejectsPageRoutesUnderReservedAPIPath(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "app", "api", "products", "page.tsx"))
	if _, err := Discover(root); err == nil || !strings.Contains(err.Error(), "app/api") {
		t.Fatalf("expected reserved app/api page error, got %v", err)
	}
}

func writeTestFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
}
