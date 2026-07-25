package cache

import "testing"

func TestRouteKeyDeterministic(t *testing.T) {
	a, err := RouteKey("acme", "build-1", "product", "/products/widget", "color=red", "https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	b, err := RouteKey("acme", "build-1", "product", "/products/widget", "color=red", "https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("RouteKey is not deterministic: %q != %q", a, b)
	}
}

func TestRouteKeyComponentsAffectUniqueness(t *testing.T) {
	base, err := RouteKey("acme", "build-1", "product", "/products/widget", "", "https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	variants := map[string]string{}
	set := func(name, deployPrefix, buildID, routeID, path, rawQuery, publicOrigin string) {
		key, err := RouteKey(deployPrefix, buildID, routeID, path, rawQuery, publicOrigin)
		if err != nil {
			t.Fatal(err)
		}
		variants[name] = key
	}
	set("deployPrefix", "other", "build-1", "product", "/products/widget", "", "https://example.com")
	set("buildID", "acme", "build-2", "product", "/products/widget", "", "https://example.com")
	set("routeID", "acme", "build-1", "other", "/products/widget", "", "https://example.com")
	set("path", "acme", "build-1", "product", "/products/other", "", "https://example.com")
	set("rawQuery", "acme", "build-1", "product", "/products/widget", "color=red", "https://example.com")
	set("publicOrigin", "acme", "build-1", "product", "/products/widget", "", "https://other.example.com")

	for name, variant := range variants {
		if variant == base {
			t.Fatalf("changing %s did not change the route key: %q", name, base)
		}
	}
}

func TestRouteKeyRequiresComponents(t *testing.T) {
	cases := []struct {
		name                                                         string
		deployPrefix, buildID, routeID, path, rawQuery, publicOrigin string
	}{
		{"missing deployPrefix", "", "build-1", "product", "/x", "", "https://example.com"},
		{"missing buildID", "acme", "", "product", "/x", "", "https://example.com"},
		{"missing routeID", "acme", "build-1", "", "/x", "", "https://example.com"},
		{"missing publicOrigin", "acme", "build-1", "product", "/x", "", ""},
		{"relative path", "acme", "build-1", "product", "x", "", "https://example.com"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := RouteKey(c.deployPrefix, c.buildID, c.routeID, c.path, c.rawQuery, c.publicOrigin); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestNormalizePathCollapsesDuplicateAndTrailingSlashes(t *testing.T) {
	tests := map[string]string{
		"/":                  "/",
		"/products":          "/products",
		"/products/":         "/products",
		"//products//widget": "/products/widget",
		"/products/widget/":  "/products/widget",
	}
	for input, want := range tests {
		got, err := NormalizePath(input)
		if err != nil {
			t.Fatalf("NormalizePath(%q) error: %v", input, err)
		}
		if got != want {
			t.Fatalf("NormalizePath(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestNormalizePathRejectsTraversalAndRelative(t *testing.T) {
	for _, input := range []string{"products", "/products/../secret", "/./products"} {
		if _, err := NormalizePath(input); err == nil {
			t.Fatalf("NormalizePath(%q) expected an error", input)
		}
	}
}

func TestDataKeyDeterministicRegardlessOfMapKeyOrder(t *testing.T) {
	first, err := DataKey("acme", "build-1", "catalog.product", []any{
		map[string]any{"b": 2, "a": 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := DataKey("acme", "build-1", "catalog.product", []any{
		map[string]any{"a": 1, "b": 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("DataKey not canonical across map literal order: %q != %q", first, second)
	}
}

func TestDataKeyDifferentArgsProduceDifferentKeys(t *testing.T) {
	a, err := DataKey("acme", "build-1", "catalog.product", []any{"widget"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := DataKey("acme", "build-1", "catalog.product", []any{"gadget"})
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("expected different args to produce different keys")
	}
}

func TestDataKeyEmptyArgsIsValid(t *testing.T) {
	if _, err := DataKey("acme", "build-1", "nav", nil); err != nil {
		t.Fatalf("nil args: %v", err)
	}
	if _, err := DataKey("acme", "build-1", "nav", []any{}); err != nil {
		t.Fatalf("empty args: %v", err)
	}
}

func TestDataKeyRejectsNonEncodableArgs(t *testing.T) {
	if _, err := DataKey("acme", "build-1", "catalog.product", []any{make(chan int)}); err == nil {
		t.Fatal("expected an error for a channel argument")
	}
}

func TestDataKeyRequiresComponents(t *testing.T) {
	if _, err := DataKey("", "build-1", "nav", nil); err == nil {
		t.Fatal("expected an error for a missing deployPrefix")
	}
	if _, err := DataKey("acme", "", "nav", nil); err == nil {
		t.Fatal("expected an error for a missing buildID")
	}
	if _, err := DataKey("acme", "build-1", "", nil); err == nil {
		t.Fatal("expected an error for a missing name")
	}
}
