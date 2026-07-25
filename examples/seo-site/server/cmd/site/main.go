package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	gblisten "github.com/Origens-Dev/gobeyond/adapters/listen"
	"github.com/Origens-Dev/gobeyond/browserassets"
	"github.com/Origens-Dev/gobeyond/buildpaths"
	seosite "github.com/Origens-Dev/gobeyond/examples/seo-site/server"
	gbruntime "github.com/Origens-Dev/gobeyond/runtime"
)

func main() {
	planDir := os.Getenv("GOBEYOND_PLAN_DIR")
	if planDir == "" {
		planDir = "render-plans"
	}
	plans, err := seosite.LoadPlans(planDir)
	if err != nil {
		log.Fatal(err)
	}
	origin := os.Getenv("GOBEYOND_PUBLIC_ORIGIN")
	if origin == "" {
		origin = "http://localhost:8080"
	}
	buildID := os.Getenv("GOBEYOND_BUILD_ID")
	if buildID == "" {
		buildID = "development"
	}
	assets, err := loadBrowserAssets(filepath.Join(filepath.Dir(planDir), "runtime-manifest.json"), buildID)
	if err != nil {
		log.Fatal(err)
	}
	runtimeDataDirectory := os.Getenv("GOBEYOND_RUNTIME_DATA_DIR")
	if runtimeDataDirectory == "" {
		runtimeDataDirectory = filepath.Join(filepath.Dir(planDir), "runtime-data")
	}
	staticStore, err := gbruntime.LoadStaticStore(filepath.Join(runtimeDataDirectory, "static-build.json"), filepath.Join(runtimeDataDirectory, "contracts.json"))
	if err != nil {
		log.Fatal(err)
	}
	handler, err := seosite.NewWithStaticStore(buildID, origin, plans, assets, staticStore)
	if err != nil {
		log.Fatal(err)
	}
	var siteHandler http.Handler = handler
	if staticDirectory := os.Getenv("GOBEYOND_STATIC_DIR"); staticDirectory != "" {
		files := http.FileServer(http.Dir(staticDirectory))
		siteHandler = http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if buildpaths.IsStaticArtifact(request.URL.Path) ||
				staticFileExists(staticDirectory, request.URL.Path) {
				files.ServeHTTP(writer, request)
				return
			}
			handler.ServeHTTP(writer, request)
		})
	}
	// Hosted mode listens on the GOBEYOND_LISTEN socket with readiness and
	// SIGTERM drain; without it this falls back to TCP on GOBEYOND_ADDR.
	if err := gblisten.Serve(siteHandler); err != nil {
		log.Fatal(err)
	}
}

func loadBrowserAssets(path, buildID string) (*browserassets.Manifest, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var manifest struct {
		BuildID string          `json:"buildId"`
		Assets  json.RawMessage `json:"assets"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}
	if manifest.BuildID != buildID {
		return nil, errors.New("runtime asset manifest build ID does not match server build ID")
	}
	assets, err := browserassets.Parse(manifest.Assets)
	if err != nil {
		return nil, err
	}
	if assets.BuildID != buildID {
		return nil, errors.New("browser asset manifest build ID does not match server build ID")
	}
	return &assets, nil
}

func staticFileExists(directory, requestPath string) bool {
	cleaned := filepath.Clean("/" + requestPath)
	if cleaned == "/" || strings.Contains(cleaned, "..") {
		return false
	}
	path := filepath.Join(directory, filepath.FromSlash(strings.TrimPrefix(cleaned, "/")))
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
