package document

import (
	"bytes"
	"strings"
	"testing"

	gb "github.com/holbrookab/gobeyond"
)

func TestRenderCompleteSEODocument(t *testing.T) {
	var output bytes.Buffer
	err := Render(&output, Input{
		PublicOrigin: "https://example.com",
		Indexable:    true,
		Metadata: gb.Metadata{
			Lang:        "en",
			Title:       "Widget & Parts",
			Description: "A useful widget",
			Canonical:   "https://example.com/products/widget",
			Robots:      "index, follow",
			OpenGraph: gb.OpenGraph{
				Type:        "product",
				Title:       "Widget",
				Description: "A useful widget",
				URL:         "https://example.com/products/widget",
				Images:      []string{"https://example.com/widget.jpg"},
			},
			Twitter:    gb.Twitter{Card: "summary_large_image", Title: "Widget", Description: "A useful widget", Images: []string{"https://example.com/widget.jpg"}},
			Alternates: []gb.Alternate{{Language: "fr", URL: "https://example.com/fr/products/widget"}},
			JSONLD: []gb.JSONLD{{
				"@context": "https://schema.org",
				"@type":    "Product",
				"name":     "</script><script>alert(1)</script>",
			}},
		},
		Body: BodyHTML("<main><h1>Widget</h1></main>"),
		Hydration: HydrationData{
			BuildID: "build-1",
			RouteID: "product",
			Props:   map[string]any{"name": "</script>"},
		},
		Scripts:        []Asset{{URL: "https://cdn.example.com/app.js"}},
		ModulePreloads: []Asset{{URL: "https://cdn.example.com/product.js"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	document := output.String()
	for _, expected := range []string{
		"<!doctype html>",
		"<title>Widget &amp; Parts</title>",
		"rel=\"canonical\"",
		"hreflang=\"fr\"",
		"application/ld+json",
		"<main><h1>Widget</h1></main>",
		"__GOBEYOND_DATA__",
		`rel="modulepreload" href="https://cdn.example.com/product.js"`,
	} {
		if !strings.Contains(document, expected) {
			t.Fatalf("document missing %q: %s", expected, document)
		}
	}
	if strings.Contains(document, "</script><script>alert") {
		t.Fatalf("script termination was not escaped: %s", document)
	}
}

func TestPrivateDocumentForcesNoIndex(t *testing.T) {
	var output bytes.Buffer
	err := Render(&output, Input{
		PublicOrigin: "https://example.com",
		Metadata:     gb.Metadata{Lang: "en", Title: "Account"},
		Hydration:    HydrationData{BuildID: "build-1", RouteID: "account", Props: map[string]any{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `name="robots" content="noindex, nofollow"`) {
		t.Fatalf("private document must be noindex: %s", output.String())
	}
}
