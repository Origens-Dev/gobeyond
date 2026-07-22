package codegen

import (
	"bytes"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestProductAndActionGoldens(t *testing.T) {
	document := readFixtureDocument(t)
	files, err := Generate(document, Options{})
	if err != nil {
		t.Fatal(err)
	}

	assertGolden(t, files,
		"internal/gobeyondgen/contracts/routes/products_slug/types.gobeyond_gen.go",
		"products_slug.golden.go",
	)
	assertGolden(t, files,
		"internal/gobeyondgen/contracts/actions/add_to_cart/types.gobeyond_gen.go",
		"add_to_cart.golden.go",
	)
	for generatedPath, source := range files {
		if _, err := parser.ParseFile(token.NewFileSet(), generatedPath, source, parser.AllErrors); err != nil {
			t.Fatalf("parse generated %s: %v", generatedPath, err)
		}
	}
}

func TestGenerateIsDeterministic(t *testing.T) {
	document := readFixtureDocument(t)
	first, err := Generate(document, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for iteration := 0; iteration < 20; iteration++ {
		actual, err := Generate(document, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(actual, first) {
			t.Fatalf("generation changed at iteration %d", iteration)
		}
	}
}

func TestOptionalAndNullableRepresentations(t *testing.T) {
	document := mustParse(t, `{
      "apiVersion":"gobeyond.contract/v1alpha1",
      "routes":[{"routeId":"profile","props":{"kind":"object","shape":{
        "nickname":{"kind":"string","optional":true},
        "bio":{"kind":"string","nullable":true},
        "avatar":{"kind":"bytes","optional":true,"nullable":true},
        "visits":{"kind":"array","items":{"kind":"datetime"},"nullable":true}
      }}}],
      "actions":[]
    }`)
	files, err := Generate(document, Options{})
	if err != nil {
		t.Fatal(err)
	}
	source := string(files["internal/gobeyondgen/contracts/routes/profile/types.gobeyond_gen.go"])
	for _, pattern := range []string{
		`Avatar\s+\*\[\]byte\s+` + "`json:\"avatar,omitempty\"`" + ` // MVP: absent and null both decode as nil\.`,
		`Bio\s+\*string\s+` + "`json:\"bio\"`",
		`Nickname\s+\*string\s+` + "`json:\"nickname,omitempty\"`",
		`Visits\s+\*\[\]time\.Time\s+` + "`json:\"visits\"`",
	} {
		if !regexp.MustCompile(pattern).MatchString(source) {
			t.Errorf("generated source does not match %q:\n%s", pattern, source)
		}
	}
}

func TestNestedArraysObjectsAndEnums(t *testing.T) {
	document := mustParse(t, `{
      "apiVersion":"gobeyond.contract/v1alpha1",
      "routes":[{"routeId":"catalog","props":{"kind":"object","shape":{
        "groups":{"kind":"array","items":{"kind":"object","shape":{
          "name":{"kind":"string"},
          "items":{"kind":"array","items":{"kind":"object","shape":{
            "sku":{"kind":"string"},"state":{"kind":"enum","values":["in-stock","sold-out"]}
          }}}
        }}}
      }}}],"actions":[]
    }`)
	files, err := Generate(document, Options{})
	if err != nil {
		t.Fatal(err)
	}
	source := string(files["internal/gobeyondgen/contracts/routes/catalog/types.gobeyond_gen.go"])
	for _, expected := range []string{
		"[]PropsGroupsItem",
		"[]PropsGroupsItemItemsItem",
		"type PropsGroupsItemItemsItemState string",
		"PropsGroupsItemItemsItemStateInStock",
	} {
		if !strings.Contains(source, expected) {
			t.Errorf("missing %q:\n%s", expected, source)
		}
	}
}

func TestStrictDecodeRejectsMalformedDocuments(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		message string
	}{
		{"unknown root field", `{"apiVersion":"gobeyond.contract/v1alpha1","routes":[],"actions":[],"extra":true}`, "unknown field"},
		{"trailing JSON", `{"apiVersion":"gobeyond.contract/v1alpha1","routes":[],"actions":[]} {}`, "one JSON value"},
		{"missing routes", `{"apiVersion":"gobeyond.contract/v1alpha1","actions":[]}`, "routes is required"},
		{"missing actions", `{"apiVersion":"gobeyond.contract/v1alpha1","routes":[]}`, "actions is required"},
		{"wrong version", `{"apiVersion":"v2","routes":[],"actions":[]}`, "gobeyond.contract/v1alpha1"},
		{"missing array items", `{"apiVersion":"gobeyond.contract/v1alpha1","routes":[{"routeId":"x","props":{"kind":"array"}}],"actions":[]}`, "array items are required"},
		{"unexpected scalar property", `{"apiVersion":"gobeyond.contract/v1alpha1","routes":[{"routeId":"x","props":{"kind":"string","shape":{}}}],"actions":[]}`, "does not accept"},
		{"optional root", `{"apiVersion":"gobeyond.contract/v1alpha1","routes":[{"routeId":"x","props":{"kind":"string","optional":true}}],"actions":[]}`, "only valid for an object property"},
		{"nullable root", `{"apiVersion":"gobeyond.contract/v1alpha1","routes":[{"routeId":"x","props":{"kind":"string","nullable":true}}],"actions":[]}`, "root route props cannot be nullable"},
		{"null literal", `{"apiVersion":"gobeyond.contract/v1alpha1","routes":[{"routeId":"x","props":{"kind":"literal","value":null}}],"actions":[]}`, "no safe generated Go type"},
		{"mixed union", `{"apiVersion":"gobeyond.contract/v1alpha1","routes":[{"routeId":"x","props":{"kind":"union","variants":[{"kind":"literal","value":"a"},{"kind":"integer"}]}}],"actions":[]}`, "MVP unions support only"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse([]byte(test.input))
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("Parse error = %v, want containing %q", err, test.message)
			}
		})
	}
}

func TestGenerateRejectsNameCollisions(t *testing.T) {
	tests := []struct {
		name     string
		document Document
		message  string
	}{
		{
			name: "object fields",
			document: Document{APIVersion: APIVersionV1Alpha1, Routes: []Route{{RouteID: "x", Props: Value{
				Kind:  KindObject,
				Shape: map[string]Value{"foo-bar": {Kind: KindString}, "foo_bar": {Kind: KindString}},
			}}}, Actions: []Action{}},
			message: "same Go field",
		},
		{
			name: "nested types",
			document: Document{APIVersion: APIVersionV1Alpha1, Routes: []Route{{RouteID: "x", Props: Value{
				Kind: KindObject,
				Shape: map[string]Value{
					"a":   {Kind: KindObject, Shape: map[string]Value{"b": {Kind: KindObject, Shape: map[string]Value{}}}},
					"a-b": {Kind: KindObject, Shape: map[string]Value{}},
				},
			}}}, Actions: []Action{}},
			message: "generated type name",
		},
		{
			name: "enum constants",
			document: Document{APIVersion: APIVersionV1Alpha1, Routes: []Route{{RouteID: "x", Props: Value{
				Kind: KindEnum, Values: []string{"in-stock", "in_stock"},
			}}}, Actions: []Action{}},
			message: "same Go constant",
		},
		{
			name: "route packages",
			document: Document{APIVersion: APIVersionV1Alpha1, Routes: []Route{
				{RouteID: "foo-bar", Props: Value{Kind: KindString}},
				{RouteID: "foo_bar", Props: Value{Kind: KindString}},
			}, Actions: []Action{}},
			message: "same Go package",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Generate(test.document, Options{})
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("Generate error = %v, want containing %q", err, test.message)
			}
		})
	}
}

func TestGenerateOptions(t *testing.T) {
	document := Document{
		APIVersion: APIVersionV1Alpha1,
		Routes:     []Route{{RouteID: "type", Props: Value{Kind: KindSafeHTML}}},
		Actions:    []Action{},
	}
	files, err := Generate(document, Options{OutputDir: "generated", SafeHTMLImportPath: "example.test/safehtml"})
	if err != nil {
		t.Fatal(err)
	}
	source := string(files["generated/routes/contract_type/types.gobeyond_gen.go"])
	if !strings.Contains(source, `renderplan "example.test/safehtml"`) {
		t.Fatalf("custom import missing:\n%s", source)
	}
	if _, err := Generate(document, Options{OutputDir: "../generated"}); err == nil {
		t.Fatal("Generate accepted a parent-relative output directory")
	}
}

func readFixtureDocument(t *testing.T) Document {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "product.contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	return mustParseBytes(t, data)
}

func mustParse(t *testing.T, input string) Document {
	t.Helper()
	return mustParseBytes(t, []byte(input))
}

func mustParseBytes(t *testing.T, input []byte) Document {
	t.Helper()
	document, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func assertGolden(t *testing.T, files map[string][]byte, generatedPath, goldenName string) {
	t.Helper()
	actual, exists := files[generatedPath]
	if !exists {
		t.Fatalf("missing generated path %s", generatedPath)
	}
	expected, err := os.ReadFile(filepath.Join("testdata", goldenName))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, expected) {
		t.Fatalf("golden mismatch for %s\n--- actual ---\n%s\n--- expected ---\n%s", generatedPath, actual, expected)
	}
}
