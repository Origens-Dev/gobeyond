package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGeneratedRuntimeModulesMeetFrameworkGoMinimum(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module fixture.test/site\n\ngo 1.24.0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for requested, want := range map[string]string{"1.24.0": MinimumGoVersion, "1.26.0": MinimumGoVersion, "1.27.0": "1.27.0"} {
		raw, err := routeModule(root, "fixture.test/site", "fixture.test/site", requested, filepath.Join(root, "app/test"), "test")
		if err != nil || !strings.Contains(string(raw), "go "+want+"\n") {
			t.Fatalf("requested%s output=%s err=%v", requested, raw, err)
		}
	}
	if _, err := runtimeModuleGoVersion("not-a-version"); err == nil {
		t.Fatal("invalid language version accepted")
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "\ngo "+MinimumGoVersion+"\n") {
		t.Fatal("generated runtime floor differs from public module")
	}
}
