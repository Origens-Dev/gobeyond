package agents

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestPlaybackCompletionSetterCannotBeImportedByApplication(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	mod := "module example.test/application\n\ngo 1.24.5\n\nrequire github.com/Origens-Dev/gobeyond v0.0.0\n\nreplace github.com/Origens-Dev/gobeyond => " + strconv.Quote(root) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(mod), 0600); err != nil {
		t.Fatal(err)
	}
	source := "package application\nimport _ \"github.com/Origens-Dev/gobeyond/agents/internal/playbackcompletion\"\n"
	if err := os.WriteFile(filepath.Join(dir, "application.go"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "list", "-mod=mod", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off")
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "use of internal package github.com/Origens-Dev/gobeyond/agents/internal/playbackcompletion not allowed") {
		t.Fatalf("application import was not rejected at the internal boundary: %v\n%s", err, output)
	}
}
