package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"path/filepath"

	gblisten "github.com/Origens-Dev/gobeyond/adapters/listen"
	"github.com/Origens-Dev/gobeyond/browserassets"
	seosite "github.com/Origens-Dev/gobeyond/examples/seo-site/server"
	gbruntime "github.com/Origens-Dev/gobeyond/runtime"
)

func main() {
	// Pack-only runtime artifacts (ADR 004): plans and static entries are
	// opened as immutable binary packs and decoded lazily per route. The
	// pretty JSON the CLI writes next to them is inspection-only.
	planPack := os.Getenv("GOBEYOND_PLAN_PACK")
	if planPack == "" {
		planPack = filepath.Join("dist", "server", "render-plans.gbp")
	}
	staticPack := os.Getenv("GOBEYOND_STATIC_PACK")
	if staticPack == "" {
		staticPack = filepath.Join("dist", "server", "runtime-data", "static-build.gbs")
	}
	planStore, err := gbruntime.OpenPlanStore(planPack)
	if err != nil {
		log.Fatal(err)
	}
	defer planStore.Close()
	staticStore, err := gbruntime.OpenStaticStore(staticPack, filepath.Join(filepath.Dir(staticPack), "contracts.json"))
	if err != nil {
		log.Fatal(err)
	}
	defer staticStore.Close()
	origin := os.Getenv("GOBEYOND_PUBLIC_ORIGIN")
	if origin == "" {
		origin = "http://localhost:8080"
	}
	// The packs carry the build identity; GOBEYOND_BUILD_ID may confirm it
	// but the stores are authoritative.
	buildID := os.Getenv("GOBEYOND_BUILD_ID")
	if buildID == "" {
		buildID = planStore.BuildID()
	}
	assets, err := loadBrowserAssets(filepath.Join(filepath.Dir(planPack), "runtime-manifest.json"), buildID)
	if err != nil {
		log.Fatal(err)
	}
	handler, err := seosite.NewFromStores(buildID, origin, assets, planStore, staticStore)
	if err != nil {
		log.Fatal(err)
	}
	defer handler.Close()
	var siteHandler http.Handler = handler
	if staticDirectory := os.Getenv(gbruntime.EnvStaticDir); staticDirectory != "" {
		siteHandler = gbruntime.StaticFiles(staticDirectory, handler)
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
