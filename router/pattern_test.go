package router

import "testing"

func TestTablePrecedence(t *testing.T) {
	table, err := NewTable([]Route{
		{ID: "catch", Pattern: "/products/[...rest]", Mode: ModeDynamic},
		{ID: "dynamic", Pattern: "/products/[slug]", Mode: ModeDynamic},
		{ID: "literal", Pattern: "/products/new", Mode: ModeStatic},
	})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		path string
		id   string
	}{
		{path: "/products/new", id: "literal"},
		{path: "/products/widget", id: "dynamic"},
		{path: "/products/a/b", id: "catch"},
	}
	for _, test := range tests {
		route, _, ok := table.Resolve(test.path)
		if !ok || route.ID != test.id {
			t.Fatalf("resolve %s: got %q, ok=%v", test.path, route.ID, ok)
		}
	}
}

func TestPatternParamsAndEscaping(t *testing.T) {
	pattern, err := Parse("/locations/[slug]")
	if err != nil {
		t.Fatal(err)
	}
	params, ok := pattern.Match("/locations/san%20francisco")
	if !ok || params["slug"] != "san francisco" {
		t.Fatalf("unexpected match: %#v, %v", params, ok)
	}
	if _, ok := pattern.Match("/locations/a%2Fb"); ok {
		t.Fatal("encoded slash must not match a segment")
	}
}

func TestOptionalCatchAll(t *testing.T) {
	pattern, err := Parse("/docs/[[...parts]]")
	if err != nil {
		t.Fatal(err)
	}
	params, ok := pattern.Match("/docs")
	if !ok || params["parts"] != "" {
		t.Fatalf("unexpected empty catch-all: %#v, %v", params, ok)
	}
	params, ok = pattern.Match("/docs/a/b")
	if !ok || params["parts"] != "a/b" {
		t.Fatalf("unexpected catch-all: %#v, %v", params, ok)
	}
}
