package security

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReservedHeadersAreStripped(t *testing.T) {
	header := http.Header{
		"X-GoBeyond-Build": {"forged"},
		"Authorization":    {"allowed"},
	}
	StripReservedHeaders(header)
	if header.Get("X-GoBeyond-Build") != "" || header.Get("Authorization") != "allowed" {
		t.Fatalf("unexpected filtered headers: %#v", header)
	}
}

func TestCSRFVerification(t *testing.T) {
	csrf, err := NewCSRF([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	token, err := csrf.Token()
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "https://example.com/_gobeyond/builds/build/actions/save", nil)
	request.Header.Set("Origin", "https://example.com")
	request.Header.Set(CSRFHeader, token)
	request.AddCookie(csrf.Cookie(token))
	if err := csrf.Verify(request, "https://example.com"); err != nil {
		t.Fatalf("expected valid token: %v", err)
	}

	request.Header.Set(CSRFHeader, token+"x")
	if err := csrf.Verify(request, "https://example.com"); err == nil {
		t.Fatal("expected modified token to fail")
	}
}
