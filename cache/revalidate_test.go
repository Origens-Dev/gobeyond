package cache

import (
	"context"
	"errors"
	"testing"
)

func TestRevalidateRequiresRequestScope(t *testing.T) {
	if err := RevalidateTag(context.Background(), "products"); !errors.Is(err, ErrNoRequestScope) {
		t.Fatalf("RevalidateTag() error = %v, want ErrNoRequestScope", err)
	}
	if err := RevalidatePath(context.Background(), "/products/widget"); !errors.Is(err, ErrNoRequestScope) {
		t.Fatalf("RevalidatePath() error = %v, want ErrNoRequestScope", err)
	}
}

// TestRevalidateTagBumpsBeforeReturning is the guarantee an action depends on:
// once the call returns, the store can no longer serve entries built under the
// old version, so the action's response is safe to send.
func TestRevalidateTagBumpsBeforeReturning(t *testing.T) {
	clock := newTestClock()
	store := newFakeStore(clock)
	runtime := newTestRuntime(t, store, clock)
	ctx := scopedContext(runtime, false)

	if err := RevalidateTag(ctx, "products"); err != nil {
		t.Fatalf("RevalidateTag() error = %v", err)
	}

	versions, err := store.TagVersions(ctx, []string{"products"})
	if err != nil {
		t.Fatalf("TagVersions() error = %v", err)
	}
	if versions["products"] != 1 {
		t.Fatalf("products version = %d, want 1", versions["products"])
	}
	scope, _ := RequestScopeFrom(ctx)
	if tags := scope.RefreshTags(); len(tags) != 1 || tags[0] != "products" {
		t.Fatalf("recorded refresh tags = %v, want [products]", tags)
	}
}

// TestRevalidatePathIsGranular proves a path revalidation bumps that one
// path's tag rather than anything a route might share: publishing one product
// must not evict the rest of the catalog.
func TestRevalidatePathIsGranular(t *testing.T) {
	clock := newTestClock()
	store := newFakeStore(clock)
	runtime := newTestRuntime(t, store, clock)
	ctx := scopedContext(runtime, false)

	if err := RevalidatePath(ctx, "/products/widget/"); err != nil {
		t.Fatalf("RevalidatePath() error = %v", err)
	}

	versions, err := store.TagVersions(ctx, []string{"path:/products/widget", "path:/products/gadget", "products"})
	if err != nil {
		t.Fatalf("TagVersions() error = %v", err)
	}
	if versions["path:/products/widget"] != 1 {
		t.Fatalf("bumped versions = %v, want the widget path at 1", versions)
	}
	if versions["path:/products/gadget"] != 0 || versions["products"] != 0 {
		t.Fatalf("bumped versions = %v, want only the widget path bumped", versions)
	}
	scope, _ := RequestScopeFrom(ctx)
	if paths := scope.RefreshPaths(); len(paths) != 1 || paths[0] != "/products/widget" {
		t.Fatalf("recorded refresh paths = %v, want the normalized [/products/widget]", paths)
	}
}

func TestRevalidatePathRejectsUnusablePaths(t *testing.T) {
	clock := newTestClock()
	runtime := newTestRuntime(t, newFakeStore(clock), clock)
	ctx := scopedContext(runtime, false)

	if err := RevalidatePath(ctx, "products/widget"); err == nil {
		t.Fatal("expected a relative path to be rejected")
	}
	if err := RevalidatePath(ctx, "/products/../admin"); err == nil {
		t.Fatal("expected a traversal path to be rejected")
	}
}

// TestRevalidateOnPrivateRequestStillInvalidates: privacy gates what a request
// may read from or write to the cache, not what its mutations invalidate. An
// authenticated author publishing a page must still evict the public entry.
func TestRevalidateOnPrivateRequestStillInvalidates(t *testing.T) {
	clock := newTestClock()
	store := newFakeStore(clock)
	runtime := newTestRuntime(t, store, clock)
	ctx := scopedContext(runtime, true)

	if err := RevalidateTag(ctx, "products"); err != nil {
		t.Fatalf("RevalidateTag() error = %v", err)
	}
	versions, _ := store.TagVersions(ctx, []string{"products"})
	if versions["products"] != 1 {
		t.Fatalf("products version = %d, want 1", versions["products"])
	}
}

// TestRevalidateWithoutRuntimeHandleStillRecords keeps the action envelope
// honest on deployments running without a cache: there is nothing to
// invalidate, but the client still needs to know what changed.
func TestRevalidateWithoutRuntimeHandleStillRecords(t *testing.T) {
	ctx := WithRequestScope(context.Background(), NewRequestScope(false))

	if err := RevalidateTag(ctx, "products"); err != nil {
		t.Fatalf("RevalidateTag() error = %v", err)
	}
	if err := RevalidatePath(ctx, "/products/widget"); err != nil {
		t.Fatalf("RevalidatePath() error = %v", err)
	}

	scope, _ := RequestScopeFrom(ctx)
	refresh := ActionRefreshFromScope(scope)
	if refresh == nil || len(refresh.Tags) != 1 || len(refresh.Paths) != 1 {
		t.Fatalf("ActionRefreshFromScope() = %+v", refresh)
	}
}

func TestRevalidateReportsStoreFailures(t *testing.T) {
	clock := newTestClock()
	store := newFakeStore(clock)
	store.bumpErr = errors.New("cache unreachable")
	runtime := newTestRuntime(t, store, clock)

	if err := RevalidateTag(scopedContext(runtime, false), "products"); err == nil {
		t.Fatal("expected a failed bump to be reported: silently skipping it serves invalidated data")
	}
}

func TestPathTagNormalizes(t *testing.T) {
	tag, err := PathTag("/products//widget/")
	if err != nil {
		t.Fatalf("PathTag() error = %v", err)
	}
	if tag != "path:/products/widget" {
		t.Fatalf("PathTag() = %q", tag)
	}
}
