package runtime

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Origens-Dev/gobeyond/policy"
)

func TestProxyPolicyHandlerRunsBeforeStaticAndRewrites(t *testing.T) {
	data, proxyPolicy, err := policy.Compile(policy.Config{
		Rewrites: []policy.Rule{{Source: "/old", Destination: "/new"}},
	}, "build-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || proxyPolicy == nil {
		t.Fatal("policy compile returned no artifact")
	}
	calledPath := ""
	handler := ProxyPolicyHandler(proxyPolicy, http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		calledPath = request.URL.Path
	}))
	request := httptest.NewRequest(http.MethodGet, "https://example.test/old", nil)
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if calledPath != "/new" {
		t.Fatalf("rewritten path = %q, want /new", calledPath)
	}
}

func TestProxyPolicyHandlerRedirectsAndSkipsReservedPaths(t *testing.T) {
	_, proxyPolicy, err := policy.Compile(policy.Config{
		Redirects: []policy.Rule{{Source: "/old", Destination: "/new", Status: http.StatusPermanentRedirect}},
	}, "build-1")
	if err != nil {
		t.Fatal(err)
	}
	nextCalls := 0
	handler := ProxyPolicyHandler(proxyPolicy, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		nextCalls++
	}))
	redirectResponse := httptest.NewRecorder()
	handler.ServeHTTP(redirectResponse, httptest.NewRequest(http.MethodGet, "https://example.test/old", nil))
	if redirectResponse.Code != http.StatusPermanentRedirect || redirectResponse.Header().Get("Location") != "/new" {
		t.Fatalf("redirect = %d %q", redirectResponse.Code, redirectResponse.Header().Get("Location"))
	}
	reservedResponse := httptest.NewRecorder()
	handler.ServeHTTP(reservedResponse, httptest.NewRequest(http.MethodGet, "https://example.test/__gobeyond/healthz", nil))
	if nextCalls != 1 {
		t.Fatalf("reserved path next calls = %d, want 1", nextCalls)
	}
}
