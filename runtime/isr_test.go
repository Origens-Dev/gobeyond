package runtime

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gb "github.com/Origens-Dev/gobeyond"
	"github.com/Origens-Dev/gobeyond/cache"
	"github.com/Origens-Dev/gobeyond/codegen"
	"github.com/Origens-Dev/gobeyond/renderplan"
	"github.com/Origens-Dev/gobeyond/router"
)

func isrContracts() *codegen.Document {
	return &codegen.Document{
		Routes: []codegen.Route{{
			RouteID: "product",
			Props: codegen.Value{Kind: codegen.KindObject, Shape: map[string]codegen.Value{
				"name":      {Kind: codegen.KindString},
				"available": {Kind: codegen.KindBoolean},
				"body":      {Kind: codegen.KindSafeHTML, Optional: true},
			}},
			Revalidate: 60,
			Tags:       []string{"products"},
		}},
	}
}

func isrPage(load PageLoader) PageRoute {
	return PageRoute{
		Route:      router.Route{ID: "product", Pattern: "/products/[slug]", Mode: router.ModeDynamic},
		Plan:       productPlan(),
		Load:       load,
		Revalidate: time.Minute,
		Tags:       []string{"products"},
	}
}

func isrServer(t *testing.T, load PageLoader) *Server {
	t.Helper()
	server, err := New(Config{
		BuildID:      "build-1",
		PublicOrigin: "https://example.com",
		Pages:        []PageRoute{isrPage(load)},
		Cache:        &cache.RuntimeConfig{DeployPrefix: "test-deploy"},
		Contracts:    isrContracts(),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return server
}

func okPage(name string) LoadedPage {
	return LoadedPage{
		Kind:     gb.ResultOK,
		Status:   http.StatusOK,
		Props:    map[string]any{"name": name, "available": true},
		Metadata: gb.Metadata{Lang: "en", Title: name},
		Cache:    gb.CachePolicy{Mode: gb.CachePublic, MaxAge: 60},
	}
}

func getDocument(t *testing.T, server *Server, target string, header http.Header) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	for name, values := range header {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	return recorder
}

// TestRouteISRServesLoadedPropsFromCache is the whole point of the slice: the
// loader runs once per revalidate window per URL, while every request still
// renders its own document.
func TestRouteISRServesLoadedPropsFromCache(t *testing.T) {
	var loads atomic.Int32
	server := isrServer(t, func(*gb.PageContext) (LoadedPage, error) {
		loads.Add(1)
		return okPage("Widget"), nil
	})

	for i := 0; i < 2; i++ {
		recorder := getDocument(t, server, "https://example.com/products/widget", nil)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), "Widget") {
			t.Fatalf("document %d did not render the cached props: %s", i, recorder.Body.String())
		}
		if got := recorder.Header().Get("Cache-Control"); got != "public, max-age=60" {
			t.Fatalf("document %d cache-control = %q", i, got)
		}
	}
	if loads.Load() != 1 {
		t.Fatalf("loader ran %d times, want 1", loads.Load())
	}
}

// TestRouteISRSharesEntriesWithSoftNavigation proves the runtime JSON endpoint
// goes through the same loader path, so a soft navigation is served from - and
// populates - the same entry as a document request.
func TestRouteISRSharesEntriesWithSoftNavigation(t *testing.T) {
	var loads atomic.Int32
	server := isrServer(t, func(*gb.PageContext) (LoadedPage, error) {
		loads.Add(1)
		return okPage("Widget"), nil
	})

	runtimeResponse := getDocument(t, server, "https://example.com/_gobeyond/builds/build-1/runtime/product?path=%2Fproducts%2Fwidget", nil)
	if runtimeResponse.Code != http.StatusOK {
		t.Fatalf("runtime status=%d body=%s", runtimeResponse.Code, runtimeResponse.Body.String())
	}
	document := getDocument(t, server, "https://example.com/products/widget", nil)
	if document.Code != http.StatusOK {
		t.Fatalf("document status=%d body=%s", document.Code, document.Body.String())
	}
	if loads.Load() != 1 {
		t.Fatalf("loader ran %d times, want soft navigation and the document to share one entry", loads.Load())
	}
}

func TestRouteISRPrivateRequestBypassesTheCache(t *testing.T) {
	var loads atomic.Int32
	server := isrServer(t, func(*gb.PageContext) (LoadedPage, error) {
		loads.Add(1)
		return okPage("Widget"), nil
	})

	for i := 0; i < 2; i++ {
		recorder := getDocument(t, server, "https://example.com/products/widget", http.Header{"Cookie": []string{"session=abc"}})
		if recorder.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	}
	if loads.Load() != 2 {
		t.Fatalf("loader ran %d times, want a cookie-bearing request to bypass the cache both times", loads.Load())
	}
	if getDocument(t, server, "https://example.com/products/widget", nil).Code != http.StatusOK {
		t.Fatal("anonymous request failed")
	}
	if loads.Load() != 3 {
		t.Fatalf("loader ran %d times, want the private requests to have left the shared entry unwritten", loads.Load())
	}
}

// TestRouteISRSetCookieBlocksTheWrite covers the Set gate that the request
// headers cannot catch: an anonymous request whose loader mints a session.
func TestRouteISRSetCookieBlocksTheWrite(t *testing.T) {
	var loads atomic.Int32
	server := isrServer(t, func(*gb.PageContext) (LoadedPage, error) {
		loads.Add(1)
		page := okPage("Widget")
		page.Headers = http.Header{"Set-Cookie": []string{"session=minted; Path=/"}}
		return page, nil
	})

	for i := 0; i < 2; i++ {
		if code := getDocument(t, server, "https://example.com/products/widget", nil).Code; code != http.StatusOK {
			t.Fatalf("status=%d", code)
		}
	}
	if loads.Load() != 2 {
		t.Fatalf("loader ran %d times, want a Set-Cookie response never to be shared", loads.Load())
	}
}

func TestRouteISRDoesNotCacheNotFound(t *testing.T) {
	var loads atomic.Int32
	server := isrServer(t, func(*gb.PageContext) (LoadedPage, error) {
		loads.Add(1)
		return LoadedPage{
			Kind:     gb.ResultNotFound,
			Status:   http.StatusNotFound,
			Props:    map[string]any{"name": "Not found", "available": false},
			Metadata: gb.Metadata{Lang: "en", Title: "Not found", Robots: "noindex, nofollow"},
		}, nil
	})

	for i := 0; i < 2; i++ {
		if code := getDocument(t, server, "https://example.com/products/ghost", nil).Code; code != http.StatusNotFound {
			t.Fatalf("status=%d, want 404", code)
		}
	}
	if loads.Load() != 2 {
		t.Fatalf("loader ran %d times, want a not-found result never to be cached", loads.Load())
	}
}

// TestRouteISRTagAndPathRevalidation checks both invalidation handles a route
// gets: its declared tags, and the path tag the coordinator adds so
// RevalidatePath drops exactly one URL.
func TestRouteISRTagAndPathRevalidation(t *testing.T) {
	var loads atomic.Int32
	server := isrServer(t, func(ctx *gb.PageContext) (LoadedPage, error) {
		loads.Add(1)
		return okPage(ctx.Params["slug"]), nil
	})
	warm := func(path string) {
		t.Helper()
		if code := getDocument(t, server, "https://example.com"+path, nil).Code; code != http.StatusOK {
			t.Fatalf("%s status=%d", path, code)
		}
	}
	warm("/products/widget")
	warm("/products/gadget")
	if loads.Load() != 2 {
		t.Fatalf("warming loaded %d times, want 2", loads.Load())
	}

	ctx := cache.WithRequestScope(t.Context(), cache.NewRequestScope(false, cache.WithRuntimeHandle(server.cache)))
	if err := cache.RevalidatePath(ctx, "/products/widget"); err != nil {
		t.Fatalf("RevalidatePath() error = %v", err)
	}
	warm("/products/widget")
	if loads.Load() != 3 {
		t.Fatalf("loads = %d, want the revalidated path to recompute", loads.Load())
	}
	warm("/products/gadget")
	if loads.Load() != 3 {
		t.Fatalf("loads = %d, want the sibling page to survive a path revalidation", loads.Load())
	}

	if err := cache.RevalidateTag(ctx, "products"); err != nil {
		t.Fatalf("RevalidateTag() error = %v", err)
	}
	warm("/products/widget")
	warm("/products/gadget")
	if loads.Load() != 5 {
		t.Fatalf("loads = %d, want the route tag to drop both pages", loads.Load())
	}
}

// TestRouteISRRestoresSafeHTMLTrust is the reason cached props go through the
// contract rather than json.Unmarshal: a string the contract declares safeHTML
// must render as markup on a cache hit exactly as it did on the miss, and
// nothing else may gain that trust.
func TestRouteISRRestoresSafeHTMLTrust(t *testing.T) {
	plan := productPlan()
	plan.Root.(*renderplan.Element).Children = append(plan.Root.(*renderplan.Element).Children, &renderplan.RawHTML{
		Kind:  "rawHtml",
		Value: &renderplan.Path{Kind: "path", Path: []renderplan.PathSegment{renderplan.Property("body")}},
	})
	var loads atomic.Int32
	server, err := New(Config{
		BuildID:      "build-1",
		PublicOrigin: "https://example.com",
		Pages: []PageRoute{{
			Route: router.Route{ID: "product", Pattern: "/products/[slug]", Mode: router.ModeDynamic},
			Plan:  plan,
			Load: func(*gb.PageContext) (LoadedPage, error) {
				loads.Add(1)
				page := okPage("Widget")
				page.Props = map[string]any{
					"name":      "Widget <script>",
					"available": true,
					"body":      renderplan.TrustedHTML("<em>rich</em>"),
				}
				return page, nil
			},
			Revalidate: time.Minute,
			Tags:       []string{"products"},
		}},
		Cache:     &cache.RuntimeConfig{DeployPrefix: "test-deploy"},
		Contracts: isrContracts(),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	miss := getDocument(t, server, "https://example.com/products/widget", nil).Body.String()
	hit := getDocument(t, server, "https://example.com/products/widget", nil).Body.String()
	if loads.Load() != 1 {
		t.Fatalf("loader ran %d times, want 1", loads.Load())
	}
	for _, body := range []string{miss, hit} {
		if !strings.Contains(body, "<em>rich</em>") {
			t.Fatalf("trusted markup was escaped: %s", body)
		}
		if !strings.Contains(body, "Widget &lt;script&gt;") {
			t.Fatalf("untrusted text was not escaped: %s", body)
		}
	}
}

func TestRouteCachingRequiresALoaderAndContracts(t *testing.T) {
	static := &LoadedPage{Kind: gb.ResultOK, Status: http.StatusOK, Props: map[string]any{}, Metadata: gb.Metadata{Lang: "en", Title: "Home"}}
	base := func(page PageRoute, contracts *codegen.Document) error {
		_, err := New(Config{
			BuildID:      "build-1",
			PublicOrigin: "https://example.com",
			Pages:        []PageRoute{page},
			Cache:        &cache.RuntimeConfig{DeployPrefix: "test-deploy"},
			Contracts:    contracts,
		})
		return err
	}
	staticPage := isrPage(nil)
	staticPage.Static = static
	if err := base(staticPage, isrContracts()); err == nil {
		t.Fatal("expected route caching on a static page to be rejected")
	}
	if err := base(isrPage(func(*gb.PageContext) (LoadedPage, error) { return okPage("Widget"), nil }), nil); err == nil {
		t.Fatal("expected route caching without contracts to be rejected")
	}
	untagged := isrPage(func(*gb.PageContext) (LoadedPage, error) { return okPage("Widget"), nil })
	untagged.Revalidate = 0
	if err := base(untagged, isrContracts()); err == nil {
		t.Fatal("expected tags without a revalidate window to be rejected")
	}
	if err := base(isrPage(func(*gb.PageContext) (LoadedPage, error) { return okPage("Widget"), nil }), &codegen.Document{}); err == nil {
		t.Fatal("expected a route missing from the contracts to be rejected")
	}
}

// TestRouteISRWithoutACacheRuntimeLoadsEveryRequest keeps the degraded mode
// honest: a build whose deployment configures no cache still serves the route,
// it just recomputes.
func TestRouteISRWithoutACacheRuntimeLoadsEveryRequest(t *testing.T) {
	var loads atomic.Int32
	server, err := New(Config{
		BuildID:      "build-1",
		PublicOrigin: "https://example.com",
		Pages: []PageRoute{isrPage(func(*gb.PageContext) (LoadedPage, error) {
			loads.Add(1)
			return okPage("Widget"), nil
		})},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	for i := 0; i < 2; i++ {
		if code := getDocument(t, server, "https://example.com/products/widget", nil).Code; code != http.StatusOK {
			t.Fatalf("status=%d", code)
		}
	}
	if loads.Load() != 2 {
		t.Fatalf("loader ran %d times, want every request to recompute without a cache", loads.Load())
	}
}
