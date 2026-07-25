package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writePackage creates a package directory with a package.json and any
// entrypoint files listed, returning its path.
func writePackage(t *testing.T, dir, name, version, manifest string, files ...string) string {
	t.Helper()
	packageDir := filepath.Join(dir, name)
	if err := os.MkdirAll(packageDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(packageDir, "package.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		path := filepath.Join(packageDir, filepath.FromSlash(file))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("export {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_ = version
	return packageDir
}

func manifestJSON(name, version, exports string) string {
	return `{"name":"@go-beyond/` + name + `","version":"` + version + `","exports":` + exports + `}`
}

const builtExports = `{".":{"types":"./dist/index.d.ts","import":"./dist/index.js"}}`

// linkScope builds root/node_modules/@go-beyond/<name> symlinks to sources.
func linkScope(t *testing.T, root string, sources map[string]string) {
	t.Helper()
	scope := filepath.Join(root, "node_modules", "@go-beyond")
	if err := os.MkdirAll(scope, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, source := range sources {
		if err := os.Symlink(source, filepath.Join(scope, name)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDoctorInspectsMonorepoPackagesDirectory(t *testing.T) {
	root := t.TempDir()
	for _, name := range linkedPackages {
		version := "0.1.0-alpha.5"
		writePackage(t, filepath.Join(root, "packages"), name, version,
			manifestJSON(name, version, builtExports),
			"dist/index.js", "dist/index.d.ts")
	}

	var out bytes.Buffer
	if err := runDoctor(&out, root); err != nil {
		t.Fatalf("runDoctor error = %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "workspace link") {
		t.Fatalf("workspace packages not reported:\n%s", out.String())
	}
}

func TestDoctorWithoutProjectPackagesSucceeds(t *testing.T) {
	var out bytes.Buffer
	if err := runDoctor(&out, t.TempDir()); err != nil {
		t.Fatalf("runDoctor error = %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "not installed (run pnpm install)") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestDoctorReportsBuiltWorkspacePackages(t *testing.T) {
	workspace := t.TempDir()
	root := t.TempDir()
	sources := map[string]string{}
	for _, name := range linkedPackages {
		sources[name] = writePackage(t, workspace, name, "0.1.0-alpha.5",
			manifestJSON(name, "0.1.0-alpha.5", builtExports),
			"dist/index.js", "dist/index.d.ts")
	}
	linkScope(t, root, sources)

	var out bytes.Buffer
	if err := runDoctor(&out, root); err != nil {
		t.Fatalf("runDoctor error = %v\n%s", err, out.String())
	}
	output := out.String()
	for _, name := range linkedPackages {
		if !strings.Contains(output, "@go-beyond/"+name+": 0.1.0-alpha.5") {
			t.Fatalf("missing report for %s:\n%s", name, output)
		}
	}
	if !strings.Contains(output, "workspace link") {
		t.Fatalf("workspace link not detected:\n%s", output)
	}
}

func TestDoctorFailsOnMissingCompiledEntrypoint(t *testing.T) {
	workspace := t.TempDir()
	root := t.TempDir()
	sources := map[string]string{}
	for _, name := range linkedPackages {
		files := []string{"dist/index.js", "dist/index.d.ts"}
		if name == "compiler" {
			files = nil // simulate a package that was never built
		}
		sources[name] = writePackage(t, workspace, name, "0.1.0-alpha.5",
			manifestJSON(name, "0.1.0-alpha.5", builtExports), files...)
	}
	linkScope(t, root, sources)

	var out bytes.Buffer
	err := runDoctor(&out, root)
	if err == nil {
		t.Fatalf("runDoctor succeeded for an unbuilt package:\n%s", out.String())
	}
	output := out.String()
	if !strings.Contains(output, "pnpm --filter @go-beyond/compiler build") {
		t.Fatalf("missing actionable fix:\n%s", output)
	}
	if !strings.Contains(output, "dist/index.js") {
		t.Fatalf("missing entrypoint name:\n%s", output)
	}
	if strings.Contains(output, "ERR_MODULE_NOT_FOUND") {
		t.Fatalf("raw module error leaked:\n%s", output)
	}
}

func TestDoctorFlagsBinEntrypointsAndVersionSkew(t *testing.T) {
	workspace := t.TempDir()
	root := t.TempDir()
	sources := map[string]string{}
	for _, name := range linkedPackages {
		version := "0.1.0-alpha.5"
		if name == "vite" {
			version = "0.1.0-alpha.4"
		}
		manifest := manifestJSON(name, version, builtExports)
		files := []string{"dist/index.js", "dist/index.d.ts"}
		if name == "compiler" {
			manifest = `{"name":"@go-beyond/compiler","version":"` + version +
				`","bin":{"gobeyond-compile":"./dist/src/cli.js"},"exports":{".":{"import":"./dist/src/index.js"}}}`
			files = []string{"dist/src/index.js", "dist/src/cli.js"}
		}
		sources[name] = writePackage(t, workspace, name, version, manifest, files...)
	}
	linkScope(t, root, sources)

	var out bytes.Buffer
	err := runDoctor(&out, root)
	if err == nil {
		t.Fatalf("runDoctor succeeded despite version skew:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "skewed versions") {
		t.Fatalf("skew not reported:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "0.1.0-alpha.4") {
		t.Fatalf("skewed version not named:\n%s", out.String())
	}
}

func TestDoctorFailsOnMissingPackage(t *testing.T) {
	workspace := t.TempDir()
	root := t.TempDir()
	sources := map[string]string{}
	for _, name := range linkedPackages {
		if name == "schema" {
			continue
		}
		sources[name] = writePackage(t, workspace, name, "0.1.0-alpha.5",
			manifestJSON(name, "0.1.0-alpha.5", builtExports),
			"dist/index.js", "dist/index.d.ts")
	}
	linkScope(t, root, sources)

	var out bytes.Buffer
	if err := runDoctor(&out, root); err == nil {
		t.Fatalf("runDoctor succeeded with a missing package:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "@go-beyond/schema: not installed") {
		t.Fatalf("missing package not reported:\n%s", out.String())
	}
}

func TestEntrypointFilesIgnoresWildcards(t *testing.T) {
	manifest, err := readPackageManifestFromString(`{"name":"x","version":"1","exports":{"./*":"./dist/*.js",".":"./dist/index.js"},"main":"./dist/index.js"}`)
	if err != nil {
		t.Fatal(err)
	}
	files := manifest.entrypointFiles()
	if len(files) != 1 || files[0] != "./dist/index.js" {
		t.Fatalf("entrypointFiles = %v", files)
	}
}

func readPackageManifestFromString(data string) (packageManifest, error) {
	dir, err := os.MkdirTemp("", "manifest")
	if err != nil {
		return packageManifest{}, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "package.json")
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		return packageManifest{}, err
	}
	return readPackageManifest(path)
}
