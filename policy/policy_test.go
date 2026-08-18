package policy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCompileParseAndApply(t *testing.T) {
	data, _, err := Compile(Config{
		Redirects: []Rule{{Source: "/old/:slug", Destination: "/new/:slug", Status: http.StatusPermanentRedirect}},
		Rewrites: []Rule{{
			Source:      "/docs/[...path]",
			Destination: "/guide/[...path]?from=docs",
			Methods:     []string{http.MethodGet},
			Headers:     map[string]string{"X-Mode": "preview"},
		}},
	}, "build-1")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Parse(data, "build-1")
	if err != nil {
		t.Fatal(err)
	}
	redirect, err := loaded.Apply(httptest.NewRequest(http.MethodGet, "https://example.com/old/widget", nil))
	if err != nil || redirect.Kind != DecisionRedirect || redirect.Location != "/new/widget" || redirect.Status != http.StatusPermanentRedirect {
		t.Fatalf("redirect=%+v err=%v", redirect, err)
	}
	rewriteRequest := httptest.NewRequest(http.MethodGet, "https://example.com/docs/start/here?keep=yes", nil)
	rewriteRequest.Header.Set("X-Mode", "preview")
	rewrite, err := loaded.Apply(rewriteRequest)
	if err != nil || rewrite.Kind != DecisionRewrite || rewrite.RewriteURL == nil {
		t.Fatalf("rewrite=%+v err=%v", rewrite, err)
	}
	if got := rewrite.RewriteURL.RequestURI(); got != "/guide/start/here?from=docs" {
		t.Fatalf("rewritten URI=%q", got)
	}
}

func TestPolicyConditionsAndDigest(t *testing.T) {
	data, _, err := Compile(Config{Rewrites: []Rule{{
		Source:      "/private/[id]",
		Destination: "/account/[id]",
		Methods:     []string{http.MethodPost},
		Hosts:       []string{"*.example.com"},
		Cookies:     map[string]string{"session": "*"},
		Query:       map[string]string{"mode": "preview"},
	}}}, "build-2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(append([]byte{}, data...), "wrong-build"); err == nil || !strings.Contains(err.Error(), "build ID mismatch") {
		t.Fatalf("wrong build accepted: %v", err)
	}
	tampered := []byte(strings.Replace(string(data), `"digest": "`, `"digest": "0`, 1))
	if _, err := Parse(tampered, "build-2"); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("tampered policy accepted: %v", err)
	}
}

func TestPolicyRejectsUnsafeDestinations(t *testing.T) {
	if _, _, err := Compile(Config{Rewrites: []Rule{{Source: "/old", Destination: "https://evil.example"}}}, "build-1"); err == nil {
		t.Fatal("external rewrite accepted")
	}
	if _, _, err := Compile(Config{Redirects: []Rule{{Source: "/old", Destination: "http://evil.example", Status: http.StatusFound}}}, "build-1"); err == nil {
		t.Fatal("insecure redirect accepted")
	}
}
