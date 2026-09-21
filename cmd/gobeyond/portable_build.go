package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// PublicRuntime declares browser exposure explicitly. Sensitive=false and the
// VITE_ prefix alone never grant exposure in a portable build.
type portableBuildConfig struct {
	PortableBuild         bool     `json:"portableBuild"`
	PublicRuntime         []string `json:"publicRuntime"`
	RequiredPublicRuntime []string `json:"requiredPublicRuntime"`
}

func readPortableBuildConfig(root string) (portableBuildConfig, error) {
	var config portableBuildConfig
	raw, err := os.ReadFile(filepath.Join(root, "gobeyond.json"))
	if os.IsNotExist(err) {
		return config, nil
	}
	if err != nil {
		return config, err
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return config, err
	}
	seen := map[string]bool{}
	valid := regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	for _, name := range config.PublicRuntime {
		if !valid.MatchString(name) || seen[name] || name == "GOBEYOND_PUBLIC_CONFIG" || name == "GOBEYOND_DEPLOYMENT_REVISION" {
			return config, fmt.Errorf("invalid or duplicate publicRuntime name %q", name)
		}
		seen[name] = true
	}
	required := map[string]bool{}
	for _, name := range config.RequiredPublicRuntime {
		if required[name] {
			return config, fmt.Errorf("duplicate requiredPublicRuntime name %s", name)
		}
		required[name] = true
		if !seen[name] {
			return config, fmt.Errorf("requiredPublicRuntime name %s is not public", name)
		}
	}
	return config, nil
}
func writePortableBuildContract(root, dist string) error {
	config, err := readPortableBuildConfig(root)
	if err != nil {
		return err
	}
	if os.Getenv("GOBEYOND_PORTABLE_BUILD") == "1" && !config.PortableBuild {
		return fmt.Errorf("project must declare portableBuild and migrate environment-dependent compilation before shared builds")
	}
	return writeJSONFile(filepath.Join(dist, "deploy", "public-runtime.json"), map[string]any{"version": 1, "portable": config.PortableBuild && os.Getenv("GOBEYOND_PORTABLE_BUILD") == "1", "public": config.PublicRuntime, "required": config.RequiredPublicRuntime})
}

// Local development follows the same explicit publication contract as deployment.
// This is assembled only for the runtime child, never a compiler environment.
func developmentPublicConfig(root string, environment []string) ([]string, error) {
	config, err := readPortableBuildConfig(root)
	if err != nil {
		return nil, err
	}
	if len(config.PublicRuntime) == 0 {
		return nil, nil
	}
	values := map[string]string{}
	for _, entry := range environment {
		if name, value, ok := strings.Cut(entry, "="); ok {
			values[name] = value
		}
	}
	public := map[string]string{}
	for _, name := range config.PublicRuntime {
		value, exists := values[name]
		if exists {
			public[name] = value
		}
	}
	for _, name := range config.RequiredPublicRuntime {
		if strings.TrimSpace(public[name]) == "" {
			return nil, fmt.Errorf("required public runtime variable %s is missing", name)
		}
	}
	raw, err := json.Marshal(public)
	if err != nil {
		return nil, err
	}
	if len(raw) > 64<<10 {
		return nil, fmt.Errorf("public runtime configuration exceeds 64 KiB")
	}
	return []string{"GOBEYOND_PUBLIC_CONFIG=" + string(raw), fmt.Sprintf("GOBEYOND_DEPLOYMENT_REVISION=dev-%x", sha256.Sum256(raw))}, nil
}
