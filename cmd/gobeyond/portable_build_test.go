package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDevelopmentPublicConfigurationContract(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "gobeyond.json"), []byte(`{"publicRuntime":["KEY","DOMAIN"],"requiredPublicRuntime":["KEY"]}`), 0600); err != nil {
		t.Fatal(err)
	}
	first, err := developmentPublicConfig(root, []string{"KEY=</script>", "SECRET=hidden"})
	if err != nil {
		t.Fatal(err)
	}
	var public map[string]string
	if err := json.Unmarshal([]byte(strings.TrimPrefix(first[0], "GOBEYOND_PUBLIC_CONFIG=")), &public); err != nil {
		t.Fatal(err)
	}
	if len(public) != 1 || public["KEY"] != "</script>" {
		t.Fatalf("unexpected public config: %v", public)
	}
	second, err := developmentPublicConfig(root, []string{"KEY=</script>", "SECRET=changed"})
	if err != nil || first[1] != second[1] {
		t.Fatalf("private config changed public revision: %v", err)
	}
	changed, err := developmentPublicConfig(root, []string{"KEY=another"})
	if err != nil || first[1] == changed[1] {
		t.Fatalf("public config did not change revision: %v", err)
	}
	if _, err := developmentPublicConfig(root, []string{"KEY= "}); err == nil {
		t.Fatal("accepted blank required setting")
	}
}

func TestRequiredPublicConfigurationMustBeUniqueAndPublic(t *testing.T) {
	for _, content := range []string{
		`{"publicRuntime":["KEY"],"requiredPublicRuntime":["SECRET"]}`,
		`{"publicRuntime":["KEY"],"requiredPublicRuntime":["KEY","KEY"]}`,
	} {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "gobeyond.json"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readPortableBuildConfig(root); err == nil {
			t.Fatal("accepted invalid required settings")
		}
	}
}
