package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// materializeAppMetadataFiles asks the TypeScript compiler to discover and
// materialize Next.js-compatible Metadata files under app/ into staticDir.
// Build-time only — the Go server never evaluates these modules.
func materializeAppMetadataFiles(projectRoot, staticDir, compilerCLI string, environment []string) ([]string, error) {
	if compilerCLI == "" {
		// No compiler and no app/ metadata is fine; presence of code modules
		// is validated inside the compiler when it runs.
		if !appMetadataLikelyPresent(projectRoot) {
			return nil, nil
		}
		return nil, fmt.Errorf("app metadata files require @go-beyond/compiler to materialize")
	}
	temporary, err := os.MkdirTemp("", "gobeyond-metadata-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(temporary)
	outputPath := filepath.Join(temporary, "paths.json")
	if err := runCommandWithEnvironment(projectRoot, environment, "node", compilerCLI,
		"materialize-metadata",
		"--project-root", projectRoot,
		"--static-dir", staticDir,
		"--out", outputPath,
	); err != nil {
		return nil, fmt.Errorf("materialize app metadata: %w", err)
	}
	data, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Paths []string `json:"paths"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("parse metadata paths: %w", err)
	}
	return payload.Paths, nil
}

func appMetadataLikelyPresent(projectRoot string) bool {
	appDir := filepath.Join(projectRoot, "app")
	candidates := []string{
		"robots.txt", "robots.ts", "robots.js",
		"sitemap.xml", "sitemap.ts", "sitemap.js",
		"manifest.json", "manifest.webmanifest", "manifest.ts", "manifest.js",
		"favicon.ico",
		"icon.png", "icon.svg", "icon.ico", "icon.jpg", "icon.jpeg",
		"apple-icon.png", "apple-icon.jpg", "apple-icon.jpeg",
		"opengraph-image.png", "opengraph-image.jpg", "opengraph-image.jpeg", "opengraph-image.gif",
		"twitter-image.png", "twitter-image.jpg", "twitter-image.jpeg", "twitter-image.gif",
		"icon.tsx", "icon.ts", "icon.jsx", "icon.js",
		"apple-icon.tsx", "apple-icon.ts",
		"opengraph-image.tsx", "opengraph-image.ts",
		"twitter-image.tsx", "twitter-image.ts",
	}
	for _, name := range candidates {
		if _, err := os.Stat(filepath.Join(appDir, name)); err == nil {
			return true
		}
	}
	return false
}
