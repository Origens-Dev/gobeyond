// Package buildpaths centralizes the gobeyond.builds/v1 asset layout: the
// on-disk locations gobeyond build writes and the public URLs the runtime and
// CDN serve them from. Every producer and consumer of a build artifact must
// go through this package so the disk layout and the URL space never drift
// apart.
//
// Disk:
//
//	dist/static/_gobeyond/builds/<build-id>/assets/...
//	dist/static/_gobeyond/builds/<build-id>/manifest.json
//	dist/static/_gobeyond/builds/<build-id>/static/<route-id>
//
// URLs:
//
//	/_gobeyond/builds/<id>/assets/...
//	/_gobeyond/builds/<id>/manifest.json
//	/_gobeyond/builds/<id>/static/<route-id>
//	/_gobeyond/builds/<id>/runtime/...
//	/_gobeyond/builds/<id>/actions/...
package buildpaths

import (
	"path/filepath"
	"strings"
)

// AssetLayoutV5 runs authored Go middleware inside the application process
// and emits the immutable proxy policy artifact.
const AssetLayoutV5 = "gobeyond.builds/v5"

// AssetLayout identifies the current on-disk/URL layout implemented by this package.
// It is published in build metadata so tooling and deployment references can
// detect the layout a build was produced with.
const AssetLayout = AssetLayoutV5

// Worker artifact locations inside dist/ under AssetLayoutV3.
const (
	WorkersDir      = "workers"
	WorkerEntryName = "gobeyond-worker"
	WorkersManifest = "workers.json"
	AgentsManifest  = "agents.json"
	WakeManifest    = "wake.json"
	// DurablesAdapterTemporal is the v1 runtime adapter stamped into
	// dist/deploy/workers.json. Platform ensure/provisioning keys off this id.
	DurablesAdapterTemporal = "temporal"
)

// BuildsPrefix is the reserved public URL prefix every build-scoped path
// falls under.
const BuildsPrefix = "/_gobeyond/builds/"

// Well-known path segments immediately following a build ID.
const (
	KindAssets   = "assets"
	KindManifest = "manifest.json"
	KindStatic   = "static"
	KindRuntime  = "runtime"
	KindActions  = "actions"
)

// BuildRootURL is the public URL root for one build's artifacts.
func BuildRootURL(buildID string) string {
	return BuildsPrefix + buildID
}

// AssetBaseURL is the public URL root for one build's browser assets.
func AssetBaseURL(buildID string) string {
	return BuildRootURL(buildID) + "/" + KindAssets
}

// AssetURL is the public URL for one emitted browser asset file. file may be
// slash-separated and is normalized to forward slashes.
func AssetURL(buildID, file string) string {
	return AssetBaseURL(buildID) + "/" + strings.TrimPrefix(filepath.ToSlash(file), "/")
}

// ManifestURL is the public URL for a build's browser asset manifest.
func ManifestURL(buildID string) string {
	return BuildRootURL(buildID) + "/" + KindManifest
}

// StaticRouteURL is the public URL for one build's packaged static route data.
func StaticRouteURL(buildID, routeID string) string {
	return BuildRootURL(buildID) + "/" + KindStatic + "/" + routeID
}

// RuntimeURL is the public URL soft navigation fetches for one page route.
func RuntimeURL(buildID, routeID string) string {
	return BuildRootURL(buildID) + "/" + KindRuntime + "/" + routeID
}

// ActionURL is the public URL an action is submitted to.
func ActionURL(buildID, actionID string) string {
	return BuildRootURL(buildID) + "/" + KindActions + "/" + actionID
}

// BuildPathKind returns the path segment immediately following the build ID
// in a "/_gobeyond/builds/<id>/<kind>/..." request, e.g. "runtime",
// "actions", "assets", "static", or "manifest.json". It reports ok=false when
// path is not under the builds namespace or is missing that segment.
func BuildPathKind(path string) (kind string, ok bool) {
	trimmed := strings.TrimPrefix(path, BuildsPrefix)
	if trimmed == path {
		return "", false
	}
	_, after, found := strings.Cut(trimmed, "/")
	if !found {
		return "", false
	}
	kind, _, _ = strings.Cut(after, "/")
	if kind == "" {
		return "", false
	}
	return kind, true
}

// IsStaticArtifact reports whether path addresses a build's static-served
// artifact (browser assets, the manifest, or packaged static route data)
// rather than a dynamic runtime/action request that must reach the Go
// handler.
func IsStaticArtifact(path string) bool {
	switch kind, ok := BuildPathKind(path); {
	case !ok:
		return false
	case kind == KindAssets || kind == KindManifest || kind == KindStatic:
		return true
	default:
		return false
	}
}

// ParseRuntimePath parses a "/_gobeyond/builds/<id>/runtime/<routeId>"
// request path.
func ParseRuntimePath(path string) (buildID, routeID string, ok bool) {
	return parseBuildScoped(path, KindRuntime)
}

// ParseActionPath parses a "/_gobeyond/builds/<id>/actions/<actionId>"
// request path.
func ParseActionPath(path string) (buildID, actionID string, ok bool) {
	return parseBuildScoped(path, KindActions)
}

func parseBuildScoped(path, kind string) (buildID, id string, ok bool) {
	trimmed := strings.TrimPrefix(path, BuildsPrefix)
	if trimmed == path {
		return "", "", false
	}
	parts := strings.Split(trimmed, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] != kind || parts[2] == "" {
		return "", "", false
	}
	return parts[0], parts[2], true
}

// Disk paths. These mirror the URL structure above so a static-artifact URL
// can always be mapped onto disk by joining staticDir with the URL path.

// StaticBuildRoot is the on-disk root for one build's static artifacts.
func StaticBuildRoot(staticDir, buildID string) string {
	return filepath.Join(staticDir, "_gobeyond", "builds", buildID)
}

// AssetsDir is the on-disk directory browser assets are emitted into.
func AssetsDir(staticDir, buildID string) string {
	return filepath.Join(StaticBuildRoot(staticDir, buildID), "assets")
}

// ManifestPath is the on-disk location of a build's browser asset manifest.
func ManifestPath(staticDir, buildID string) string {
	return filepath.Join(StaticBuildRoot(staticDir, buildID), "manifest.json")
}

// StaticRoutePath is the on-disk location of one route's packaged static data.
func StaticRoutePath(staticDir, buildID, routeID string) string {
	return filepath.Join(StaticBuildRoot(staticDir, buildID), "static", routeID)
}
