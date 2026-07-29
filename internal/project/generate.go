package project

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Manifest struct {
	APIVersion string  `json:"apiVersion"`
	BuildID    string  `json:"buildId"`
	Routes     []Route `json:"routes"`
}

func Generate(root, buildRoot string, check bool) error {
	routes, err := Discover(root)
	if err != nil {
		return err
	}
	buildID, err := BuildID(buildRoot, routes)
	if err != nil {
		return err
	}
	return Write(root, routes, buildID, check)
}

// Write persists deterministic route registries using an already finalized
// build identity. The build command uses this after hashing compiler outputs.
func Write(root string, routes []Route, buildID string, check bool) error {
	if buildID == "" {
		return errors.New("build ID is required")
	}
	if err := SyncGoSources(root, routes, check); err != nil {
		return err
	}
	manifest := Manifest{APIVersion: "gobeyond.routes/v1alpha1", BuildID: buildID, Routes: portableRoutes(root, routes)}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	manifestBytes = append(manifestBytes, '\n')

	goBytes, err := generateGo(routes, manifest.BuildID)
	if err != nil {
		return err
	}

	manifestPath := filepath.Join(root, ".gobeyond", "routes.json")
	outputs := map[string][]byte{
		manifestPath: manifestBytes,
		filepath.Join(root, "internal", "gobeyondgen", "routes", "routes_gen.go"): goBytes,
	}
	for path, content := range outputs {
		// .gobeyond is ignored and may be absent in a clean clone. Check mode
		// materializes that build input, then checks the committed Go registry.
		if check && path != manifestPath {
			existing, readErr := os.ReadFile(path)
			if readErr != nil || !bytes.Equal(existing, content) {
				return fmt.Errorf(
					"generated output is stale: %s (expected build ID %s)",
					path,
					buildID,
				)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, content, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func portableRoutes(root string, routes []Route) []Route {
	portable := make([]Route, len(routes))
	copy(portable, routes)
	for index := range portable {
		for _, target := range []*string{
			&portable[index].PageFile,
			&portable[index].SchemaFile,
			&portable[index].BuildFile,
			&portable[index].ServerFile,
		} {
			if *target == "" || !filepath.IsAbs(*target) {
				continue
			}
			relative, err := filepath.Rel(root, filepath.FromSlash(*target))
			if err == nil {
				*target = filepath.ToSlash(relative)
			}
		}
	}
	return portable
}

func LoadManifest(root string) (Manifest, error) {
	data, err := os.ReadFile(filepath.Join(root, ".gobeyond", "routes.json"))
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, err
	}
	if manifest.APIVersion != "gobeyond.routes/v1alpha1" {
		return Manifest{}, errors.New("unsupported route manifest API version")
	}
	return manifest, nil
}

func BuildID(root string, routes []Route) (string, error) {
	values := make([]string, len(routes))
	for i, route := range routes {
		values[i] = route.ID + ":" + route.Mode
	}
	sort.Strings(values)
	hash := sha256.New()
	_, _ = hash.Write([]byte("gobeyond-build/v1\x00" + strings.Join(values, "|") + "\x00"))
	err := walkBuildInputs(root, func(relative, path string) error {
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		_, _ = hash.Write([]byte(relative))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(content)
		_, _ = hash.Write([]byte{0})
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("fingerprint build inputs: %w", err)
	}
	digest := hash.Sum(nil)
	return "b_" + hex.EncodeToString(digest[:8]), nil
}

// BuildSnapshot records the same source files used by BuildID, keyed by their
// slash-normalized path relative to root. Development mode uses it to identify
// which build products a source edit can affect.
func BuildSnapshot(root string) (map[string]string, error) {
	snapshot := make(map[string]string)
	err := walkBuildInputs(root, func(relative, path string) error {
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(content)
		snapshot[relative] = hex.EncodeToString(digest[:])
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("snapshot build inputs: %w", err)
	}
	return snapshot, nil
}

func walkBuildInputs(root string, visit func(relative, path string) error) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.IsDir() {
			if relative != "." && ignoredBuildDirectory(relative, entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if ignoredBuildFile(path, relative, entry.Name()) {
			return nil
		}
		return visit(relative, path)
	})
}

func ignoredBuildDirectory(relative, name string) bool {
	switch name {
	case ".git", ".gobeyond", ".terraform", "node_modules", "dist", "coverage":
		return true
	}
	if strings.HasPrefix(name, ".tmp") || strings.Contains(relative, "/internal/gobeyondgen") || strings.HasPrefix(relative, "internal/gobeyondgen") {
		return true
	}
	return false
}

func ignoredBuildFile(file, relative, name string) bool {
	if name == ".DS_Store" || strings.HasPrefix(name, ".env") || name == "page.schema.go" || name == "page.schema.ts" || strings.HasSuffix(name, ".gobeyond_gen.go") {
		return true
	}
	if strings.HasPrefix(relative, "internal/gobeyondgen/") || strings.Contains(relative, "/internal/gobeyondgen/") {
		return true
	}
	return name == "go.mod" && isManagedRouteModule(file)
}

func isManagedRouteModule(file string) bool {
	content, err := os.ReadFile(file)
	return err == nil && bytes.HasPrefix(content, []byte(generatedModuleMarker))
}

func generateGo(routes []Route, buildID string) ([]byte, error) {
	var source strings.Builder
	source.WriteString("// Code generated by gobeyond generate; DO NOT EDIT.\n")
	source.WriteString("package routes\n\n")
	source.WriteString("const BuildID = \"")
	source.WriteString(buildID)
	source.WriteString("\"\n\nconst (\n")
	for _, route := range routes {
		source.WriteString("\t")
		source.WriteString(goName(route.ServerKey))
		source.WriteString(" = \"")
		source.WriteString(route.ID)
		source.WriteString("\"\n")
	}
	source.WriteString(")\n")
	return format.Source([]byte(source.String()))
}

func goName(key string) string {
	parts := strings.FieldsFunc(key, func(r rune) bool { return r == '_' || r == '-' })
	var result strings.Builder
	result.WriteString("Route")
	for _, part := range parts {
		if part == "" {
			continue
		}
		result.WriteString(strings.ToUpper(part[:1]))
		result.WriteString(part[1:])
	}
	return result.String()
}
