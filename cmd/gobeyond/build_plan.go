package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Origens-Dev/gobeyond/internal/project"
)

// prepareTargetPlan deliberately has no Node/compiler/typecheck prerequisite.
// Only the web child materializes portable contracts and browser dependencies.
func prepareTargetPlan(root string) error {
	started := time.Now()
	defer func() { slog.Info("gobeyond_target_plan_duration", "duration_seconds", time.Since(started).Seconds()) }()
	website := websiteRoot(root)
	// Do not let a developer's ignored projections affect dependency discovery.
	for _, dir := range []string{filepath.Join(website, "generated"), filepath.Join(root, ".gobeyond"), filepath.Join(root, "dist")} {
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	if err := syncRouteSchemaFiles(website, false); err != nil {
		return err
	}
	routes, err := project.Discover(website)
	if err != nil {
		return err
	}
	// This provisional identity is also a safe fallback for agent graphs that
	// cannot be resolved. Scoped revisions replace it when dependency resolution succeeds.
	provisional, err := project.BuildID(root, routes)
	if err != nil {
		return err
	}
	if err := project.Write(website, routes, provisional, false); err != nil {
		return err
	}
	server, err := serverBuildTarget(website)
	if err != nil {
		return err
	}
	workers, err := workerBuildTargets(website)
	if err != nil {
		return err
	}
	c := buildCheckpoint{Version: 2, Root: root, Dist: filepath.Join(root, "dist"), ProjectRoot: website,
		BrowserMode: "production", Manifest: project.Manifest{APIVersion: "gobeyond.routes/v1alpha1", BuildID: provisional, Routes: routes},
		ServerTarget: server, Workers: workers}
	if err := fingerprintCheckpoint(&c); err != nil {
		return err
	}
	// The web identity is its input closure, independent of unrelated worker
	// implementation changes. The immutable whole-build manifest still identifies
	// the complete commit and target set at the platform boundary.
	c.Manifest.BuildID = "b_" + c.WebInputIdentity
	if err := project.WriteWithAgentRevisions(website, routes, c.Manifest.BuildID, false, c.AgentRevisions); err != nil {
		return err
	}
	return writeBuildCheckpoint(c)
}

func fingerprintPlannedWeb(c *buildCheckpoint, graph fingerprintGraph) error {
	// Normalize the generated route identity before hashing. Otherwise the
	// application-wide provisional ID reintroduces worker implementation inputs.
	const placeholder = "b_web_input_plan"
	if err := project.WriteWithAgentRevisions(c.ProjectRoot, c.Manifest.Routes, placeholder, false, c.AgentRevisions); err != nil {
		return err
	}
	raw, err := webFingerprintInputs(&buildCheckpoint{ProjectRoot: c.ProjectRoot})
	if err != nil {
		return err
	}
	var inputs struct {
		Build   string
		Sources map[string]string
	}
	if err := json.Unmarshal(raw, &inputs); err != nil {
		return err
	}
	root := fingerprintSourceRoot(c.ProjectRoot)
	// Exclude only discovered workflow implementation files, and only with a
	// complete web import graph proving that their projections are not imported.
	// Agent packages are real web dependencies and are deliberately retained.
	graph = plannedWebGraph(c, graph)
	webDigest, graphErr := graph.digest(c.ServerTarget, nil, nil)
	if graphErr == nil {
		definitions, err := project.DiscoverWorkflowDefinitions(c.ProjectRoot)
		if err != nil {
			return err
		}
		closure := graph.reachableDirectories(c.ServerTarget)
		// Shared Go packages used exclusively by workers are also independent
		// of web. Keep all other repository inputs conservatively, including
		// browser assets, manifests and package/dependency declarations.
		workerDirs := map[string]bool{}
		for _, worker := range c.Workers {
			for dir := range graph.reachableDirectories(worker.PackageDir) {
				workerDirs[dir] = true
			}
		}
		for _, pkg := range graph {
			if pkg.Dir == "" || !workerDirs[pkg.Dir] || closure[pkg.Dir] {
				continue
			}
			for _, file := range pkg.GoFiles {
				rel, err := filepath.Rel(root, filepath.Join(pkg.Dir, file))
				if err != nil || !filepath.IsLocal(rel) {
					continue
				}
				delete(inputs.Sources, filepath.ToSlash(rel))
			}
		}

		for _, definition := range definitions {
			projected := filepath.Join(c.ProjectRoot, "generated", "workflows", definition.Key)
			if closure[projected] {
				continue
			}
			for _, source := range definition.SourceFiles {
				rel, err := filepath.Rel(root, filepath.Join(c.ProjectRoot, filepath.FromSlash(source)))
				if err != nil {
					return err
				}
				delete(inputs.Sources, filepath.ToSlash(rel))
			}
		}
	}
	// Include generated deployment metadata even when its source implementation
	// is excluded: queue topology, public workflow names and wake rules must agree
	// with reused web output. These files are not runtime-specific configuration.
	metadata := map[string]json.RawMessage{}
	for _, name := range []string{"workflows.json", "agents.json"} {
		b, err := os.ReadFile(filepath.Join(c.ProjectRoot, ".gobeyond", name))
		if err != nil {
			return err
		}
		metadata[name] = b
	}
	definitions, err := project.DiscoverWorkflowDefinitions(c.ProjectRoot)
	if err != nil {
		return err
	}
	wake, err := project.MarshalWakeManifest(c.ProjectRoot, definitions)
	if err != nil {
		return err
	}
	metadata["wake.json"] = wake
	b, err := json.Marshal(struct {
		Version  string
		Sources  map[string]string
		Go       string
		Metadata map[string]json.RawMessage
	}{
		"gobeyond-web-plan/v1", inputs.Sources, webDigest, metadata})
	if err != nil {
		return err
	}
	digest := sha256.Sum256(b)
	if c.Fingerprints == nil {
		c.Fingerprints = map[string]string{}
	}
	c.WebInputIdentity = hex.EncodeToString(digest[:])
	if graphErr == nil {
		c.Fingerprints["web"] = c.WebInputIdentity
	}
	if graphErr != nil {
		fmt.Fprintln(os.Stderr, "web plan uses all authored inputs: incomplete Go dependency graph")
	}
	return nil
}

func (g fingerprintGraph) reachableDirectories(target string) map[string]bool {
	dirs := map[string]bool{}
	var visit func(string)
	visit = func(name string) {
		p, ok := g[name]
		if !ok || dirs[p.Dir] {
			return
		}
		dirs[p.Dir] = true
		for _, dependency := range p.Imports {
			visit(dependency)
		}
	}
	for name, p := range g {
		if p.Dir == target {
			visit(name)
			break
		}
	}
	return dirs
}

// Portable contracts are web-only compiler outputs. Their authored TypeScript
// inputs are included in the source snapshot. Model only their fixed runtime
// imports here; never apply this exemption to a worker compilation graph.
func plannedWebGraph(c *buildCheckpoint, source fingerprintGraph) fingerprintGraph {
	graph := fingerprintGraph{}
	moduleRoot, modulePath, err := pageSchemaModule(c.ProjectRoot)
	if err != nil {
		return graph
	}
	relative, err := filepath.Rel(moduleRoot, c.ProjectRoot)
	if err != nil {
		return graph
	}
	prefix := modulePath
	if relative != "." {
		prefix += "/" + filepath.ToSlash(relative)
	}
	prefix += "/generated/contracts/"
	for name, p := range source {
		if p.Error != nil && (strings.HasPrefix(name, prefix+"routes/") || strings.HasPrefix(name, prefix+"actions/")) {
			p = fingerprintPackage{ImportPath: name, Imports: []string{"github.com/Origens-Dev/gobeyond", "github.com/Origens-Dev/gobeyond/codegen", "github.com/Origens-Dev/gobeyond/runtime"}}
		}
		// Incomplete is transitive; walking every import still rejects all actual
		// unresolved packages other than the explicitly deferred contracts above.
		p.Incomplete = false
		graph[name] = p
	}
	return graph
}
