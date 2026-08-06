package cache_test

import (
	"context"
	"testing"

	"github.com/Origens-Dev/gobeyond/cache"
	"github.com/Origens-Dev/gobeyond/cache/memstore"
)

func TestProfileCanBeUsedWithoutManualDuration(t *testing.T) {
	store := memstore.New(memstore.Options{})
	runtime, err := cache.NewRuntime(cache.RuntimeConfig{DeployPrefix: "d", BuildID: "b", Store: store})
	if err != nil {
		t.Fatal(err)
	}
	scope := cache.NewRequestScope(false, cache.WithRuntimeHandle(runtime))
	ctx := cache.WithRequestScope(context.Background(), scope)
	got, err := cache.Load(ctx, cache.Options{Name: "value", Profile: cache.ProfileUntilInvalidated}, cache.JSONCodec[string](), func(context.Context) (string, error) { return "ok", nil })
	if err != nil || got != "ok" {
		t.Fatalf("Load() = %q, %v", got, err)
	}
}
