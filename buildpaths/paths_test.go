package buildpaths

import "testing"

func TestURLConstruction(t *testing.T) {
	cases := map[string]string{
		BuildRootURL("b_1"):               "/_gobeyond/builds/b_1",
		AssetBaseURL("b_1"):               "/_gobeyond/builds/b_1/assets",
		AssetURL("b_1", "app.js"):         "/_gobeyond/builds/b_1/assets/app.js",
		AssetURL("b_1", "/app.js"):        "/_gobeyond/builds/b_1/assets/app.js",
		ManifestURL("b_1"):                "/_gobeyond/builds/b_1/manifest.json",
		StaticRouteURL("b_1", "home"):    "/_gobeyond/builds/b_1/static/home",
		RuntimeURL("b_1", "home"):         "/_gobeyond/builds/b_1/runtime/home",
		ActionURL("b_1", "home:addToCart"): "/_gobeyond/builds/b_1/actions/home:addToCart",
	}
	for got, want := range cases {
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
}

func TestBuildPathKind(t *testing.T) {
	tests := []struct {
		path     string
		wantKind string
		wantOK   bool
	}{
		{"/_gobeyond/builds/b_1/assets/app.js", "assets", true},
		{"/_gobeyond/builds/b_1/manifest.json", "manifest.json", true},
		{"/_gobeyond/builds/b_1/static/home", "static", true},
		{"/_gobeyond/builds/b_1/runtime/home", "runtime", true},
		{"/_gobeyond/builds/b_1/actions/home:addToCart", "actions", true},
		{"/_gobeyond/builds/b_1", "", false},
		{"/_gobeyond/builds/", "", false},
		{"/other/path", "", false},
	}
	for _, test := range tests {
		kind, ok := BuildPathKind(test.path)
		if kind != test.wantKind || ok != test.wantOK {
			t.Fatalf("BuildPathKind(%q) = (%q, %v), want (%q, %v)", test.path, kind, ok, test.wantKind, test.wantOK)
		}
	}
}

func TestIsStaticArtifact(t *testing.T) {
	static := []string{
		"/_gobeyond/builds/b_1/assets/app.js",
		"/_gobeyond/builds/b_1/manifest.json",
		"/_gobeyond/builds/b_1/static/home",
	}
	for _, path := range static {
		if !IsStaticArtifact(path) {
			t.Fatalf("expected %q to be a static artifact", path)
		}
	}
	dynamic := []string{
		"/_gobeyond/builds/b_1/runtime/home",
		"/_gobeyond/builds/b_1/actions/home:addToCart",
		"/other/path",
	}
	for _, path := range dynamic {
		if IsStaticArtifact(path) {
			t.Fatalf("expected %q not to be a static artifact", path)
		}
	}
}

func TestParseRuntimePath(t *testing.T) {
	buildID, routeID, ok := ParseRuntimePath("/_gobeyond/builds/b_1/runtime/home")
	if !ok || buildID != "b_1" || routeID != "home" {
		t.Fatalf("got (%q, %q, %v)", buildID, routeID, ok)
	}
	if _, _, ok := ParseRuntimePath("/_gobeyond/builds/b_1/runtime/home/extra"); ok {
		t.Fatal("expected extra path segments to fail")
	}
	if _, _, ok := ParseRuntimePath("/_gobeyond/builds/b_1/actions/home"); ok {
		t.Fatal("expected wrong kind to fail")
	}
	if _, _, ok := ParseRuntimePath("/other/path"); ok {
		t.Fatal("expected unrelated path to fail")
	}
}

func TestParseActionPath(t *testing.T) {
	buildID, actionID, ok := ParseActionPath("/_gobeyond/builds/b_1/actions/home:addToCart")
	if !ok || buildID != "b_1" || actionID != "home:addToCart" {
		t.Fatalf("got (%q, %q, %v)", buildID, actionID, ok)
	}
	if _, _, ok := ParseActionPath("/_gobeyond/builds/b_1/actions/"); ok {
		t.Fatal("expected empty action ID to fail")
	}
}

func TestDiskPaths(t *testing.T) {
	staticDir := "/dist/static"
	if got, want := StaticBuildRoot(staticDir, "b_1"), "/dist/static/_gobeyond/builds/b_1"; got != want {
		t.Fatalf("StaticBuildRoot = %q, want %q", got, want)
	}
	if got, want := AssetsDir(staticDir, "b_1"), "/dist/static/_gobeyond/builds/b_1/assets"; got != want {
		t.Fatalf("AssetsDir = %q, want %q", got, want)
	}
	if got, want := ManifestPath(staticDir, "b_1"), "/dist/static/_gobeyond/builds/b_1/manifest.json"; got != want {
		t.Fatalf("ManifestPath = %q, want %q", got, want)
	}
	if got, want := StaticRoutePath(staticDir, "b_1", "home"), "/dist/static/_gobeyond/builds/b_1/static/home"; got != want {
		t.Fatalf("StaticRoutePath = %q, want %q", got, want)
	}
}
