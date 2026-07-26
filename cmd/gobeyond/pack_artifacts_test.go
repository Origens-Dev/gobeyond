package main

import (
	"encoding/json"
	"testing"

	"github.com/Origens-Dev/gobeyond/pack"
)

func TestPlanPayloadsByRoute(t *testing.T) {
	payloads, err := planPayloadsByRoute([]json.RawMessage{
		json.RawMessage(`{"routeId":"r_home","apiVersion":"gobeyond.render/v1alpha1"}`),
		json.RawMessage(`{"routeId":"r_products","apiVersion":"gobeyond.render/v1alpha1"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(payloads) != 2 || payloads["r_home"] == nil || payloads["r_products"] == nil {
		t.Fatalf("unexpected payload keys: %v", payloads)
	}
	if _, err := planPayloadsByRoute([]json.RawMessage{json.RawMessage(`{"apiVersion":"x"}`)}); err == nil {
		t.Fatal("expected an error for a plan without a route ID")
	}
}

func TestStaticPackEntries(t *testing.T) {
	artifact := compilerStaticBuild{
		APIVersion: "gobeyond.static-build/v1alpha1",
		Routes: []compilerStaticRoute{
			{
				RouteID: "r_home",
				Entries: []compilerStaticEntry{{Params: map[string]any{}, Props: json.RawMessage(`{"title":"Home"}`)}},
			},
			{
				RouteID: "r_docs",
				Entries: []compilerStaticEntry{
					{Params: map[string]any{"slug": []any{"guides", "start"}}, Props: json.RawMessage(`{}`)},
					{Params: map[string]any{"slug": []any{}}, Props: json.RawMessage(`{}`)},
				},
			},
		},
	}
	entries, err := staticPackEntries(artifact)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		pack.StaticEntryKey("r_home", nil),
		pack.StaticEntryKey("r_docs", map[string]string{"slug": "guides/start"}),
		pack.StaticEntryKey("r_docs", map[string]string{"slug": ""}),
	} {
		record, ok := entries[key]
		if !ok {
			t.Fatalf("missing static pack entry %q; have %v", key, keysOf(entries))
		}
		var decoded staticPackRecord
		if err := json.Unmarshal(record, &decoded); err != nil {
			t.Fatalf("entry %q is not valid JSON: %v", key, err)
		}
		if decoded.RouteID == "" {
			t.Fatalf("entry %q does not carry its route ID", key)
		}
	}

	duplicate := compilerStaticBuild{Routes: []compilerStaticRoute{{
		RouteID: "r_home",
		Entries: []compilerStaticEntry{
			{Params: map[string]any{}, Props: json.RawMessage(`{}`)},
			{Params: map[string]any{}, Props: json.RawMessage(`{}`)},
		},
	}}}
	if _, err := staticPackEntries(duplicate); err == nil {
		t.Fatal("expected duplicate static entries to fail")
	}

	invalid := compilerStaticBuild{Routes: []compilerStaticRoute{{
		RouteID: "r_home",
		Entries: []compilerStaticEntry{{Params: map[string]any{"n": 3.0}, Props: json.RawMessage(`{}`)}},
	}}}
	if _, err := staticPackEntries(invalid); err == nil {
		t.Fatal("expected non-string params to fail")
	}
}

func keysOf(entries map[string][]byte) []string {
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	return keys
}
