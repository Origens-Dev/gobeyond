package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTargetFingerprintTracksTransitiveSourceEmbedsAndPlatform(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.test/targets\n\ngo 1.24\n")
	write("one/main.go", "package main\nimport _ \"example.test/targets/shared\"\nfunc main(){}\n")
	write("two/main.go", "package main\nfunc main(){}\n")
	write("shared/shared.go", "package shared\nimport _ \"embed\"\n//go:embed data.txt\nvar Data string\n")
	write("shared/data.txt", "first")
	write("shared/platform_linux.go", "package shared\nconst Platform=1\n")
	write("shared/platform_windows.go", "package shared\nconst Platform=2\n")
	fingerprint := func(target string) string {
		g, e := loadFingerprintGraph(root, []string{"./one", "./two"})
		if e != nil {
			t.Fatal(e)
		}
		d, e := g.digest(filepath.Join(root, target), nil, nil)
		if e != nil {
			t.Fatal(e)
		}
		return d
	}
	one, two := fingerprint("one"), fingerprint("two")
	write("unrelated/page.tsx", "different web source")
	if fingerprint("one") != one || fingerprint("two") != two {
		t.Fatal("unrelated source invalidated workers")
	}
	write("shared/platform_windows.go", "package shared\nconst Platform=3\n")
	if fingerprint("one") != one {
		t.Fatal("inactive platform invalidated target")
	}
	write("shared/data.txt", "second")
	if fingerprint("one") == one || fingerprint("two") != two {
		t.Fatal("embedded dependency invalidation incorrect")
	}
	one = fingerprint("one")
	write("shared/platform_linux.go", "package shared\nconst Platform=4\n")
	if fingerprint("one") == one || fingerprint("two") != two {
		t.Fatal("transitive source invalidation incorrect")
	}
	write("one/main.go", "package main\nimport _ \"example.test/targets/missing\"\nfunc main(){}\n")
	g, e := loadFingerprintGraph(root, []string{"./one"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = g.digest(filepath.Join(root, "one"), nil, nil); e == nil {
		t.Fatal("incomplete graph allowed reuse")
	}
}
