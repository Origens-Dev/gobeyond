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

type WorkflowsManifest struct {
	APIVersion  string                       `json:"apiVersion"`
	Definitions []WorkflowManifestDefinition `json:"definitions"`
	TaskQueues  []WorkflowManifestQueue      `json:"taskQueues"`
}

type WorkflowManifestDefinition struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Kind       string `json:"kind"`
	ParentID   string `json:"parentId,omitempty"`
	TaskQueue  string `json:"taskQueue"`
	Public     bool   `json:"public"`
	Standalone bool   `json:"standalone,omitempty"`
}

type WorkflowManifestQueue struct {
	ID          string   `json:"id"`
	Definitions []string `json:"definitions"`
}

type AgentsManifest struct {
	APIVersion string                    `json:"apiVersion"`
	BuildID    string                    `json:"buildId"`
	Agents     []AgentManifestDefinition `json:"agents"`
}

type AgentManifestDefinition struct {
	ID          string              `json:"id"`
	Kind        string              `json:"kind"`
	Mode        string              `json:"mode"`
	TaskQueue   string              `json:"taskQueue,omitempty"`
	TaskQueues  []string            `json:"taskQueues,omitempty"`
	Durable     bool                `json:"durable"`
	Realtime    bool                `json:"realtime"`
	Public      bool                `json:"public"`
	Model       string              `json:"model,omitempty"`
	MaxSteps    int                 `json:"maxSteps,omitempty"`
	Revision    string              `json:"revision,omitempty"`
	Slots       AgentSlots          `json:"slots"`
	Tools       []AgentManifestTool `json:"tools"`
	SIPHandlers []string            `json:"sipHandlers,omitempty"`
}

type AgentManifestTool struct {
	ID        string `json:"id"`
	TaskQueue string `json:"taskQueue,omitempty"`
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
	if err := syncGoSources(root, routes, buildID, check); err != nil {
		return err
	}
	workflowDefinitions, err := DiscoverWorkflowDefinitions(root)
	if err != nil {
		return err
	}
	workflowsManifest := portableWorkflowsManifest(workflowDefinitions)
	workflowsManifestBytes, err := json.MarshalIndent(workflowsManifest, "", "  ")
	if err != nil {
		return err
	}
	workflowsManifestBytes = append(workflowsManifestBytes, '\n')
	agentDefinitions, err := DiscoverAgentDefinitions(root)
	if err != nil {
		return err
	}
	setAgentRevisions(agentDefinitions, buildID)
	agentsManifest := portableAgentsManifest(agentDefinitions, buildID)
	agentsManifestBytes, err := json.MarshalIndent(agentsManifest, "", "  ")
	if err != nil {
		return err
	}
	agentsManifestBytes = append(agentsManifestBytes, '\n')
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
	workflowsManifestPath := filepath.Join(root, ".gobeyond", "workflows.json")
	agentsManifestPath := filepath.Join(root, ".gobeyond", "agents.json")
	outputs := map[string][]byte{
		manifestPath:          manifestBytes,
		workflowsManifestPath: workflowsManifestBytes,
		agentsManifestPath:    agentsManifestBytes,
		filepath.Join(root, GeneratedDir, "routes", "routes_gen.go"): goBytes,
	}
	for path, content := range outputs {
		// .gobeyond is ignored and may be absent in a clean clone. Check mode
		// materializes that build input, then checks the committed Go registry.
		if check && path != manifestPath && path != workflowsManifestPath && path != agentsManifestPath {
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

func portableAgentsManifest(definitions []AgentDefinition, buildID string) AgentsManifest {
	manifest := AgentsManifest{APIVersion: "gobeyond.agents/v1alpha3", BuildID: buildID}
	for _, definition := range definitions {
		slots := definition.Slots
		slots.Tools = nonNilStrings(slots.Tools)
		slots.Skills = nonNilStrings(slots.Skills)
		slots.Subagents = nonNilStrings(slots.Subagents)
		slots.Schedules = nonNilStrings(slots.Schedules)
		slots.Channels = nonNilChannels(slots.Channels)
		tools := make([]AgentManifestTool, 0, len(definition.Tools))
		queues := []string{}
		if definition.TaskQueue != "" {
			queues = append(queues, definition.TaskQueue)
		}
		for _, tool := range definition.Tools {
			tools = append(tools, AgentManifestTool{ID: tool.ID, TaskQueue: tool.TaskQueue})
			if tool.TaskQueue != "" && !containsString(queues, tool.TaskQueue) {
				queues = append(queues, tool.TaskQueue)
			}
		}
		sort.Strings(queues)
		manifest.Agents = append(manifest.Agents, AgentManifestDefinition{
			ID:          definition.ID,
			Kind:        definition.Kind,
			Mode:        definition.Mode,
			TaskQueue:   definition.TaskQueue,
			TaskQueues:  queues,
			Durable:     definition.Durable,
			Realtime:    definition.Realtime,
			Public:      definition.Public,
			Model:       definition.Model,
			MaxSteps:    definition.MaxSteps,
			Revision:    definition.Revision,
			Slots:       slots,
			Tools:       tools,
			SIPHandlers: nonNilStrings(definition.SIPHandlers),
		})
	}
	if manifest.Agents == nil {
		manifest.Agents = []AgentManifestDefinition{}
	}
	return manifest
}

func setAgentRevisions(definitions []AgentDefinition, buildID string) {
	for index := range definitions {
		definitions[index].Revision = buildID
	}
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func nonNilChannels(values []AgentChannel) []AgentChannel {
	if values == nil {
		return []AgentChannel{}
	}
	return values
}

func portableWorkflowsManifest(definitions []WorkflowDefinition) WorkflowsManifest {
	manifest := WorkflowsManifest{APIVersion: "gobeyond.workflows/v1alpha1"}
	for _, definition := range definitions {
		manifest.Definitions = append(manifest.Definitions, WorkflowManifestDefinition{
			ID:         definition.ID,
			Name:       definition.Name,
			Kind:       definition.Kind,
			ParentID:   definition.ParentID,
			TaskQueue:  definition.TaskQueue,
			Public:     definition.Public,
			Standalone: definition.Standalone,
		})
	}
	for _, queue := range GroupWorkflowQueues(definitions) {
		entry := WorkflowManifestQueue{ID: queue.ID}
		for _, definition := range queue.Definitions {
			entry.Definitions = append(entry.Definitions, definition.ID)
		}
		manifest.TaskQueues = append(manifest.TaskQueues, entry)
	}
	if manifest.Definitions == nil {
		manifest.Definitions = []WorkflowManifestDefinition{}
	}
	if manifest.TaskQueues == nil {
		manifest.TaskQueues = []WorkflowManifestQueue{}
	}
	return manifest
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

// LoadAgentsManifest reads the compiler-owned portable agent deployment
// projection. Build copies this validated value to dist/deploy/agents.json.
func LoadAgentsManifest(root string) (AgentsManifest, error) {
	data, err := os.ReadFile(filepath.Join(root, ".gobeyond", "agents.json"))
	if err != nil {
		return AgentsManifest{}, err
	}
	var manifest AgentsManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return AgentsManifest{}, err
	}
	if manifest.APIVersion != "gobeyond.agents/v1alpha3" || strings.TrimSpace(manifest.BuildID) == "" {
		return AgentsManifest{}, errors.New("unsupported or incomplete agent manifest")
	}
	if manifest.Agents == nil {
		manifest.Agents = []AgentManifestDefinition{}
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
	if strings.HasPrefix(name, ".tmp") ||
		strings.Contains(relative, "/internal/gobeyondgen") || strings.HasPrefix(relative, "internal/gobeyondgen") ||
		strings.Contains(relative, "/"+GeneratedDir) || strings.HasPrefix(relative, GeneratedDir) {
		return true
	}
	return false
}

func ignoredBuildFile(file, relative, name string) bool {
	if relative == ".git" || name == ".DS_Store" || strings.HasPrefix(name, ".env") || name == "page.schema.go" || name == "page.schema.ts" || strings.HasSuffix(name, ".gobeyond_gen.go") {
		return true
	}
	if strings.HasPrefix(relative, "internal/gobeyondgen/") || strings.Contains(relative, "/internal/gobeyondgen/") ||
		strings.HasPrefix(relative, GeneratedDir+"/") || strings.Contains(relative, "/"+GeneratedDir+"/") {
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
