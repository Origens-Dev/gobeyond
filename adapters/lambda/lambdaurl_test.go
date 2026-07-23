package lambdaurl_test

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-lambda-go/events"
	lambdaurl "github.com/Origens-Dev/gobeyond/adapters/lambda"
)

func TestDispatchRoundTrip(t *testing.T) {
	t.Parallel()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/api/ping" {
			t.Errorf("path = %q, want /api/ping", r.URL.Path)
		}
		if got := r.URL.Query().Get("n"); got != "1" {
			t.Errorf("query n = %q, want 1", got)
		}
		if got := r.Header.Get("X-Test"); got != "yes" {
			t.Errorf("X-Test = %q, want yes", got)
		}
		if got := r.Host; got != "app.example.com" {
			t.Errorf("Host = %q, want app.example.com", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if string(body) != `{"ok":true}` {
			t.Errorf("body = %q", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"pong":true}`))
	})

	resp, err := lambdaurl.Dispatch(context.Background(), handler, events.LambdaFunctionURLRequest{
		RawPath: "/api/ping",
		Headers: map[string]string{
			"host":   "app.example.com",
			"x-test": "yes",
		},
		QueryStringParameters: map[string]string{"n": "1"},
		Body:                  `{"ok":true}`,
		RequestContext: events.LambdaFunctionURLRequestContext{
			HTTP: events.LambdaFunctionURLRequestContextHTTPDescription{Method: http.MethodPost},
		},
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("status = %d, want 201", resp.StatusCode)
	}
	if resp.Headers["Content-Type"] != "application/json" {
		t.Errorf("Content-Type = %q", resp.Headers["Content-Type"])
	}
	if resp.Body != `{"pong":true}` {
		t.Errorf("body = %q", resp.Body)
	}
	if resp.IsBase64Encoded {
		t.Error("expected utf-8 body not base64")
	}
}

func TestDispatchBase64Body(t *testing.T) {
	t.Parallel()

	raw := []byte{0xff, 0xfe, 0x00}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if string(body) != string(raw) {
			t.Errorf("decoded body mismatch")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(raw)
	})

	resp, err := lambdaurl.Dispatch(context.Background(), handler, events.LambdaFunctionURLRequest{
		RawPath:         "/",
		Body:            base64.StdEncoding.EncodeToString(raw),
		IsBase64Encoded: true,
		RequestContext: events.LambdaFunctionURLRequestContext{
			HTTP: events.LambdaFunctionURLRequestContextHTTPDescription{Method: http.MethodGet},
		},
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if !resp.IsBase64Encoded {
		t.Fatal("expected base64 response body")
	}
	decoded, err := base64.StdEncoding.DecodeString(resp.Body)
	if err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if string(decoded) != string(raw) {
		t.Errorf("response body mismatch")
	}
}

func TestDispatchViewerHostOverride(t *testing.T) {
	t.Parallel()

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "example.com" {
			t.Errorf("Host = %q, want example.com", r.Host)
		}
		w.WriteHeader(http.StatusOK)
	})

	_, err := lambdaurl.Dispatch(context.Background(), handler, events.LambdaFunctionURLRequest{
		RawPath: "/",
		Headers: map[string]string{
			"host":                   "lambda-url.amazonaws.com",
			"x-gobeyond-viewer-host": "example.com",
		},
		RequestContext: events.LambdaFunctionURLRequestContext{
			HTTP: events.LambdaFunctionURLRequestContextHTTPDescription{Method: http.MethodGet},
		},
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
}

func TestDispatchBadBase64(t *testing.T) {
	t.Parallel()

	resp, err := lambdaurl.Dispatch(context.Background(), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler should not run")
	}), events.LambdaFunctionURLRequest{
		RawPath:         "/",
		Body:            "%%%",
		IsBase64Encoded: true,
		RequestContext: events.LambdaFunctionURLRequestContext{
			HTTP: events.LambdaFunctionURLRequestContextHTTPDescription{Method: http.MethodGet},
		},
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	if !strings.Contains(resp.Body, "bad request") {
		t.Errorf("body = %q", resp.Body)
	}
}
