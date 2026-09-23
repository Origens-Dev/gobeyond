package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"

	"github.com/Origens-Dev/gobeyond/internal/project"
)

type fingerprintPackage struct {
	Dir, ImportPath                                                                                                     string
	Standard, Incomplete                                                                                                bool
	Error                                                                                                               *struct{ Err string }
	Imports                                                                                                             []string
	GoFiles, CgoFiles, CFiles, CXXFiles, MFiles, HFiles, FFiles, SFiles, SwigFiles, SwigCXXFiles, SysoFiles, EmbedFiles []string
	Module                                                                                                              *struct{ Path, Version, Sum, GoMod string }
}
type fingerprintGraph map[string]fingerprintPackage

// go list resolves the actual target-platform import and embed graph without
// compiling. Incomplete resolution disables reuse; it never guesses from paths.
func loadFingerprintGraph(root string, targets []string) (fingerprintGraph, error) {
	cmd := exec.Command("go", append([]string{"list", "-e", "-deps", "-json"}, targets...)...)
	cmd.Dir = root
	cmd.Env = withEnvironment(compilerEnvironment(), "GOOS=linux", "GOARCH=arm64", "CGO_ENABLED=0", "GOFLAGS=")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("resolve compilation dependencies: %w", err)
	}
	graph := fingerprintGraph{}
	dec := json.NewDecoder(bytes.NewReader(raw))
	for {
		var p fingerprintPackage
		err := dec.Decode(&p)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		graph[p.ImportPath] = p
	}
	return graph, nil
}

func (g fingerprintGraph) digest(target string, excluded map[string]bool, salt []byte) (string, error) {
	var root string
	for name, p := range g {
		if p.Dir == target {
			root = name
			break
		}
	}
	if root == "" {
		return "", fmt.Errorf("target dependency graph unavailable")
	}
	visited := map[string]bool{}
	var names []string
	var visit func(string) error
	visit = func(name string) error {
		if name == "unsafe" || visited[name] {
			return nil
		}
		visited[name] = true
		p, ok := g[name]
		if !ok || p.Error != nil || p.Incomplete {
			return fmt.Errorf("incomplete dependency graph for %s", name)
		}
		if p.Standard {
			return nil
		}
		names = append(names, name)
		for _, dependency := range p.Imports {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(root); err != nil {
		return "", err
	}
	sort.Strings(names)
	h := sha256.New()
	enc := json.NewEncoder(h)
	_ = enc.Encode([]string{"gobeyond-target/v1", runtime.Version(), "linux/arm64", "CGO_ENABLED=0", "-trimpath", "-ldflags=-s -w"})
	_ = enc.Encode(salt)
	for _, name := range names {
		p := g[name]
		_ = enc.Encode(name)
		if p.Module != nil {
			_ = enc.Encode([]string{p.Module.Path, p.Module.Version, p.Module.Sum})
			if p.Module.GoMod != "" {
				b, err := os.ReadFile(p.Module.GoMod)
				if err != nil {
					return "", err
				}
				_ = enc.Encode(b)
			}
		}
		files := []string{}
		for _, group := range [][]string{p.GoFiles, p.CgoFiles, p.CFiles, p.CXXFiles, p.MFiles, p.HFiles, p.FFiles, p.SFiles, p.SwigFiles, p.SwigCXXFiles, p.SysoFiles, p.EmbedFiles} {
			files = append(files, group...)
		}
		sort.Strings(files)
		for _, file := range files {
			full := filepath.Join(p.Dir, file)
			if excluded[full] {
				continue
			}
			b, err := os.ReadFile(full)
			if err != nil {
				return "", err
			}
			_ = enc.Encode(file)
			_ = enc.Encode(b)
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Fingerprinting is additive to the Lambda checkpoint path. Normal full builds
// continue using the existing generation and compilation path.
func fingerprintCheckpoint(c *buildCheckpoint) error {
	agents, err := project.DiscoverAgentDefinitions(c.ProjectRoot)
	if err != nil {
		return err
	}
	targets := []string{c.ServerTarget}
	if c.Version == 2 {
		targets = append(targets, "github.com/Origens-Dev/gobeyond/codegen")
	}
	for _, w := range c.Workers {
		targets = append(targets, w.PackageDir)
	}
	for _, a := range agents {
		targets = append(targets, filepath.Join(c.ProjectRoot, "generated", "agents", a.Key))
	}
	revisions := map[string]string{}
	for _, a := range agents {
		revisions[a.ID] = c.Manifest.BuildID
	}
	c.AgentRevisions = revisions
	graph, err := loadFingerprintGraph(c.ProjectRoot, targets)
	if err != nil {
		fmt.Fprintln(os.Stderr, "target dependency graph unavailable; using conservative web inputs")
		if c.Version == 2 {
			c.NeedsPortablePreparation = len(c.Workers) > 0
			return fingerprintPlannedWeb(c, nil)
		}
		return nil
	}
	for _, a := range agents {
		revisions[a.ID] = c.Manifest.BuildID
		dir := filepath.Join(c.ProjectRoot, "generated", "agents", a.Key)
		salt, _ := json.Marshal(a)
		digest, e := graph.digest(dir, map[string]bool{filepath.Join(dir, "gobeyond_register_gen.go"): true}, salt)
		if e == nil {
			revisions[a.ID] = "a_" + digest
		}
	}
	c.AgentRevisions = revisions
	// Keep the web registry, voice manifests, and workers on the same revisions.
	if err := project.WriteWithAgentRevisions(c.ProjectRoot, c.Manifest.Routes, c.Manifest.BuildID, false, revisions); err != nil {
		return err
	}
	c.Fingerprints = map[string]string{}
	for _, w := range c.Workers {
		if digest, e := graph.digest(w.PackageDir, nil, nil); e == nil {
			c.Fingerprints["worker/"+w.ID] = digest
		} else if c.Version == 2 {
			c.NeedsPortablePreparation = true
		}
	}
	if c.Version == 2 {
		return fingerprintPlannedWeb(c, graph)
	}
	// Web output still contains the application build ID, so it is deliberately
	// conservative. Include all authored/local package sources in the repository
	// as well as the Go closure; unrelated worker reuse remains independent.
	salt, err := webFingerprintInputs(c)
	if err != nil {
		return err
	}
	if digest, e := graph.digest(c.ServerTarget, nil, salt); e == nil {
		c.Fingerprints["web"] = digest
	}
	return nil
}

func webFingerprintInputs(c *buildCheckpoint) ([]byte, error) {
	// BuildID covers the project, compiler output and static inputs. Hash sibling
	// sources too: file: browser packages may live outside the project root.
	root := fingerprintSourceRoot(c.ProjectRoot)
	snapshot, err := project.BuildSnapshot(root)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		Build   string
		Sources map[string]string
	}{c.Manifest.BuildID, snapshot})
}

func fingerprintSourceRoot(projectRoot string) string {
	root := projectRoot
	for dir := root; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			root = dir
			break
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	// Lambda workspaces have no .git; source extraction uses a stable src root.
	for dir := projectRoot; filepath.Dir(dir) != dir; dir = filepath.Dir(dir) {
		if filepath.Base(dir) == "src" {
			root = dir
			break
		}
	}
	return root
}
