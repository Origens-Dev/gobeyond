package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/holbrookab/gobeyond/internal/project"
)

func TestGenerateClientEntryIncludesManifestPatterns(t *testing.T) {
	website := t.TempDir()
	input, err := generateClientEntry(
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
	if input.EntryFile != filepath.Join(website, ".gobeyond", "client-entry.tsx") {
		t.Fatalf("path = %q", input.EntryFile)
	}
	source, err := os.ReadFile(input.EntryFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`import { bootstrapAsync } from '@gobeyond/react/browser'`,
		`"r_products": { load: () => import("./routes/`,
		`pattern: "/products/[slug]" }`,
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
		`createElement(Layout0 as ComponentType<any>, props, createElement(Page as ComponentType<any>, props))`,
		`export default Route`,
	} {
		if !strings.Contains(string(routeSource), expected) {
			t.Fatalf("generated route module is missing %q:\n%s", expected, routeSource)
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
