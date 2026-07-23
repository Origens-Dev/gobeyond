package runtime

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gb "github.com/holbrookab/gobeyond"
	"github.com/holbrookab/gobeyond/browserassets"
	gbmiddleware "github.com/holbrookab/gobeyond/middleware"
	"github.com/holbrookab/gobeyond/renderplan"
	"github.com/holbrookab/gobeyond/router"
)

func testAction(id string, handler func(*gb.ActionContext, json.RawMessage) (any, error)) Action {
	return RegisterAction(
		id,
		func(raw json.RawMessage) (json.RawMessage, error) {
			if !json.Valid(raw) {
				return nil, errors.New("invalid JSON")
			}
			return raw, nil
		},
		func(any) error { return nil },
		handler,
	)
}

func TestDynamicDocumentIsSEOComplete(t *testing.T) {
	plan := productPlan()
	server, err := New(Config{
		BuildID:      "build-1",
		PublicOrigin: "https://example.com",
		Pages: []PageRoute{{
			Route: router.Route{ID: "product", Pattern: "/products/[slug]", Mode: router.ModeDynamic},
			Plan:  plan,
			Load: func(ctx *gb.PageContext) (LoadedPage, error) {
				return LoadedPage{
					Kind:   gb.ResultOK,
					Props:  map[string]any{"name": "Safe <Widget>", "available": true},
					Status: http.StatusOK,
					Cache:  gb.CachePolicy{Mode: gb.CachePublic, MaxAge: 60},
					Metadata: gb.Metadata{
						Lang:        "en",
						Title:       "Safe Widget",
						Description: "A useful widget",
						Canonical:   "https://example.com/products/widget",
						Robots:      "index, follow",
						OpenGraph:   gb.OpenGraph{Type: "product", Title: "Safe Widget", Description: "A useful widget", URL: "https://example.com/products/widget", Images: []string{"https://example.com/widget.jpg"}},
						Twitter:     gb.Twitter{Card: "summary_large_image", Title: "Safe Widget", Description: "A useful widget", Images: []string{"https://example.com/widget.jpg"}},
						JSONLD:      []gb.JSONLD{{"@context": "https://schema.org", "@type": "Product", "name": "Safe Widget"}},
					},
				}, nil
			},
			Indexable:    true,
			ClientScript: "https://cdn.example.com/product.js",
		}},
		Middleware: []gbmiddleware.Rule{{Name: "consume-session", Middleware: func(next gb.Handler) gb.Handler {
			return func(ctx *gb.RequestContext) (gb.Response, error) {
				_ = ctx.Request.Header.Get("Cookie")
				ctx.Request.Header.Del("Cookie")
				return next(ctx)
			}
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "https://example.com/products/widget", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	for _, expected := range []string{"Safe &lt;Widget&gt;", "rel=\"canonical\"", "application/ld+json", "__GOBEYOND_DATA__", "product.js"} {
		if !strings.Contains(recorder.Body.String(), expected) {
			t.Fatalf("body missing %q: %s", expected, recorder.Body.String())
		}
	}
	privateRequest := httptest.NewRequest(http.MethodGet, "https://example.com/products/widget", nil)
	privateRequest.Header.Set("Cookie", "session=present")
	privateRecorder := httptest.NewRecorder()
	server.ServeHTTP(privateRecorder, privateRequest)
	if got := privateRecorder.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Fatalf("cookie-bearing response cache = %q", got)
	}
}

func TestDocumentUsesRouteAwareBrowserAssets(t *testing.T) {
	manifest := &browserassets.Manifest{
		APIVersion: browserassets.APIVersionV1Alpha1,
		BuildID:    "build-1",
		Bootstrap:  browserassets.BrowserAssets{Bootstrap: "/assets/bootstrap.js", ModulePreloads: []string{"/assets/react.js"}, Styles: []string{"/assets/base.css"}},
		Routes: map[string]browserassets.BrowserAssets{
			"home": {Bootstrap: "/assets/home.js", ModulePreloads: []string{}, Styles: []string{"/assets/home.css"}},
		},
	}
	server, err := New(Config{
		BuildID: "build-1", PublicOrigin: "https://example.com", BrowserAssets: manifest,
		Pages: []PageRoute{{
			Route:        router.Route{ID: "home", Pattern: "/", Mode: router.ModeStatic},
			Plan:         &renderplan.Plan{APIVersion: gb.RenderAPIVersion, RouteID: "home", Root: &renderplan.Element{Kind: "element", Tag: "main"}},
			Static:       &LoadedPage{Kind: gb.ResultOK, Status: http.StatusOK, Props: map[string]any{}, Metadata: gb.Metadata{Lang: "en", Title: "Home"}},
			ClientScript: "/assets/legacy.js", Styles: []string{"/assets/legacy.css"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "https://example.com/", nil))
	body := recorder.Body.String()
	for _, want := range []string{"/assets/bootstrap.js", "/assets/react.js", "/assets/home.js", "/assets/base.css", "/assets/home.css"} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, "legacy") {
		t.Fatalf("route-aware assets must replace legacy assets: %s", body)
	}
}

func TestBuildMismatchRejectsActionBeforeExecution(t *testing.T) {
	var calls atomic.Int32
	server, err := New(Config{
		BuildID:      "new-build",
		PublicOrigin: "https://example.com",
		Actions: []Action{testAction("save", func(*gb.ActionContext, json.RawMessage) (any, error) {
			calls.Add(1)
			return map[string]bool{"saved": true}, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "https://example.com/_gobeyond/actions/old-build/save", strings.NewReader(`{}`)))
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "build_mismatch") {
		t.Fatalf("unexpected mismatch response: %d %s", recorder.Code, recorder.Body.String())
	}
	if calls.Load() != 0 {
		t.Fatal("mismatched action must not execute")
	}
}

func TestActionRejectsCrossOriginBeforeExecution(t *testing.T) {
	var calls atomic.Int32
	server, err := New(Config{
		BuildID:      "build-1",
		PublicOrigin: "https://example.com",
		Actions: []Action{testAction("save", func(*gb.ActionContext, json.RawMessage) (any, error) {
			calls.Add(1)
			return nil, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "https://example.com/_gobeyond/actions/build-1/save", strings.NewReader(`{}`))
	request.Header.Set("Origin", "https://attacker.example")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden || calls.Load() != 0 {
		t.Fatalf("status=%d calls=%d body=%s", recorder.Code, calls.Load(), recorder.Body.String())
	}
}

func TestCompressesTextResponsesWhenRequested(t *testing.T) {
	server, err := New(Config{BuildID: "build-1", PublicOrigin: "https://example.com"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "https://example.com/unknown", nil)
	request.Header.Set("Accept-Encoding", "br, gzip;q=1")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Header().Get("Content-Encoding") != "gzip" || !strings.Contains(recorder.Header().Get("Vary"), "Accept-Encoding") {
		t.Fatalf("compression headers = %v", recorder.Header())
	}
	reader, err := gzip.NewReader(recorder.Body)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "Not found") {
		t.Fatalf("decompressed body = %q", body)
	}
}

func TestHealthEndpointsDoNotDependOnPublicHost(t *testing.T) {
	server, err := New(Config{BuildID: "build-1", PublicOrigin: "https://www.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/__gobeyond/healthz", "/__gobeyond/readyz"} {
		request := httptest.NewRequest(http.MethodGet, "http://internal-alb.local"+path, nil)
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: status=%d body=%s", path, recorder.Code, recorder.Body.String())
		}
	}
}

func TestMiddlewareRewrite(t *testing.T) {
	plan := &renderplan.Plan{APIVersion: gb.RenderAPIVersion, RouteID: "new", Root: &renderplan.Element{Kind: "element", Tag: "h1", Children: []renderplan.Node{&renderplan.Text{Kind: "text", Value: &renderplan.Literal{Kind: "literal", Value: "New"}}}}}
	server, err := New(Config{
		BuildID:      "build-1",
		PublicOrigin: "https://example.com",
		Pages: []PageRoute{{
			Route:  router.Route{ID: "new", Pattern: "/new", Mode: router.ModeStatic},
			Plan:   plan,
			Static: &LoadedPage{Kind: gb.ResultOK, Props: map[string]any{}, Status: http.StatusOK, Metadata: gb.Metadata{Lang: "en", Title: "New"}},
		}},
		Middleware: []gbmiddleware.Rule{{
			Name:   "legacy",
			Config: gb.MiddlewareConfig{Patterns: []string{"/legacy"}},
			Middleware: func(next gb.Handler) gb.Handler {
				return func(ctx *gb.RequestContext) (gb.Response, error) {
					return gb.Rewrite("/new"), nil
				}
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "https://example.com/legacy", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "<h1>New</h1>") {
		t.Fatalf("rewrite failed: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestMiddlewareAppliesToAPIsAndActions(t *testing.T) {
	var apiCalls atomic.Int32
	var actionCalls atomic.Int32
	server, err := New(Config{
		BuildID:      "build-1",
		PublicOrigin: "https://example.com",
		APIs: []APIRoute{{
			Route: router.Route{ID: "api_private", Pattern: "/api/private", Mode: router.ModeAPI},
			Methods: map[string]gb.Handler{http.MethodGet: func(*gb.RequestContext) (gb.Response, error) {
				apiCalls.Add(1)
				return gb.Response{Status: http.StatusOK}, nil
			}},
		}},
		Actions: []Action{testAction("save", func(*gb.ActionContext, json.RawMessage) (any, error) {
			actionCalls.Add(1)
			return map[string]bool{"saved": true}, nil
		})},
		Middleware: []gbmiddleware.Rule{{
			Name:   "auth",
			Config: gb.MiddlewareConfig{Patterns: []string{"/api/[...path]", "/_gobeyond/actions/[...path]"}},
			Middleware: func(next gb.Handler) gb.Handler {
				return func(ctx *gb.RequestContext) (gb.Response, error) {
					if ctx.Request.Header.Get("Authorization") == "" {
						return gb.Response{Status: http.StatusUnauthorized}, nil
					}
					return next(ctx)
				}
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	requests := []*http.Request{
		httptest.NewRequest(http.MethodGet, "https://example.com/api/private", nil),
		httptest.NewRequest(http.MethodPost, "https://example.com/_gobeyond/actions/build-1/save", strings.NewReader(`{}`)),
	}
	requests[1].Header.Set("Origin", "https://example.com")
	for _, request := range requests {
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s status = %d", request.URL.Path, recorder.Code)
		}
	}
	if apiCalls.Load() != 0 || actionCalls.Load() != 0 {
		t.Fatalf("middleware must run before handlers: api=%d action=%d", apiCalls.Load(), actionCalls.Load())
	}
}

func TestRuntimeDataAppliesMiddlewareToThePublicPath(t *testing.T) {
	server, err := New(Config{
		BuildID:      "build-1",
		PublicOrigin: "https://example.com",
		Pages: []PageRoute{{
			Route: router.Route{ID: "private", Pattern: "/private/[slug]", Mode: router.ModeDynamic},
			Plan: &renderplan.Plan{APIVersion: gb.RenderAPIVersion, RouteID: "private", Root: &renderplan.Element{
				Kind: "element", Tag: "p", Children: []renderplan.Node{&renderplan.Text{Kind: "text", Value: &renderplan.Literal{Kind: "literal", Value: "secret"}}},
			}},
			Load: func(*gb.PageContext) (LoadedPage, error) {
				return LoadedPage{Kind: gb.ResultOK, Status: http.StatusOK, Props: map[string]any{}}, nil
			},
		}},
		Middleware: []gbmiddleware.Rule{{
			Name:   "protect-private-pages",
			Config: gb.MiddlewareConfig{Patterns: []string{"/private/[slug]"}},
			Middleware: func(gb.Handler) gb.Handler {
				return func(ctx *gb.RequestContext) (gb.Response, error) {
					if ctx.Request.URL.Path != "/private/report" {
						t.Fatalf("middleware saw internal path %q", ctx.Request.URL.Path)
					}
					return gb.Response{Status: http.StatusUnauthorized}, nil
				}
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "https://example.com/_gobeyond/runtime/build-1/private?path=%2Fprivate%2Freport", nil)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestRuntimeDataUsesDocumentCachePolicyAndPrivacyDowngrade(t *testing.T) {
	page := PageRoute{
		Route: router.Route{ID: "public", Pattern: "/public", Mode: router.ModeDynamic},
		Plan:  &renderplan.Plan{APIVersion: gb.RenderAPIVersion, RouteID: "public", Root: &renderplan.Element{Kind: "element", Tag: "main"}},
		Load: func(*gb.PageContext) (LoadedPage, error) {
			return LoadedPage{Kind: gb.ResultOK, Status: http.StatusOK, Props: map[string]any{}, Metadata: gb.Metadata{Lang: "en", Title: "Public"}, Cache: gb.PublicRevalidate(time.Minute, 5*time.Minute, 24*time.Hour)}, nil
		},
	}
	server, err := New(Config{BuildID: "build-1", PublicOrigin: "https://example.com", Pages: []PageRoute{page}})
	if err != nil {
		t.Fatal(err)
	}
	want := "public, max-age=0, s-maxage=60, stale-while-revalidate=300, stale-if-error=86400"
	for _, path := range []string{"/public", "/_gobeyond/runtime/build-1/public?path=%2Fpublic"} {
		request := httptest.NewRequest(http.MethodGet, "https://example.com"+path, nil)
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, request)
		if got := recorder.Header().Get("Cache-Control"); got != want {
			t.Fatalf("%s cache = %q, want %q", path, got, want)
		}

		request = httptest.NewRequest(http.MethodGet, "https://example.com"+path, nil)
		request.Header.Set("Cookie", "session=present")
		recorder = httptest.NewRecorder()
		server.ServeHTTP(recorder, request)
		if got := recorder.Header().Get("Cache-Control"); got != "private, no-store" {
			t.Fatalf("%s private cache = %q", path, got)
		}
	}
}

func TestRequestResolvedPublicOrigin(t *testing.T) {
	resolver := func(request *http.Request) (string, error) {
		switch request.Host {
		case "one.example.com", "two.example.com":
			return "https://" + request.Host, nil
		default:
			return "", errors.New("unknown host")
		}
	}
	server, err := New(Config{
		BuildID:             "build-1",
		ResolvePublicOrigin: resolver,
		Pages: []PageRoute{{
			Route: router.Route{ID: "home", Pattern: "/", Mode: router.ModeDynamic},
			Plan:  &renderplan.Plan{APIVersion: gb.RenderAPIVersion, RouteID: "home", Root: &renderplan.Element{Kind: "element", Tag: "main"}},
			Load: func(ctx *gb.PageContext) (LoadedPage, error) {
				return LoadedPage{Kind: gb.ResultOK, Status: http.StatusOK, Props: map[string]any{}, Metadata: gb.Metadata{Lang: "en", Title: ctx.PublicOrigin}}, nil
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"one.example.com", "two.example.com"} {
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "https://"+host+"/", nil))
		if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "<title>https://"+host+"</title>") {
			t.Fatalf("host %s: status=%d body=%s", host, recorder.Code, recorder.Body.String())
		}
	}
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "https://unknown.example.com/", nil))
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "invalid_host") {
		t.Fatalf("unknown host: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestResolvedPublicOriginProtectsActions(t *testing.T) {
	server, err := New(Config{
		BuildID: "build-1",
		ResolvePublicOrigin: func(request *http.Request) (string, error) {
			if request.Host != "preview.example.com" {
				return "", errors.New("unknown host")
			}
			return "https://preview.example.com", nil
		},
		Actions: []Action{testAction("save", func(ctx *gb.ActionContext, _ json.RawMessage) (any, error) {
			if ctx.PublicOrigin != "https://preview.example.com" {
				t.Fatalf("action public origin = %q", ctx.PublicOrigin)
			}
			return map[string]bool{"saved": true}, nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "https://preview.example.com/_gobeyond/actions/build-1/save", strings.NewReader(`{}`))
	request.Header.Set("Origin", "https://preview.example.com")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestPublicOriginStrategyIsExclusive(t *testing.T) {
	if _, err := New(Config{BuildID: "build-1"}); err == nil {
		t.Fatal("expected missing origin strategy to fail")
	}
	if _, err := New(Config{BuildID: "build-1", PublicOrigin: "https://example.com", ResolvePublicOrigin: func(*http.Request) (string, error) { return "https://example.com", nil }}); err == nil {
		t.Fatal("expected multiple origin strategies to fail")
	}
}

func TestDocumentMiddlewareParamsAndValuesReachLoader(t *testing.T) {
	server, err := New(Config{
		BuildID:      "build-1",
		PublicOrigin: "https://example.com",
		Pages: []PageRoute{{
			Route: router.Route{ID: "tenant", Pattern: "/tenants/[slug]", Mode: router.ModeDynamic},
			Plan: &renderplan.Plan{APIVersion: gb.RenderAPIVersion, RouteID: "tenant", Root: &renderplan.Element{
				Kind: "element", Tag: "main",
			}},
			Load: func(ctx *gb.PageContext) (LoadedPage, error) {
				if ctx.Params["slug"] != "acme" || ctx.Values["tenant"] != "acme" {
					t.Fatalf("loader context params=%#v values=%#v", ctx.Params, ctx.Values)
				}
				return LoadedPage{Kind: gb.ResultRedirect, Status: http.StatusTemporaryRedirect, RedirectTo: "/signed-in"}, nil
			},
		}},
		Middleware: []gbmiddleware.Rule{{
			Name:   "tenant-context",
			Config: gb.MiddlewareConfig{Patterns: []string{"/tenants/[slug]"}},
			Middleware: func(next gb.Handler) gb.Handler {
				return func(ctx *gb.RequestContext) (gb.Response, error) {
					if ctx.Params["slug"] != "acme" {
						t.Fatalf("middleware params=%#v", ctx.Params)
					}
					ctx.Values["tenant"] = ctx.Params["slug"]
					return next(ctx)
				}
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "https://example.com/tenants/acme", nil))
	if recorder.Code != http.StatusTemporaryRedirect || recorder.Header().Get("Location") != "/signed-in" {
		t.Fatalf("status=%d location=%q", recorder.Code, recorder.Header().Get("Location"))
	}
}

func productPlan() *renderplan.Plan {
	return &renderplan.Plan{
		APIVersion: gb.RenderAPIVersion,
		RouteID:    "product",
		Root: &renderplan.Element{
			Kind: "element",
			Tag:  "main",
			Children: []renderplan.Node{
				&renderplan.Element{Kind: "element", Tag: "h1", Children: []renderplan.Node{&renderplan.Text{Kind: "text", Value: &renderplan.Path{Kind: "path", Path: []renderplan.PathSegment{renderplan.Property("name")}}}}},
				&renderplan.Conditional{Kind: "conditional", Test: &renderplan.Path{Kind: "path", Path: []renderplan.PathSegment{renderplan.Property("available")}}, Consequent: &renderplan.Element{Kind: "element", Tag: "p", Children: []renderplan.Node{&renderplan.Text{Kind: "text", Value: &renderplan.Literal{Kind: "literal", Value: "In stock"}}}}},
			},
		},
	}
}
