package project

import "path/filepath"

// GeneratedDir is the website-relative directory owned by gobeyond for
// projections, registries, contracts, and generated process mains.
// Authors write app/, workers/, and internal/ only.
const GeneratedDir = ".generated"

// LegacyGeneratedDir is the previous projection root; SyncGoSources removes it.
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
