package renderer

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Origens-Dev/gobeyond/renderplan"
)

func TestRenderEscapingAttributesAndScriptTermination(t *testing.T) {
	plan := plan(&renderplan.Element{Kind: "element", Tag: "main", Attributes: []renderplan.Attribute{
		{Name: "className", Value: lit(`hero "<&`)},
		{Name: "hidden", Mode: renderplan.AttributeBoolean, Value: path("hidden")},
		{Name: "data-ready", Value: path("ready")},
	}, Children: []renderplan.Node{
		&renderplan.Text{Kind: "text", Value: lit(`<script>alert("x")</script> & goodbye`)},
		&renderplan.Element{Kind: "element", Tag: "script", Children: []renderplan.Node{&renderplan.Text{Kind: "text", Value: lit(`</script><img src=x onerror=alert(1)>`)}}},
	}})
	html, err := Render(plan, map[string]any{"hidden": true, "ready": false})
	if err != nil {
		t.Fatal(err)
	}
	want := `<main class="hero &#34;&lt;&amp;" hidden="" data-ready="false">&lt;script&gt;alert("x")&lt;/script&gt; &amp; goodbye<script>&lt;/script&gt;&lt;img src=x onerror=alert(1)&gt;</script></main>`
	if html != want {
		t.Fatalf("\nwant %s\n got %s", want, html)
	}
}

func TestRenderConditionsListsAndExpressions(t *testing.T) {
	itemPath := func(name string) *renderplan.Path {
		return &renderplan.Path{Kind: "path", Path: []renderplan.PathSegment{renderplan.Property("item"), renderplan.Property(name)}}
	}
	root := &renderplan.Element{Kind: "element", Tag: "ul", Children: []renderplan.Node{
		&renderplan.Each{Kind: "each", Items: path("items"), Item: "item", Index: "i", Key: itemPath("id"), Body: &renderplan.Conditional{Kind: "conditional", Test: itemPath("visible"), Consequent: &renderplan.Element{Kind: "element", Tag: "li", Attributes: []renderplan.Attribute{{Name: "data-index", Value: &renderplan.Path{Kind: "path", Path: []renderplan.PathSegment{renderplan.Property("i")}}}}, Children: []renderplan.Node{&renderplan.Text{Kind: "text", Value: &renderplan.Helper{Kind: "helper", Name: "upper", Arguments: []renderplan.Expression{itemPath("name")}}}}}, Alternate: &renderplan.Fragment{Kind: "fragment", Children: []renderplan.Node{}}}},
	}}
	props := map[string]any{"items": []map[string]any{{"id": 1, "name": "alpha", "visible": true}, {"id": 2, "name": "beta", "visible": false}, {"id": 3, "name": "gamma", "visible": true}}}
	got, err := Render(plan(root), props)
	if err != nil {
		t.Fatal(err)
	}
	want := `<ul><li data-index="0">ALPHA</li><li data-index="2">GAMMA</li></ul>`
	if got != want {
		t.Fatalf("want %s, got %s", want, got)
	}
}

func TestNamedStringContractsAndOptionalPointers(t *testing.T) {
	type Availability string
	label := "Previous"
	root := &renderplan.Fragment{Kind: "fragment", Children: []renderplan.Node{
		&renderplan.Conditional{Kind: "conditional", Test: &renderplan.Binary{Kind: "binary", Operator: "==", Left: path("availability"), Right: lit("InStock")}, Consequent: &renderplan.Text{Kind: "text", Value: lit("available")}},
		&renderplan.Element{Kind: "element", Tag: "a", Attributes: []renderplan.Attribute{{Name: "aria-label", Value: path("label")}}},
	}}
	got, err := Render(plan(root), map[string]any{"availability": Availability("InStock"), "label": &label})
	if err != nil {
		t.Fatal(err)
	}
	if want := `available<a aria-label="Previous"></a>`; got != want {
		t.Fatalf("want %s, got %s", want, got)
	}
}

func TestDuplicateEachKeysFail(t *testing.T) {
	n := &renderplan.Each{Kind: "each", Items: lit([]string{"a", "b"}), Item: "item", Key: lit("same"), Body: &renderplan.Text{Kind: "text", Value: path("item")}}
	_, err := Render(plan(n), nil)
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != CodeEvaluation || !strings.Contains(err.Error(), "duplicate key") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFormsAndStyles(t *testing.T) {
	root := &renderplan.Element{Kind: "element", Tag: "form", Attributes: []renderplan.Attribute{{Name: "style", Mode: renderplan.AttributeStyle, Value: lit(map[string]any{"zIndex": 2, "marginTop": 8, "opacity": 0.5, "WebkitLineClamp": 2, "--accent": "red"})}}, Children: []renderplan.Node{
		&renderplan.Element{Kind: "element", Tag: "label", Attributes: []renderplan.Attribute{{Name: "htmlFor", Value: lit("name")}}, Children: []renderplan.Node{&renderplan.Text{Kind: "text", Value: lit("Name")}}},
		&renderplan.Element{Kind: "element", Tag: "input", Attributes: []renderplan.Attribute{{Name: "id", Value: lit("name")}, {Name: "value", Value: lit("Ada")}, {Name: "checked", Value: lit(true)}}},
		&renderplan.Element{Kind: "element", Tag: "textarea", Attributes: []renderplan.Attribute{{Name: "value", Value: lit("<hello>&")}}},
		&renderplan.Element{Kind: "element", Tag: "select", Attributes: []renderplan.Attribute{{Name: "value", Value: lit("b")}}, Children: []renderplan.Node{
			&renderplan.Element{Kind: "element", Tag: "option", Attributes: []renderplan.Attribute{{Name: "value", Value: lit("a")}}, Children: []renderplan.Node{&renderplan.Text{Kind: "text", Value: lit("A")}}},
			&renderplan.Element{Kind: "element", Tag: "option", Attributes: []renderplan.Attribute{{Name: "value", Value: lit("b")}}, Children: []renderplan.Node{&renderplan.Text{Kind: "text", Value: lit("B")}}},
		}},
	}}
	got, err := Render(plan(root), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := `<form style="--accent:red;-webkit-line-clamp:2;margin-top:8px;opacity:0.5;z-index:2;"><label for="name">Name</label><input id="name" value="Ada" checked=""><textarea>&lt;hello&gt;&amp;</textarea><select><option value="a">A</option><option value="b" selected="">B</option></select></form>`
	if got != want {
		t.Fatalf("\nwant %s\n got %s", want, got)
	}
}

func TestOrderedStyleHelperMatchesSourceOrder(t *testing.T) {
	style := &renderplan.Helper{Kind: "helper", Name: "style", Arguments: []renderplan.Expression{
		lit("color"), lit("red"), lit("backgroundColor"), lit("blue"), lit("marginTop"), lit(8), lit("aspectRatio"), lit(2),
	}}
	root := &renderplan.Element{Kind: "element", Tag: "div", Attributes: []renderplan.Attribute{{Name: "style", Mode: renderplan.AttributeStyle, Value: style}}}
	got, err := Render(plan(root), nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := `<div style="color:red;background-color:blue;margin-top:8px;aspect-ratio:2;"></div>`; got != want {
		t.Fatalf("want %s, got %s", want, got)
	}
}

func TestDateIntrinsicsUseOneRenderSnapshot(t *testing.T) {
	snapshot := time.Date(2026, 12, 31, 23, 59, 59, 0, time.FixedZone("local", -8*60*60))
	root := &renderplan.Element{Kind: "element", Tag: "footer", Children: []renderplan.Node{
		&renderplan.Text{Kind: "text", Value: &renderplan.Intrinsic{Kind: "intrinsic", Name: renderplan.IntrinsicDateGetFullYear}},
		&renderplan.Text{Kind: "text", Value: lit("/")},
		&renderplan.Text{Kind: "text", Value: &renderplan.Intrinsic{Kind: "intrinsic", Name: renderplan.IntrinsicDateGetUTCFullYear}},
	}}
	r := New()
	clockReads := 0
	r.now = func() time.Time {
		clockReads++
		return snapshot
	}
	got, err := r.Render(plan(root), nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := strconv.Itoa(snapshot.Year()) + `<!-- -->/<!-- -->` + strconv.Itoa(snapshot.UTC().Year()); got != `<footer>`+want+`</footer>` {
		t.Fatalf("want current year %s, got %s", want, got)
	}
	if clockReads != 1 {
		t.Fatalf("request snapshot clock read %d times, want 1", clockReads)
	}
}

func TestDateTimeUsesHydrationJSONRepresentation(t *testing.T) {
	value := time.Date(2026, 7, 22, 12, 34, 56, 123000000, time.FixedZone("example", -7*60*60))
	root := &renderplan.Element{Kind: "element", Tag: "time", Attributes: []renderplan.Attribute{{Name: "dateTime", Value: path("publishedAt")}}}
	got, err := Render(plan(root), map[string]any{"publishedAt": value})
	if err != nil {
		t.Fatal(err)
	}
	if want := `<time dateTime="2026-07-22T12:34:56.123-07:00"></time>`; got != want {
		t.Fatalf("want %s, got %s", want, got)
	}
}

func TestSVGNamespaceAndTableNormalization(t *testing.T) {
	root := &renderplan.Fragment{Kind: "fragment", Children: []renderplan.Node{
		&renderplan.Element{Kind: "element", Tag: "svg", Attributes: []renderplan.Attribute{{Name: "viewBox", Value: lit("0 0 10 10")}, {Name: "strokeWidth", Value: lit(2)}}, Children: []renderplan.Node{&renderplan.Element{Kind: "element", Tag: "use", Attributes: []renderplan.Attribute{{Name: "xlinkHref", Value: lit("#mark")}}}}},
		&renderplan.Element{Kind: "element", Tag: "table", Children: []renderplan.Node{
			&renderplan.Element{Kind: "element", Tag: "tr", Children: []renderplan.Node{&renderplan.Element{Kind: "element", Tag: "td", Children: []renderplan.Node{&renderplan.Text{Kind: "text", Value: lit("one")}}}}},
			&renderplan.Element{Kind: "element", Tag: "tr", Children: []renderplan.Node{&renderplan.Element{Kind: "element", Tag: "td", Children: []renderplan.Node{&renderplan.Text{Kind: "text", Value: lit("two")}}}}},
		}},
	}}
	got, err := Render(plan(root), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := `<svg viewBox="0 0 10 10" stroke-width="2"><use xlink:href="#mark"></use></svg><table><tbody><tr><td>one</td></tr><tr><td>two</td></tr></tbody></table>`
	if got != want {
		t.Fatalf("want %s, got %s", want, got)
	}
}

func TestJavaScriptNumberSerialization(t *testing.T) {
	root := &renderplan.Element{Kind: "element", Tag: "p", Children: []renderplan.Node{
		&renderplan.Text{Kind: "text", Value: path("small")},
		&renderplan.Text{Kind: "text", Value: lit("|")},
		&renderplan.Text{Kind: "text", Value: path("large")},
		&renderplan.Text{Kind: "text", Value: lit("|")},
		&renderplan.Text{Kind: "text", Value: path("decimal")},
	}}
	got, err := Render(plan(root), map[string]any{
		"small": json.Number("1e-07"), "large": float64(1e21), "decimal": float64(0.000001),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `<p>1e-7<!-- -->|<!-- -->1e+21<!-- -->|<!-- -->0.000001</p>`
	if got != want {
		t.Fatalf("want %s, got %s", want, got)
	}
}

func TestPreLeadingNewlineMatchesReactParserCompensation(t *testing.T) {
	root := &renderplan.Element{Kind: "element", Tag: "pre", Children: []renderplan.Node{
		&renderplan.Text{Kind: "text", Value: lit("\ncode")},
	}}
	got, err := Render(plan(root), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "<pre>\n\ncode</pre>" {
		t.Fatalf("unexpected pre output %q", got)
	}
}

func TestClientOnlyAndRawHTMLTrustBoundary(t *testing.T) {
	fallback := &renderplan.ClientOnly{Kind: "clientOnly", Fallback: &renderplan.Element{Kind: "element", Tag: "p", Children: []renderplan.Node{&renderplan.Text{Kind: "text", Value: lit("Map loading")}}}}
	trusted := &renderplan.RawHTML{Kind: "rawHtml", Value: path("body")}
	root := &renderplan.Fragment{Kind: "fragment", Children: []renderplan.Node{fallback, trusted}}
	got, err := Render(plan(root), map[string]any{"body": renderplan.TrustedHTML(`<strong>Sanitized</strong>`)})
	if err != nil {
		t.Fatal(err)
	}
	if got != `<p>Map loading</p><strong>Sanitized</strong>` {
		t.Fatal(got)
	}
	_, err = Render(plan(root), map[string]any{"body": `<script>unsafe</script>`})
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != CodeUnsafeHTML {
		t.Fatalf("expected unsafe HTML error, got %v", err)
	}
	literal, err := Render(plan(&renderplan.RawHTML{Kind: "rawHtml", Value: lit(`<em>compiler trusted</em>`)}), nil)
	if err != nil || literal != `<em>compiler trusted</em>` {
		t.Fatalf("literal raw HTML: %q, %v", literal, err)
	}
	encoded, err := json.Marshal(TrustedHTML(`<b>safe</b>`))
	if err != nil || string(encoded) != `"\u003cb\u003esafe\u003c/b\u003e"` {
		t.Fatalf("SafeHTML JSON: %s, %v", encoded, err)
	}
}

func TestClientOnlyWithoutFallbackRendersEmptyMarkup(t *testing.T) {
	got, err := Render(plan(&renderplan.ClientOnly{Kind: "clientOnly"}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("expected empty client-only markup, got %q", got)
	}
}

func TestURLSafetyVoidElementsAndUnsupportedAttributes(t *testing.T) {
	cases := []struct {
		name string
		root renderplan.Node
		code ErrorCode
	}{
		{"javascript URL", &renderplan.Element{Kind: "element", Tag: "a", Attributes: []renderplan.Attribute{{Name: "href", Mode: renderplan.AttributeURL, Value: lit("javascript:alert(1)")}}}, CodeUnsafeURL},
		{"void children", &renderplan.Element{Kind: "element", Tag: "img", Children: []renderplan.Node{&renderplan.Text{Kind: "text", Value: lit("no")}}}, CodeRender},
		{"event attribute", &renderplan.Element{Kind: "element", Tag: "button", Attributes: []renderplan.Attribute{{Name: "onClick", Value: lit("bad")}}}, CodeRender},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Render(plan(tc.root), nil)
			var typed *Error
			if !errors.As(err, &typed) || typed.Code != tc.code {
				t.Fatalf("expected %s, got %v", tc.code, err)
			}
		})
	}
}

func TestStructPathsAndAdjacentTextMarkers(t *testing.T) {
	type Product struct {
		Name   string `json:"name"`
		Hidden string `json:"-"`
	}
	root := &renderplan.Element{Kind: "element", Tag: "h1", Children: []renderplan.Node{&renderplan.Text{Kind: "text", Value: lit("Hello ")}, &renderplan.Text{Kind: "text", Value: &renderplan.Path{Kind: "path", Path: []renderplan.PathSegment{renderplan.Property("product"), renderplan.Property("name")}}}}}
	got, err := Render(plan(root), struct {
		Product Product `json:"product"`
	}{Product: Product{Name: "Ada"}})
	if err != nil {
		t.Fatal(err)
	}
	if got != `<h1>Hello <!-- -->Ada</h1>` {
		t.Fatal(got)
	}
}

func TestReactBooleanChildrenAndTextareaLeadingNewline(t *testing.T) {
	root := &renderplan.Fragment{Kind: "fragment", Children: []renderplan.Node{
		&renderplan.Text{Kind: "text", Value: lit(true)},
		&renderplan.Element{Kind: "element", Tag: "div", Attributes: []renderplan.Attribute{{Name: "title", Value: lit(false)}, {Name: "aria-hidden", Value: lit(false)}}},
		&renderplan.Element{Kind: "element", Tag: "textarea", Attributes: []renderplan.Attribute{{Name: "value", Value: lit("\nhello")}}},
	}}
	got, err := Render(plan(root), nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := `<div aria-hidden="false"></div><textarea>` + "\n\n" + `hello</textarea>`; got != want {
		t.Fatalf("want %q, got %q", want, got)
	}
}

func TestJSONRoundTripFixtureRenders(t *testing.T) {
	input := `{"apiVersion":"gobeyond.render/v1alpha1","routeId":"article_slug","root":{"kind":"element","tag":"article","attributes":[{"name":"data-id","value":{"kind":"path","path":["id"]}}],"children":[{"kind":"element","tag":"h1","children":[{"kind":"text","value":{"kind":"path","path":["title"]}}]},{"kind":"conditional","test":{"kind":"binary","operator":">","left":{"kind":"path","path":["views"]},"right":{"kind":"literal","value":10}},"consequent":{"kind":"text","value":{"kind":"literal","value":"Popular"}}}]}}`
	p, err := renderplan.Parse([]byte(input))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip, err := renderplan.Parse(encoded)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Render(roundTrip, map[string]any{"id": "a&b", "title": "Go < React", "views": 11})
	if err != nil {
		t.Fatal(err)
	}
	want := `<article data-id="a&amp;b"><h1>Go &lt; React</h1>Popular</article>`
	if got != want {
		t.Fatalf("want %s, got %s", want, got)
	}
}

func plan(root renderplan.Node) *renderplan.Plan {
	return &renderplan.Plan{APIVersion: renderplan.APIVersionV1Alpha1, RouteID: "test", Root: root}
}
func lit(value any) renderplan.Expression { return &renderplan.Literal{Kind: "literal", Value: value} }

func TestTernaryExpressionSelectsBranch(t *testing.T) {
	t.Parallel()
	expr := &renderplan.Ternary{
		Kind:       "ternary",
		Test:       lit(nil),
		Consequent: lit("yes"),
		Alternate:  lit("navy"),
	}
	value, err := evaluate(expr, environment{}, "ternary")
	if err != nil {
		t.Fatal(err)
	}
	if value != "navy" {
		t.Fatalf("ternary = %v, want navy", value)
	}

	expr.Test = lit("prefix")
	value, err = evaluate(expr, environment{}, "ternary")
	if err != nil {
		t.Fatal(err)
	}
	if value != "yes" {
		t.Fatalf("ternary = %v, want yes", value)
	}
}

func TestIndexExprReturnsNullForMissingAndOutOfRange(t *testing.T) {
	t.Parallel()
	env := environment{
		props: map[string]any{
			"items":  []any{"a", "b"},
			"sparse": []any{"only-first"},
			"obj":    map[string]any{"present": "yes"},
		},
		locals: map[string]any{},
	}
	cases := []struct {
		name  string
		expr  *renderplan.IndexExpr
		want  any
		isNil bool
	}{
		{
			name: "in range",
			expr: &renderplan.IndexExpr{
				Kind:   "index",
				Object: path("items"),
				Index:  lit(1),
			},
			want: "b",
		},
		{
			name: "sparse out of range",
			expr: &renderplan.IndexExpr{
				Kind:   "index",
				Object: path("sparse"),
				Index:  lit(3),
			},
			isNil: true,
		},
		{
			name: "negative index",
			expr: &renderplan.IndexExpr{
				Kind:   "index",
				Object: path("items"),
				Index:  lit(-1),
			},
			isNil: true,
		},
		{
			name: "missing key",
			expr: &renderplan.IndexExpr{
				Kind:   "index",
				Object: path("obj"),
				Index:  lit("absent"),
			},
			isNil: true,
		},
		{
			name: "present key",
			expr: &renderplan.IndexExpr{
				Kind:   "index",
				Object: path("obj"),
				Index:  lit("present"),
			},
			want: "yes",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value, err := evaluate(tc.expr, env, "index")
			if err != nil {
				t.Fatal(err)
			}
			if tc.isNil {
				if value != nil {
					t.Fatalf("got %#v, want null", value)
				}
				return
			}
			if value != tc.want {
				t.Fatalf("got %#v, want %#v", value, tc.want)
			}
		})
	}
}

func TestArrayAndStringLengthMatchJavaScript(t *testing.T) {
	t.Parallel()
	env := environment{
		props: map[string]any{
			"items": []any{"a", "b"},
			"label": "A😀",
		},
		locals: map[string]any{},
	}
	for _, tc := range []struct {
		name string
		expr renderplan.Expression
		want int
	}{
		{name: "array path", expr: path("items", "length"), want: 2},
		{name: "array dynamic property", expr: &renderplan.IndexExpr{
			Kind: "index", Object: path("items"), Index: lit("length"),
		}, want: 2},
		{name: "string path uses UTF-16 units", expr: path("label", "length"), want: 3},
		{name: "string dynamic property uses UTF-16 units", expr: &renderplan.IndexExpr{
			Kind: "index", Object: path("label"), Index: lit("length"),
		}, want: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value, err := evaluate(tc.expr, env, "length")
			if err != nil {
				t.Fatal(err)
			}
			if value != tc.want {
				t.Fatalf("got %#v, want %d", value, tc.want)
			}
		})
	}
}

func TestIndexExprRendersNullAsEmptyText(t *testing.T) {
	root := &renderplan.Element{Kind: "element", Tag: "p", Children: []renderplan.Node{
		&renderplan.Text{Kind: "text", Value: &renderplan.IndexExpr{
			Kind:   "index",
			Object: path("items"),
			Index:  lit(5),
		}},
	}}
	got, err := Render(plan(root), map[string]any{"items": []any{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := `<p></p>`; got != want {
		t.Fatalf("want %s, got %s", want, got)
	}
}

func TestEachWhenSkipsItemsAndKeyUniqueness(t *testing.T) {
	itemPath := func(name string) *renderplan.Path {
		return &renderplan.Path{Kind: "path", Path: []renderplan.PathSegment{renderplan.Property("item"), renderplan.Property(name)}}
	}
	root := &renderplan.Element{Kind: "element", Tag: "ul", Children: []renderplan.Node{
		&renderplan.Each{
			Kind:  "each",
			Items: path("items"),
			Item:  "item",
			Index: "i",
			When:  itemPath("visible"),
			Key:   lit("shared"),
			Body: &renderplan.Element{Kind: "element", Tag: "li", Children: []renderplan.Node{
				&renderplan.Text{Kind: "text", Value: itemPath("name")},
			}},
		},
	}}
	props := map[string]any{"items": []map[string]any{
		{"name": "alpha", "visible": true},
		{"name": "beta", "visible": false},
		{"name": "gamma", "visible": false},
	}}
	got, err := Render(plan(root), props)
	if err != nil {
		t.Fatal(err)
	}
	want := `<ul><li>alpha</li></ul>`
	if got != want {
		t.Fatalf("want %s, got %s", want, got)
	}
}

func path(parts ...string) renderplan.Expression {
	segments := make([]renderplan.PathSegment, len(parts))
	for i, part := range parts {
		segments[i] = renderplan.Property(part)
	}
	return &renderplan.Path{Kind: "path", Path: segments}
}
