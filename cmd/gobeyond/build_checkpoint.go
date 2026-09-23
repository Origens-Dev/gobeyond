package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Origens-Dev/gobeyond/imageopt"
	"github.com/Origens-Dev/gobeyond/internal/project"
)

// The platform restores this workspace at the same path in each isolated
// Lambda. Only the pinned compiler image consumes these private checkpoints.
// Normal `build` never writes or reads a checkpoint.
type buildCheckpoint struct {
	Fingerprints                                 map[string]string
	Version                                      int
	Root, Dist, ProjectRoot, BrowserMode         string
	Manifest                                     project.Manifest
	Compiled                                     *compilerProjectOutput
	PlanBytes                                    [][]byte
	ContractBytes                                []byte
	ClientEntry                                  browserBuildInput
	ServerTarget                                 string
	Workers                                      []workerBuildTargetInfo
	GeneratedIconAssets, GeneratedMetadataAssets []string
	HasGoMiddleware, HasImageConfig              bool
	ImageConfig                                  imageopt.DeploymentConfig
	ProxyPolicy                                  []byte
}

func checkpointPath(root string) string {
	return filepath.Join(root, ".gobeyond", "build-checkpoint.json")
}

func prepareBuildCheckpoint(root string) error {
	return prepareAndCompile(root, filepath.Join(root, "dist"), false, "", "production", true)
}

func writeBuildCheckpoint(c buildCheckpoint) error {
	// JSON reformatting must not change raw plans/contracts used in packs.
	// Encode those bytes as base64 while retaining JSON's empty versus nil
	// semantics for the rest of the compiler's structured output.
	compiled := *c.Compiled
	c.Compiled = &compiled
	c.ContractBytes = compiled.Contracts
	c.PlanBytes = make([][]byte, len(compiled.Plans))
	for i, plan := range compiled.Plans {
		c.PlanBytes[i] = plan
	}
	compiled.Contracts, compiled.Plans = nil, nil
	if err := writeJSONFile(checkpointPath(c.Root), c); err != nil {
		return err
	}
	// Small dispatch plan; the controller never loads render plans or source.
	ids := make([]string, 0, len(c.Workers))
	for _, w := range c.Workers {
		ids = append(ids, w.ID)
	}
	return writeJSONFile(filepath.Join(c.Root, ".gobeyond", "build-targets.json"), struct {
		Version      int               `json:"version"`
		BuildID      string            `json:"build_id"`
		Workers      []string          `json:"workers"`
		Fingerprints map[string]string `json:"fingerprints,omitempty"`
	}{1, c.Manifest.BuildID, ids, c.Fingerprints})
}

func readBuildCheckpoint(root string) (buildCheckpoint, error) {
	var c buildCheckpoint
	raw, err := os.ReadFile(checkpointPath(root))
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, err
	}
	if c.Version != 1 || c.Root != root || c.Dist != filepath.Join(root, "dist") || c.ProjectRoot != websiteRoot(root) || c.Compiled == nil || c.Manifest.BuildID == "" {
		return c, fmt.Errorf("incompatible or relocated build checkpoint")
	}
	c.Compiled.Contracts = c.ContractBytes
	c.Compiled.Plans = make([]json.RawMessage, len(c.PlanBytes))
	for i, plan := range c.PlanBytes {
		c.Compiled.Plans[i] = plan
	}
	return c, nil
}

func resumeWebCheckpoint(root string) error {
	c, err := readBuildCheckpoint(root)
	if err != nil {
		return err
	}
	return compileCheckpoint(c, false)
}

// Worker execution is Go only. Generation, Node and dependency installation
// have already completed in the prepared workspace.
func resumeWorkerCheckpoint(root, id string) error {
	c, err := readBuildCheckpoint(root)
	if err != nil {
		return err
	}
	for _, w := range c.Workers {
		if w.ID != id {
			continue
		}
		if !filepath.IsLocal(id) || filepath.Base(id) != id {
			return fmt.Errorf("invalid worker ID")
		}
		p, err := filepath.Rel(root, w.PackageDir)
		if err != nil || !filepath.IsLocal(p) {
			return fmt.Errorf("worker package escapes project")
		}
		output := filepath.Join(c.Dist, "workers", id, "gobeyond-worker")
		if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
			return err
		}
		return runCommandWithEnvironment(root, withEnvironment(compilerEnvironment(), "CGO_ENABLED=0"), "go", "build", "-trimpath", "-ldflags=-s -w", "-o", output, w.PackageDir)
	}
	return fmt.Errorf("worker %q is absent from prepared build", id)
}
