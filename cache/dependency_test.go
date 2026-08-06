package cache

import (
	"context"
	"sync/atomic"
	"testing"
)

func TestLoadRouteCarriesDataDependencyTags(t *testing.T) {
	clock := newTestClock()
	store := newFakeStore(clock)
	runtime := newTestRuntime(t, store, clock)
	var routeCalls atomic.Int32
	var dataCalls atomic.Int32

	route := productRouteOptions()
	route.Tags = nil
	loader := func(ctx context.Context) (string, error) {
		data, err := Load(ctx, Options{
			Name:       "products.list",
			Revalidate: testRevalidate,
			Tags:       []string{"products"},
		}, JSONCodec[string](), func(context.Context) (string, error) {
			return counterLoader(&dataCalls)(ctx)
		})
		if err != nil {
			return "", err
		}
		routeCalls.Add(1)
		return data, nil
	}

	if _, err := LoadRoute(scopedContext(runtime, false), route, JSONCodec[string](), alwaysStorable, loader); err != nil {
		t.Fatalf("LoadRoute() error = %v", err)
	}
	key := productRouteKey(t)
	record, ok := store.record(key)
	if !ok {
		t.Fatal("route entry was not stored")
	}
	if _, ok := record.TagVersions["products"]; !ok {
		t.Fatalf("route TagVersions = %v, want products dependency", record.TagVersions)
	}

	if err := RevalidateTag(scopedContext(runtime, false), "products"); err != nil {
		t.Fatalf("RevalidateTag() error = %v", err)
	}
	if _, err := LoadRoute(scopedContext(runtime, false), route, JSONCodec[string](), alwaysStorable, loader); err != nil {
		t.Fatalf("LoadRoute() after invalidation error = %v", err)
	}
	if routeCalls.Load() != 2 || dataCalls.Load() != 2 {
		t.Fatalf("calls after dependency invalidation = route %d/data %d, want 2/2", routeCalls.Load(), dataCalls.Load())
	}
}
