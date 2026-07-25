package cache

import (
	"net/http"
	"testing"
)

func TestIsPrivateRequest(t *testing.T) {
	tests := []struct {
		name   string
		header http.Header
		want   bool
	}{
		{"empty", http.Header{}, false},
		{"cookie", http.Header{"Cookie": {"session=1"}}, true},
		{"authorization", http.Header{"Authorization": {"Bearer x"}}, true},
		{"auth-context", http.Header{"X-Gobeyond-Auth-Context": {"eyJ0ZXN0Ijp0cnVlfQ"}}, true},
		{"oidc-token", http.Header{"X-Origens-Oidc-Token": {"token"}}, true},
		{"unrelated header", http.Header{"X-Request-Id": {"abc"}}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsPrivateRequest(test.header); got != test.want {
				t.Fatalf("IsPrivateRequest(%v) = %v, want %v", test.header, got, test.want)
			}
		})
	}
}

func TestIsPrivateRequestNilHeader(t *testing.T) {
	if IsPrivateRequest(nil) {
		t.Fatal("nil header must not be private")
	}
}

func TestIsPrivateResponse(t *testing.T) {
	tests := []struct {
		name     string
		request  http.Header
		response http.Header
		want     bool
	}{
		{"neither", http.Header{}, http.Header{}, false},
		{"private request", http.Header{"Cookie": {"a=1"}}, http.Header{}, true},
		{"set-cookie response", http.Header{}, http.Header{"Set-Cookie": {"a=1"}}, true},
		{"both", http.Header{"Authorization": {"Bearer x"}}, http.Header{"Set-Cookie": {"a=1"}}, true},
		{"auth-context request only", http.Header{"X-Gobeyond-Auth-Context": {"ctx"}}, http.Header{}, true},
		{"oidc request only", http.Header{"X-Origens-Oidc-Token": {"tok"}}, http.Header{}, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsPrivateResponse(test.request, test.response); got != test.want {
				t.Fatalf("IsPrivateResponse(%v, %v) = %v, want %v", test.request, test.response, got, test.want)
			}
		})
	}
}
