package runtime

import (
	"net/http"
	"testing"

	gb "github.com/Origens-Dev/gobeyond"
)

func TestFromPageResultPreservesPublicRouteResult(t *testing.T) {
	result := gb.NotFound(struct{ Found bool }{Found: false}, gb.Metadata{Lang: "en", Title: "Missing"})
	result.Headers = map[string]string{"X-Route": "project"}
	result.Cache = gb.CachePolicy{Mode: gb.CachePublic, MaxAge: 30}
	loaded := FromPageResult(result)
	if loaded.Kind != gb.ResultNotFound || loaded.Status != http.StatusNotFound {
		t.Fatalf("loaded page result = %#v", loaded)
	}
	if props, ok := loaded.Props.(struct{ Found bool }); !ok || props.Found {
		t.Fatalf("loaded props = %#v", loaded.Props)
	}
	if loaded.Headers.Get("X-Route") != "project" || loaded.Cache != result.Cache {
		t.Fatalf("loaded headers/cache = %#v %#v", loaded.Headers, loaded.Cache)
	}
}
