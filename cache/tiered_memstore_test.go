// This file is an external test package on purpose: cache/memstore imports
// cache, so only a cache_test package can exercise the real store through the
// public API.
package cache_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Origens-Dev/gobeyond/cache"
	"github.com/Origens-Dev/gobeyond/cache/memstore"
)

func newTieredRuntime(t *testing.T, l1, l2 cache.Store) *cache.Runtime {
	t.Helper()
	runtime, err := cache.NewRuntime(cache.RuntimeConfig{
		DeployPrefix: "deploy",
		BuildID:      "build-1",
		Store:        cache.Tiered(l1, l2, cache.TieredOptions{}),
	})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	return runtime
}

func scoped(runtime *cache.Runtime) context.Context {
	return cache.WithRequestScope(context.Background(), cache.NewRequestScope(false, cache.WithRuntimeHandle(runtime)))
}

func loadTitle(t *testing.T, ctx context.Context, calls *atomic.Int32) string {
	t.Helper()
	value, err := cache.Load(ctx, cache.Options{
		Name:       "catalog.title",
		Args:       []any{"widget"},
		Revalidate: time.Minute,
		Tags:       []string{"catalog", "product:widget"},
	}, cache.JSONCodec[string](), func(context.Context) (string, error) {
		return fmt.Sprintf("v%d", calls.Add(1)), nil
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	return value
}

// TestLoadOverTieredMemstores runs the real composite the runtime installs -
// a local tier in front of a shared one - and checks the two behaviours that
// only show up when both are present: the shared tier answers an instance that
// never computed the value, and a tag bump clears both tiers.
func TestLoadOverTieredMemstores(t *testing.T) {
	shared := memstore.New(memstore.Options{MaxTTL: time.Hour})
	firstInstance := newTieredRuntime(t, memstore.New(memstore.Options{}), shared)
	secondInstance := newTieredRuntime(t, memstore.New(memstore.Options{}), shared)
	var calls atomic.Int32

	if got := loadTitle(t, scoped(firstInstance), &calls); got != "v1" {
		t.Fatalf("first instance Load() = %q, want v1", got)
	}
	if got := loadTitle(t, scoped(secondInstance), &calls); got != "v1" {
		t.Fatalf("second instance Load() = %q, want the shared tier's v1", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("loader ran %d times, want 1", calls.Load())
	}

	if err := cache.RevalidateTag(scoped(firstInstance), "product:widget"); err != nil {
		t.Fatalf("RevalidateTag() error = %v", err)
	}
	if got := loadTitle(t, scoped(firstInstance), &calls); got != "v2" {
		t.Fatalf("Load() after the bump = %q, want v2", got)
	}
}

// TestLoadWithoutASharedTier is the deployment with no cache endpoint
// configured: Tiered runs L1-only and everything above it is unchanged.
func TestLoadWithoutASharedTier(t *testing.T) {
	runtime := newTieredRuntime(t, memstore.New(memstore.Options{}), nil)
	var calls atomic.Int32

	if got := loadTitle(t, scoped(runtime), &calls); got != "v1" {
		t.Fatalf("Load() = %q, want v1", got)
	}
	if got := loadTitle(t, scoped(runtime), &calls); got != "v1" {
		t.Fatalf("Load() = %q, want the cached v1", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("loader ran %d times, want 1", calls.Load())
	}
}

// TestWatchTagBumpsIsOptionalWiring: a local tier pair has nothing to
// broadcast, and the helper must degrade to a no-op rather than error.
func TestWatchTagBumpsIsOptionalWiring(t *testing.T) {
	if err := cache.WatchTagBumps(context.Background(), memstore.New(memstore.Options{}), memstore.New(memstore.Options{})); err != nil {
		t.Fatalf("WatchTagBumps() error = %v", err)
	}
}
