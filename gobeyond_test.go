package gobeyond

import (
	"testing"
	"time"
)

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

func TestMetadataValidationRequiresHTTPSForPrivateSocialImages(t *testing.T) {
	metadata := Metadata{
		Lang:  "en",
		Title: "Sign in",
		OpenGraph: OpenGraph{
			Image: &OpenGraphImage{URL: "http://example.com/social.png", Width: 1200, Height: 630},
		},
	}
	if err := metadata.Validate("https://example.com", false); err == nil {
		t.Fatal("expected an HTTP social image on a private page to fail")
	}

	metadata.OpenGraph.Image.URL = "https://example.com/social.png"
	if err := metadata.Validate("https://example.com", false); err != nil {
		t.Fatalf("expected absolute HTTPS social image to pass: %v", err)
	}

	metadata.OpenGraph.Image.URL = "/social.png"
	if err := metadata.Validate("https://example.com", false); err == nil {
		t.Fatal("expected a relative social image on a private page to fail")
	}
}

func TestMetadataValidationTreatsNoIndexAsNonIndexable(t *testing.T) {
	metadata := Metadata{
		Lang:      "en",
		Title:     "Preview",
		Canonical: "https://ahpstaffing.com/",
		Robots:    "NOINDEX, nofollow",
	}
	if !metadata.IsNoIndex() {
		t.Fatal("expected noindex metadata to be recognized")
	}
	if err := metadata.Validate("https://preview.origens.page", true); err != nil {
		t.Fatalf("noindex metadata should not require a preview canonical origin: %v", err)
	}

	metadata.Robots = "none"
	if !metadata.IsNoIndex() {
		t.Fatal("expected the none robots directive to be recognized")
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

func TestPublicRevalidate(t *testing.T) {
	policy := PublicRevalidate(time.Minute, 5*time.Minute, 24*time.Hour)
	if got, want := policy.HeaderValue(), "public, max-age=0, s-maxage=60, stale-while-revalidate=300, stale-if-error=86400"; got != want {
		t.Fatalf("HeaderValue() = %q, want %q", got, want)
	}
}

func TestMetadataURLsResolveWithoutMutatingSharedCode(t *testing.T) {
	original := Metadata{Canonical: "/", OpenGraph: OpenGraph{URL: "/", Image: &OpenGraphImage{URL: "/social.png"}, Images: []string{"/social.png", "https://external.example/image.png"}}, Twitter: Twitter{Images: []string{"/social.png"}}, Alternates: []Alternate{{Language: "fr", URL: "/fr"}}}
	a, b := original.ResolveURLs("https://preview.example"), original.ResolveURLs("https://prod.example")
	if a.Canonical != "https://preview.example/" || b.Canonical != "https://prod.example/" {
		t.Fatal("canonical did not follow deployment origin")
	}
	if original.OpenGraph.Image.URL != "/social.png" || original.OpenGraph.Images[0] != "/social.png" || original.Twitter.Images[0] != "/social.png" || original.Alternates[0].URL != "/fr" {
		t.Fatal("shared metadata was mutated")
	}
	if b.OpenGraph.Images[1] != "https://external.example/image.png" {
		t.Fatal("external image changed")
	}
	if got := original.ResolveURLs("http://localhost:8080"); got.OpenGraph.Image.URL != "https://localhost:8080/social.png" {
		t.Fatal("relative social image must use HTTPS")
	}
	for _, invalid := range []string{"//evil.example/x", "/\\evil.example/x"} {
		got := (Metadata{Canonical: invalid}).ResolveURLs("https://prod.example")
		if got.Canonical != invalid {
			t.Fatal("unsafe URL was resolved")
		}
	}
}
