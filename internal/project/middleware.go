package project

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// DiscoverMiddlewareSource returns the one supported authored middleware
// entry. GoBeyond middleware is a root middleware.ts or middleware.js default
// export; the compiler rejects legacy Go and edge-middleware layouts so one
// application never has two request-middleware contracts.
func DiscoverMiddlewareSource(root string) (string, error) {
	for _, legacy := range []struct {
		path    string
		message string
	}{
		{filepath.Join(root, "middleware.go"), "middleware.go is no longer supported; migrate request middleware to a root middleware.ts or middleware.js default export"},
		{filepath.Join(root, "server", "cmd", "middleware"), "server/cmd/middleware is no longer supported; migrate request middleware to a root middleware.ts or middleware.js default export"},
		{filepath.Join(root, "edge-middleware.ts"), "edge-middleware.ts is no longer supported; rename it to middleware.ts"},
		{filepath.Join(root, "edge-middleware.js"), "edge-middleware.js is no longer supported; rename it to middleware.js"},
		{filepath.Join(root, "edge-middleware"), "edge-middleware/ is no longer a supported authored layout; move its default middleware export to root middleware.ts or middleware.js"},
	} {
		if _, err := os.Stat(legacy.path); err == nil {
			return "", errors.New(legacy.message)
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}

	var sources []string
	for _, name := range []string{"middleware.ts", "middleware.js"} {
		candidate := filepath.Join(root, name)
		info, err := os.Stat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("%s must be a regular file", name)
		}
		sources = append(sources, candidate)
	}
	if len(sources) > 1 {
		return "", errors.New("middleware.ts and middleware.js cannot both exist; keep exactly one root middleware entry")
	}
	if len(sources) == 0 {
		return "", nil
	}
	return sources[0], nil
}
