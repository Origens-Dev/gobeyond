package listen_test

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Origens-Dev/gobeyond/adapters/listen"
)

// shortSocketPath returns a socket path under /tmp: macOS caps sun_path
// at 104 bytes and t.TempDir() paths can exceed it.
func shortSocketPath(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("/tmp", "gb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	return filepath.Join(directory, "app.sock")
}

func TestParseTarget(t *testing.T) {
	cases := []struct {
		target  string
		network string
		address string
		wantErr bool
	}{
		{"unix:///run/gobeyond/app.sock", "unix", "/run/gobeyond/app.sock", false},
		{"tcp://127.0.0.1:8080", "tcp", "127.0.0.1:8080", false},
		{":8080", "tcp", ":8080", false},
		{"127.0.0.1:9000", "tcp", "127.0.0.1:9000", false},
		{"", "", "", true},
		{"unix://relative.sock", "", "", true},
		{"tcp://", "", "", true},
		{"http://example.com", "", "", true},
	}
	for _, tc := range cases {
		network, address, err := listen.ParseTarget(tc.target)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseTarget(%q) = %q,%q, want error", tc.target, network, address)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseTarget(%q) error: %v", tc.target, err)
			continue
		}
		if network != tc.network || address != tc.address {
			t.Errorf("ParseTarget(%q) = %q,%q, want %q,%q", tc.target, network, address, tc.network, tc.address)
		}
	}
}

func TestIsReservedEnvName(t *testing.T) {
	for name, reserved := range map[string]bool{
		"GOBEYOND_LISTEN": true,
		"gobeyond_listen": true,
		"ORIGENS_TOKEN":   true,
		"AWS_REGION":      true,
		"aws_region":      true,
		"DATABASE_URL":    false,
		"MY_GOBEYOND_X":   false,
	} {
		if got := listen.IsReservedEnvName(name); got != reserved {
			t.Errorf("IsReservedEnvName(%q) = %v, want %v", name, got, reserved)
		}
	}
}

func udsClient(socket string) *http.Client {
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var dialer net.Dialer
				return dialer.DialContext(ctx, "unix", socket)
			},
		},
	}
}

func TestServeContextOverUnixSocket(t *testing.T) {
	socket := shortSocketPath(t)
	listener, err := listen.Listener("unix://" + socket)
	if err != nil {
		t.Fatal(err)
	}

	app := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-App", "yes")
		writer.WriteHeader(http.StatusTeapot)
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- listen.ServeContext(ctx, listener, app) }()

	client := udsClient(socket)

	// Readiness: healthz answers 200 before app routing and independent of
	// the Host header.
	request, err := http.NewRequest(http.MethodGet, "http://placeholder"+listen.HealthzPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "any.host.example"
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("healthz over UDS: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("healthz status = %d, want 200", response.StatusCode)
	}
	if response.Header.Get("X-App") != "" {
		t.Fatal("healthz must be served before app routing")
	}

	// App traffic reaches the handler.
	response, err = client.Get("http://placeholder/some/page")
	if err != nil {
		t.Fatalf("app request over UDS: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusTeapot || response.Header.Get("X-App") != "yes" {
		t.Fatalf("app response = %d %q", response.StatusCode, response.Header.Get("X-App"))
	}

	// Graceful drain: cancellation returns nil (exit 0).
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ServeContext returned %v after drain, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ServeContext did not return after cancellation")
	}
}

func TestListenerRemovesStaleSocket(t *testing.T) {
	socket := shortSocketPath(t)
	first, err := listen.Listener("unix://" + socket)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a crashed predecessor leaving the socket file behind: the
	// file exists but nothing accepts.
	first.(*net.UnixListener).SetUnlinkOnClose(false)
	first.Close()

	second, err := listen.Listener("unix://" + socket)
	if err != nil {
		t.Fatalf("stale socket was not replaced: %v", err)
	}
	second.Close()
}

func TestShutdownGrace(t *testing.T) {
	t.Setenv(listen.EnvShutdownGrace, "45s")
	if got := listen.ShutdownGrace(); got != 45*time.Second {
		t.Fatalf("ShutdownGrace = %v, want 45s", got)
	}
	t.Setenv(listen.EnvShutdownGrace, "not-a-duration")
	if got := listen.ShutdownGrace(); got != listen.DefaultShutdownGrace {
		t.Fatalf("ShutdownGrace fallback = %v, want %v", got, listen.DefaultShutdownGrace)
	}
}
