package cache

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

func productRouteOptions() RouteOptions {
	return RouteOptions{
		RouteID:      "r_products_slug",
		Path:         "/products/widget",
		PublicOrigin: "https://example.test",
		Revalidate:   testRevalidate,
		Tags:         []string{"products"},
	}
}

func productRouteKey(t *testing.T) string {
	t.Helper()
	key, err := RouteKey("deploy", "build-1", "r_products_slug", "/products/widget", "", "https://example.test")
	if err != nil {
		t.Fatalf("RouteKey() error = %v", err)
	}
	return key
}

func alwaysStorable(string) bool { return true }

func TestLoadRouteCachesAcrossRequests(t *testing.T) {
	clock := newTestClock()
	store := newFakeStore(clock)
	runtime := newTestRuntime(t, store, clock)
	var calls atomic.Int32

	first, err := LoadRoute(scopedContext(runtime, false), productRouteOptions(), JSONCodec[string](), alwaysStorable, counterLoader(&calls))
	if err != nil {
		t.Fatalf("LoadRoute() error = %v", err)
	}
	second, err := LoadRoute(scopedContext(runtime, false), productRouteOptions(), JSONCodec[string](), alwaysStorable, counterLoader(&calls))
	if err != nil {
		t.Fatalf("LoadRoute() error = %v", err)
	}
	if first != "v1" || second != "v1" {
		t.Fatalf("LoadRoute() = %q then %q, want v1 twice", first, second)
	}
	if calls.Load() != 1 {
		t.Fatalf("loader called %d times, want 1", calls.Load())
	}
	if _, stored := store.record(productRouteKey(t)); !stored {
		t.Fatalf("no entry stored under the frozen route key")
	}
}

// TestLoadRoutePathsDoNotShareEntries is why the key carries the request path
// rather than the route pattern: one route serves many pages.
func TestLoadRoutePathsDoNotShareEntries(t *testing.T) {
	clock := newTestClock()
	store := newFakeStore(clock)
	runtime := newTestRuntime(t, store, clock)
	var calls atomic.Int32
	loader := counterLoader(&calls)

	widget, err := LoadRoute(scopedContext(runtime, false), productRouteOptions(), JSONCodec[string](), alwaysStorable, loader)
	if err != nil {
		t.Fatalf("LoadRoute() error = %v", err)
	}
	other := productRouteOptions()
	other.Path = "/products/gadget"
	gadget, err := LoadRoute(scopedContext(runtime, false), other, JSONCodec[string](), alwaysStorable, loader)
	if err != nil {
		t.Fatalf("LoadRoute() error = %v", err)
	}
	if widget == gadget {
		t.Fatalf("two paths shared one entry: %q", widget)
	}
}

func TestLoadRouteOnPrivateRequestSkipsTheStore(t *testing.T) {
	clock := newTestClock()
	store := newFakeStore(clock)
	runtime := newTestRuntime(t, store, clock)
	var calls atomic.Int32

	for i := 0; i < 2; i++ {
		if _, err := LoadRoute(scopedContext(runtime, true), productRouteOptions(), JSONCodec[string](), alwaysStorable, counterLoader(&calls)); err != nil {
			t.Fatalf("LoadRoute() error = %v", err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("loader called %d times, want 2", calls.Load())
	}
	if gets, sets, _ := store.counts(); gets != 0 || sets != 0 {
		t.Fatalf("store touched on a private request: gets=%d sets=%d", gets, sets)
	}
}

// TestLoadRouteHonoursTheStorableGate covers the Set gate the runtime uses for
// responses that mint a cookie or are not a plain OK: the value is still
// returned, it just never reaches a store other visitors read.
func TestLoadRouteHonoursTheStorableGate(t *testing.T) {
	clock := newTestClock()
	store := newFakeStore(clock)
	runtime := newTestRuntime(t, store, clock)
	var calls atomic.Int32

	value, err := LoadRoute(scopedContext(runtime, false), productRouteOptions(), JSONCodec[string](), func(string) bool { return false }, counterLoader(&calls))
	if err != nil {
		t.Fatalf("LoadRoute() error = %v", err)
	}
	if value != "v1" {
		t.Fatalf("LoadRoute() = %q, want the computed v1", value)
	}
	if _, sets, _ := store.counts(); sets != 0 {
		t.Fatalf("store writes = %d, want 0 for an unstorable value", sets)
	}
}

func TestLoadRouteNonPositiveRevalidateSkipsTheStore(t *testing.T) {
	clock := newTestClock()
	store := newFakeStore(clock)
	runtime := newTestRuntime(t, store, clock)
	options := productRouteOptions()
	options.Revalidate = 0
	var calls atomic.Int32

	if _, err := LoadRoute(scopedContext(runtime, false), options, JSONCodec[string](), alwaysStorable, counterLoader(&calls)); err != nil {
		t.Fatalf("LoadRoute() error = %v", err)
	}
	if gets, sets, _ := store.counts(); gets != 0 || sets != 0 {
		t.Fatalf("store touched without a revalidate window: gets=%d sets=%d", gets, sets)
	}
}

func TestLoadRouteRejectsMisconfiguration(t *testing.T) {
	clock := newTestClock()
	runtime := newTestRuntime(t, newFakeStore(clock), clock)
	ctx := scopedContext(runtime, false)
	loader := func(context.Context) (string, error) { return "", nil }

	if _, err := LoadRoute(ctx, RouteOptions{Revalidate: testRevalidate}, JSONCodec[string](), alwaysStorable, loader); err == nil {
		t.Fatal("expected an error for an empty RouteID")
	}
	if _, err := LoadRoute(ctx, productRouteOptions(), nil, alwaysStorable, loader); err == nil {
		t.Fatal("expected an error for a nil codec")
	}
	if _, err := LoadRoute[string](ctx, productRouteOptions(), JSONCodec[string](), alwaysStorable, nil); err == nil {
		t.Fatal("expected an error for a nil fn")
	}
	traversal := productRouteOptions()
	traversal.Path = "/products/../admin"
	if _, err := LoadRoute(ctx, traversal, JSONCodec[string](), alwaysStorable, loader); err == nil {
		t.Fatal("expected an error for a path with traversal segments")
	}
}

func TestLoadRouteTagBumpInvalidatesEntry(t *testing.T) {
	clock := newTestClock()
	store := newFakeStore(clock)
	runtime := newTestRuntime(t, store, clock)
	var calls atomic.Int32
	loader := counterLoader(&calls)

	if _, err := LoadRoute(scopedContext(runtime, false), productRouteOptions(), JSONCodec[string](), alwaysStorable, loader); err != nil {
		t.Fatalf("LoadRoute() error = %v", err)
	}
	if err := RevalidateTag(scopedContext(runtime, false), "products"); err != nil {
		t.Fatalf("RevalidateTag() error = %v", err)
	}
	value, err := LoadRoute(scopedContext(runtime, false), productRouteOptions(), JSONCodec[string](), alwaysStorable, loader)
	if err != nil {
		t.Fatalf("LoadRoute() error = %v", err)
	}
	if value != "v2" {
		t.Fatalf("LoadRoute() = %q, want v2 after the tag bump", value)
	}
}

// TestLoadRoutePathRevalidationIsGranular is why the coordinator adds
// PathTag itself: publishing one product must invalidate that page without the
// author having to remember the tag, and without evicting sibling pages.
func TestLoadRoutePathRevalidationIsGranular(t *testing.T) {
	clock := newTestClock()
	store := newFakeStore(clock)
	runtime := newTestRuntime(t, store, clock)
	var calls atomic.Int32
	loader := counterLoader(&calls)
	sibling := productRouteOptions()
	sibling.Path = "/products/gadget"

	if _, err := LoadRoute(scopedContext(runtime, false), productRouteOptions(), JSONCodec[string](), alwaysStorable, loader); err != nil {
		t.Fatalf("LoadRoute() error = %v", err)
	}
	siblingFirst, err := LoadRoute(scopedContext(runtime, false), sibling, JSONCodec[string](), alwaysStorable, loader)
	if err != nil {
		t.Fatalf("LoadRoute() error = %v", err)
	}
	if err := RevalidatePath(scopedContext(runtime, false), "/products/widget/"); err != nil {
		t.Fatalf("RevalidatePath() error = %v", err)
	}

	refreshed, err := LoadRoute(scopedContext(runtime, false), productRouteOptions(), JSONCodec[string](), alwaysStorable, loader)
	if err != nil {
		t.Fatalf("LoadRoute() error = %v", err)
	}
	if refreshed == "v1" {
		t.Fatal("the revalidated path still served its invalidated entry")
	}
	siblingSecond, err := LoadRoute(scopedContext(runtime, false), sibling, JSONCodec[string](), alwaysStorable, loader)
	if err != nil {
		t.Fatalf("LoadRoute() error = %v", err)
	}
	if siblingSecond != siblingFirst {
		t.Fatalf("sibling page was evicted: %q then %q", siblingFirst, siblingSecond)
	}
}

func TestLoadRouteServesStaleWhileRefreshing(t *testing.T) {
	clock := newTestClock()
	store := newFakeStore(clock)
	runtime := newTestRuntime(t, store, clock)
	var calls atomic.Int32
	loader := counterLoader(&calls)

	if _, err := LoadRoute(scopedContext(runtime, false), productRouteOptions(), JSONCodec[string](), alwaysStorable, loader); err != nil {
		t.Fatalf("LoadRoute() error = %v", err)
	}
	clock.advance(testRevalidate + time.Second)

	stale, err := LoadRoute(scopedContext(runtime, false), productRouteOptions(), JSONCodec[string](), alwaysStorable, loader)
	if err != nil {
		t.Fatalf("LoadRoute() error = %v", err)
	}
	if stale != "v1" {
		t.Fatalf("LoadRoute() = %q, want the stale v1", stale)
	}
	key := productRouteKey(t)
	waitFor(t, "the background refresh to store v2", func() bool {
		record, stored := store.record(key)
		return stored && string(record.Value) == `"v2"`
	})
}

// TestLoadRouteBuildIDNamespacesEntries proves a redeploy cannot serve the
// previous build's props shape for the same URL.
func TestLoadRouteBuildIDNamespacesEntries(t *testing.T) {
	clock := newTestClock()
	store := newFakeStore(clock)
	first := newTestRuntime(t, store, clock)
	next, err := NewRuntime(RuntimeConfig{
		DeployPrefix: "deploy",
		BuildID:      "build-2",
		Store:        store,
		Clock:        clock.Now,
		Logger:       first.logger,
	})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	var calls atomic.Int32
	loader := counterLoader(&calls)

	if _, err := LoadRoute(scopedContext(first, false), productRouteOptions(), JSONCodec[string](), alwaysStorable, loader); err != nil {
		t.Fatalf("LoadRoute() error = %v", err)
	}
	value, err := LoadRoute(scopedContext(next, false), productRouteOptions(), JSONCodec[string](), alwaysStorable, loader)
	if err != nil {
		t.Fatalf("LoadRoute() error = %v", err)
	}
	if value != "v2" {
		t.Fatalf("LoadRoute() = %q, want a fresh v2 for the new build", value)
	}
}

func TestLoadRouteWithoutRequestScopeComputesWithoutCaching(t *testing.T) {
	var calls atomic.Int32
	for i := 0; i < 2; i++ {
		value, err := LoadRoute(context.Background(), productRouteOptions(), JSONCodec[string](), alwaysStorable, counterLoader(&calls))
		if err != nil {
			t.Fatalf("LoadRoute() error = %v", err)
		}
		if want := fmt.Sprintf("v%d", i+1); value != want {
			t.Fatalf("LoadRoute() = %q, want %q", value, want)
		}
	}
}
