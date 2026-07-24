// Package listen implements the hosted supervisor <-> tenant listen
// contract (gobeyond-internal data-plane contracts §6). A listen-mode
// server binary:
//
//   - reads GOBEYOND_LISTEN, a URL selecting the ingress socket:
//     unix:///run/gobeyond/app.sock (hosted) or tcp://127.0.0.1:8080
//     (local dev). When GOBEYOND_LISTEN is unset, Serve falls back to a
//     TCP listener on GOBEYOND_ADDR (default ":8080") so existing local
//     workflows keep working;
//   - answers GET /_gobeyond/healthz with 200 before app routing and
//     independent of the Host header (readiness = socket accepts AND
//     healthz returns 200);
//   - on SIGTERM stops accepting, drains in-flight requests up to
//     GOBEYOND_SHUTDOWN_GRACE (duration string, default 20s), then
//     returns nil so the process exits 0.
//
// The Lambda adapter (adapters/lambda) is unaffected; listen mode is a
// separate cmd.
package listen

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const (
	// EnvListen selects the ingress socket: unix://<absolute path> or
	// tcp://<host:port>.
	EnvListen = "GOBEYOND_LISTEN"
	// EnvAddr is the legacy local-dev TCP address consulted only when
	// GOBEYOND_LISTEN is unset.
	EnvAddr = "GOBEYOND_ADDR"
	// EnvShutdownGrace is the graceful-drain budget injected by the
	// supervisor (Go duration string).
	EnvShutdownGrace = "GOBEYOND_SHUTDOWN_GRACE"
	// HealthzPath is the readiness endpoint served before app routing.
	HealthzPath = "/_gobeyond/healthz"
	// DefaultShutdownGrace applies when GOBEYOND_SHUTDOWN_GRACE is unset
	// or unparseable.
	DefaultShutdownGrace = 20 * time.Second
)

// ReservedEnvPrefixes are environment-variable name prefixes owned by the
// platform. Customer variable names beginning with any of these are
// rejected at write time by the control plane; supervisors inject only
// platform-defined variables under them.
var ReservedEnvPrefixes = []string{"GOBEYOND_", "ORIGENS_", "AWS_"}

// IsReservedEnvName reports whether name collides with the reserved
// platform prefixes (compared case-insensitively).
func IsReservedEnvName(name string) bool {
	upper := strings.ToUpper(strings.TrimSpace(name))
	for _, prefix := range ReservedEnvPrefixes {
		if strings.HasPrefix(upper, prefix) {
			return true
		}
	}
	return false
}

// ParseTarget splits a GOBEYOND_LISTEN / GOBEYOND_UPSTREAM value into a
// net network and address. Accepted forms: unix://<absolute path>,
// tcp://<host:port>, or a bare <host:port> (legacy GOBEYOND_ADDR form).
func ParseTarget(target string) (network, address string, err error) {
	target = strings.TrimSpace(target)
	switch {
	case target == "":
		return "", "", errors.New("listen target is empty")
	case strings.HasPrefix(target, "unix://"):
		path := strings.TrimPrefix(target, "unix://")
		if !strings.HasPrefix(path, "/") {
			return "", "", fmt.Errorf("unix listen target must use an absolute path: %q", target)
		}
		return "unix", path, nil
	case strings.HasPrefix(target, "tcp://"):
		address := strings.TrimPrefix(target, "tcp://")
		if address == "" {
			return "", "", fmt.Errorf("tcp listen target is missing an address: %q", target)
		}
		return "tcp", address, nil
	case strings.Contains(target, "://"):
		return "", "", fmt.Errorf("unsupported listen scheme (want unix:// or tcp://): %q", target)
	default:
		return "tcp", target, nil
	}
}

// Listener opens the socket described by target. Unix sockets remove a
// stale socket file first. The socket file is created world-connectable:
// the per-instance socket directory (bind-mounted by the supervisor,
// mode 0700) is the access boundary.
func Listener(target string) (net.Listener, error) {
	network, address, err := ParseTarget(target)
	if err != nil {
		return nil, err
	}
	if network != "unix" {
		return net.Listen(network, address)
	}
	if err := os.Remove(address); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("remove stale socket %s: %w", address, err)
	}
	listener, err := net.Listen("unix", address)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(address, 0o666); err != nil {
		listener.Close()
		return nil, fmt.Errorf("chmod socket %s: %w", address, err)
	}
	return listener, nil
}

// WithHealthz answers GET/HEAD /_gobeyond/healthz before app routing and
// independent of the Host header; everything else reaches next.
func WithHealthz(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != HealthzPath {
			next.ServeHTTP(writer, request)
			return
		}
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			writer.Header().Set("Allow", http.MethodGet)
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.Header().Set("Cache-Control", "no-store")
		_, _ = writer.Write([]byte(`{"ok":true}`))
	})
}

// ShutdownGrace returns the supervisor-injected drain budget, falling
// back to DefaultShutdownGrace when unset or unparseable.
func ShutdownGrace() time.Duration {
	value := os.Getenv(EnvShutdownGrace)
	if value == "" {
		return DefaultShutdownGrace
	}
	grace, err := time.ParseDuration(value)
	if err != nil || grace <= 0 {
		return DefaultShutdownGrace
	}
	return grace
}

// Serve runs handler per the listen contract: socket from GOBEYOND_LISTEN
// (falling back to a TCP GOBEYOND_ADDR listener, default ":8080"),
// readiness endpoint, and SIGTERM graceful drain. It returns nil after a
// clean drain so main can exit 0.
func Serve(handler http.Handler) error {
	target := os.Getenv(EnvListen)
	if target == "" {
		target = os.Getenv(EnvAddr)
		if target == "" {
			target = ":8080"
		}
	}
	listener, err := Listener(target)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return ServeContext(ctx, listener, handler)
}

// ServeContext serves handler on listener until ctx is cancelled, then
// drains in-flight requests up to ShutdownGrace before returning nil.
func ServeContext(ctx context.Context, listener net.Listener, handler http.Handler) error {
	server := &http.Server{
		Handler:           WithHealthz(handler),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	select {
	case err := <-served:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		drainCtx, cancel := context.WithTimeout(context.Background(), ShutdownGrace())
		defer cancel()
		// Drain up to the grace budget; a deadline overrun still returns
		// nil because the supervisor escalates to SIGKILL itself (§6.3).
		_ = server.Shutdown(drainCtx)
		return nil
	}
}
