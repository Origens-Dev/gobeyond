package cache

import (
	"encoding/json"
	"testing"
)

func TestActionEnvelopeJSONShapeIsFrozen(t *testing.T) {
	envelope := ActionEnvelope{
		APIVersion: ActionAPIVersion,
		BuildID:    "build-1",
		Data:       map[string]any{"saved": true},
		Refresh: &ActionRefresh{
			Paths: []string{"/products/widget"},
			Tags:  []string{"products", "product:widget"},
		},
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"apiVersion":"gobeyond.action/v1alpha1","buildId":"build-1","data":{"saved":true},"refresh":{"paths":["/products/widget"],"tags":["products","product:widget"]}}`
	if string(encoded) != want {
		t.Fatalf("ActionEnvelope JSON shape changed:\n got  %s\n want %s", encoded, want)
	}
}

func TestActionEnvelopeOmitsRefreshWhenNil(t *testing.T) {
	envelope := ActionEnvelope{APIVersion: ActionAPIVersion, BuildID: "build-1", Data: map[string]any{"saved": true}}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"apiVersion":"gobeyond.action/v1alpha1","buildId":"build-1","data":{"saved":true}}`
	if string(encoded) != want {
		t.Fatalf("got %s, want %s", encoded, want)
	}
}

func TestActionRefreshFromScope(t *testing.T) {
	if got := ActionRefreshFromScope(nil); got != nil {
		t.Fatalf("nil scope: got %v", got)
	}
	empty := NewRequestScope(false)
	if got := ActionRefreshFromScope(empty); got != nil {
		t.Fatalf("scope with nothing recorded: got %v", got)
	}
	recorded := NewRequestScope(false)
	recorded.RecordRefreshPath("/products/widget")
	recorded.RecordRefreshTag("products")
	got := ActionRefreshFromScope(recorded)
	if got == nil || len(got.Paths) != 1 || got.Paths[0] != "/products/widget" || len(got.Tags) != 1 || got.Tags[0] != "products" {
		t.Fatalf("ActionRefreshFromScope = %+v", got)
	}
}
