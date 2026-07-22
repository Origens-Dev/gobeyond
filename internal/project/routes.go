package project

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Route struct {
	ID          string `json:"id"`
	Pattern     string `json:"pattern"`
	Mode        string `json:"mode"`
	Reason      string `json:"reason"`
	AppDir      string `json:"appDir"`
	ServerKey   string `json:"serverKey"`
	PageFile    string `json:"pageFile"`
	SchemaFile  string `json:"schemaFile,omitempty"`
	BuildFile   string `json:"buildFile,omitempty"`
	ServerFile  string `json:"serverFile,omitempty"`
	PlanFile    string `json:"planFile"`
	ClientEntry string `json:"clientEntry"`
}

func Discover(root string) ([]Route, error) {
	appRoot := filepath.Join(root, "app")
	if _, err := os.Stat(appRoot); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	_, middlewareErr := os.Stat(filepath.Join(root, "server", "middleware", "middleware.go"))
	hasRuntimeMiddleware := middlewareErr == nil
	var routes []Route
	err := filepath.WalkDir(appRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != appRoot && strings.HasPrefix(entry.Name(), "@") {
				return fs.SkipDir
			}
			return nil
		}
		if entry.Name() != "page.tsx" {
			return nil
		}
		dir := filepath.Dir(path)
		relative, err := filepath.Rel(appRoot, dir)
		if err != nil {
			return err
		}
		relativeSlash := filepath.ToSlash(relative)
		if relativeSlash == "api" || strings.HasPrefix(relativeSlash, "api/") {
			return fmt.Errorf("page routes under app/api are reserved for Go API route.go files: %s", filepath.ToSlash(path))
		}
		pattern, serverKey, err := routeNames(relative)
		if err != nil {
			return err
		}
		route := Route{
			ID:          stableID(pattern),
			Pattern:     pattern,
			Mode:        "static",
			Reason:      "page.tsx has no request-time Go dependency",
			AppDir:      filepath.ToSlash(relative),
			ServerKey:   serverKey,
			PageFile:    filepath.ToSlash(path),
			PlanFile:    filepath.ToSlash(filepath.Join("render-plans", stableID(pattern)+".json")),
			ClientEntry: filepath.ToSlash(filepath.Join("assets", stableID(pattern)+".js")),
		}
		if route.AppDir == "." {
			route.AppDir = ""
		}
		for _, optional := range []struct {
			name   string
			target *string
		}{
			{name: "page.schema.ts", target: &route.SchemaFile},
			{name: "page.build.ts", target: &route.BuildFile},
		} {
			candidate := filepath.Join(dir, optional.name)
			if _, statErr := os.Stat(candidate); statErr == nil {
				*optional.target = filepath.ToSlash(candidate)
			}
		}
		serverFile := filepath.Join(root, "server", "pages", serverKey, "page.go")
		coLocatedFile := filepath.Join(dir, "page.go")
		_, coLocatedErr := os.Stat(coLocatedFile)
		_, serverErr := os.Stat(serverFile)
		if coLocatedErr == nil && serverErr == nil {
			return fmt.Errorf("route %s has both %s and legacy %s; keep only the co-located app page", pattern, filepath.ToSlash(coLocatedFile), filepath.ToSlash(serverFile))
		}
		if coLocatedErr == nil {
			route.Mode = "dynamic"
			route.Reason = "co-located request-time Go page loader"
			route.ServerFile = filepath.ToSlash(coLocatedFile)
		} else if serverErr == nil {
			route.Mode = "dynamic"
			route.Reason = "paired request-time Go page loader"
			route.ServerFile = filepath.ToSlash(serverFile)
		} else if hasRuntimeMiddleware {
			route.Mode = "dynamic"
			route.Reason = "request-time Go middleware applies conservatively to the MVP route tree"
		}
		routes = append(routes, route)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].Pattern < routes[j].Pattern })
	seen := make(map[string]struct{}, len(routes))
	for _, route := range routes {
		if _, exists := seen[route.Pattern]; exists {
			return nil, errors.New("multiple app directories resolve to " + route.Pattern)
		}
		seen[route.Pattern] = struct{}{}
	}
	return routes, nil
}

// APIKey returns the deterministic, Go-safe generated package directory for a
// co-located app/api route. The input is relative to app/api.
func APIKey(relative string) string {
	return stableID("/api/" + strings.Trim(filepath.ToSlash(relative), "/"))
}

func routeNames(relative string) (string, string, error) {
	if relative == "." || relative == "" {
		return "/", "root", nil
	}
	parts := strings.Split(filepath.ToSlash(relative), "/")
	urlParts := make([]string, 0, len(parts))
	keyParts := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.HasPrefix(part, "(") && strings.HasSuffix(part, ")") {
			continue
		}
		if strings.HasPrefix(part, "@") {
			return "", "", errors.New("parallel route slots are deferred in the MVP: " + part)
		}
		urlParts = append(urlParts, part)
		keyParts = append(keyParts, safePart(part))
	}
	pattern := "/" + strings.Join(urlParts, "/")
	if pattern == "/" {
		return pattern, "root", nil
	}
	return pattern, strings.Join(keyParts, "_"), nil
}

func safePart(part string) string {
	switch {
	case strings.HasPrefix(part, "[[...") && strings.HasSuffix(part, "]]"):
		part = "optional_" + part[5:len(part)-2]
	case strings.HasPrefix(part, "[...") && strings.HasSuffix(part, "]"):
		part = "all_" + part[4:len(part)-1]
	case strings.HasPrefix(part, "[") && strings.HasSuffix(part, "]"):
		part = part[1 : len(part)-1]
	}
	var builder strings.Builder
	for _, char := range strings.ToLower(part) {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' {
			builder.WriteRune(char)
		} else {
			builder.WriteByte('_')
		}
	}
	result := strings.Trim(builder.String(), "_")
	if result == "" {
		return "route"
	}
	return result
}

func stableID(pattern string) string {
	digest := sha256.Sum256([]byte(pattern))
	name := strings.Trim(safePart(strings.ReplaceAll(strings.Trim(pattern, "/"), "/", "_")), "_")
	if name == "" {
		name = "root"
	}
	return "r_" + name + "_" + hex.EncodeToString(digest[:4])
}
