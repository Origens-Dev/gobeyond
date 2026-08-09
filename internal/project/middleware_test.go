package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverMiddlewareSource(t *testing.T) {
	root := t.TempDir()
	if source, err := DiscoverMiddlewareSource(root); err != nil || source != "" {
		t.Fatalf("empty middleware source = %q, %v", source, err)
	}

	typescript := filepath.Join(root, "middleware.ts")
	if err := os.WriteFile(typescript, []byte("export default () => new Response()\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if source, err := DiscoverMiddlewareSource(root); err != nil || source != typescript {
		t.Fatalf("TypeScript middleware source = %q, %v", source, err)
	}

	javascript := filepath.Join(root, "middleware.js")
	if err := os.WriteFile(javascript, []byte("export default () => new Response()\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := DiscoverMiddlewareSource(root); err == nil || !strings.Contains(err.Error(), "cannot both exist") {
		t.Fatalf("expected duplicate middleware error, got %v", err)
	}
	if err := os.Remove(typescript); err != nil {
		t.Fatal(err)
	}
	if source, err := DiscoverMiddlewareSource(root); err != nil || source != javascript {
		t.Fatalf("JavaScript middleware source = %q, %v", source, err)
	}
}

func TestDiscoverMiddlewareSourceRejectsLegacyLayouts(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "Go root", path: "middleware.go", want: "middleware.go is no longer supported"},
		{name: "Go process", path: filepath.Join("server", "cmd", "middleware"), want: "server/cmd/middleware is no longer supported"},
		{name: "flat edge", path: "edge-middleware.ts", want: "rename it to middleware.ts"},
		{name: "edge directory", path: "edge-middleware", want: "no longer a supported authored layout"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, test.path)
			if filepath.Ext(path) == "" {
				if err := os.MkdirAll(path, 0o755); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("legacy"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := DiscoverMiddlewareSource(root); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q error, got %v", test.want, err)
			}
		})
	}
}
