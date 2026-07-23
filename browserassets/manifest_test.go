package browserassets

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestForRouteMergesBootstrapAndRouteAssets(t *testing.T) {
	manifest := Manifest{
		APIVersion: APIVersionV1Alpha1,
		BuildID:    "build-1",
		Bootstrap: BrowserAssets{
			Bootstrap:      "/assets/app.js",
			ModulePreloads: []string{"/assets/react.js"},
			Styles:         []string{"/assets/base.css"},
		},
		Routes: map[string]BrowserAssets{
			"home": {
				Bootstrap:      "/assets/home.js",
				ModulePreloads: []string{"/assets/react.js", "/assets/gallery.js"},
				Styles:         []string{"/assets/base.css", "/assets/home.css"},
			},
		},
	}
	assets, err := manifest.ForRoute("home")
	if err != nil {
		t.Fatal(err)
	}
	if assets.Bootstrap != "/assets/app.js" {
		t.Fatalf("bootstrap = %q", assets.Bootstrap)
	}
	if want := []string{"/assets/react.js", "/assets/home.js", "/assets/gallery.js"}; !reflect.DeepEqual(assets.ModulePreloads, want) {
		t.Fatalf("preloads = %#v, want %#v", assets.ModulePreloads, want)
	}
	if want := []string{"/assets/base.css", "/assets/home.css"}; !reflect.DeepEqual(assets.Styles, want) {
		t.Fatalf("styles = %#v, want %#v", assets.Styles, want)
	}
}

func TestLoadRuntimeManifestValidatesEnvelopeAndAssetBuilds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-manifest.json")
	source := `{"apiVersion":"gobeyond.runtime/v1alpha1","buildId":"build-1","assets":{"apiVersion":"gobeyond.browser-assets/v1alpha1","buildId":"build-1","bootstrapAssets":{"bootstrap":"/app.js","modulePreloads":[],"styles":[]},"routes":{}}}`
	if err := os.WriteFile(path, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest, err := LoadRuntimeManifest(path, "build-1")
	if err != nil {
		t.Fatal(err)
	}
	if manifest.BuildID != "build-1" {
		t.Fatalf("build ID = %q", manifest.BuildID)
	}
	if _, err := LoadRuntimeManifest(path, "build-2"); err == nil {
		t.Fatal("mismatched envelope build unexpectedly succeeded")
	}
}

func TestParseRejectsPartialAndUnknownManifests(t *testing.T) {
	for _, source := range []string{
		`{"apiVersion":"gobeyond.browser-assets/v2","buildId":"build-1","routes":{}}`,
		`{"apiVersion":"gobeyond.browser-assets/v1alpha1","buildId":"build-1","bootstrapAssets":{"modulePreloads":[],"styles":[]}}`,
		`{"apiVersion":"gobeyond.browser-assets/v1alpha1","buildId":"build-1","bootstrapAssets":{"modulePreloads":[],"styles":[]},"routes":{"home":{"modulePreloads":[],"styles":[]}}}`,
	} {
		if _, err := Parse([]byte(source)); err == nil {
			t.Fatalf("Parse(%s) unexpectedly succeeded", source)
		}
	}
}

func TestLegacyManifestIsUsedOnlyWithoutVersion(t *testing.T) {
	manifest, err := Parse([]byte(`{"clientScript":"/assets/app.js","styles":["/assets/app.css"]}`))
	if err != nil {
		t.Fatal(err)
	}
	assets, err := manifest.ForRoute("unknown")
	if err != nil {
		t.Fatal(err)
	}
	if assets.Bootstrap != "/assets/app.js" || !reflect.DeepEqual(assets.Styles, []string{"/assets/app.css"}) {
		t.Fatalf("legacy assets = %#v", assets)
	}
}
