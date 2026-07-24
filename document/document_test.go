package document

import (
	"bytes"
	"strings"
	"testing"

	gb "github.com/Origens-Dev/gobeyond"
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
				SiteName:    "Example Store",
				Locale:      "en_US",
				Image: &gb.OpenGraphImage{
					URL:    "https://example.com/widget.jpg",
					Width:  1200,
					Height: 630,
					Alt:    "Widget on a workbench",
					Type:   "image/jpeg",
				},
			},
			Twitter:    gb.Twitter{Card: "summary_large_image", Title: "Widget", Description: "A useful widget", Site: "@example", ImageAlt: "Widget on a workbench", Images: []string{"https://example.com/widget.jpg"}},
			Icons:      gb.Icons{Icon: "/favicon-32x32.png", AppleTouch: "/apple-touch-icon.png"},
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
		`rel="icon" href="/favicon-32x32.png"`,
		`rel="apple-touch-icon" href="/apple-touch-icon.png"`,
		`property="og:site_name" content="Example Store"`,
		`property="og:locale" content="en_US"`,
		`property="og:image" content="https://example.com/widget.jpg"`,
		`property="og:image:width" content="1200"`,
		`property="og:image:height" content="630"`,
		`property="og:image:alt" content="Widget on a workbench"`,
		`property="og:image:type" content="image/jpeg"`,
		`name="twitter:site" content="@example"`,
		`name="twitter:image:alt" content="Widget on a workbench"`,
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
