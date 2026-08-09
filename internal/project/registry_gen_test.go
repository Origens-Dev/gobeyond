package project

import (
	"strings"
	"testing"
)

func TestRenderRegistryImportsGoBeyondOnlyForServerLoadedPages(t *testing.T) {
	const gbImport = `gb "github.com/Origens-Dev/gobeyond"`

	staticRegistry, err := renderRegistry("example.com/site", []pageWire{{
		Route: Route{ID: "home", Pattern: "/"},
	}}, nil, nil, nil, false)
	if err != nil {
		t.Fatalf("render static registry: %v", err)
	}
	if strings.Contains(string(staticRegistry), gbImport) {
		t.Fatalf("static-only registry contains unused GoBeyond import")
	}

	dynamicRegistry, err := renderRegistry("example.com/site", []pageWire{{
		Route:      Route{ID: "home", Pattern: "/"},
		Alias:      "page0",
		ImportPath: "example.com/site/generated/routes/home",
		HasPage:    true,
	}}, nil, nil, nil, false)
	if err != nil {
		t.Fatalf("render dynamic registry: %v", err)
	}
	if !strings.Contains(string(dynamicRegistry), gbImport) {
		t.Fatalf("server-loaded registry is missing GoBeyond import")
	}
}
