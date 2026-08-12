package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Origens-Dev/gobeyond/internal/project"
)

func TestGenerateClientEntryIncludesManifestPatterns(t *testing.T) {
	website := t.TempDir()
	input, err := generateClientEntry(
		website,
		[]compilerRouteModules{{
			RouteID:     "r_products",
			EntryFile:   "app/products/[slug]/page.tsx",
			LayoutFiles: []string{"app/layout.tsx"},
			Prefetch:    "data",
			PrefetchImages: []compilerPrefetchImage{{
				Path: "hero.src",
				W:    1920,
				Q:    intPtr(82),
				F:    "auto",
			}},
		}},
		[]project.Route{{ID: "r_products", Pattern: "/products/[slug]"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if input.EntryFile != filepath.Join(website, ".gobeyond", "client-entry.tsx") {
		t.Fatalf("path = %q", input.EntryFile)
	}
	source, err := os.ReadFile(input.EntryFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`import { bootstrapAsync } from '@go-beyond/react/browser'`,
		`"r_products": { load: () => import("./routes/`,
		`pattern: "/products/[slug]", prefetch: "data", prefetchImages: [{"path":"hero.src","w":1920,"q":82,"f":"auto"}] }`,
	} {
		if !strings.Contains(string(source), expected) {
			t.Fatalf("generated client entry is missing %q:\n%s", expected, source)
		}
	}
	routeSource, err := os.ReadFile(input.RouteEntries["r_products"])
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`import Page from "../../app/products/[slug]/page.js"`,
		`import Layout0 from "../../app/layout.js"`,
		`export const page = Page as ComponentType<any>`,
		`export const layouts = [Layout0 as ComponentType<any>] as const`,
		`export default Page`,
	} {
		if !strings.Contains(string(routeSource), expected) {
			t.Fatalf("generated route module is missing %q:\n%s", expected, routeSource)
		}
	}
	for _, unexpected := range []string{
		`createElement(Layout0`,
		`export default Route`,
	} {
		if strings.Contains(string(routeSource), unexpected) {
			t.Fatalf("generated route module unexpectedly contains %q:\n%s", unexpected, routeSource)
		}
	}
}

func intPtr(value int) *int { return &value }

func TestGenerateClientEntryExposesNestedLayoutChain(t *testing.T) {
	website := t.TempDir()
	input, err := generateClientEntry(
		website,
		[]compilerRouteModules{{
			RouteID:   "r_products",
			EntryFile: "app/products/[slug]/page.tsx",
			LayoutFiles: []string{
				"app/layout.tsx",
				"app/products/layout.tsx",
			},
		}},
		[]project.Route{{ID: "r_products", Pattern: "/products/[slug]"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	routeSource, err := os.ReadFile(input.RouteEntries["r_products"])
	if err != nil {
		t.Fatal(err)
	}
	expected := `export const layouts = [Layout0 as ComponentType<any>, Layout1 as ComponentType<any>] as const`
	if !strings.Contains(string(routeSource), expected) {
		t.Fatalf("generated route module is missing nested layouts:\n%s", routeSource)
	}
	if !strings.Contains(string(routeSource), `import Layout1 from "../../app/products/layout.js"`) {
		t.Fatalf("generated route module is missing Layout1 import:\n%s", routeSource)
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
