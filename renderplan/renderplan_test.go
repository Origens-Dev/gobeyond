package renderplan

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const fixture = `{
  "apiVersion": "gobeyond.render/v1alpha1",
  "routeId": "products_slug",
  "root": {
    "kind": "element",
    "tag": "main",
    "attributes": [{"name":"className","value":{"kind":"literal","value":"product"}}],
    "children": [
      {"kind":"text","value":{"kind":"path","path":["product","name"]}},
      {"kind":"conditional","test":{"kind":"path","path":["available"]},"consequent":{"kind":"text","value":{"kind":"literal","value":"Available"}}},
      {"kind":"each","items":{"kind":"path","path":["tags"]},"item":"tag","index":"index","key":{"kind":"path","path":["tag"]},"body":{"kind":"element","tag":"span","children":[{"kind":"text","value":{"kind":"path","path":["tag"]}}]}}
    ]
  }
}`

func TestParseAndJSONRoundTrip(t *testing.T) {
	plan, err := Parse([]byte(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if plan.APIVersion != APIVersionV1Alpha1 || plan.RouteID != "products_slug" {
		t.Fatalf("unexpected plan: %#v", plan)
	}
	root, ok := plan.Root.(*Element)
	if !ok || len(root.Children) != 3 {
		t.Fatalf("unexpected root: %#v", plan.Root)
	}

	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	reparsed, err := Parse(encoded)
	if err != nil {
		t.Fatalf("round-trip failed: %v\n%s", err, encoded)
	}
	reencoded, err := json.Marshal(reparsed)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != string(reencoded) {
		t.Fatalf("round-trip is not deterministic:\n%s\n%s", encoded, reencoded)
	}
}

func TestParsePreservesJSONNumbers(t *testing.T) {
	plan, err := Parse([]byte(`{"apiVersion":"gobeyond.render/v1alpha1","routeId":"n","root":{"kind":"text","value":{"kind":"literal","value":9007199254740991}}}`))
	if err != nil {
		t.Fatal(err)
	}
	literal := plan.Root.(*Text).Value.(*Literal)
	if got := literal.Value.(json.Number).String(); got != "9007199254740991" {
		t.Fatalf("number changed: %s", got)
	}
}

func TestClientOnlyFallbackMayBeOmittedOrNull(t *testing.T) {
	for _, root := range []string{
		`{"kind":"clientOnly"}`,
		`{"kind":"clientOnly","fallback":null}`,
	} {
		plan, err := Parse([]byte(`{"apiVersion":"gobeyond.render/v1alpha1","routeId":"client","root":` + root + `}`))
		if err != nil {
			t.Fatalf("parse %s: %v", root, err)
		}
		client, ok := plan.Root.(*ClientOnly)
		if !ok || client.Fallback != nil {
			t.Fatalf("unexpected client-only node: %#v", plan.Root)
		}
	}
}

func TestIntrinsicRoundTripAndRegistryValidation(t *testing.T) {
	input := `{"apiVersion":"gobeyond.render/v1alpha1","routeId":"footer","root":{"kind":"text","value":{"kind":"intrinsic","name":"ecmascript.Date.prototype.getFullYear","arguments":[]}}}`
	plan, err := Parse([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	text := plan.Root.(*Text)
	intrinsic, ok := text.Value.(*Intrinsic)
	if !ok || intrinsic.Name != IntrinsicDateGetFullYear {
		t.Fatalf("unexpected intrinsic: %#v", text.Value)
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(encoded); err != nil {
		t.Fatalf("intrinsic round-trip failed: %v", err)
	}
}

func TestStrictDecodeRejectsUnknownProperties(t *testing.T) {
	cases := []string{
		`{"apiVersion":"gobeyond.render/v1alpha1","routeId":"x","extra":true,"root":{"kind":"fragment","children":[]}}`,
		`{"apiVersion":"gobeyond.render/v1alpha1","routeId":"x","root":{"kind":"element","tag":"p","surprise":1}}`,
		`{"apiVersion":"gobeyond.render/v1alpha1","routeId":"x","root":{"kind":"text","value":{"kind":"literal","value":"x","extra":1}}}`,
	}
	for _, input := range cases {
		_, err := Parse([]byte(input))
		var typed *Error
		if !errors.As(err, &typed) || typed.Code != CodeDecode {
			t.Errorf("expected typed decode error, got %v", err)
		}
	}
}

func TestMalformedAndUnsupportedJSON(t *testing.T) {
	cases := []struct{ name, input, contains string }{
		{"trailing", fixture + ` {}`, "one JSON value"},
		{"unknown node", `{"apiVersion":"gobeyond.render/v1alpha1","routeId":"x","root":{"kind":"portal"}}`, "unsupported node"},
		{"unknown expression", `{"apiVersion":"gobeyond.render/v1alpha1","routeId":"x","root":{"kind":"text","value":{"kind":"call"}}}`, "unsupported expression"},
		{"bad segment", `{"apiVersion":"gobeyond.render/v1alpha1","routeId":"x","root":{"kind":"text","value":{"kind":"path","path":[true]}}}`, "path segment"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.input))
			if err == nil || !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("expected %q, got %v", tc.contains, err)
			}
		})
	}
}

func TestValidation(t *testing.T) {
	valid := &Plan{APIVersion: APIVersionV1Alpha1, RouteID: "x", Root: &Fragment{Kind: "fragment", Children: []Node{}}}
	if err := Validate(valid); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		mutate func(*Plan)
		path   string
	}{
		{"version", func(p *Plan) { p.APIVersion = "v2" }, "$.apiVersion"},
		{"route", func(p *Plan) { p.RouteID = " " }, "$.routeId"},
		{"tag", func(p *Plan) { p.Root = &Element{Kind: "element", Tag: "script onclick"} }, "$.root.tag"},
		{"duplicate attribute", func(p *Plan) {
			p.Root = &Element{Kind: "element", Tag: "p", Attributes: []Attribute{{Name: "id", Value: lit("a")}, {Name: "ID", Value: lit("b")}}}
		}, "attributes[1].name"},
		{"negative path", func(p *Plan) {
			p.Root = &Text{Kind: "text", Value: &Path{Kind: "path", Path: []PathSegment{Index(-1)}}}
		}, "path[0]"},
		{"bad operator", func(p *Plan) {
			p.Root = &Text{Kind: "text", Value: &Binary{Kind: "binary", Operator: "**", Left: lit(1), Right: lit(2)}}
		}, "operator"},
		{"unknown intrinsic", func(p *Plan) {
			p.Root = &Text{Kind: "text", Value: &Intrinsic{Kind: "intrinsic", Name: "host.secret"}}
		}, "name"},
		{"same bindings", func(p *Plan) {
			p.Root = &Each{Kind: "each", Items: lit([]any{}), Item: "x", Index: "x", Key: lit(1), Body: &Fragment{Kind: "fragment"}}
		}, "index"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			copy := *valid
			tc.mutate(&copy)
			err := Validate(&copy)
			if err == nil || !strings.Contains(err.Error(), tc.path) {
				t.Fatalf("expected path %q, got %v", tc.path, err)
			}
		})
	}
}

func lit(value any) Expression { return &Literal{Kind: "literal", Value: value} }
