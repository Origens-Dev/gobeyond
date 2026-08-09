package project

import "path/filepath"

// GeneratedDir is the website-relative directory owned by gobeyond for
// projections, registries, contracts, and generated process mains.
// Authors write app/, workflows/, agents/, and internal/ only.
//
// Must not start with "." or "_": Go ignores those directory names when
// matching package patterns, and import paths under them break normal
// `go build` / `go test` workflows.
const GeneratedDir = "generated"

// Legacy dotted root from alpha.11–alpha.12; SyncGoSources removes it.
const legacyDottedGeneratedDir = ".generated"

// LegacyGeneratedDir is the pre-alpha.11 projection root; SyncGoSources removes it.
const LegacyGeneratedDir = "internal/gobeyondgen"

// websiteFile resolves a Discover path that may be absolute or website-relative.
// Go 1.24+ filepath.Join no longer discards earlier elements when a later
// element is absolute, so callers must not Join(root, absPath) blindly.
func websiteFile(root, file string) string {
	file = filepath.FromSlash(file)
	if filepath.IsAbs(file) {
		return file
	}
	return filepath.Join(root, file)
}
