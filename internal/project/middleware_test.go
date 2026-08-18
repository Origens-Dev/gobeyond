package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverGoMiddleware(t *testing.T) {
	root := t.TempDir()
	if source, err := DiscoverGoMiddleware(root); err != nil || source != "" {
		t.Fatalf("missing middleware: source=%q err=%v", source, err)
	}
	writeMiddlewareTestFile(t, root, `package middleware

import gb "github.com/Origens-Dev/gobeyond"

func Middleware(next gb.Handler) gb.Handler { return next }
`)
	source, err := DiscoverGoMiddleware(root)
	if err != nil || source != filepath.Join(root, "middleware.go") {
		t.Fatalf("valid middleware: source=%q err=%v", source, err)
	}
}

func TestDiscoverGoMiddlewareRejectsInvalidContracts(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "wrong package",
			body: `package main
import gb "github.com/Origens-Dev/gobeyond"
func Middleware(next gb.Handler) gb.Handler { return next }
`,
			want: "package middleware",
		},
		{
			name: "wrong signature",
			body: `package middleware
import gb "github.com/Origens-Dev/gobeyond"
func Middleware(next gb.Handler) error { return nil }
`,
			want: "signature",
		},
		{
			name: "missing function",
			body: `package middleware
import gb "github.com/Origens-Dev/gobeyond"
var _ gb.Handler
`,
			want: "must declare Middleware",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeMiddlewareTestFile(t, root, test.body)
			if _, err := DiscoverGoMiddleware(root); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v, want substring %q", err, test.want)
			}
		})
	}
}

func TestDiscoverMiddlewareSourceRejectsJavaScriptContracts(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "middleware.ts"), []byte("export default () => fetch(request)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := DiscoverMiddlewareSource(root); err == nil || !strings.Contains(err.Error(), "middleware.go") {
		t.Fatalf("error=%v, want migration error", err)
	}
}

func writeMiddlewareTestFile(t *testing.T, root, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "middleware.go"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
