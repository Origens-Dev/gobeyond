package oidc

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestTokenPrefersRequestOverEnvironment(t *testing.T) {
	t.Setenv(EnvTokenOrigens, "environment-token")
	request := httptest.NewRequest(http.MethodGet, "https://example.test", nil)
	request.Header.Set(HeaderOrigens, "request-token")
	token, err := (&TokenSource{}).Token(context.Background(), TokenOptions{Request: request})
	if err != nil || token != "request-token" {
		t.Fatalf("token = %q, err = %v", token, err)
	}
}

func TestTokenUsesContextBeforeEnvironment(t *testing.T) {
	t.Setenv(EnvTokenOrigens, "environment-token")
	token, err := (&TokenSource{}).Token(ContextWithToken(context.Background(), "context-token"), TokenOptions{})
	if err != nil || token != "context-token" {
		t.Fatalf("token = %q, err = %v", token, err)
	}
}

func TestTokenUsesSlotBrokerWhenNoRequestOrEnvironment(t *testing.T) {
	t.Setenv(EnvTokenOrigens, "")
	t.Setenv(EnvTokenGoBeyond, "")
	socketPath := "/tmp/gb-oidc-test.sock"
	_ = os.Remove(socketPath)
	t.Cleanup(func() { _ = os.Remove(socketPath) })
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go http.Serve(listener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/oidc/token" || r.Method != http.MethodPost {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"broker-token"}`))
	}))

	token, err := (&TokenSource{SocketPath: socketPath}).Token(context.Background(), TokenOptions{})
	if err != nil || token != "broker-token" {
		t.Fatalf("token = %q, err = %v", token, err)
	}
}

func TestTokenRetriesTransientSlotBrokerConnection(t *testing.T) {
	t.Setenv(EnvTokenOrigens, "")
	t.Setenv(EnvTokenGoBeyond, "")
	socketPath := "/tmp/gb-oidc-retry.sock"
	_ = os.Remove(socketPath)
	t.Cleanup(func() { _ = os.Remove(socketPath) })
	listenerCh := make(chan net.Listener, 1)
	serverErrCh := make(chan error, 1)
	go func() {
		// The first broker dial sees the not-yet-created socket. A healthy broker
		// appearing during the bounded retry window must be usable.
		time.Sleep(75 * time.Millisecond)
		listener, err := net.Listen("unix", socketPath)
		if err != nil {
			serverErrCh <- err
			return
		}
		listenerCh <- listener
		serverErrCh <- http.Serve(listener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"recovered-broker-token"}`))
		}))
	}()

	token, err := (&TokenSource{SocketPath: socketPath}).Token(context.Background(), TokenOptions{})
	if err != nil || token != "recovered-broker-token" {
		t.Fatalf("token = %q, err = %v", token, err)
	}
	select {
	case listener := <-listenerCh:
		_ = listener.Close()
	case err := <-serverErrCh:
		t.Fatalf("broker listener = %v", err)
	case <-time.After(time.Second):
		t.Fatal("broker listener did not start")
	}
}

func TestTokenExchangesAudience(t *testing.T) {
	t.Setenv(EnvTokenOrigens, "source-token")
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/~token" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"exchanged-token"}`))
	}))
	defer issuer.Close()

	token, err := (&TokenSource{IssuerBase: issuer.URL}).Token(context.Background(), TokenOptions{
		Audience: AWSTSAudience,
	})
	if err != nil {
		t.Fatal(err)
	}
	if token != "exchanged-token" {
		t.Fatalf("token = %q", token)
	}
}

func TestTokenExpiry(t *testing.T) {
	if _, ok := TokenExpiry("not-a-jwt"); ok {
		t.Fatal("invalid token reported an expiry")
	}
	if strings.TrimSpace(os.Getenv(EnvTokenOrigens)) != "" {
		t.Fatal("test environment unexpectedly provided a token")
	}
}
