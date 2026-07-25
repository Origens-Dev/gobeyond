package runtime

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	gb "github.com/Origens-Dev/gobeyond"
	"github.com/Origens-Dev/gobeyond/cache"
	"github.com/Origens-Dev/gobeyond/codegen"
)

// cachedPage is the persisted shape of one route entry. It deliberately holds
// what a document can be rebuilt from and nothing more: no response headers,
// because a loader's headers can carry Set-Cookie and other viewer-specific
// state that must never be replayed to a second visitor, and no rendered HTML,
// because every request re-renders to get its own CSP nonce and render clock.
//
// The loader's gb.CachePolicy travels with the entry so a cache hit and a
// cache miss put the same Cache-Control on the wire. It is a policy, not
// viewer state, and only entries that passed the Set gate are ever stored.
type cachedPage struct {
	Kind     gb.ResultKind   `json:"kind"`
	Props    json.RawMessage `json:"props,omitempty"`
	Metadata gb.Metadata     `json:"metadata,omitempty"`
	Status   int             `json:"status,omitempty"`
	Cache    gb.CachePolicy  `json:"cache"`
}

// routePropsCodec moves a LoadedPage in and out of the cache.
//
// Decoding runs the same trust path as packaged static build data: props come
// back as plain JSON, so every string the route's contract declares as
// safeHTML has to be re-marked through codegen.TrustStaticSafeHTML before the
// renderer sees it. Restoring the marker blindly (or decoding into an
// unchecked any) would let cached bytes decide what counts as trusted HTML.
//
// A hit therefore hands the renderer generic maps and json.Numbers where the
// miss handed it the loader's typed props, exactly as the static path does.
// Both the renderer and the hydration payload are driven by the plan and the
// contract rather than by Go types, so the document is identical either way.
type routePropsCodec struct {
	routeID   string
	contracts *codegen.Document
}

func (c routePropsCodec) Encode(page LoadedPage) ([]byte, error) {
	props, err := json.Marshal(page.Props)
	if err != nil {
		return nil, err
	}
	return json.Marshal(cachedPage{
		Kind:     page.Kind,
		Props:    props,
		Metadata: page.Metadata,
		Status:   page.Status,
		Cache:    page.Cache,
	})
}

func (c routePropsCodec) Decode(data []byte) (LoadedPage, error) {
	if c.contracts == nil {
		return LoadedPage{}, errors.New("route cache decode requires the build's value contracts")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var stored cachedPage
	if err := decoder.Decode(&stored); err != nil {
		return LoadedPage{}, err
	}
	if stored.Kind != gb.ResultOK {
		return LoadedPage{}, fmt.Errorf("route cache entry for %s is not an OK result", c.routeID)
	}
	page := LoadedPage{
		Kind:     stored.Kind,
		Metadata: stored.Metadata,
		Status:   stored.Status,
		Cache:    stored.Cache,
	}
	if len(stored.Props) == 0 {
		return page, nil
	}
	propsDecoder := json.NewDecoder(bytes.NewReader(stored.Props))
	propsDecoder.UseNumber()
	var props any
	if err := propsDecoder.Decode(&props); err != nil {
		return LoadedPage{}, fmt.Errorf("decode cached props for %s: %w", c.routeID, err)
	}
	trusted, err := codegen.TrustStaticSafeHTML(*c.contracts, c.routeID, props)
	if err != nil {
		return LoadedPage{}, err
	}
	page.Props = trusted
	return page, nil
}

// storablePage is the Set gate for route caching, applied after the loader ran
// because that is the first moment either half of it is knowable.
//
// A response is only shareable when it is a plain OK: redirects, not-found,
// and error results describe one request's outcome, and caching them would
// pin a 404 minted during a deploy over the page that replaced it. The privacy
// half re-checks the request headers and adds the loaded response's
// Set-Cookie, because a loader can mint viewer identity for a request that
// arrived without any.
func storablePage(request *http.Request) func(LoadedPage) bool {
	return func(page LoadedPage) bool {
		if page.Kind != gb.ResultOK {
			return false
		}
		if page.Status != 0 && page.Status != http.StatusOK {
			return false
		}
		return !cache.IsPrivateResponse(request.Header, page.Headers)
	}
}

// validateRouteCaching rejects route caching a server could not honour, at
// startup rather than on the first request that would have been cached.
func validateRouteCaching(config Config) error {
	for _, page := range config.Pages {
		if page.Revalidate == 0 && len(page.Tags) == 0 {
			continue
		}
		if page.Revalidate <= 0 {
			return fmt.Errorf("page %s declares cache tags without a positive Revalidate window", page.Route.ID)
		}
		for _, tag := range page.Tags {
			if tag == "" {
				return fmt.Errorf("page %s declares an empty cache tag", page.Route.ID)
			}
		}
		// Static pages are computed once per build and served from memory;
		// there is no request-time work for an ISR window to bound.
		if page.Static != nil || page.Load == nil {
			return fmt.Errorf("page %s cannot cache props without a request-time loader", page.Route.ID)
		}
		if config.Cache == nil {
			continue
		}
		if config.Contracts == nil {
			return fmt.Errorf("page %s caches props, which requires Config.Contracts to restore SafeHTML on decode", page.Route.ID)
		}
		if !hasRouteContract(*config.Contracts, page.Route.ID) {
			return fmt.Errorf("page %s caches props but has no route in the configured value contracts", page.Route.ID)
		}
	}
	return nil
}

func hasRouteContract(document codegen.Document, routeID string) bool {
	for _, route := range document.Routes {
		if route.RouteID == routeID {
			return true
		}
	}
	return false
}
