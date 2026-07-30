package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	staticMetadataImage = regexp.MustCompile(`^(icon|apple-icon|opengraph-image|twitter-image)(\d*)\.(ico|jpg|jpeg|png|svg|gif)$`)
	codeMetadataImage   = regexp.MustCompile(`^(icon|apple-icon|opengraph-image|twitter-image)(\d*)\.(ts|tsx|js|jsx)$`)
)

// materializeAppMetadataFiles asks the TypeScript compiler to discover and
// materialize Next.js-compatible Metadata files under app/ into staticDir.
// Build-time only — the Go server never evaluates these modules.
//
// When the site authors no Metadata files, this is a no-op and does not invoke
// Node (so older @go-beyond/compiler installs are unaffected).
func materializeAppMetadataFiles(projectRoot, staticDir, compilerCLI string, environment []string) ([]string, error) {
	present, err := appMetadataPresent(projectRoot)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, nil
	}
	if compilerCLI == "" {
		return nil, fmt.Errorf("app metadata files require @go-beyond/compiler >= 0.1.0-alpha.14 to materialize")
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
		return nil, fmt.Errorf("materialize app metadata: %w (requires @go-beyond/compiler >= 0.1.0-alpha.14)", err)
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

func appMetadataPresent(projectRoot string) (bool, error) {
	appDir := filepath.Join(projectRoot, "app")
	info, err := os.Stat(appDir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !info.IsDir() {
		return false, nil
	}
	found := false
	walkErr := filepath.WalkDir(appDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			name := entry.Name()
			if name == "node_modules" || name == "generated" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		name := entry.Name()
		switch name {
		case "robots.txt", "robots.ts", "robots.js",
			"sitemap.xml", "sitemap.ts", "sitemap.js",
			"manifest.json", "manifest.webmanifest", "manifest.ts", "manifest.js",
			"favicon.ico":
			found = true
			return fs.SkipAll
		}
		if staticMetadataImage.MatchString(name) || codeMetadataImage.MatchString(name) {
			found = true
			return fs.SkipAll
		}
		return nil
	})
	if walkErr != nil {
		return false, walkErr
	}
	return found, nil
}
