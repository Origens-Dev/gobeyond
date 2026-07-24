package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

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
	address := os.Getenv("GOBEYOND_ADDR")
	if address == "" {
		address = ":8080"
	}
	server := &http.Server{
		Addr:              address,
		Handler:           siteHandler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	stopping, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-stopping.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			log.Printf("graceful shutdown: %v", err)
		}
	}()
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
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
