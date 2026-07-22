package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gobeyond-dev/gobeyond/internal/project"
)

func TestGenerateClientEntryIncludesManifestPatterns(t *testing.T) {
	website := t.TempDir()
	path, err := generateClientEntry(
		website,
		[]compilerRouteModules{{
			RouteID:     "r_products",
			EntryFile:   "app/products/[slug]/page.tsx",
			LayoutFiles: []string{"app/layout.tsx"},
		}},
		[]project.Route{{ID: "r_products", Pattern: "/products/[slug]"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(website, ".gobeyond", "client-entry.tsx") {
		t.Fatalf("path = %q", path)
	}
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`createElement(Layout0_0 as ComponentType<any>, props,`,
		`"r_products": { component: Route0, pattern: "/products/[slug]" }`,
	} {
		if !strings.Contains(string(source), expected) {
			t.Fatalf("generated client entry is missing %q:\n%s", expected, source)
		}
	}
}

func TestGenerateClientEntryRejectsMissingManifestPattern(t *testing.T) {
	_, err := generateClientEntry(
		t.TempDir(),
		[]compilerRouteModules{{RouteID: "r_missing", EntryFile: "app/page.tsx"}},
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "missing its manifest pattern") {
		t.Fatalf("error = %v", err)
	}
}
