package main

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/format"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	gb "github.com/Origens-Dev/gobeyond"
	"github.com/Origens-Dev/gobeyond/browserassets"
	"github.com/Origens-Dev/gobeyond/buildpaths"
	"github.com/Origens-Dev/gobeyond/codegen"
	"github.com/Origens-Dev/gobeyond/document"
	"github.com/Origens-Dev/gobeyond/internal/project"
	"github.com/Origens-Dev/gobeyond/renderer"
	"github.com/Origens-Dev/gobeyond/renderplan"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "gobeyond:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return usage()
	}
	root, err := findRoot()
	if err != nil {
		return err
	}
	switch args[0] {
	case "generate":
		set := flag.NewFlagSet("generate", flag.ContinueOnError)
		check := set.Bool("check", false, "fail if generated output is stale")
		goOnly := set.Bool("go-only", false, "materialize Go route projections without running the TypeScript compiler")
		if err := set.Parse(args[1:]); err != nil {
			return err
		}
		if *goOnly {
			if *check {
				return errors.New("generate --go-only cannot be combined with --check")
			}
			return generateGoSources(root)
		}
		return generate(root, *check)
	case "routes":
		website := websiteRoot(root)
		routes, err := project.Discover(website)
		if err != nil {
			return err
		}
		fmt.Printf("%-10s %-32s %s\n", "MODE", "ROUTE", "REASON")
		for _, route := range routes {
			fmt.Printf("%-10s %-32s %s\n", route.Mode, route.Pattern, route.Reason)
		}
		return nil
	case "doctor":
		return doctor(root)
	case "report":
		return reportCommand(root, args[1:])
	case "preview":
		return preview(root, args[1:])
	case "build":
		return build(root)
	case "dev":
		return dev(root, args[1:])
	case "add":
		return add(root, args[1:])
	default:
		return usage()
	}
}

func build(root string) error {
	return buildTo(root, filepath.Join(root, "dist"))
}

func buildTo(root, dist string) error {
	return buildToMode(root, dist, true)
}

func buildToMode(root, dist string, checkContracts bool) error {
	environment, err := projectEnvironment(websiteRoot(root), "production")
	if err != nil {
		return err
	}
	return buildToModeWithCompilerAndEnvironment(root, dist, checkContracts, "", environment, "production")
}

func buildToModeWithCompiler(root, dist string, checkContracts bool, preparedCompilerCLI string) error {
	environment, err := projectEnvironment(websiteRoot(root), "production")
	if err != nil {
		return err
	}
	return buildToModeWithCompilerAndEnvironment(root, dist, checkContracts, preparedCompilerCLI, environment, "production")
}

func buildToModeWithCompilerAndEnvironment(root, dist string, checkContracts bool, preparedCompilerCLI string, environment []string, browserMode string) error {
	projectRoot := websiteRoot(root)
	routes, err := project.Discover(projectRoot)
	if err != nil {
		return err
	}
	provisionalID, err := project.BuildID(root, routes)
	if err != nil {
		return err
	}
	manifest := project.Manifest{APIVersion: "gobeyond.routes/v1alpha1", BuildID: provisionalID, Routes: routes}
	if err := os.RemoveAll(dist); err != nil {
		return err
	}
	planDir := filepath.Join(dist, "server", "render-plans")
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		return err
	}
	compilerCLI := preparedCompilerCLI
	prerequisites := []buildTask{
		{
			name: "type-check website",
			run: func() error {
				return typecheckWebsite(root, projectRoot, environment)
			},
		},
	}
	if compilerCLI == "" {
		prerequisites = append([]buildTask{{
			name: "prepare portable compiler",
			run: func() error {
				var prepareErr error
				compilerCLI, prepareErr = prepareCompiler(root, environment)
				return prepareErr
			},
		}}, prerequisites...)
	} else if _, err := os.Stat(compilerCLI); err != nil {
		return fmt.Errorf("prepared portable compiler is unavailable: %w", err)
	}
	if err := runBuildTasks(prerequisites...); err != nil {
		return err
	}
	compiled, err := compilePortableProject(root, projectRoot, compilerCLI, manifest, planDir, environment)
	if err != nil {
		return err
	}
	if err := writeJSONFile(filepath.Join(projectRoot, ".gobeyond", "client-boundaries.json"), compiled.ClientBoundaries); err != nil {
		return fmt.Errorf("write client-boundary manifest: %w", err)
	}
	if err := syncContractFiles(projectRoot, compiled.Contracts, checkContracts); err != nil {
		return fmt.Errorf("generated contracts are stale; run gobeyond generate: %w", err)
	}
	contractDocument, err := codegen.Parse(compiled.Contracts)
	if err != nil {
		return err
	}
	manifest.BuildID, err = finalizedBuildID(provisionalID, compiled)
	if err != nil {
		return err
	}
	if err := project.Write(projectRoot, routes, manifest.BuildID, false); err != nil {
		return err
	}
	staticDir := filepath.Join(dist, "static")
	if err := os.MkdirAll(staticDir, 0o755); err != nil {
		return err
	}
	if err := copyTree(filepath.Join(projectRoot, "public"), staticDir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	generatedIconAssets, err := generateAppIcons(projectRoot, staticDir)
	if err != nil {
		return err
	}
	clientEntry, err := generateClientEntry(projectRoot, compiled.RouteModules, manifest.Routes)
	if err != nil {
		return err
	}
	serverTarget, err := serverBuildTarget(projectRoot)
	if err != nil {
		return err
	}
	serverOutput := filepath.Join(dist, "server", "gobeyond-server")
	if err := os.MkdirAll(filepath.Join(dist, "server", "runtime-data"), 0o755); err != nil {
		return err
	}
	middlewareTarget, err := middlewareBuildTarget(projectRoot)
	if err != nil {
		return err
	}
	var publicAssets []string
	tasks := []buildTask{
		{
			name: "build browser assets",
			run: func() error {
				return buildBrowserAssets(root, projectRoot, staticDir, manifest.BuildID, clientEntry.EntryFile, environment, browserMode)
			},
		},
		{
			name: "build Go server",
			run: func() error {
				return runCommandWithEnvironment(root, environment, "go", "build", "-trimpath", "-ldflags=-s -w", "-o", serverOutput, serverTarget)
			},
		},
		{
			name: "discover public assets",
			run: func() error {
				var discoverErr error
				publicAssets, discoverErr = discoverPublicAssets(filepath.Join(projectRoot, "public"))
				return discoverErr
			},
		},
	}
	if middlewareTarget != "" {
		middlewareOutput := filepath.Join(dist, buildpaths.MiddlewareDir, buildpaths.MiddlewareEntryName)
		if err := os.MkdirAll(filepath.Dir(middlewareOutput), 0o755); err != nil {
			return err
		}
		tasks = append(tasks, buildTask{
			name: "build middleware binary",
			run: func() error {
				// The middleware artifact must be statically linked so it
				// runs inside a scratch tenant sandbox.
				return runCommandWithEnvironment(root, withEnvironment(environment, "CGO_ENABLED=0"), "go", "build", "-trimpath", "-ldflags=-s -w", "-o", middlewareOutput, middlewareTarget)
			},
		})
	}
	if err := runBuildTasks(tasks...); err != nil {
		return err
	}
	publicAssets = mergeAssetPaths(publicAssets, generatedIconAssets)
	assetLayout := buildpaths.AssetLayout
	if middlewareTarget != "" {
		assetLayout = buildpaths.AssetLayoutV2
		if err := writeJSONFile(filepath.Join(dist, buildpaths.MiddlewareDir, buildpaths.MiddlewareManifestName), map[string]any{
			"v":        1,
			"entry":    buildpaths.MiddlewareEntryName,
			"matchers": []string{"/*"},
		}); err != nil {
			return err
		}
	}
	browserAssets, err := collectBrowserAssets(staticDir, manifest.BuildID, clientEntry)
	if err != nil {
		return err
	}
	if err := renderStaticDocuments(staticDir, planDir, manifest.BuildID, manifest.Routes, compiled.StaticBuild, contractDocument, browserAssets); err != nil {
		return err
	}
	portableRoutes := portableRouteManifest(projectRoot, manifest.Routes)
	manifestOutput := map[string]any{
		"apiVersion": "gobeyond.runtime/v1alpha1",
		"buildId":    manifest.BuildID,
		"routes":     portableRoutes,
		"react":      "19.2.8",
		"assets":     browserAssets,
	}
	if err := writeJSONFile(filepath.Join(dist, "server", "runtime-manifest.json"), manifestOutput); err != nil {
		return err
	}
	artifacts := map[string]any{
		"apiVersion":    "gobeyond.artifacts/v1alpha1",
		"buildId":       manifest.BuildID,
		"staticDir":     "static",
		"server":        "server/gobeyond-server",
		"renderPlans":   "server/render-plans",
		"browserAssets": browserAssets,
		"publicAssets":  publicAssets,
	}
	if err := writeJSONFile(filepath.Join(dist, "deploy", "artifacts.json"), artifacts); err != nil {
		return err
	}
	if err := writeBrowserBundleDiagnostics(filepath.Join(dist, "deploy", "browser-bundles.json"), staticDir, browserAssets); err != nil {
		return err
	}
	if err := writeJSONFile(filepath.Join(dist, "deploy", "route-trie.json"), map[string]any{
		"apiVersion":       "gobeyond.route-trie/v1alpha1",
		"buildId":          manifest.BuildID,
		"routes":           portableRoutes,
		"staticAssetPaths": publicAssets,
	}); err != nil {
		return err
	}
	if err := writeJSONFile(filepath.Join(dist, "deploy", "compatibility.json"), map[string]any{
		"apiVersion":        "gobeyond.compatibility/v1alpha1",
		"buildId":           manifest.BuildID,
		"react":             "19.2.8",
		"renderPlan":        "gobeyond.render/v1alpha1",
		"valueContract":     codegen.APIVersionV1Alpha1,
		"compilerProject":   "gobeyond.compiler-project/v1alpha1",
		"productionRuntime": "go",
		"assetLayout":       assetLayout,
	}); err != nil {
		return err
	}
	if err := writeJSONFile(buildpaths.ManifestPath(staticDir, manifest.BuildID), map[string]any{
		"apiVersion":  "gobeyond.browser-manifest/v1alpha1",
		"buildId":     manifest.BuildID,
		"react":       "19.2.8",
		"assets":      browserAssets,
		"clientEntry": browserAssets.ClientScript,
		"styles":      browserAssets.Styles,
		"routes":      portableRoutes,
	}); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dist, "deploy", "contracts.json"), append(compiled.Contracts, '\n'), 0o644); err != nil {
		return err
	}
	if err := writeJSONFile(filepath.Join(dist, "server", "runtime-data", "static-build.json"), compiled.StaticBuild); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dist, "server", "runtime-data", "contracts.json"), append(compiled.Contracts, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("GoBeyond build %s complete: %s\n", manifest.BuildID, dist)
	return nil
}

func portableRouteManifest(website string, routes []project.Route) []project.Route {
	portable := make([]project.Route, len(routes))
	copy(portable, routes)
	for i := range portable {
		for source, target := range map[*string]*string{
			&routes[i].PageFile:   &portable[i].PageFile,
			&routes[i].SchemaFile: &portable[i].SchemaFile,
			&routes[i].BuildFile:  &portable[i].BuildFile,
			&routes[i].ServerFile: &portable[i].ServerFile,
		} {
			if *source == "" {
				continue
			}
			if relative, err := filepath.Rel(website, filepath.FromSlash(*source)); err == nil {
				*target = filepath.ToSlash(relative)
			}
		}
	}
	return portable
}

type browserBuildInput struct {
	EntryFile    string
	RouteEntries map[string]string
}

func generateClientEntry(website string, modules []compilerRouteModules, manifestRoutes []project.Route) (browserBuildInput, error) {
	generatedDirectory := filepath.Join(website, ".gobeyond")
	if err := os.MkdirAll(generatedDirectory, 0o755); err != nil {
		return browserBuildInput{}, err
	}
	routeDirectory := filepath.Join(generatedDirectory, "routes")
	if err := os.RemoveAll(routeDirectory); err != nil {
		return browserBuildInput{}, err
	}
	if err := os.MkdirAll(routeDirectory, 0o755); err != nil {
		return browserBuildInput{}, err
	}
	patterns := make(map[string]string, len(manifestRoutes))
	for _, route := range manifestRoutes {
		patterns[route.ID] = route.Pattern
	}
	for _, route := range modules {
		if patterns[route.RouteID] == "" {
			return browserBuildInput{}, fmt.Errorf("browser route %s is missing its manifest pattern", route.RouteID)
		}
	}
	input := browserBuildInput{RouteEntries: make(map[string]string, len(modules))}
	var source strings.Builder
	source.WriteString("// Code generated by gobeyond build; DO NOT EDIT.\n")
	source.WriteString("import { bootstrapAsync } from '@go-beyond/react/browser'\n\n")
	source.WriteString("void bootstrapAsync({ routes: {\n")
	for _, route := range modules {
		fileName := routeModuleFileName(route.RouteID)
		routePath := filepath.Join(routeDirectory, fileName)
		if err := writeClientRouteModule(routePath, routeDirectory, website, route); err != nil {
			return browserBuildInput{}, err
		}
		input.RouteEntries[route.RouteID] = routePath
		fmt.Fprintf(&source, "  %q: { load: () => import(%q), pattern: %q },\n", route.RouteID, browserGeneratedImport(generatedDirectory, routePath), patterns[route.RouteID])
	}
	source.WriteString("} })\n")
	path := filepath.Join(generatedDirectory, "client-entry.tsx")
	if err := os.WriteFile(path, []byte(source.String()), 0o644); err != nil {
		return browserBuildInput{}, err
	}
	input.EntryFile = path
	return input, nil
}

func writeClientRouteModule(path, routeDirectory, website string, route compilerRouteModules) error {
	var source strings.Builder
	source.WriteString("// Code generated by gobeyond build; DO NOT EDIT.\n")
	source.WriteString("import { type ComponentType } from 'react'\n")
	fmt.Fprintf(&source, "import Page from %q\n", browserImport(routeDirectory, website, route.EntryFile))
	for index, layout := range route.LayoutFiles {
		fmt.Fprintf(&source, "import Layout%d from %q\n", index, browserImport(routeDirectory, website, layout))
	}
	source.WriteString("\nexport const page = Page as ComponentType<any>\n")
	source.WriteString("export const layouts = [")
	for index := range route.LayoutFiles {
		if index > 0 {
			source.WriteString(", ")
		}
		fmt.Fprintf(&source, "Layout%d as ComponentType<any>", index)
	}
	source.WriteString("] as const\n")
	// Default remains the page so legacy resolveRouteComponent(default) still works;
	// the runtime composes layouts + page for persistent nested layout identity.
	source.WriteString("export default Page\n")
	return os.WriteFile(path, []byte(source.String()), 0o644)
}

func routeModuleFileName(routeID string) string {
	var name strings.Builder
	for _, character := range routeID {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_' || character == '-' {
			name.WriteRune(character)
		} else {
			name.WriteByte('_')
		}
	}
	digest := sha256.Sum256([]byte(routeID))
	return fmt.Sprintf("%s-%s.tsx", name.String(), hex.EncodeToString(digest[:4]))
}

func browserGeneratedImport(fromDirectory, target string) string {
	relative, err := filepath.Rel(fromDirectory, target)
	if err != nil {
		relative = target
	}
	relative = filepath.ToSlash(relative)
	if extension := filepath.Ext(relative); extension != "" {
		relative = strings.TrimSuffix(relative, extension) + ".js"
	}
	if !strings.HasPrefix(relative, ".") {
		relative = "./" + relative
	}
	return relative
}

func browserImport(generatedDirectory, website, projectPath string) string {
	absolute := filepath.Join(website, filepath.FromSlash(projectPath))
	relative, err := filepath.Rel(generatedDirectory, absolute)
	if err != nil {
		relative = absolute
	}
	relative = filepath.ToSlash(relative)
	extension := filepath.Ext(relative)
	if extension != "" {
		relative = strings.TrimSuffix(relative, extension) + ".js"
	}
	if !strings.HasPrefix(relative, ".") {
		relative = "./" + relative
	}
	return relative
}

type browserBuildAssets = browserassets.Manifest

type viteManifestChunk struct {
	File           string   `json:"file"`
	Src            string   `json:"src"`
	IsEntry        bool     `json:"isEntry"`
	IsDynamicEntry bool     `json:"isDynamicEntry"`
	Imports        []string `json:"imports"`
	CSS            []string `json:"css"`
}

func collectBrowserAssets(staticDir, buildID string, input browserBuildInput) (browserBuildAssets, error) {
	assetRoot := buildpaths.AssetsDir(staticDir, buildID)
	assets := browserBuildAssets{Styles: []string{}}
	if _, err := os.Stat(assetRoot); errors.Is(err, os.ErrNotExist) {
		return assets, nil
	} else if err != nil {
		return assets, err
	}
	if _, err := os.Stat(filepath.Join(assetRoot, "app.js")); err == nil {
		assets.ClientScript = buildpaths.AssetURL(buildID, "app.js")
	}
	viteManifestPath := filepath.Join(assetRoot, ".vite", "manifest.json")
	viteSource, manifestErr := os.ReadFile(viteManifestPath)
	if manifestErr == nil {
		var viteManifest map[string]viteManifestChunk
		if err := json.Unmarshal(viteSource, &viteManifest); err != nil {
			return browserBuildAssets{}, fmt.Errorf("parse Vite browser manifest: %w", err)
		}
		projected, err := projectBrowserAssets(buildID, viteManifest, input)
		if err != nil {
			return browserBuildAssets{}, err
		}
		assets = projected
		if err := os.Remove(viteManifestPath); err != nil {
			return browserBuildAssets{}, fmt.Errorf("remove internal Vite manifest: %w", err)
		}
		_ = os.Remove(filepath.Dir(viteManifestPath))
	} else if !errors.Is(manifestErr, os.ErrNotExist) {
		return browserBuildAssets{}, manifestErr
	}
	if err := filepath.WalkDir(assetRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || strings.ToLower(filepath.Ext(entry.Name())) != ".css" {
			return nil
		}
		relative, err := filepath.Rel(assetRoot, path)
		if err != nil {
			return err
		}
		style := buildpaths.AssetURL(buildID, relative)
		if !containsString(assets.Styles, style) {
			assets.Styles = append(assets.Styles, style)
		}
		return nil
	}); err != nil {
		return browserBuildAssets{}, err
	}
	sort.Strings(assets.Styles)
	return assets, nil
}

func projectBrowserAssets(buildID string, manifest map[string]viteManifestChunk, input browserBuildInput) (browserBuildAssets, error) {
	entryKey, ok := viteChunkKey(manifest, input.EntryFile)
	if !ok {
		return browserBuildAssets{}, fmt.Errorf("Vite manifest is missing browser bootstrap %s", input.EntryFile)
	}
	entry := manifest[entryKey]
	if !entry.IsEntry || entry.File == "" {
		return browserBuildAssets{}, errors.New("Vite browser bootstrap is not an emitted entry")
	}
	bootstrap := viteChunkAssets(buildID, manifest, entryKey)
	assets := browserBuildAssets{
		APIVersion:   browserassets.APIVersionV1Alpha1,
		BuildID:      buildID,
		Bootstrap:    bootstrap,
		Routes:       make(map[string]browserassets.BrowserAssets, len(input.RouteEntries)),
		ClientScript: bootstrap.Bootstrap,
		Styles:       []string{},
	}
	for routeID, routeEntry := range input.RouteEntries {
		routeKey, ok := viteChunkKey(manifest, routeEntry)
		if !ok {
			return browserBuildAssets{}, fmt.Errorf("Vite manifest is missing browser route %s", routeID)
		}
		chunk := manifest[routeKey]
		if !chunk.IsDynamicEntry || chunk.File == "" {
			return browserBuildAssets{}, fmt.Errorf("Vite browser route %s is not an emitted dynamic entry", routeID)
		}
		assets.Routes[routeID] = viteChunkAssets(buildID, manifest, routeKey)
	}
	return assets, nil
}

func viteChunkKey(manifest map[string]viteManifestChunk, absoluteSource string) (string, bool) {
	absoluteSource = filepath.ToSlash(filepath.Clean(absoluteSource))
	for key, chunk := range manifest {
		source := filepath.ToSlash(filepath.Clean(chunk.Src))
		if source == absoluteSource || strings.HasSuffix(absoluteSource, "/"+strings.TrimPrefix(source, "./")) {
			return key, true
		}
	}
	return "", false
}

func viteChunkAssets(buildID string, manifest map[string]viteManifestChunk, rootKey string) browserassets.BrowserAssets {
	root := manifest[rootKey]
	preloads := []string{}
	styles := append([]string{}, root.CSS...)
	seen := map[string]bool{rootKey: true}
	var visit func(string)
	visit = func(key string) {
		if seen[key] {
			return
		}
		seen[key] = true
		chunk, ok := manifest[key]
		if !ok {
			return
		}
		if chunk.File != "" {
			preloads = append(preloads, browserAssetURL(buildID, chunk.File))
		}
		styles = append(styles, chunk.CSS...)
		for _, imported := range chunk.Imports {
			visit(imported)
		}
	}
	for _, imported := range root.Imports {
		visit(imported)
	}
	for index := range styles {
		styles[index] = browserAssetURL(buildID, styles[index])
	}
	sort.Strings(preloads)
	sort.Strings(styles)
	return browserassets.BrowserAssets{
		Bootstrap:      browserAssetURL(buildID, root.File),
		ModulePreloads: uniqueSortedStrings(preloads),
		Styles:         uniqueSortedStrings(styles),
	}
}

func browserAssetURL(buildID, file string) string {
	return buildpaths.AssetURL(buildID, file)
}

func uniqueSortedStrings(values []string) []string {
	sort.Strings(values)
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

type browserBundleSize struct {
	Bytes     int64 `json:"bytes"`
	GzipBytes int64 `json:"gzipBytes"`
}

type browserRouteBundleDiagnostics struct {
	Files []string `json:"files"`
	browserBundleSize
}

type browserBundleDiagnostics struct {
	APIVersion string                                   `json:"apiVersion"`
	BuildID    string                                   `json:"buildId"`
	Files      map[string]browserBundleSize             `json:"files"`
	Routes     map[string]browserRouteBundleDiagnostics `json:"routes"`
}

func writeBrowserBundleDiagnostics(path, staticDir string, assets browserBuildAssets) error {
	report := browserBundleDiagnostics{
		APIVersion: "gobeyond.browser-bundles/v1alpha1",
		BuildID:    assets.BuildID,
		Files:      map[string]browserBundleSize{},
		Routes:     map[string]browserRouteBundleDiagnostics{},
	}
	routeIDs := make([]string, 0, len(assets.Routes))
	for routeID := range assets.Routes {
		routeIDs = append(routeIDs, routeID)
	}
	sort.Strings(routeIDs)
	for _, routeID := range routeIDs {
		routeAssets, err := assets.ForRoute(routeID)
		if err != nil {
			return err
		}
		files := append([]string{routeAssets.Bootstrap}, routeAssets.ModulePreloads...)
		files = append(files, routeAssets.Styles...)
		files = uniqueSortedStrings(files)
		routeReport := browserRouteBundleDiagnostics{Files: files}
		for _, file := range files {
			size, ok := report.Files[file]
			if !ok {
				var sizeErr error
				size, sizeErr = browserBundleFileSize(staticDir, file)
				if sizeErr != nil {
					return sizeErr
				}
				report.Files[file] = size
			}
			routeReport.Bytes += size.Bytes
			routeReport.GzipBytes += size.GzipBytes
		}
		report.Routes[routeID] = routeReport
		fmt.Printf("browser route %-40s %8d bytes (%8d gzip)\n", routeID, routeReport.Bytes, routeReport.GzipBytes)
	}
	return writeJSONFile(path, report)
}

func browserBundleFileSize(staticDir, assetURL string) (browserBundleSize, error) {
	const prefix = "/_gobeyond/"
	if !strings.HasPrefix(assetURL, prefix) {
		return browserBundleSize{}, fmt.Errorf("browser bundle asset is outside the GoBeyond namespace: %s", assetURL)
	}
	file := filepath.Join(staticDir, filepath.FromSlash(strings.TrimPrefix(assetURL, "/")))
	data, err := os.ReadFile(file)
	if err != nil {
		return browserBundleSize{}, fmt.Errorf("read browser bundle %s: %w", assetURL, err)
	}
	var compressed bytes.Buffer
	writer, err := gzip.NewWriterLevel(&compressed, gzip.BestCompression)
	if err != nil {
		return browserBundleSize{}, err
	}
	if _, err := writer.Write(data); err != nil {
		return browserBundleSize{}, err
	}
	if err := writer.Close(); err != nil {
		return browserBundleSize{}, err
	}
	return browserBundleSize{Bytes: int64(len(data)), GzipBytes: int64(compressed.Len())}, nil
}

func discoverPublicAssets(publicDir string) ([]string, error) {
	assets := []string{}
	if _, err := os.Stat(publicDir); errors.Is(err, os.ErrNotExist) {
		return assets, nil
	} else if err != nil {
		return nil, err
	}
	if err := filepath.WalkDir(publicDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(publicDir, path)
		if err != nil {
			return err
		}
		assets = append(assets, "/"+filepath.ToSlash(relative))
		return nil
	}); err != nil {
		return nil, err
	}
	sort.Strings(assets)
	return assets, nil
}

func renderStaticDocuments(staticDir, planDir, buildID string, routes []project.Route, artifact compilerStaticBuild, contracts codegen.Document, assets browserBuildAssets) error {
	patterns := make(map[string]string, len(routes))
	for _, route := range routes {
		patterns[route.ID] = route.Pattern
	}
	for _, staticRoute := range artifact.Routes {
		planData, err := os.ReadFile(filepath.Join(planDir, staticRoute.RouteID+".json"))
		if err != nil {
			return err
		}
		plan, err := renderplan.Parse(planData)
		if err != nil {
			return err
		}
		for _, entry := range staticRoute.Entries {
			routeAssets, err := assets.ForRoute(staticRoute.RouteID)
			if err != nil {
				return fmt.Errorf("resolve browser assets for %s: %w", staticRoute.RouteID, err)
			}
			props, err := decodeJSONValue(entry.Props)
			if err != nil {
				return fmt.Errorf("decode static props for %s: %w", staticRoute.RouteID, err)
			}
			props, err = trustStaticSafeHTML(contracts, staticRoute.RouteID, props)
			if err != nil {
				return fmt.Errorf("trust static SafeHTML for %s: %w", staticRoute.RouteID, err)
			}
			body, renderNow, err := renderer.New().RenderAt(plan, props, time.Time{})
			if err != nil {
				return fmt.Errorf("render static route %s: %w", staticRoute.RouteID, err)
			}
			metadata := gb.Metadata{Lang: "en", Title: staticRoute.RouteID}
			indexable := len(entry.Metadata) > 0 && string(entry.Metadata) != "null"
			if indexable {
				if err := json.Unmarshal(entry.Metadata, &metadata); err != nil {
					return fmt.Errorf("decode static metadata for %s: %w", staticRoute.RouteID, err)
				}
			}
			publicOrigin := "https://invalid.gobeyond.local"
			if canonical, parseErr := url.Parse(metadata.Canonical); parseErr == nil && canonical.Scheme != "" && canonical.Host != "" {
				publicOrigin = canonical.Scheme + "://" + canonical.Host
			}
			var output bytes.Buffer
			styles := make([]document.Asset, len(routeAssets.Styles))
			for index, style := range routeAssets.Styles {
				styles[index] = document.Asset{URL: style}
			}
			modulePreloads := make([]document.Asset, len(routeAssets.ModulePreloads))
			for index, module := range routeAssets.ModulePreloads {
				modulePreloads[index] = document.Asset{URL: module}
			}
			scripts := []document.Asset{}
			if routeAssets.Bootstrap != "" {
				scripts = append(scripts, document.Asset{URL: routeAssets.Bootstrap})
			}
			renderLocale := metadata.Lang
			if renderLocale == "" {
				renderLocale = "en"
			}
			if err := document.Render(&output, document.Input{
				PublicOrigin: publicOrigin,
				Indexable:    indexable,
				Metadata:     metadata,
				Body:         document.BodyHTML(body),
				Hydration: document.HydrationData{
					BuildID:      buildID,
					RouteID:      staticRoute.RouteID,
					Props:        props,
					RenderNow:    renderNow.Format(time.RFC3339Nano),
					RenderLocale: renderLocale,
				},
				Styles:         styles,
				ModulePreloads: modulePreloads,
				Scripts:        scripts,
			}); err != nil {
				return fmt.Errorf("render static document %s: %w", staticRoute.RouteID, err)
			}
			publicPath, err := expandStaticPattern(patterns[staticRoute.RouteID], entry.Params)
			if err != nil {
				return err
			}
			destination := filepath.Join(staticDir, filepath.FromSlash(strings.Trim(publicPath, "/")), "index.html")
			if publicPath == "/" {
				destination = filepath.Join(staticDir, "index.html")
			}
			if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(destination, output.Bytes(), 0o644); err != nil {
				return err
			}
		}
		if err := writeJSONFile(buildpaths.StaticRoutePath(staticDir, buildID, staticRoute.RouteID), staticRoute); err != nil {
			return err
		}
	}
	return nil
}

func trustStaticSafeHTML(document codegen.Document, routeID string, props any) (any, error) {
	return codegen.TrustStaticSafeHTML(document, routeID, props)
}

func decodeJSONValue(raw json.RawMessage) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

func expandStaticPattern(pattern string, params map[string]any) (string, error) {
	if pattern == "" {
		return "", errors.New("static route pattern is missing")
	}
	parts := strings.Split(strings.Trim(pattern, "/"), "/")
	if pattern == "/" {
		return "/", nil
	}
	output := make([]string, 0, len(parts))
	for _, part := range parts {
		name := ""
		catchAll := false
		optional := false
		switch {
		case strings.HasPrefix(part, "[[...") && strings.HasSuffix(part, "]]"):
			name, catchAll, optional = part[5:len(part)-2], true, true
		case strings.HasPrefix(part, "[...") && strings.HasSuffix(part, "]"):
			name, catchAll = part[4:len(part)-1], true
		case strings.HasPrefix(part, "[") && strings.HasSuffix(part, "]"):
			name = part[1 : len(part)-1]
		default:
			output = append(output, part)
			continue
		}
		value, exists := params[name]
		if optional && !exists {
			continue
		}
		if catchAll {
			values, ok := value.([]any)
			if !ok {
				if stringsValue, stringsOK := value.([]string); stringsOK {
					for _, item := range stringsValue {
						output = append(output, url.PathEscape(item))
					}
					continue
				}
				return "", fmt.Errorf("static route %s parameter %s must be an array", pattern, name)
			}
			for _, item := range values {
				text, ok := item.(string)
				if !ok {
					return "", fmt.Errorf("static route %s parameter %s must contain strings", pattern, name)
				}
				output = append(output, url.PathEscape(text))
			}
			continue
		}
		text, ok := value.(string)
		if !exists || !ok {
			return "", fmt.Errorf("static route %s parameter %s is missing", pattern, name)
		}
		output = append(output, url.PathEscape(text))
	}
	return "/" + strings.Join(output, "/"), nil
}

type compilerProjectConfig struct {
	ProjectRoot  string                 `json:"projectRoot"`
	AppDirectory string                 `json:"appDirectory,omitempty"`
	SourceRoots  []compilerSourceRoot   `json:"sourceRoots,omitempty"`
	Routes       []compilerProjectRoute `json:"routes"`
}

type compilerSourceRoot struct {
	Prefix    string `json:"prefix"`
	Directory string `json:"directory"`
}

type compilerProjectRoute struct {
	RouteID      string `json:"routeId"`
	EntryFile    string `json:"entryFile"`
	SchemaFile   string `json:"schemaFile,omitempty"`
	ActionsFile  string `json:"actionsFile,omitempty"`
	BuildFile    string `json:"buildFile,omitempty"`
	Kind         string `json:"kind,omitempty"`
	RoutePattern string `json:"routePattern,omitempty"`
}

type compilerProjectOutput struct {
	APIVersion       string                         `json:"apiVersion"`
	Plans            []json.RawMessage              `json:"plans"`
	Contracts        json.RawMessage                `json:"contracts"`
	RouteModules     []compilerRouteModules         `json:"routeModules"`
	StaticBuild      compilerStaticBuild            `json:"staticBuild"`
	ClientBoundaries compilerClientBoundaryManifest `json:"clientBoundaries"`
}

type compilerClientBoundaryManifest struct {
	APIVersion         string                         `json:"apiVersion"`
	Boundaries         []compilerClientBoundaryRecord `json:"boundaries"`
	UseIDSites         []compilerUseIDSiteRecord      `json:"useIdSites"`
	DateIntrinsicSites []compilerDateIntrinsicSite    `json:"dateIntrinsicSites"`
}

type compilerClientBoundaryRecord struct {
	ID        string `json:"id"`
	RouteID   string `json:"routeId"`
	Source    string `json:"source"`
	Component string `json:"component"`
	Boundary  string `json:"boundary"`
	Reason    string `json:"reason"`
	Target    string `json:"target"`
	Start     int    `json:"start"`
	End       int    `json:"end"`
	Line      int    `json:"line"`
	Column    int    `json:"column"`
}

type compilerUseIDSiteRecord struct {
	ID              string `json:"id"`
	RouteID         string `json:"routeId"`
	Source          string `json:"source"`
	Start           int    `json:"start"`
	End             int    `json:"end"`
	Line            int    `json:"line"`
	Column          int    `json:"column"`
	KeyExpression   string `json:"keyExpression,omitempty"`
	SkipViteRewrite bool   `json:"skipViteRewrite,omitempty"`
}

type compilerDateIntrinsicSite struct {
	RouteID string `json:"routeId"`
	Source  string `json:"source"`
	Start   int    `json:"start"`
	End     int    `json:"end"`
	Line    int    `json:"line"`
	Column  int    `json:"column"`
	Getter  string `json:"getter"`
}

type compilerRouteModules struct {
	RouteID     string   `json:"routeId"`
	EntryFile   string   `json:"entryFile"`
	LayoutFiles []string `json:"layoutFiles"`
}

type compilerStaticBuild struct {
	APIVersion string                `json:"apiVersion"`
	Routes     []compilerStaticRoute `json:"routes"`
}

type compilerStaticRoute struct {
	RouteID      string                `json:"routeId"`
	BuildFile    string                `json:"buildFile,omitempty"`
	MetadataFile string                `json:"metadataFile,omitempty"`
	LayoutFiles  []string              `json:"layoutFiles"`
	Entries      []compilerStaticEntry `json:"entries"`
}

type compilerStaticEntry struct {
	Params   map[string]any  `json:"params"`
	Props    json.RawMessage `json:"props"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

func compilePortableProject(root, website, compilerCLI string, manifest project.Manifest, planDir string, environment []string) (*compilerProjectOutput, error) {
	temporary, err := os.MkdirTemp("", "gobeyond-compiler-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(temporary)

	config := compilerProjectConfig{
		ProjectRoot:  website,
		AppDirectory: "app",
		SourceRoots:  []compilerSourceRoot{{Prefix: "@/", Directory: "."}},
		Routes:       make([]compilerProjectRoute, 0, len(manifest.Routes)),
	}
	for _, route := range manifest.Routes {
		entry, relErr := filepath.Rel(website, route.PageFile)
		if relErr != nil {
			return nil, relErr
		}
		compiledRoute := compilerProjectRoute{RouteID: route.ID, EntryFile: filepath.ToSlash(entry), Kind: route.Mode, RoutePattern: route.Pattern}
		if route.SchemaFile != "" {
			schema, relErr := filepath.Rel(website, route.SchemaFile)
			if relErr != nil {
				return nil, relErr
			}
			compiledRoute.SchemaFile = filepath.ToSlash(schema)
		}
		if route.BuildFile != "" {
			buildFile, relErr := filepath.Rel(website, route.BuildFile)
			if relErr != nil {
				return nil, relErr
			}
			compiledRoute.BuildFile = filepath.ToSlash(buildFile)
		}
		actions := filepath.Join(filepath.Dir(route.PageFile), "actions.ts")
		if _, statErr := os.Stat(actions); statErr == nil {
			relative, relErr := filepath.Rel(website, actions)
			if relErr != nil {
				return nil, relErr
			}
			compiledRoute.ActionsFile = filepath.ToSlash(relative)
		}
		config.Routes = append(config.Routes, compiledRoute)
	}
	configPath := filepath.Join(temporary, "project.json")
	outputPath := filepath.Join(temporary, "output.json")
	if err := writeJSONFile(configPath, config); err != nil {
		return nil, err
	}
	command := exec.Command("node", compilerCLI, "--project", configPath, "--out", outputPath)
	command.Dir = root
	command.Env = environment
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("compile website: %w", err)
	}
	encoded, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, err
	}
	var output compilerProjectOutput
	if err := json.Unmarshal(encoded, &output); err != nil {
		return nil, fmt.Errorf("decode compiler output: %w", err)
	}
	if output.APIVersion != "gobeyond.compiler-project/v1alpha1" || output.StaticBuild.APIVersion != "gobeyond.static-build/v1alpha1" || output.ClientBoundaries.APIVersion != "gobeyond.client-boundaries/v1alpha1" || len(output.Plans) != len(manifest.Routes) || len(output.RouteModules) != len(manifest.Routes) || len(output.Contracts) == 0 {
		return nil, errors.New("portable compiler returned an incomplete or incompatible project")
	}
	for _, boundary := range output.ClientBoundaries.Boundaries {
		fmt.Fprintf(os.Stderr, "GoBeyond browser-only downgrade: %s (%s) at %s:%d:%d; boundary %s; %s\n", boundary.Component, boundary.RouteID, boundary.Source, boundary.Line, boundary.Column, boundary.Boundary, boundary.Reason)
	}
	if err := os.MkdirAll(planDir, 0o755); err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(output.Plans))
	for _, raw := range output.Plans {
		var identity struct {
			RouteID string `json:"routeId"`
		}
		if err := json.Unmarshal(raw, &identity); err != nil || identity.RouteID == "" {
			return nil, errors.New("compiler emitted a render plan without a route ID")
		}
		if _, exists := seen[identity.RouteID]; exists {
			return nil, fmt.Errorf("compiler emitted duplicate route plan %s", identity.RouteID)
		}
		seen[identity.RouteID] = struct{}{}
		var formatted bytes.Buffer
		if err := json.Indent(&formatted, raw, "", "  "); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(planDir, identity.RouteID+".json"), append(formatted.Bytes(), '\n'), 0o644); err != nil {
			return nil, err
		}
	}
	return &output, nil
}

func syncContractFiles(website string, contracts json.RawMessage, check bool) error {
	document, err := codegen.Parse(contracts)
	if err != nil {
		return err
	}
	files, err := codegen.Generate(document, codegen.Options{})
	if err != nil {
		return err
	}
	expected := make(map[string]struct{}, len(files))
	for relative, content := range files {
		path := filepath.Join(website, filepath.FromSlash(relative))
		expected[filepath.Clean(path)] = struct{}{}
		existing, readErr := os.ReadFile(path)
		if check {
			if readErr != nil || !bytes.Equal(existing, content) {
				return fmt.Errorf("generated output is stale: %s", path)
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
	generatedRoot := filepath.Join(website, "internal", "gobeyondgen", "contracts")
	if err := filepath.WalkDir(generatedRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".gobeyond_gen.go") {
			return nil
		}
		if _, ok := expected[filepath.Clean(path)]; ok {
			return nil
		}
		if check {
			return fmt.Errorf("orphaned generated output is stale: %s", path)
		}
		return os.Remove(path)
	}); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func websiteRoot(root string) string {
	if _, err := os.Stat(filepath.Join(root, "app")); err == nil {
		return root
	}
	return filepath.Join(root, "examples", "seo-site")
}

func generate(root string, check bool) error {
	website := websiteRoot(root)
	environment, err := projectEnvironment(website, "production")
	if err != nil {
		return err
	}
	routes, err := project.Discover(website)
	if err != nil {
		return err
	}
	provisionalID, err := project.BuildID(root, routes)
	if err != nil {
		return err
	}
	manifest := project.Manifest{APIVersion: "gobeyond.routes/v1alpha1", BuildID: provisionalID, Routes: routes}
	compilerCLI, err := prepareCompiler(root, environment)
	if err != nil {
		return err
	}
	temporary, err := os.MkdirTemp("", "gobeyond-generate-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	compiled, err := compilePortableProject(root, website, compilerCLI, manifest, filepath.Join(temporary, "plans"), environment)
	if err != nil {
		return err
	}
	finalID, err := finalizedBuildID(provisionalID, compiled)
	if err != nil {
		return err
	}
	if err := project.Write(website, routes, finalID, check); err != nil {
		return err
	}
	return syncContractFiles(website, compiled.Contracts, check)
}

func generateGoSources(root string) error {
	website := websiteRoot(root)
	routes, err := project.Discover(website)
	if err != nil {
		return err
	}
	return project.SyncGoSources(website, routes, false)
}

func finalizedBuildID(sourceID string, compiled *compilerProjectOutput) (string, error) {
	encoded, err := json.Marshal(struct {
		Version          string                         `json:"version"`
		SourceID         string                         `json:"sourceId"`
		React            string                         `json:"react"`
		Plans            []json.RawMessage              `json:"plans"`
		Contracts        json.RawMessage                `json:"contracts"`
		RouteModules     []compilerRouteModules         `json:"routeModules"`
		StaticBuild      compilerStaticBuild            `json:"staticBuild"`
		ClientBoundaries compilerClientBoundaryManifest `json:"clientBoundaries"`
	}{
		Version: "gobeyond-build/v2", SourceID: sourceID, React: "19.2.8",
		Plans: compiled.Plans, Contracts: compiled.Contracts,
		RouteModules: compiled.RouteModules, StaticBuild: compiled.StaticBuild,
		ClientBoundaries: compiled.ClientBoundaries,
	})
	if err != nil {
		return "", fmt.Errorf("encode final build inputs: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return "b_" + hex.EncodeToString(digest[:8]), nil
}

func prepareCompiler(root string, environment []string) (string, error) {
	developmentConfig := filepath.Join(root, "packages", "compiler", "tsconfig.json")
	if _, err := os.Stat(developmentConfig); err == nil {
		if err := runCommandWithEnvironment(root, environment, filepath.Join(root, "node_modules", ".bin", "tsc"), "-p", developmentConfig); err != nil {
			return "", fmt.Errorf("build portable compiler: %w", err)
		}
	}
	return preparedCompilerCLI(root)
}

func preparedCompilerCLI(root string) (string, error) {
	developmentCLI := filepath.Join(root, "packages", "compiler", "dist", "src", "cli.js")
	if _, err := os.Stat(developmentCLI); err == nil {
		return developmentCLI, nil
	}
	installedCLI := filepath.Join(root, "node_modules", "@go-beyond", "compiler", "dist", "src", "cli.js")
	if _, err := os.Stat(installedCLI); err == nil {
		return installedCLI, nil
	}
	return "", errors.New("@go-beyond/compiler is not installed; install dependencies before generating or building")
}

func typecheckWebsite(root, website string, environment []string) error {
	config := filepath.Join(website, "tsconfig.json")
	if _, err := os.Stat(config); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("website tsconfig.json is required for the production type-check gate")
		}
		return err
	}
	tsc := filepath.Join(root, "node_modules", ".bin", "tsc")
	if _, err := os.Stat(tsc); err != nil {
		tsc = filepath.Join(website, "node_modules", ".bin", "tsc")
	}
	if err := runCommandWithEnvironment(website, environment, tsc, "-p", config, "--noEmit"); err != nil {
		return err
	}
	return nil
}

type buildTask struct {
	name string
	run  func() error
}

func runBuildTasks(tasks ...buildTask) error {
	errorsByTask := make([]error, len(tasks))
	var group sync.WaitGroup
	group.Add(len(tasks))
	for index, task := range tasks {
		index, task := index, task
		go func() {
			defer group.Done()
			errorsByTask[index] = task.run()
		}()
	}
	group.Wait()
	for index, taskErr := range errorsByTask {
		if taskErr != nil {
			return fmt.Errorf("%s: %w", tasks[index].name, taskErr)
		}
	}
	return nil
}

func buildBrowserAssets(root, website, staticDir, buildID, clientEntry string, environment []string, mode string) error {
	config := filepath.Join(website, "vite.config.ts")
	if _, err := os.Stat(config); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	vite := filepath.Join(root, "node_modules", ".bin", "vite")
	command := exec.Command(vite, viteBuildArguments(config, buildID, mode)...)
	command.Dir = website
	command.Env = withEnvironment(environment,
		"GOBEYOND_BUILD_ID="+buildID,
		"GOBEYOND_STATIC_OUT="+buildpaths.AssetsDir(staticDir, buildID),
		"GOBEYOND_CLIENT_ENTRY="+clientEntry,
		"GOBEYOND_CLIENT_BOUNDARIES="+filepath.Join(website, ".gobeyond", "client-boundaries.json"),
		"GOBEYOND_MODE="+mode,
		"NODE_ENV="+browserNodeEnvironment(mode),
	)
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	return command.Run()
}

func viteBuildArguments(config, buildID, mode string) []string {
	return []string{
		"build",
		"--config",
		config,
		"--manifest",
		"--mode",
		mode,
		"--base",
		buildpaths.AssetBaseURL(buildID) + "/",
	}
}

func browserNodeEnvironment(mode string) string {
	if mode == "development" {
		return "development"
	}
	return "production"
}

func serverBuildTarget(website string) (string, error) {
	target := filepath.Join(website, "server", "cmd", "app")
	if _, err := os.Stat(target); err == nil {
		return target, nil
	}
	target = filepath.Join(website, "server", "cmd", "site")
	if _, err := os.Stat(target); err == nil {
		return target, nil
	}
	return "", errors.New("production server entry is missing; add server/cmd/app or server/cmd/site")
}

// middlewareBuildTarget reports the optional middleware artifact entry
// (server/cmd/middleware). Projects without one produce a v1 layout.
func middlewareBuildTarget(website string) (string, error) {
	target := filepath.Join(website, "server", "cmd", "middleware")
	info, err := os.Stat(target)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("server/cmd/middleware must be a directory containing the middleware main package")
	}
	return target, nil
}

func runCommand(directory, name string, args ...string) error {
	return runCommandWithEnvironment(directory, os.Environ(), name, args...)
}

func runCommandWithEnvironment(directory string, environment []string, name string, args ...string) error {
	command := exec.Command(name, args...)
	command.Dir = directory
	command.Env = environment
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	return command.Run()
}

func copyTree(source, destination string) error {
	return filepath.Walk(source, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		defer input.Close()
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(output, input)
		closeErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}

func writeJSONFile(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func preview(root string, args []string) error {
	set := flag.NewFlagSet("preview", flag.ContinueOnError)
	address := set.String("addr", ":8080", "listen address")
	if err := set.Parse(args); err != nil {
		return err
	}
	serverBinary := filepath.Join(root, "dist", "server", "gobeyond-server")
	manifestPath := filepath.Join(root, "dist", "server", "runtime-manifest.json")
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return errors.New("production build does not exist; run gobeyond build first")
	}
	var manifest struct {
		BuildID string `json:"buildId"`
	}
	if err := json.Unmarshal(manifestData, &manifest); err != nil || manifest.BuildID == "" {
		return errors.New("production runtime manifest is invalid")
	}
	if _, err := os.Stat(serverBinary); err != nil {
		return errors.New("production server executable does not exist; run gobeyond build first")
	}
	environment, err := projectEnvironment(websiteRoot(root), "production")
	if err != nil {
		return err
	}
	host := *address
	if strings.HasPrefix(host, ":") {
		host = "localhost" + host
	} else if strings.HasPrefix(host, "0.0.0.0:") {
		host = "localhost:" + strings.TrimPrefix(host, "0.0.0.0:")
	}
	command := exec.Command(serverBinary)
	command.Dir = root
	command.Env = withEnvironment(environment,
		"GOBEYOND_ADDR="+*address,
		"GOBEYOND_BUILD_ID="+manifest.BuildID,
		"GOBEYOND_PUBLIC_ORIGIN=http://"+host,
		"GOBEYOND_PLAN_DIR="+filepath.Join(root, "dist", "server", "render-plans"),
		"GOBEYOND_RUNTIME_DATA_DIR="+filepath.Join(root, "dist", "server", "runtime-data"),
		"GOBEYOND_STATIC_DIR="+filepath.Join(root, "dist", "static"),
	)
	command.Stdout, command.Stderr, command.Stdin = os.Stdout, os.Stderr, os.Stdin
	fmt.Println("GoBeyond preview listening on", *address)
	return command.Run()
}

func add(root string, args []string) error {
	if len(args) < 2 {
		return errors.New("usage: gobeyond add <page|dynamic|api|action> <route> [name]")
	}
	kind, route := args[0], strings.Trim(args[1], "/")
	if route == "" && kind != "page" {
		return errors.New("route cannot be empty")
	}
	if kind != "api" && (route == "api" || strings.HasPrefix(route, "api/")) {
		return errors.New("app/api is reserved for Go API route.go files; choose a different page route")
	}
	if err := validateCLIRoute(route); err != nil {
		return err
	}
	moduleRoot := root
	website := websiteRoot(root)
	appDir := filepath.Join(website, "app", filepath.FromSlash(route))
	routeInfo, err := routeForCLI(route)
	if err != nil {
		return err
	}
	switch kind {
	case "page":
		return writeAndSyncScaffolds(website, []scaffoldFile{
			{path: filepath.Join(appDir, "page.tsx"), content: []byte(pageTemplate(route))},
			{path: filepath.Join(appDir, "page.schema.ts"), content: []byte(schemaTemplate())},
		})
	case "dynamic":
		contractImport, err := generatedContractImport(moduleRoot, website, "routes", routeInfo.ID)
		if err != nil {
			return err
		}
		return writeAndSyncScaffolds(website, []scaffoldFile{
			{path: filepath.Join(appDir, "page.tsx"), content: []byte(pageTemplate(route)), preserve: true},
			{path: filepath.Join(appDir, "page.schema.ts"), content: []byte(schemaTemplate()), preserve: true},
			{path: filepath.Join(appDir, "page.go"), content: []byte(goPageTemplate(routeInfo, contractImport)), preserve: true},
		})
	case "api":
		return writeAndSyncScaffolds(website, []scaffoldFile{{
			path:    filepath.Join(website, "app", "api", filepath.FromSlash(route), "route.go"),
			content: []byte(apiTemplate(routeInfo.ServerKey)),
		}})
	case "action":
		if len(args) < 3 {
			return errors.New("usage: gobeyond add action <route> <name>")
		}
		actionName, err := validActionName(args[2])
		if err != nil {
			return err
		}
		pagePath := filepath.Join(appDir, "page.tsx")
		if _, err := os.Stat(pagePath); errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("cannot add an action for %q: create %s first with gobeyond add page %s", routeInfo.Pattern, pagePath, route)
		} else if err != nil {
			return err
		}
		actionSource, changed, err := mergeActionSource(filepath.Join(appDir, "actions.ts"), actionName)
		if err != nil {
			return err
		}
		contractImport, err := generatedContractImport(moduleRoot, website, "actions", routeInfo.ID+":"+actionName)
		if err != nil {
			return err
		}
		actionGo, goChanged, err := mergeActionGoSource(filepath.Join(appDir, "actions.go"), routeInfo.ServerKey, actionName, contractImport)
		if err != nil {
			return err
		}
		if changed && !goChanged {
			return fmt.Errorf("action name %q collides with an existing Go handler %s; choose a name with a distinct exported Go identifier", actionName, exportedActionName(actionName))
		}
		files := []scaffoldFile{}
		if goChanged {
			files = append(files, scaffoldFile{path: filepath.Join(appDir, "actions.go"), content: actionGo, replace: true})
		}
		if changed {
			files = append(files, scaffoldFile{path: filepath.Join(appDir, "actions.ts"), content: actionSource, replace: true})
		}
		return writeAndSyncScaffolds(website, files)
	default:
		return errors.New("unknown add target: " + kind)
	}
}

func writeAndSyncScaffolds(website string, files []scaffoldFile) error {
	if err := writeScaffolds(files); err != nil {
		return err
	}
	routes, err := project.Discover(website)
	if err != nil {
		return err
	}
	return project.SyncGoSources(website, routes, false)
}

func findRoot() (string, error) {
	current, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		moduleFile := filepath.Join(current, "go.mod")
		if data, err := os.ReadFile(moduleFile); err == nil {
			if !bytes.HasPrefix(data, []byte("// Code generated by gobeyond route tooling; DO NOT EDIT.")) {
				return current, nil
			}
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", errors.New("could not find a GoBeyond project root")
		}
		current = parent
	}
}

func usage() error {
	return errors.New("usage: gobeyond <dev|build|preview|generate|routes|doctor|report|add>")
}

// reportCommand prints a portability summary from a prior compiler-project JSON
// artifact (plans + clientBoundaries). Prefer `gobeyond-compile report-portability`
// during package development; this wraps the same data for app roots.
func reportCommand(root string, args []string) error {
	if len(args) == 0 || args[0] != "portability" {
		return errors.New("usage: gobeyond report portability [--project <compiler-output.json>]")
	}
	projectPath := ""
	rest := args[1:]
	for i := 0; i < len(rest); i++ {
		if rest[i] == "--project" {
			if i+1 >= len(rest) {
				return errors.New("usage: gobeyond report portability [--project <compiler-output.json>]")
			}
			projectPath = rest[i+1]
			i++
		}
	}
	if projectPath == "" {
		candidates := []string{
			filepath.Join(root, "dist", "server", "compiler-output.json"),
			filepath.Join(root, "dist", "compiler-output.json"),
		}
		for _, candidate := range candidates {
			if _, err := os.Stat(candidate); err == nil {
				projectPath = candidate
				break
			}
		}
	}
	if projectPath == "" {
		return errors.New("gobeyond report portability: pass --project <compiler-output.json> (gobeyond.compiler-project/v1alpha1)")
	}
	raw, err := os.ReadFile(projectPath)
	if err != nil {
		return err
	}
	var payload struct {
		Plans            []json.RawMessage `json:"plans"`
		ClientBoundaries struct {
			Boundaries []struct {
				RouteID          string  `json:"routeId"`
				Component        string  `json:"component"`
				Source           string  `json:"source"`
				Reason           string  `json:"reason"`
				TriggerCode      string  `json:"triggerCode"`
				TriggerConstruct string  `json:"triggerConstruct"`
				Suggestion       string  `json:"suggestion"`
				MarkupLostShare  float64 `json:"markupLostShare"`
			} `json:"boundaries"`
		} `json:"clientBoundaries"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return fmt.Errorf("parse compiler output: %w", err)
	}
	if len(payload.ClientBoundaries.Boundaries) == 0 {
		fmt.Println("No client-boundary downgrades recorded.")
		return nil
	}
	fmt.Println("Portability report")
	fmt.Println()
	byRoute := map[string][]struct {
		RouteID          string  `json:"routeId"`
		Component        string  `json:"component"`
		Source           string  `json:"source"`
		Reason           string  `json:"reason"`
		TriggerCode      string  `json:"triggerCode"`
		TriggerConstruct string  `json:"triggerConstruct"`
		Suggestion       string  `json:"suggestion"`
		MarkupLostShare  float64 `json:"markupLostShare"`
	}{}
	for _, boundary := range payload.ClientBoundaries.Boundaries {
		byRoute[boundary.RouteID] = append(byRoute[boundary.RouteID], boundary)
	}
	routeIDs := make([]string, 0, len(byRoute))
	for routeID := range byRoute {
		routeIDs = append(routeIDs, routeID)
	}
	sort.Strings(routeIDs)
	for _, routeID := range routeIDs {
		fmt.Printf("Route %s\n", routeID)
		for _, downgrade := range byRoute[routeID] {
			trigger := downgrade.TriggerCode
			if downgrade.TriggerConstruct != "" {
				if trigger != "" {
					trigger += " (" + downgrade.TriggerConstruct + ")"
				} else {
					trigger = downgrade.TriggerConstruct
				}
			}
			if trigger == "" {
				trigger = "unknown"
			}
			fmt.Printf("  • %s @ %s\n", downgrade.Component, downgrade.Source)
			fmt.Printf("      trigger: %s\n", trigger)
			if downgrade.Suggestion != "" {
				fmt.Printf("      hint: %s\n", downgrade.Suggestion)
			}
		}
		fmt.Println()
	}
	return nil
}

func writeIfMissing(path string, content []byte) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, content, 0o644)
}

type scaffoldFile struct {
	path     string
	content  []byte
	replace  bool
	preserve bool
}

// writeScaffolds preserves user changes while making an unchanged command
// idempotent. It validates all targets before writing any of them.
func writeScaffolds(files []scaffoldFile) error {
	for _, file := range files {
		existing, err := os.ReadFile(file.path)
		switch {
		case err == nil:
			if !file.replace && !file.preserve && !bytes.Equal(existing, file.content) {
				return fmt.Errorf("refusing to overwrite %s; edit it directly or remove it before rerunning gobeyond add", file.path)
			}
		case !errors.Is(err, os.ErrNotExist):
			return err
		}
	}
	for _, file := range files {
		if _, err := os.Stat(file.path); err == nil {
			if file.replace {
				if err := os.WriteFile(file.path, file.content, 0o644); err != nil {
					return err
				}
			}
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(file.path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(file.path, file.content, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func validateCLIRoute(route string) error {
	if route == "" {
		return nil
	}
	for _, segment := range strings.Split(route, "/") {
		if segment == "" || segment == "." || segment == ".." || strings.Contains(segment, `\`) {
			return fmt.Errorf("invalid route %q: use slash-separated app route segments without . or ..", route)
		}
	}
	return nil
}

func routeForCLI(route string) (project.Route, error) {
	temporary, err := os.MkdirTemp("", "gobeyond-route")
	if err != nil {
		return project.Route{}, err
	}
	defer os.RemoveAll(temporary)
	path := filepath.Join(temporary, "app", filepath.FromSlash(route), "page.tsx")
	if err := writeIfMissing(path, []byte("fixture")); err != nil {
		return project.Route{}, err
	}
	routes, err := project.Discover(temporary)
	if err != nil {
		return project.Route{}, err
	}
	if len(routes) != 1 {
		return project.Route{}, fmt.Errorf("route %q did not resolve to exactly one app route", route)
	}
	return routes[0], nil
}

func safeIdentifier(value string) string {
	value = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '_'
	}, value)
	return strings.Trim(value, "_")
}

func validActionName(value string) (string, error) {
	if value == "" {
		return "", errors.New("action name must be an ASCII TypeScript identifier")
	}
	for index, character := range value {
		letter := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z'
		digit := character >= '0' && character <= '9'
		if !letter && character != '_' && (index == 0 || !digit) {
			return "", fmt.Errorf("invalid action name %q: use an ASCII TypeScript identifier", value)
		}
	}
	if exportedActionName(value) == "" {
		return "", fmt.Errorf("invalid action name %q: include at least one ASCII letter or digit", value)
	}
	return value, nil
}

const actionInsertionMarker = "// gobeyond:add-action declarations"
const actionScaffoldImport = "import { defineAction, schema } from '@go-beyond/schema'\n\n"
const actionGoImportMarker = "// gobeyond:add-action imports"
const actionGoDeclarationMarker = "// gobeyond:add-action declarations"

func mergeActionSource(path, actionName string) ([]byte, bool, error) {
	definition := actionDefinition(actionName)
	existing, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return []byte(actionScaffoldImport + actionInsertionMarker + "\n" + definition), true, nil
	}
	if err != nil {
		return nil, false, err
	}
	source := string(existing)
	if strings.Contains(source, "export const "+actionName+" = defineAction(") {
		return existing, false, nil
	}
	if !strings.HasPrefix(source, actionScaffoldImport) || strings.Count(source, actionInsertionMarker) != 1 {
		return nil, false, fmt.Errorf("cannot safely update %s: add %q manually or use an actions.ts scaffold created by gobeyond add action", path, actionName)
	}
	marker := actionInsertionMarker + "\n"
	index := strings.Index(source, marker)
	if index < 0 {
		return nil, false, fmt.Errorf("cannot safely update %s: add %q manually or use an actions.ts scaffold created by gobeyond add action", path, actionName)
	}
	updated := source[:index+len(marker)] + definition + "\n" + source[index+len(marker):]
	return []byte(updated), true, nil
}

func actionDefinition(actionName string) string {
	return "export const " + actionName + " = defineAction({\n\tinput: schema.object({}),\n\toutput: schema.object({}),\n})\n"
}

func mergeActionGoSource(file, packageName, actionName, contractImport string) ([]byte, bool, error) {
	exported := exportedActionName(actionName)
	existing, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		existing = []byte("package " + safeIdentifier(packageName) + "\n\nimport (\n\tgb \"github.com/Origens-Dev/gobeyond\"\n\t" + actionGoImportMarker + "\n)\n\n" + actionGoDeclarationMarker + "\n")
	} else if err != nil {
		return nil, false, err
	} else if strings.Contains(string(existing), "func "+exported+"(") {
		if strings.Contains(string(existing), strconv.Quote(contractImport)) {
			return existing, false, nil
		}
		return nil, false, fmt.Errorf("action name %q collides with an existing Go handler %s; choose a name with a distinct exported Go identifier", actionName, exported)
	}
	source := string(existing)
	if strings.Count(source, actionGoImportMarker) != 1 || strings.Count(source, actionGoDeclarationMarker) != 1 {
		return nil, false, fmt.Errorf("cannot safely update %s: add %q manually or use an actions.go scaffold created by gobeyond add action", file, exported)
	}
	importMarker := actionGoImportMarker + "\n"
	importIndex := strings.Index(source, importMarker)
	declarationMarker := actionGoDeclarationMarker + "\n"
	declarationIndex := strings.Index(source, declarationMarker)
	if importIndex < 0 || declarationIndex < 0 {
		return nil, false, fmt.Errorf("cannot safely update %s: GoBeyond action markers must each end with a newline", file)
	}
	alias := "contract" + exported
	source = source[:importIndex+len(importMarker)] + "\t" + alias + " \"" + contractImport + "\"\n" + source[importIndex+len(importMarker):]
	declarationIndex = strings.Index(source, declarationMarker)
	source = source[:declarationIndex+len(declarationMarker)] + actionGoDefinition(alias, actionName) + "\n" + source[declarationIndex+len(declarationMarker):]
	formatted, formatErr := format.Source([]byte(source))
	if formatErr != nil {
		return nil, false, fmt.Errorf("format %s action scaffold: %w", file, formatErr)
	}
	return formatted, true, nil
}

func generatedContractImport(moduleRoot, website, kind, identifier string) (string, error) {
	document := codegen.Document{APIVersion: codegen.APIVersionV1Alpha1, Routes: []codegen.Route{}, Actions: []codegen.Action{}}
	switch kind {
	case "routes":
		document.Routes = append(document.Routes, codegen.Route{RouteID: identifier, Props: codegen.Value{Kind: codegen.KindObject, Shape: map[string]codegen.Value{}}})
	case "actions":
		emptyObject := codegen.Value{Kind: codegen.KindObject, Shape: map[string]codegen.Value{}}
		document.Actions = append(document.Actions, codegen.Action{ActionID: identifier, Input: emptyObject, Output: emptyObject})
	default:
		return "", fmt.Errorf("unknown generated contract kind %q", kind)
	}
	files, err := codegen.Generate(document, codegen.Options{})
	if err != nil {
		return "", err
	}
	if len(files) != 1 {
		return "", errors.New("generated contract path is ambiguous")
	}
	for generated := range files {
		relative, err := filepath.Rel(moduleRoot, filepath.Join(website, filepath.FromSlash(filepath.Dir(generated))))
		if err != nil {
			return "", err
		}
		if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return "", errors.New("website root must be inside the Go module root")
		}
		module, err := modulePath(moduleRoot)
		if err != nil {
			return "", err
		}
		return path.Join(module, filepath.ToSlash(relative)), nil
	}
	return "", errors.New("generated contract path is missing")
}

func modulePath(root string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", fmt.Errorf("read module path: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "module" {
			return fields[1], nil
		}
	}
	return "", errors.New("go.mod does not declare a module path")
}

func pageTemplate(route string) string {
	title := route
	if title == "" {
		title = "GoBeyond"
	}
	return "export default function Page() {\n  return <main><h1>" + jsxText(title) + "</h1></main>\n}\n"
}

func jsxText(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "{", "&#123;", "}", "&#125;")
	return replacer.Replace(value)
}

func schemaTemplate() string {
	return "import { definePage, schema } from '@go-beyond/schema'\n\nexport const page = definePage({ props: schema.object({}) })\n"
}

func goPageTemplate(route project.Route, contractImport string) string {
	return "package " + safeIdentifier(route.ServerKey) + "\n\nimport (\n\t\"net/http\"\n\n\tgb \"github.com/Origens-Dev/gobeyond\"\n\tcontract \"" + contractImport + "\"\n\tgbruntime \"github.com/Origens-Dev/gobeyond/runtime\"\n)\n\n// Page implements the generated route contract. Run gobeyond generate after editing the schema.\n// Register it in gbruntime.Config.Pages with generatedroutes." + routeRegistryName(route.ServerKey) + " for " + route.Pattern + ".\nfunc Page(_ *gb.PageContext) (gbruntime.LoadedPage, error) {\n\treturn gbruntime.LoadedPage{\n\t\tKind:   gb.ResultOK,\n\t\tStatus: http.StatusOK,\n\t\tCache:  gb.CachePolicy{Mode: gb.CachePrivateNoStore},\n\t\tProps:  contract.Props{},\n\t}, nil\n}\n"
}

func apiTemplate(packageName string) string {
	return "package " + safeIdentifier(packageName) + "\n\nimport (\n\t\"net/http\"\n\n\tgb \"github.com/Origens-Dev/gobeyond\"\n)\n\nfunc GET(ctx *gb.RequestContext) (gb.Response, error) {\n\treturn gb.Response{Status: http.StatusOK, Headers: http.Header{\"Content-Type\": {\"application/json\"}}, Body: []byte(`{\"ok\":true}`)}, nil\n}\n"
}

func actionGoDefinition(contractAlias, actionName string) string {
	exported := exportedActionName(actionName)
	return "// " + exported + " implements the generated action contract. Run gobeyond generate after editing actions.ts.\n// Register it with " + contractAlias + ".Register(" + exported + ") so input and output stay schema-checked at the HTTP boundary.\nfunc " + exported + "(_ *gb.ActionContext, _ " + contractAlias + ".Input) (" + contractAlias + ".Output, error) {\n\treturn " + contractAlias + ".Output{}, nil\n}"
}

func exportedActionName(actionName string) string {
	parts := strings.FieldsFunc(actionName, func(character rune) bool { return character == '_' })
	var result strings.Builder
	for _, part := range parts {
		if part == "" {
			continue
		}
		result.WriteString(strings.ToUpper(part[:1]))
		result.WriteString(part[1:])
	}
	return result.String()
}

func routeRegistryName(serverKey string) string {
	parts := strings.FieldsFunc(serverKey, func(character rune) bool { return character == '_' || character == '-' })
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
