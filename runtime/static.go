package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	gb "github.com/Origens-Dev/gobeyond"
	"github.com/Origens-Dev/gobeyond/codegen"
)

type staticBuildArtifact struct {
	APIVersion string             `json:"apiVersion"`
	Routes     []staticBuildRoute `json:"routes"`
}
type staticBuildRoute struct {
	RouteID string             `json:"routeId"`
	Entries []staticBuildEntry `json:"entries"`
}
type staticBuildEntry struct {
	Params   map[string]any  `json:"params"`
	Props    json.RawMessage `json:"props"`
	Metadata json.RawMessage `json:"metadata"`
}

// StaticStore is the startup-loaded, packaged build data used for soft
// navigation and for static pages promoted to the Go origin by middleware.
type StaticStore struct {
	routes    map[string][]LoadedPageEntry
	contracts codegen.Document
}

// LoadContracts reads the build's value-contract document, which a server
// needs as Config.Contracts before any route may cache its props.
func LoadContracts(path string) (*codegen.Document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	document, err := codegen.Parse(data)
	if err != nil {
		return nil, err
	}
	return &document, nil
}

// Contracts returns the value-contract document this store was built from, so
// a server that already packages static data does not have to read and parse
// the same file twice to enable route caching.
func (s *StaticStore) Contracts() *codegen.Document {
	if s == nil {
		return nil
	}
	return &s.contracts
}

type LoadedPageEntry struct {
	Params map[string]any
	Page   LoadedPage
}

func LoadStaticStore(buildPath, contractsPath string) (*StaticStore, error) {
	buildData, err := os.ReadFile(buildPath)
	if err != nil {
		return nil, err
	}
	parsed, err := LoadContracts(contractsPath)
	if err != nil {
		return nil, err
	}
	contracts := *parsed
	var artifact staticBuildArtifact
	if err := json.Unmarshal(buildData, &artifact); err != nil || artifact.APIVersion != "gobeyond.static-build/v1alpha1" {
		return nil, fmt.Errorf("invalid static build artifact")
	}
	store := &StaticStore{routes: make(map[string][]LoadedPageEntry, len(artifact.Routes)), contracts: contracts}
	for _, route := range artifact.Routes {
		for _, entry := range route.Entries {
			page, err := decodeStaticEntry(contracts, route.RouteID, entry.Props, entry.Metadata)
			if err != nil {
				return nil, err
			}
			store.routes[route.RouteID] = append(store.routes[route.RouteID], LoadedPageEntry{Params: entry.Params, Page: page})
		}
	}
	return store, nil
}

// decodeStaticEntry rebuilds one packaged page from its props and metadata
// JSON, re-marking contract-declared SafeHTML strings before the renderer can
// see them. It is the single decode path for build data: LoadStaticStore runs
// it eagerly for every entry, PackStaticStore lazily per requested entry.
func decodeStaticEntry(contracts codegen.Document, routeID string, rawProps, rawMetadata json.RawMessage) (LoadedPage, error) {
	decoder := json.NewDecoder(bytes.NewReader(rawProps))
	decoder.UseNumber()
	var props any
	if err := decoder.Decode(&props); err != nil {
		return LoadedPage{}, fmt.Errorf("decode static props for %s: %w", routeID, err)
	}
	props, err := codegen.TrustStaticSafeHTML(contracts, routeID, props)
	if err != nil {
		return LoadedPage{}, err
	}
	metadata := gb.Metadata{Lang: "en", Title: "Not found", Robots: "noindex, nofollow"}
	if len(rawMetadata) > 0 && string(rawMetadata) != "null" {
		if err := json.Unmarshal(rawMetadata, &metadata); err != nil {
			return LoadedPage{}, fmt.Errorf("decode static metadata for %s: %w", routeID, err)
		}
	}
	return LoadedPage{Kind: gb.ResultOK, Props: props, Metadata: metadata, Status: 200, Cache: gb.CachePolicy{Mode: gb.CachePublic, MaxAge: 300}}, nil
}

// staticNotFoundPage is the packaged not-found shape served when a URL
// matches a static route but no entry was packaged for its params.
func staticNotFoundPage() LoadedPage {
	return LoadedPage{Kind: gb.ResultNotFound, Status: 404, Cache: gb.CachePolicy{Mode: gb.CachePrivateNoStore}, Metadata: gb.Metadata{Lang: "en", Title: "Not found", Robots: "noindex, nofollow"}}
}

// BuildID identifies the eager store as build-agnostic: the JSON artifact
// carries no build header, so New accepts it alongside any Config.BuildID.
func (s *StaticStore) BuildID() string { return "" }

// Has reports whether the store carries at least one entry for routeID.
func (s *StaticStore) Has(routeID string) bool {
	_, ok := s.routes[routeID]
	return ok
}

// Entry returns the packaged page for (routeID, params). Everything is
// already decoded at load time, so the context is unused.
func (s *StaticStore) Entry(_ context.Context, routeID string, params map[string]string) (LoadedPage, bool, error) {
	for _, entry := range s.routes[routeID] {
		if staticParamsMatch(entry.Params, params) {
			return entry.Page, true, nil
		}
	}
	return LoadedPage{}, false, nil
}

func (s *StaticStore) Loader(routeID string) PageLoader {
	return staticEntryLoader(s, routeID)
}

// staticEntryLoader adapts a StaticEntries store to a PageLoader for pages
// that ship neither inline static data nor a loader. A URL that matches the
// route but has no packaged entry renders the packaged not-found shape.
func staticEntryLoader(entries StaticEntries, routeID string) PageLoader {
	return func(ctx *gb.PageContext) (LoadedPage, error) {
		page, ok, err := entries.Entry(ctx.Context, routeID, ctx.Params)
		if err != nil {
			return LoadedPage{}, err
		}
		if !ok {
			return staticNotFoundPage(), nil
		}
		return page, nil
	}
}

func staticParamsMatch(expected map[string]any, actual map[string]string) bool {
	if len(expected) != len(actual) {
		return false
	}
	for name, raw := range expected {
		var value string
		switch typed := raw.(type) {
		case string:
			value = typed
		case []any:
			parts := make([]string, len(typed))
			for index, part := range typed {
				text, ok := part.(string)
				if !ok {
					return false
				}
				parts[index] = text
			}
			value = strings.Join(parts, "/")
		default:
			return false
		}
		if actual[name] != value {
			return false
		}
	}
	return true
}
