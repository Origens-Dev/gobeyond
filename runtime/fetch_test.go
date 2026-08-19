package runtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	gb "github.com/Origens-Dev/gobeyond"
	"github.com/Origens-Dev/gobeyond/renderplan"
	"github.com/Origens-Dev/gobeyond/router"
)

func TestPlatformOriginFetcherPreservesRequestHostAndPath(t *testing.T) {
	var gotURL *url.URL
	var gotHost string
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		gotURL = request.URL
		gotHost = request.Host
		return &http.Response{StatusCode: http.StatusNoContent, Header: make(http.Header), Body: http.NoBody, Request: request}, nil
	})}
	fetcher, err := NewPlatformOriginFetcher("https://platform.internal/base", client)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "https://tenant.example/source?q=1", nil)
	response, err := fetcher.Fetch(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if gotURL.String() != "https://platform.internal/base/source?q=1" || gotHost != "tenant.example" {
		t.Fatalf("fallback request URL=%q host=%q", gotURL, gotHost)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func fetchTestPage(id, pattern string) PageRoute {
	return PageRoute{
		Route: router.Route{ID: id, Pattern: pattern, Mode: router.ModeDynamic},
		Plan: &renderplan.Plan{
			APIVersion: gb.RenderAPIVersion,
			RouteID:    id,
			Root:       &renderplan.Element{Kind: "element", Tag: "main"},
		},
		Load: func(*gb.PageContext) (LoadedPage, error) {
			return LoadedPage{
				Kind: gb.ResultOK, Status: http.StatusOK, Props: map[string]any{},
				Metadata: gb.Metadata{Lang: "en", Title: id},
			}, nil
		},
	}
}

func TestFetchSameSlotRunsMiddlewareAgain(t *testing.T) {
	var middlewareCalls atomic.Int32
	server, err := New(Config{
		BuildID:      "build-1",
		PublicOrigin: "https://example.com",
		Pages:        []PageRoute{fetchTestPage("source", "/source"), fetchTestPage("target", "/target")},
		Middleware: func(next gb.Handler) gb.Handler {
			return func(ctx *gb.RequestContext) (gb.Response, error) {
				middlewareCalls.Add(1)
				if ctx.Request.URL.Path == "/source" {
					response, err := gb.Fetch(ctx.Context, httptest.NewRequest(http.MethodGet, "https://example.com/target", nil))
					if err != nil {
						return gb.Response{Status: http.StatusBadGateway, Body: []byte(err.Error())}, nil
					}
					defer response.Body.Close()
					if response.StatusCode != http.StatusOK {
						return gb.Response{Status: http.StatusBadGateway, Body: []byte("nested status")}, nil
					}
				}
				return next(ctx)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "https://example.com/source", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if got := middlewareCalls.Load(); got != 2 {
		t.Fatalf("middleware calls=%d, want outer and nested fetch", got)
	}
}

func TestFetchFallsBackOnlyWhenRouteIsMissing(t *testing.T) {
	var fallbackCalls atomic.Int32
	server, err := New(Config{
		BuildID:      "build-1",
		PublicOrigin: "https://example.com",
		Pages:        []PageRoute{fetchTestPage("source", "/source")},
		FetchOrigin: gb.FetcherFunc(func(_ context.Context, request *http.Request) (*http.Response, error) {
			fallbackCalls.Add(1)
			response := httptest.NewRecorder()
			response.Header().Set("X-Fallback-Path", request.URL.Path)
			response.WriteHeader(http.StatusNoContent)
			return response.Result(), nil
		}),
		Middleware: func(next gb.Handler) gb.Handler {
			return func(ctx *gb.RequestContext) (gb.Response, error) {
				if ctx.Request.URL.Path == "/source" {
					response, err := gb.Fetch(ctx.Context, httptest.NewRequest(http.MethodGet, "https://example.com/missing", nil))
					if err != nil {
						return gb.Response{Status: http.StatusBadGateway, Body: []byte(err.Error())}, nil
					}
					defer response.Body.Close()
					if response.Header.Get("X-Fallback-Path") != "/missing" {
						return gb.Response{Status: http.StatusBadGateway}, nil
					}
				}
				return next(ctx)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "https://example.com/source", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if fallbackCalls.Load() != 1 {
		t.Fatalf("fallback calls=%d, want one route-miss fallback", fallbackCalls.Load())
	}
}
