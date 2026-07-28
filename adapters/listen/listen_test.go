package listen_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"syscall"
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

func TestServeContextSignalsReadinessAfterInstallingHealthHandler(t *testing.T) {
	appSocket := shortSocketPath(t)
	signalSocket := filepath.Join(filepath.Dir(appSocket), "ready.sock")
	signalAddress, err := net.ResolveUnixAddr("unixgram", signalSocket)
	if err != nil {
		t.Fatal(err)
	}
	signalListener, err := net.ListenUnixgram("unixgram", signalAddress)
	if err != nil {
		t.Fatal(err)
	}
	defer signalListener.Close()
	t.Setenv(listen.EnvReadinessNonce, "one-use-proof")
	t.Setenv(listen.EnvReadinessSignal, "unixgram://"+signalSocket)

	listener, err := listen.Listener("unix://" + appSocket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- listen.ServeContext(ctx, listener, http.NotFoundHandler())
	}()

	_ = signalListener.SetReadDeadline(time.Now().Add(time.Second))
	buffer := make([]byte, 128)
	count, _, err := signalListener.ReadFromUnix(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(buffer[:count]); got != "one-use-proof" {
		t.Fatalf("readiness signal = %q", got)
	}

	request, _ := http.NewRequest(http.MethodGet, "http://placeholder"+listen.HealthzPath, nil)
	request.Header.Set(listen.ReadinessNonceHeader, "one-use-proof")
	response, err := udsClient(appSocket).Do(request)
	if err != nil {
		t.Fatalf("immediate post-signal health: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("immediate post-signal health = %d", response.StatusCode)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestServeContextResignalsReadinessOnSIGCONT(t *testing.T) {
	appSocket := shortSocketPath(t)
	signalSocket := filepath.Join(filepath.Dir(appSocket), "resume.sock")
	signalListener, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: signalSocket, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer signalListener.Close()
	t.Setenv(listen.EnvReadinessNonce, "resume-proof")
	t.Setenv(listen.EnvReadinessSignal, "unixgram://"+signalSocket)

	listener, err := listen.Listener("unix://" + appSocket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- listen.ServeContext(ctx, listener, http.NotFoundHandler()) }()
	readReadinessDatagram(t, signalListener, "resume-proof")

	if err := syscall.Kill(os.Getpid(), syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	readReadinessDatagram(t, signalListener, "resume-proof")
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func readReadinessDatagram(t *testing.T, listener *net.UnixConn, want string) {
	t.Helper()
	_ = listener.SetReadDeadline(time.Now().Add(time.Second))
	buffer := make([]byte, 128)
	count, _, err := listener.ReadFromUnix(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(buffer[:count]); got != want {
		t.Fatalf("readiness signal = %q, want %q", got, want)
	}
}

func TestHealthzRequiresOneUseHostedReadinessNonce(t *testing.T) {
	t.Setenv(listen.EnvReadinessNonce, "one-use-proof")
	appCalled := false
	handler := listen.WithHealthz(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		appCalled = true
	}))

	request := httptest.NewRequest(http.MethodGet, listen.HealthzPath, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || appCalled {
		t.Fatalf("unproved health response=%d appCalled=%v", response.Code, appCalled)
	}

	request = httptest.NewRequest(http.MethodGet, listen.HealthzPath, nil)
	request.Header.Set(listen.ReadinessNonceHeader, "wrong")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("wrong proof response=%d", response.Code)
	}

	request = httptest.NewRequest(http.MethodGet, listen.HealthzPath, nil)
	request.Header.Set(listen.ReadinessNonceHeader, "one-use-proof")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("valid proof response=%d", response.Code)
	}

	// Once the startup proof succeeds, ordinary health checks used after
	// pause/resume do not need to retain the secret nonce.
	request = httptest.NewRequest(http.MethodGet, listen.HealthzPath, nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("post-proof health response=%d", response.Code)
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
