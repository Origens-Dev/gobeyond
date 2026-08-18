package project

import (
	"strings"
	"testing"
)

func TestRenderRegistryAlwaysImportsGoBeyondRuntimeContracts(t *testing.T) {
	const gbImport = `gb "github.com/Origens-Dev/gobeyond"`

	staticRegistry, err := renderRegistry("example.com/site", []pageWire{{
		Route: Route{ID: "home", Pattern: "/"},
	}}, nil, nil, nil, false)
	if err != nil {
		t.Fatalf("render static registry: %v", err)
	}
	if !strings.Contains(string(staticRegistry), gbImport) {
		t.Fatalf("registry is missing GoBeyond runtime contract import")
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

	apiRegistry, err := renderRegistry("example.com/site", []pageWire{{
		Route: Route{ID: "home", Pattern: "/"},
	}}, []apiWire{{
		Key:        "status",
		Pattern:    "/api/status",
		Alias:      "api0",
		ImportPath: "example.com/site/generated/api/status",
		Methods:    []string{"GET"},
	}}, nil, nil, false)
	if err != nil {
		t.Fatalf("render API registry: %v", err)
	}
	if !strings.Contains(string(apiRegistry), gbImport) {
		t.Fatalf("API registry is missing GoBeyond import for gb.Handler")
	}
}
