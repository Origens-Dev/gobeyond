package gobeyond

import "testing"

func TestMetadataValidation(t *testing.T) {
	valid := Metadata{
		Lang:        "en",
		Title:       "Product",
		Description: "Product description",
		Canonical:   "https://example.com/products/widget",
		Robots:      "index, follow",
		OpenGraph:   OpenGraph{Type: "product", Title: "Product", Description: "Product description", URL: "https://example.com/products/widget", Images: []string{"https://cdn.example.com/product.jpg"}},
		Twitter:     Twitter{Card: "summary_large_image", Title: "Product", Description: "Product description", Images: []string{"https://cdn.example.com/product.jpg"}},
		Alternates: []Alternate{
			{Language: "fr", URL: "https://example.com/fr/products/widget"},
		},
	}
	if err := valid.Validate("https://example.com", true); err != nil {
		t.Fatalf("expected valid metadata: %v", err)
	}

	invalid := valid
	invalid.Canonical = "https://attacker.example/products/widget"
	if err := invalid.Validate("https://example.com", true); err == nil {
		t.Fatal("expected foreign canonical origin to fail")
	}
}

func TestCachePolicy(t *testing.T) {
	if got := (CachePolicy{}).HeaderValue(); got != "private, no-store" {
		t.Fatalf("unexpected private policy: %s", got)
	}
	got := (CachePolicy{Mode: CachePublic, MaxAge: 60, StaleWhileRevalidate: 30}).HeaderValue()
	if got != "public, max-age=60, stale-while-revalidate=30" {
		t.Fatalf("unexpected public policy: %s", got)
	}
}
