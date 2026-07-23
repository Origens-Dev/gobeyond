package seosite

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gb "github.com/holbrookab/gobeyond"
	actioncontract "github.com/holbrookab/gobeyond/examples/seo-site/internal/gobeyondgen/contracts/actions/r_products_slug_3e2e8eb9_add_to_cart"
	"github.com/holbrookab/gobeyond/renderplan"
	gbruntime "github.com/holbrookab/gobeyond/runtime"
)

func TestLiveNoJavaScriptSEOArticle(t *testing.T) {
	server, err := New("test-build", "https://example.com", testPlans())
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "https://example.com/articles/portable-react", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	for _, expected := range []string{
		"<h1>Portable React rendered by Go</h1>",
		"React owns the website",
		"rel=\"canonical\"",
		"application/ld+json",
		"__GOBEYOND_DATA__",
	} {
		if !strings.Contains(recorder.Body.String(), expected) {
			t.Fatalf("live document missing %q: %s", expected, recorder.Body.String())
		}
	}
}

func TestDynamicDocumentsLinkExactBuildStyles(t *testing.T) {
	assets := AssetConfig{
		ClientScript: "/_gobeyond/assets/test-build/app.js",
		Styles:       []string{"/_gobeyond/assets/test-build/assets/site-a1b2.css"},
	}
	server, err := NewWithAssets("test-build", "https://example.com", testPlans(), assets)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "https://example.com/products/trail-pack", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	for _, expected := range []string{
		`<link rel="stylesheet" href="/_gobeyond/assets/test-build/assets/site-a1b2.css">`,
		`<script type="module" src="/_gobeyond/assets/test-build/app.js"></script>`,
	} {
		if !strings.Contains(recorder.Body.String(), expected) {
			t.Fatalf("dynamic document missing %q", expected)
		}
	}
}

func TestLiveSEOFixturesExposeCrawlerContent(t *testing.T) {
	server, err := New("test-build", "https://example.com", testPlans())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		path     string
		contains []string
	}{
		{"/", []string{"<h1>GoBeyond Field Guide</h1>", `href="/articles/portable-react"`, `href="/products/trail-pack"`, `property="og:image"`, `name="twitter:card"`, "application/ld+json"}},
		{"/products/trail-pack", []string{`<img src="https://example.com/images/trail-pack.svg" alt="Blue Trail Pack"`, "$129.00", "In stock", `property="og:image"`, `name="twitter:image"`, `"@type":"Product"`}},
		{"/category/1", []string{"<h1>Field notes · page ", `href="/articles/portable-react"`, `href="/category/2"`, `aria-label="Category pages"`, "CollectionPage"}},
		{"/locations/seattle", []string{"<address>", "500 Pine Street", `href="tel:+12065550100"`, "Open <!-- -->GoBeyond Seattle", "LocalBusiness"}},
	}
	for _, test := range tests {
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "https://example.com"+test.path, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: status=%d body=%s", test.path, recorder.Code, recorder.Body.String())
		}
		for _, expected := range test.contains {
			if !strings.Contains(recorder.Body.String(), expected) {
				t.Fatalf("%s: missing %q in %s", test.path, expected, recorder.Body.String())
			}
		}
	}
}

func TestLocalizedRoutesAreReciprocalAndAccountIsPrivate(t *testing.T) {
	server, err := New("test-build", "https://example.com", testPlans())
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		path, lang, canonical, alternate string
	}{
		{"/en/articles/portable-react", `lang="en"`, `href="https://example.com/en/articles/portable-react"`, `hreflang="fr" href="https://example.com/fr/articles/react-portable"`},
		{"/fr/articles/react-portable", `lang="fr"`, `href="https://example.com/fr/articles/react-portable"`, `hreflang="en" href="https://example.com/en/articles/portable-react"`},
	} {
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "https://example.com"+test.path, nil))
		body := recorder.Body.String()
		for _, expected := range []string{test.lang, test.canonical, test.alternate, `name="twitter:card"`, "application/ld+json"} {
			if recorder.Code != http.StatusOK || !strings.Contains(body, expected) {
				t.Fatalf("%s: status=%d missing %q in %s", test.path, recorder.Code, expected, body)
			}
		}
	}
	account := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "https://example.com/account", nil)
	request.AddCookie(&http.Cookie{Name: "gobeyond_account", Value: "Avery"})
	server.ServeHTTP(account, request)
	for _, expected := range []string{"Your account", "Avery", `name="robots" content="noindex, nofollow"`, "private, no-store"} {
		if account.Code != http.StatusOK || !strings.Contains(account.Body.String()+account.Header().Get("Cache-Control"), expected) {
			t.Fatalf("account: status=%d missing %q", account.Code, expected)
		}
	}
}

func TestCrawlerControlDocuments(t *testing.T) {
	server, err := New("test-build", "https://example.com", testPlans())
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ path, contentType, contains string }{
		{"/robots.txt", "text/plain", "Disallow: /account"},
		{"/sitemap.xml", "application/xml", "https://example.com/fr/articles/react-portable"},
	} {
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "https://example.com"+test.path, nil))
		if recorder.Code != http.StatusOK || !strings.Contains(recorder.Header().Get("Content-Type"), test.contentType) || !strings.Contains(recorder.Body.String(), test.contains) {
			t.Fatalf("%s: status=%d type=%q body=%s", test.path, recorder.Code, recorder.Header().Get("Content-Type"), recorder.Body.String())
		}
	}
}

func TestLiveSEOStatusAndRedirectSemantics(t *testing.T) {
	server, err := New("test-build", "https://example.com", testPlans())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		path     string
		status   int
		location string
	}{
		{path: "/articles/missing", status: http.StatusNotFound},
		{path: "/articles/old-portable-react", status: http.StatusPermanentRedirect, location: "/articles/portable-react"},
	}
	for _, test := range tests {
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "https://example.com"+test.path, nil))
		if recorder.Code != test.status || recorder.Header().Get("Location") != test.location {
			t.Fatalf("%s: status=%d location=%q", test.path, recorder.Code, recorder.Header().Get("Location"))
		}
	}
}

func TestLiveTypedActionContract(t *testing.T) {
	server, err := New("test-build", "https://example.com", testPlans())
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"https://example.com/_gobeyond/actions/test-build/"+actioncontract.ActionID,
		strings.NewReader(`{"productSlug":"trail-pack","quantity":2}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://example.com")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		BuildID string                `json:"buildId"`
		Data    actioncontract.Output `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.BuildID != "test-build" || !response.Data.Saved || response.Data.CartItemCount != 2 {
		t.Fatalf("unexpected action response: %#v", response)
	}
}

func TestLiveActionRejectsInvalidInputBeforeHandler(t *testing.T) {
	calls := 0
	server, err := gbruntime.New(gbruntime.Config{
		BuildID:      "test-build",
		PublicOrigin: "https://example.com",
		Actions: []gbruntime.Action{actioncontract.Register(func(_ *gb.ActionContext, input actioncontract.Input) (actioncontract.Output, error) {
			calls++
			return actioncontract.Output{Saved: true, CartItemCount: input.Quantity}, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "unknown field", body: `{"productSlug":"trail-pack","quantity":2,"admin":true}`},
		{name: "concatenated JSON", body: `{"productSlug":"trail-pack","quantity":2}{}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "https://example.com/_gobeyond/actions/test-build/"+actioncontract.ActionID, strings.NewReader(test.body))
			request.Header.Set("Origin", "https://example.com")
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "invalid_action_input") {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
	if calls != 0 {
		t.Fatalf("invalid inputs executed the handler %d times", calls)
	}

	request := httptest.NewRequest(http.MethodPost, "https://example.com/_gobeyond/actions/test-build/"+actioncontract.ActionID, strings.NewReader(`{"productSlug":"trail-pack","quantity":2}`))
	request.Header.Set("Origin", "https://example.com")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || calls != 1 {
		t.Fatalf("valid action status=%d calls=%d body=%s", recorder.Code, calls, recorder.Body.String())
	}
}

func TestLiveActionRejectsInvalidOutputBeforeDelivery(t *testing.T) {
	server, err := gbruntime.New(gbruntime.Config{
		BuildID:      "test-build",
		PublicOrigin: "https://example.com",
		Actions: []gbruntime.Action{actioncontract.Register(func(*gb.ActionContext, actioncontract.Input) (actioncontract.Output, error) {
			return actioncontract.Output{Saved: true, CartItemCount: 1 << 53}, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "https://example.com/_gobeyond/actions/test-build/"+actioncontract.ActionID, strings.NewReader(`{"productSlug":"trail-pack","quantity":2}`))
	request.Header.Set("Origin", "https://example.com")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusInternalServerError || !strings.Contains(recorder.Body.String(), "action_failed") || strings.Contains(recorder.Body.String(), "cartItemCount") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func testPlans() map[string]*renderplan.Plan {
	text := func(value renderplan.Expression) renderplan.Node {
		return &renderplan.Text{Kind: "text", Value: value}
	}
	path := func(name string) renderplan.Expression {
		return &renderplan.Path{Kind: "path", Path: []renderplan.PathSegment{renderplan.Property(name)}}
	}
	literal := func(value any) renderplan.Expression {
		return &renderplan.Literal{Kind: "literal", Value: value}
	}
	attr := func(name string, value renderplan.Expression, mode renderplan.AttributeMode) renderplan.Attribute {
		return renderplan.Attribute{Name: name, Value: value, Mode: mode}
	}
	local := func(name string, fields ...string) renderplan.Expression {
		segments := []renderplan.PathSegment{renderplan.Property(name)}
		for _, field := range fields {
			segments = append(segments, renderplan.Property(field))
		}
		return &renderplan.Path{Kind: "path", Path: segments}
	}
	element := func(tag string, children ...renderplan.Node) *renderplan.Element {
		return &renderplan.Element{Kind: "element", Tag: tag, Children: children}
	}
	link := func(href renderplan.Expression, children ...renderplan.Node) *renderplan.Element {
		return &renderplan.Element{Kind: "element", Tag: "a", Attributes: []renderplan.Attribute{attr("href", href, renderplan.AttributeURL)}, Children: children}
	}
	home := &renderplan.Plan{APIVersion: "gobeyond.render/v1alpha1", RouteID: HomeRouteID, Root: element("article",
		element("h1", text(literal("GoBeyond Field Guide"))),
		element("p", text(literal("Practical notes and equipment for building beyond the usual path."))),
		element("ul", element("li", link(path("featuredArticleHref"), text(literal("Read the featured article")))), element("li", link(path("featuredProductHref"), text(literal("See the featured product"))))),
	)}
	account := &renderplan.Plan{APIVersion: "gobeyond.render/v1alpha1", RouteID: AccountRouteID, Root: element("section", element("h1", text(literal("Your account"))), element("p", text(literal("Signed in as ")), text(path("displayName"))))}
	articleTime := element("time", text(path("publishedLabel")))
	articleTime.Attributes = []renderplan.Attribute{attr("dateTime", path("publishedAt"), renderplan.AttributeString)}
	article := &renderplan.Plan{APIVersion: "gobeyond.render/v1alpha1", RouteID: ArticleRouteID, Root: element("article",
		element("header",
			element("h1", text(path("title"))),
			element("p", text(path("description"))),
			element("p", text(literal("By ")), text(path("authorName")), text(literal(" · ")), articleTime),
		),
		&renderplan.Each{Kind: "each", Items: path("paragraphs"), Item: "paragraph", Key: path("paragraph"), Body: element("p", text(path("paragraph")))},
	)}
	category := &renderplan.Plan{APIVersion: "gobeyond.render/v1alpha1", RouteID: CategoryRouteID, Root: element("section",
		element("h1", text(literal("Field notes · page ")), text(path("currentPage"))),
		element("ol", &renderplan.Each{Kind: "each", Items: path("items"), Item: "item", Key: local("item", "href"), Body: element("li", element("h2", link(local("item", "href"), text(local("item", "name")))), element("p", text(local("item", "summary"))))}),
		&renderplan.Element{Kind: "element", Tag: "nav", Attributes: []renderplan.Attribute{attr("aria-label", literal("Category pages"), renderplan.AttributeString)}, Children: []renderplan.Node{
			&renderplan.Conditional{Kind: "conditional", Test: path("previousHref"), Consequent: link(path("previousHref"), text(literal("Previous page")))},
			&renderplan.Element{Kind: "element", Tag: "span", Attributes: []renderplan.Attribute{attr("aria-current", literal("page"), renderplan.AttributeString)}, Children: []renderplan.Node{text(literal("Page ")), text(path("currentPage"))}},
			&renderplan.Conditional{Kind: "conditional", Test: path("nextHref"), Consequent: link(path("nextHref"), text(literal("Next page")))},
		}},
	)}
	localizedPlan := func(routeID string, languageLabel string, hrefName string) *renderplan.Plan {
		linkLabel := "Français"
		if languageLabel == "Langues" {
			linkLabel = "English"
		}
		navigation := element("nav", link(path(hrefName), text(literal(linkLabel))))
		navigation.Attributes = []renderplan.Attribute{attr("aria-label", literal(languageLabel), renderplan.AttributeString)}
		return &renderplan.Plan{APIVersion: "gobeyond.render/v1alpha1", RouteID: routeID, Root: element("article",
			element("h1", text(path("title"))), element("p", text(path("description"))),
			&renderplan.Each{Kind: "each", Items: path("paragraphs"), Item: "paragraph", Key: path("paragraph"), Body: element("p", text(path("paragraph")))},
			navigation,
		)}
	}
	location := &renderplan.Plan{APIVersion: "gobeyond.render/v1alpha1", RouteID: LocationRouteID, Root: element("article",
		element("h1", text(path("name"))), element("p", text(path("description"))),
		element("address", text(path("streetAddress")), element("br"), text(path("locality")), text(literal(", ")), text(path("region")), text(literal(" ")), text(path("postalCode")), element("br"), link(path("phoneHref"), text(path("phone")))),
		element("h2", text(literal("Hours"))), element("ul", &renderplan.Each{Kind: "each", Items: path("hours"), Item: "hours", Key: path("hours"), Body: element("li", text(path("hours")))}),
		&renderplan.ClientOnly{Kind: "clientOnly", Fallback: link(path("mapHref"), text(literal("Open ")), text(path("name")), text(literal(" in maps")))},
	)}
	productImage := element("img")
	productImage.Attributes = []renderplan.Attribute{attr("src", path("image"), renderplan.AttributeURL), attr("alt", path("imageAlt"), renderplan.AttributeString), attr("width", literal(1200), renderplan.AttributeString), attr("height", literal(800), renderplan.AttributeString)}
	price := element("data", text(path("priceLabel")))
	price.Attributes = []renderplan.Attribute{attr("value", path("price"), renderplan.AttributeString)}
	product := &renderplan.Plan{APIVersion: "gobeyond.render/v1alpha1", RouteID: ProductRouteID, Root: element("article",
		element("h1", text(path("name"))), productImage, element("p", text(path("description"))), element("p", price),
		&renderplan.Conditional{Kind: "conditional", Test: &renderplan.Binary{Kind: "binary", Operator: "==", Left: path("availability"), Right: literal("InStock")}, Consequent: element("p", text(literal("In stock"))), Alternate: element("p", text(literal("Out of stock")))},
	)}
	return map[string]*renderplan.Plan{
		HomeRouteID: home, AccountRouteID: account, ArticleRouteID: article, CategoryRouteID: category,
		EnglishArticleRouteID: localizedPlan(EnglishArticleRouteID, "Languages", "alternateFrench"),
		FrenchArticleRouteID:  localizedPlan(FrenchArticleRouteID, "Langues", "alternateEnglish"),
		LocationRouteID:       location, ProductRouteID: product,
	}
}
