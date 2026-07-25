package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// reactCompatibility is the React version this CLI's hydration runtime is
// pinned against. Keep it in sync with packages/react's peerDependencies.
const reactCompatibility = "19.2.8"

// linkedPackages are the workspace packages a project consumes from
// @go-beyond. Every one that resolves must expose a built exports entrypoint.
var linkedPackages = []string{"react", "schema", "compiler", "vite"}

// doctor prints toolchain information and, when run from a project (or the
// monorepo) root, verifies the linked @go-beyond packages are installed, built,
// and on one release line. It returns an error when any check fails, so CI can
// rely on the exit code.
func doctor(root string) error {
	return runDoctor(os.Stdout, root)
}

func runDoctor(out io.Writer, root string) error {
	var failures []string

	fmt.Fprintln(out, "go:", runtime.Version())
	for _, command := range []string{"node", "pnpm"} {
		path, err := exec.LookPath(command)
		if err != nil {
			fmt.Fprintf(out, "%s: missing\n", command)
			failures = append(failures, fmt.Sprintf("%s is not on PATH; install it before running gobeyond", command))
			continue
		}
		fmt.Fprintf(out, "%s: %s\n", command, path)
	}
	fmt.Fprintln(out, "react compatibility:", reactCompatibility)

	reports := inspectLinkedPackages(root)
	if len(reports) == 0 {
		fmt.Fprintln(out, "@go-beyond packages: not installed (run pnpm install)")
		return summarize(out, failures)
	}
	for _, report := range reports {
		fmt.Fprintln(out, report.line())
		failures = append(failures, report.failures...)
	}
	failures = append(failures, versionSkew(reports)...)
	return summarize(out, failures)
}

func summarize(out io.Writer, failures []string) error {
	if len(failures) == 0 {
		return nil
	}
	fmt.Fprintln(out)
	for _, failure := range failures {
		fmt.Fprintln(out, "problem:", failure)
	}
	return fmt.Errorf("doctor found %d problem(s)", len(failures))
}

// packageReport is one resolved @go-beyond/<name> installation.
type packageReport struct {
	name        string
	resolved    string
	version     string
	workspace   bool
	missing     bool
	entrypoints []string
	failures    []string
}

func (report packageReport) line() string {
	label := "@go-beyond/" + report.name
	if report.missing {
		return fmt.Sprintf("%s: not installed", label)
	}
	suffix := ""
	if report.workspace {
		suffix = " (workspace link)"
	}
	status := "ok"
	if len(report.failures) > 0 {
		status = "unbuilt"
	}
	return fmt.Sprintf("%s: %s %s%s [%s]", label, report.version, report.resolved, suffix, status)
}

// inspectLinkedPackages resolves each @go-beyond package from root's
// node_modules, following symlinks, and verifies its exports entrypoints
// exist on disk. When node_modules/@go-beyond is absent but this is the
// GoBeyond monorepo (packages/<name>/package.json), it inspects those source
// packages instead so `gobeyond doctor` works from the workspace root under
// pnpm, which does not hoist workspace packages into the root node_modules.
func inspectLinkedPackages(root string) []packageReport {
	if root == "" {
		return nil
	}
	scope := filepath.Join(root, "node_modules", "@go-beyond")
	if info, err := os.Stat(scope); err == nil && info.IsDir() {
		reports := make([]packageReport, 0, len(linkedPackages))
		for _, name := range linkedPackages {
			reports = append(reports, inspectPackage(root, scope, name))
		}
		return reports
	}
	return inspectWorkspacePackages(root)
}

// inspectWorkspacePackages looks at packages/<name> when this tree is the
// GoBeyond monorepo. It returns nil when none of the linked packages are
// present as source directories.
func inspectWorkspacePackages(root string) []packageReport {
	reports := make([]packageReport, 0, len(linkedPackages))
	found := false
	for _, name := range linkedPackages {
		packageDir := filepath.Join(root, "packages", name)
		manifestPath := filepath.Join(packageDir, "package.json")
		if _, err := os.Stat(manifestPath); err != nil {
			continue
		}
		found = true
		report := packageReport{name: name, resolved: packageDir, workspace: true}
		manifest, err := readPackageManifest(manifestPath)
		if err != nil {
			report.failures = append(report.failures, fmt.Sprintf("@go-beyond/%s package.json is unreadable (%v)", name, err))
			reports = append(reports, report)
			continue
		}
		report.version = manifest.Version
		report.entrypoints = manifest.entrypointFiles()
		var missing []string
		for _, entrypoint := range report.entrypoints {
			if _, err := os.Stat(filepath.Join(packageDir, filepath.FromSlash(entrypoint))); err != nil {
				missing = append(missing, entrypoint)
			}
		}
		if len(missing) > 0 {
			report.failures = append(report.failures, fmt.Sprintf(
				"@go-beyond/%s is not built: missing %s. Run `pnpm --filter @go-beyond/%s build` (or `pnpm build:packages` from the workspace root)",
				name, strings.Join(missing, ", "), name))
		}
		reports = append(reports, report)
	}
	if !found {
		return nil
	}
	// Report missing workspace packages that the monorepo is expected to have.
	present := map[string]struct{}{}
	for _, report := range reports {
		present[report.name] = struct{}{}
	}
	for _, name := range linkedPackages {
		if _, ok := present[name]; ok {
			continue
		}
		reports = append(reports, packageReport{
			name:     name,
			missing:  true,
			failures: []string{fmt.Sprintf("@go-beyond/%s is missing from packages/; expected in the GoBeyond monorepo", name)},
		})
	}
	sort.Slice(reports, func(i, j int) bool { return reports[i].name < reports[j].name })
	return reports
}

func inspectPackage(root, scope, name string) packageReport {
	report := packageReport{name: name}
	linkPath := filepath.Join(scope, name)
	if _, err := os.Stat(linkPath); err != nil {
		report.missing = true
		report.failures = append(report.failures, fmt.Sprintf("@go-beyond/%s is not installed; run pnpm install", name))
		return report
	}
	resolved, err := filepath.EvalSymlinks(linkPath)
	if err != nil {
		report.missing = true
		report.failures = append(report.failures, fmt.Sprintf("@go-beyond/%s link is broken (%v); run pnpm install", name, err))
		return report
	}
	report.resolved = resolved
	report.workspace = isWorkspaceLink(root, linkPath, resolved)

	manifest, err := readPackageManifest(filepath.Join(resolved, "package.json"))
	if err != nil {
		report.failures = append(report.failures, fmt.Sprintf("@go-beyond/%s package.json is unreadable (%v)", name, err))
		return report
	}
	report.version = manifest.Version
	report.entrypoints = manifest.entrypointFiles()

	var missing []string
	for _, entrypoint := range report.entrypoints {
		if _, err := os.Stat(filepath.Join(resolved, filepath.FromSlash(entrypoint))); err != nil {
			missing = append(missing, entrypoint)
		}
	}
	if len(missing) > 0 {
		report.failures = append(report.failures, fmt.Sprintf(
			"@go-beyond/%s is not built: missing %s. Run `pnpm --filter @go-beyond/%s build` (or `pnpm build:packages` from the workspace root)",
			name, strings.Join(missing, ", "), name))
	}
	return report
}

// isWorkspaceLink reports whether the package resolved outside the project's
// node_modules, which is what a workspace/file: link looks like after
// realpath. Those are the installs whose versions can silently skew.
func isWorkspaceLink(root, linkPath, resolved string) bool {
	if info, err := os.Lstat(linkPath); err == nil && info.Mode()&os.ModeSymlink != 0 {
		nodeModules := filepath.Join(root, "node_modules")
		relative, err := filepath.Rel(nodeModules, resolved)
		return err != nil || strings.HasPrefix(relative, "..")
	}
	return false
}

// versionSkew flags linked @go-beyond packages that are not all on the same
// release line. Published installs pin one line; a workspace checkout can
// drift when only some packages were version-bumped.
func versionSkew(reports []packageReport) []string {
	versions := map[string][]string{}
	for _, report := range reports {
		if report.missing || report.version == "" {
			continue
		}
		versions[report.version] = append(versions[report.version], "@go-beyond/"+report.name)
	}
	if len(versions) < 2 {
		return nil
	}
	lines := make([]string, 0, len(versions))
	for version, names := range versions {
		sort.Strings(names)
		lines = append(lines, fmt.Sprintf("%s at %s", strings.Join(names, ", "), version))
	}
	sort.Strings(lines)
	return []string{fmt.Sprintf(
		"linked @go-beyond packages have skewed versions (%s); align them on one release line and rebuild with `pnpm build:packages`",
		strings.Join(lines, "; "))}
}

type packageManifest struct {
	Name    string          `json:"name"`
	Version string          `json:"version"`
	Main    string          `json:"main"`
	Types   string          `json:"types"`
	Bin     json.RawMessage `json:"bin"`
	Exports json.RawMessage `json:"exports"`
}

func readPackageManifest(path string) (packageManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return packageManifest{}, err
	}
	var manifest packageManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return packageManifest{}, err
	}
	return manifest, nil
}

// entrypointFiles returns the relative files a consumer would load: every
// "import"/"require"/"types"/default string in exports, plus main and bin.
// Wildcard subpaths are skipped: they cannot be checked without globbing.
func (manifest packageManifest) entrypointFiles() []string {
	seen := map[string]struct{}{}
	var files []string
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" || !strings.HasPrefix(value, "./") || strings.Contains(value, "*") {
			return
		}
		if _, exists := seen[value]; exists {
			return
		}
		seen[value] = struct{}{}
		files = append(files, value)
	}
	collectExportTargets(manifest.Exports, add)
	collectExportTargets(manifest.Bin, add)
	add(manifest.Main)
	add(manifest.Types)
	sort.Strings(files)
	return files
}

// collectExportTargets walks an exports/bin value of any supported shape
// (string, conditional object, subpath object, array fallback).
func collectExportTargets(raw json.RawMessage, add func(string)) {
	if len(raw) == 0 {
		return
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		add(asString)
		return
	}
	var asObject map[string]json.RawMessage
	if err := json.Unmarshal(raw, &asObject); err == nil {
		keys := make([]string, 0, len(asObject))
		for key := range asObject {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			collectExportTargets(asObject[key], add)
		}
		return
	}
	var asArray []json.RawMessage
	if err := json.Unmarshal(raw, &asArray); err == nil {
		for _, element := range asArray {
			collectExportTargets(element, add)
		}
	}
}
