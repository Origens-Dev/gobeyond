// Package browserassets defines the versioned browser bundle manifest emitted
// by gobeyond build. It keeps Vite's implementation-specific manifest out of
// production servers and exposes only the assets needed to render a route.
package browserassets

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

const APIVersionV1Alpha1 = "gobeyond.browser-assets/v1alpha1"

// BrowserAssets describes one executable browser entry and its eager assets.
// Bootstrap is the executable module for this group. For a route entry it is
// preloaded by the document and imported by the framework bootstrap at runtime.
type BrowserAssets struct {
	Bootstrap      string   `json:"bootstrap,omitempty"`
	ModulePreloads []string `json:"modulePreloads"`
	Styles         []string `json:"styles"`
}

// Manifest is the stable route-aware projection of the bundler output.
// ClientScript and Styles are retained for one compatibility release and are
// used only by servers that do not understand the route-aware fields.
type Manifest struct {
	APIVersion   string                   `json:"apiVersion,omitempty"`
	BuildID      string                   `json:"buildId,omitempty"`
	Bootstrap    BrowserAssets            `json:"bootstrapAssets"`
	Routes       map[string]BrowserAssets `json:"routes"`
	ClientScript string                   `json:"clientScript,omitempty"`
	Styles       []string                 `json:"styles,omitempty"`
}

// Load parses and validates a standalone browser asset manifest.
func Load(path string) (Manifest, error) {
	source, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	return Parse(source)
}

// LoadRuntimeManifest reads the browser assets embedded in gobeyond build's
// production runtime envelope and verifies that no artifact crossed builds.
func LoadRuntimeManifest(path, buildID string) (Manifest, error) {
	source, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var envelope struct {
		APIVersion string   `json:"apiVersion"`
		BuildID    string   `json:"buildId"`
		Assets     Manifest `json:"assets"`
	}
	if err := json.Unmarshal(source, &envelope); err != nil {
		return Manifest{}, fmt.Errorf("parse runtime manifest: %w", err)
	}
	if envelope.APIVersion != "gobeyond.runtime/v1alpha1" {
		return Manifest{}, fmt.Errorf("unsupported runtime manifest: %s", envelope.APIVersion)
	}
	if buildID == "" {
		return Manifest{}, errors.New("expected build ID is required")
	}
	if envelope.BuildID != buildID {
		return Manifest{}, fmt.Errorf("runtime manifest build %s does not match %s", envelope.BuildID, buildID)
	}
	if err := envelope.Assets.Validate(); err != nil {
		return Manifest{}, err
	}
	if envelope.Assets.APIVersion != "" && envelope.Assets.BuildID != buildID {
		return Manifest{}, fmt.Errorf("browser asset build %s does not match %s", envelope.Assets.BuildID, buildID)
	}
	return envelope.Assets, nil
}

// Parse validates the manifest version and required collection fields.
func Parse(source []byte) (Manifest, error) {
	var manifest Manifest
	if err := json.Unmarshal(source, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("parse browser asset manifest: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// Validate rejects partial route-aware manifests. A completely unversioned
// manifest is accepted as the one-release legacy representation.
func (manifest Manifest) Validate() error {
	if manifest.APIVersion == "" {
		return nil
	}
	if manifest.APIVersion != APIVersionV1Alpha1 {
		return fmt.Errorf("unsupported browser asset manifest: %s", manifest.APIVersion)
	}
	if manifest.BuildID == "" {
		return errors.New("browser asset manifest is missing buildId")
	}
	if manifest.Bootstrap.Bootstrap == "" {
		return errors.New("browser asset manifest is missing its bootstrap module")
	}
	if manifest.Bootstrap.ModulePreloads == nil || manifest.Bootstrap.Styles == nil {
		return errors.New("browser asset manifest bootstrap collections must not be null")
	}
	if manifest.Routes == nil {
		return errors.New("browser asset manifest routes must not be null")
	}
	for routeID, assets := range manifest.Routes {
		if routeID == "" {
			return errors.New("browser asset manifest contains an empty route ID")
		}
		if assets.Bootstrap == "" {
			return fmt.Errorf("browser asset manifest route %s is missing its module", routeID)
		}
		if assets.ModulePreloads == nil || assets.Styles == nil {
			return fmt.Errorf("browser asset manifest route %s collections must not be null", routeID)
		}
	}
	return nil
}

// ForRoute returns the bootstrap script and the deduplicated eager assets for
// an initial document. Legacy manifests return their global asset set.
func (manifest Manifest) ForRoute(routeID string) (BrowserAssets, error) {
	if manifest.APIVersion == "" {
		return BrowserAssets{
			Bootstrap:      manifest.ClientScript,
			ModulePreloads: []string{},
			Styles:         cloneStrings(manifest.Styles),
		}, nil
	}
	if err := manifest.Validate(); err != nil {
		return BrowserAssets{}, err
	}
	route, ok := manifest.Routes[routeID]
	if !ok {
		return BrowserAssets{}, fmt.Errorf("browser asset manifest has no route %s", routeID)
	}
	preloads := append(cloneStrings(manifest.Bootstrap.ModulePreloads), route.Bootstrap)
	preloads = append(preloads, route.ModulePreloads...)
	preloads = removeString(uniqueStrings(preloads), manifest.Bootstrap.Bootstrap)
	return BrowserAssets{
		Bootstrap:      manifest.Bootstrap.Bootstrap,
		ModulePreloads: preloads,
		Styles:         uniqueStrings(append(cloneStrings(manifest.Bootstrap.Styles), route.Styles...)),
	}, nil
}

func removeString(values []string, unwanted string) []string {
	result := values[:0]
	for _, value := range values {
		if value != unwanted {
			result = append(result, value)
		}
	}
	return result
}

func uniqueStrings(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func cloneStrings(values []string) []string {
	return append([]string{}, values...)
}
