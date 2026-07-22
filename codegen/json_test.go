package codegen

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeJSONEnforcesClosedRequiredSchema(t *testing.T) {
	schema := Value{Kind: KindObject, Shape: map[string]Value{
		"name": {Kind: KindString},
		"mode": {Kind: KindEnum, Values: []string{"create", "update"}},
		"details": {Kind: KindObject, Optional: true, Shape: map[string]Value{
			"count": {Kind: KindInteger},
		}},
	}}
	type details struct {
		Count int64 `json:"count"`
	}
	type input struct {
		Name    string   `json:"name"`
		Mode    string   `json:"mode"`
		Details *details `json:"details,omitempty"`
	}

	var decoded input
	if err := DecodeJSON(schema, []byte(`{"name":"portable","mode":"create","details":{"count":2}}`), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Name != "portable" || decoded.Details == nil || decoded.Details.Count != 2 {
		t.Fatalf("decoded = %#v", decoded)
	}

	for _, test := range []struct {
		name    string
		payload string
		want    string
	}{
		{name: "missing", payload: `{"mode":"create"}`, want: "name is required"},
		{name: "unknown root", payload: `{"name":"portable","mode":"create","extra":true}`, want: "unknown property"},
		{name: "unknown nested", payload: `{"name":"portable","mode":"create","details":{"count":2,"extra":true}}`, want: "unknown property"},
		{name: "enum", payload: `{"name":"portable","mode":"delete"}`, want: "allowed enum"},
		{name: "unsafe integer", payload: `{"name":"portable","mode":"create","details":{"count":9007199254740992}}`, want: "safe integer"},
		{name: "trailing", payload: `{"name":"portable","mode":"create"}{}`, want: "trailing JSON"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var target input
			err := DecodeJSON(schema, []byte(test.payload), &target)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("DecodeJSON error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestValidateEncodedValueRejectsInvalidTypedOutput(t *testing.T) {
	schema := Value{Kind: KindObject, Shape: map[string]Value{
		"saved":  {Kind: KindBoolean},
		"status": {Kind: KindEnum, Values: []string{"accepted", "rejected"}},
	}}
	type output struct {
		Saved  bool   `json:"saved"`
		Status string `json:"status"`
	}
	if err := ValidateEncodedValue(schema, output{Saved: true, Status: "accepted"}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateEncodedValue(schema, output{Saved: true, Status: "invented"}); err == nil {
		t.Fatal("invalid typed output passed its generated value contract")
	}
}

func TestDecodeJSONRejectsConcatenatedDocuments(t *testing.T) {
	schema := Value{Kind: KindObject, Shape: map[string]Value{}}
	var target struct{}
	err := DecodeJSON(schema, json.RawMessage(`{} {}`), &target)
	if err == nil || !strings.Contains(err.Error(), "trailing JSON") {
		t.Fatalf("DecodeJSON error = %v", err)
	}
}
