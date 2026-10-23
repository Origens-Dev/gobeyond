package cache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type identityKey struct{}

func TestSharedComputationContextIsolation(t *testing.T) {
	for _, mode := range []string{"cached", "no-duration", "no-runtime", "no-scope", "store-failure"} {
		t.Run(mode, func(t *testing.T) {
			clock := newTestClock()
			store := newFakeStore(clock)
			runtime := newTestRuntime(t, store, clock)
			options := productOptions()
			var ctx context.Context = scopedContext(runtime, true)
			switch mode {
			case "no-duration":
				options.Revalidate = 0
			case "no-runtime":
				ctx = WithRequestScope(context.Background(), NewRequestScope(true))
			case "no-scope":
				ctx = context.Background()
			case "store-failure":
				store.getErr = errors.New("offline")
			}
			ctx = context.WithValue(ctx, identityKey{}, "viewer-secret")
			ctx, cancel := context.WithTimeout(ctx, time.Minute)
			defer cancel()
			deadline, _ := ctx.Deadline()
			if scope, ok := RequestScopeFrom(ctx); ok {
				scope.RecordRefreshTag("request-tag")
				if _, err := Memo(ctx, "identity", func(context.Context) (string, error) { return "request-user", nil }); err != nil {
					t.Fatal(err)
				}
			}
			_, err := Load(ctx, options, JSONCodec[string](), func(callback context.Context) (string, error) {
				if callback.Value(identityKey{}) != nil {
					t.Fatal("inherited request identity")
				}
				if got, ok := callback.Deadline(); !ok || !got.Equal(deadline) {
					t.Fatal("deadline not preserved")
				}
				got, err := Memo(callback, "identity", func(context.Context) (string, error) { return "isolated", nil })
				if err != nil || got != "isolated" {
					t.Fatalf("memo inherited: %q %v", got, err)
				}
				scope, _ := RequestScopeFrom(callback)
				if len(scope.RefreshTags()) != 0 {
					t.Fatal("inherited refresh recorder")
				}
				scope.RecordRefreshTag("forbidden")
				if len(scope.RefreshTags()) != 0 {
					t.Fatal("computation recorded request mutation")
				}
				if !errors.Is(RevalidateTag(callback, "products"), ErrSharedComputation) {
					t.Fatal("revalidation allowed")
				}
				if !errors.Is(RevalidatePath(callback, "/products"), ErrSharedComputation) {
					t.Fatal("path revalidation allowed")
				}
				if _, err := InvalidateRemote(callback, RemoteInvalidationOptions{}); !errors.Is(err, ErrSharedComputation) {
					t.Fatal("remote invalidation allowed")
				}
				if _, err := InvalidateHosted(callback, HostedInvalidationOptions{}); !errors.Is(err, ErrSharedComputation) {
					t.Fatal("hosted invalidation allowed")
				}
				if _, err := LoadRoute(callback, productRouteOptions(), JSONCodec[string](), alwaysStorable, func(context.Context) (string, error) { t.Fatal("route loader ran"); return "", nil }); !errors.Is(err, ErrSharedComputation) {
					t.Fatal("route cache allowed")
				}
				cancel()
				if !errors.Is(callback.Err(), context.Canceled) {
					t.Fatal("cancellation not preserved")
				}
				return "ok", nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSharedDataArgsPartitionIdentity(t *testing.T) {
	clock := newTestClock()
	runtime := newTestRuntime(t, newFakeStore(clock), clock)
	var calls atomic.Int32
	for _, input := range []struct{ tenant, user, locale string }{{"a", "u1", "en"}, {"a", "u2", "en"}, {"b", "u1", "en"}, {"a", "u1", "fr"}, {"a", "u1", "en"}} {
		options := productOptions()
		options.Args = []any{input.tenant, input.user, input.locale}
		if _, err := Load(scopedContext(runtime, true), options, JSONCodec[string](), counterLoader(&calls)); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 4 {
		t.Fatalf("identity partitions: %d fills, want 4", calls.Load())
	}
}

func nestedDataLoader(t *testing.T, innerCached bool) func(context.Context) (string, error) {
	t.Helper()
	return func(ctx context.Context) (string, error) {
		duration := testRevalidate
		if !innerCached {
			duration = 0
		}
		return Load(ctx, Options{Name: "inner", Revalidate: duration, Tags: []string{"inner-tag"}}, JSONCodec[string](), func(context.Context) (string, error) { return "data", nil })
	}
}

func TestNestedHitDependenciesReachNewRoute(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "stale"}[stale], func(t *testing.T) {
			clock := newTestClock()
			store := newFakeStore(clock)
			runtime := newTestRuntime(t, store, clock)
			outer := Options{Name: "outer", Revalidate: testRevalidate, Tags: []string{"outer-tag"}}
			loader := nestedDataLoader(t, true)
			if _, err := Load(scopedContext(runtime, true), outer, JSONCodec[string](), loader); err != nil {
				t.Fatal(err)
			}
			if stale {
				clock.advance(testRevalidate + time.Second)
			}
			route := productRouteOptions()
			route.Tags = nil
			if _, err := LoadRoute(scopedContext(runtime, false), route, JSONCodec[string](), alwaysStorable, func(ctx context.Context) (string, error) {
				return Load(ctx, outer, JSONCodec[string](), loader)
			}); err != nil {
				t.Fatal(err)
			}
			record, ok := store.record(productRouteKey(t))
			if !ok {
				t.Fatal("route not stored")
			}
			if _, ok := record.TagVersions["inner-tag"]; !ok {
				t.Fatalf("lost transitive dependency: %v", record.TagVersions)
			}
			if err := RevalidateTag(scopedContext(runtime, true), "inner-tag"); err != nil {
				t.Fatal(err)
			}
			if _, hit, err := store.Get(context.Background(), productRouteKey(t)); err != nil || hit {
				t.Fatalf("route survived dependency invalidation: hit=%v err=%v", hit, err)
			}
		})
	}
}

func TestInvalidationDuringNestedFillRejectsOuterAndRoute(t *testing.T) {
	for _, cached := range []bool{false, true} {
		t.Run(map[bool]string{false: "uncached-inner", true: "cached-inner"}[cached], func(t *testing.T) {
			clock := newTestClock()
			store := newFakeStore(clock)
			runtime := newTestRuntime(t, store, clock)
			outer := Options{Name: "outer", Revalidate: testRevalidate, Tags: []string{"outer-tag"}}
			route := productRouteOptions()
			route.Tags = nil
			_, err := LoadRoute(scopedContext(runtime, false), route, JSONCodec[string](), alwaysStorable, func(ctx context.Context) (string, error) {
				return Load(ctx, outer, JSONCodec[string](), func(ctx context.Context) (string, error) {
					value, err := nestedDataLoader(t, cached)(ctx)
					if err != nil {
						return "", err
					}
					if err := RevalidateTag(scopedContext(runtime, true), "inner-tag"); err != nil {
						return "", err
					}
					return value, nil
				})
			})
			if err != nil {
				t.Fatal(err)
			}
			key, _ := DataKey("deploy", "build-1", "outer", nil)
			if _, ok := store.record(key); ok {
				t.Fatal("stale outer stored")
			}
			if _, ok := store.record(productRouteKey(t)); ok {
				t.Fatal("stale route stored")
			}
		})
	}
}

func TestConflictingDependencyVersionsRejectWrite(t *testing.T) {
	clock := newTestClock()
	store := newFakeStore(clock)
	runtime := newTestRuntime(t, store, clock)
	outer := Options{Name: "outer", Revalidate: testRevalidate, Tags: []string{"inner-tag"}}
	_, err := Load(scopedContext(runtime, false), outer, JSONCodec[string](), func(ctx context.Context) (string, error) {
		if err := RevalidateTag(scopedContext(runtime, true), "inner-tag"); err != nil {
			return "", err
		}
		return nestedDataLoader(t, true)(ctx)
	})
	if err != nil {
		t.Fatal(err)
	}
	key, _ := DataKey("deploy", "build-1", "outer", nil)
	if _, ok := store.record(key); ok {
		t.Fatal("conflicting snapshots stored")
	}
}

func TestSingleflightWaitersReceiveDependencyVersions(t *testing.T) {
	clock := newTestClock()
	store := newFakeStore(clock)
	runtime := newTestRuntime(t, store, clock)
	started, release := make(chan struct{}), make(chan struct{})
	options := Options{Name: "outer", Revalidate: testRevalidate}
	var calls atomic.Int32
	loader := func(ctx context.Context) (string, error) {
		calls.Add(1)
		close(started)
		<-release
		return nestedDataLoader(t, true)(ctx)
	}
	leaderDone := make(chan error, 1)
	go func() {
		_, err := Load(scopedContext(runtime, true), options, JSONCodec[string](), loader)
		leaderDone <- err
	}()
	<-started
	const n = 5
	var wg sync.WaitGroup
	var reached atomic.Int32
	scopes := make([]context.Context, n)
	for i := range scopes {
		deps := &dependencies{}
		scopes[i] = context.WithValue(scopedContext(runtime, false), dependencyContextKey{}, deps)
		wg.Add(1)
		go func(ctx context.Context) {
			defer wg.Done()
			reached.Add(1)
			if _, err := Load(ctx, options, JSONCodec[string](), loader); err != nil {
				t.Error(err)
			}
		}(scopes[i])
	}
	waitFor(t, "waiters", func() bool { return reached.Load() == n })
	close(release)
	wg.Wait()
	if err := <-leaderDone; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("fills=%d", calls.Load())
	}
	for _, ctx := range scopes {
		snapshot := ctx.Value(dependencyContextKey{}).(*dependencies).snapshot()
		if _, ok := snapshot.versions["inner-tag"]; !ok {
			t.Fatalf("waiter lost metadata: %v", snapshot)
		}
	}
}

func TestSharedRefreshHasFreshScopeAndDetachedDeadline(t *testing.T) {
	clock := newTestClock()
	store := newFakeStore(clock)
	runtime := newTestRuntime(t, store, clock)
	ctx, cancel := context.WithTimeout(context.WithValue(scopedContext(runtime, true), identityKey{}, "user"), time.Hour)
	scope, _ := RequestScopeFrom(ctx)
	var calls atomic.Int32
	refreshed := make(chan struct{})
	loader := func(callback context.Context) (string, error) {
		n := calls.Add(1)
		memo, err := Memo(callback, "per-computation", func(context.Context) (int32, error) { return n, nil })
		if err != nil || memo != n {
			t.Errorf("refresh inherited memo: %d %v", memo, err)
		}
		if callback.Value(identityKey{}) != nil {
			t.Error("refresh inherited identity")
		}
		if n == 2 {
			if callback.Err() != nil {
				t.Error("refresh inherited canceled request")
			}
			deadline, ok := callback.Deadline()
			if !ok || time.Until(deadline) > runtime.refreshTimeout {
				t.Error("missing refresh timeout")
			}
			callbackScope, _ := RequestScopeFrom(callback)
			callbackScope.RecordRefreshTag("forbidden")
			close(refreshed)
		}
		return nestedDataLoader(t, true)(callback)
	}
	if _, err := Load(ctx, productOptions(), JSONCodec[string](), loader); err != nil {
		t.Fatal(err)
	}
	clock.advance(testRevalidate + time.Second)
	cancel()
	if _, err := Load(ctx, productOptions(), JSONCodec[string](), loader); err != nil {
		t.Fatal(err)
	}
	select {
	case <-refreshed:
	case <-time.After(2 * time.Second):
		t.Fatal("refresh not run")
	}
	if len(scope.RefreshTags()) != 0 {
		t.Fatal("refresh mutated request recorder")
	}
}

func TestNestedDependencyFailurePreventsParentWrite(t *testing.T) {
	clock := newTestClock()
	store := newFakeStore(clock)
	runtime := newTestRuntime(t, store, clock)
	outer := Options{Name: "outer", Revalidate: testRevalidate}
	_, err := Load(scopedContext(runtime, false), outer, JSONCodec[string](), func(ctx context.Context) (string, error) {
		store.mu.Lock()
		store.versionsErr = errors.New("versions unavailable")
		store.mu.Unlock()
		value, err := nestedDataLoader(t, true)(ctx)
		store.mu.Lock()
		store.versionsErr = nil
		store.mu.Unlock()
		return value, err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, sets, _ := store.counts(); sets != 0 {
		t.Fatalf("unsafe dependency allowed %d writes", sets)
	}
}

func TestCaughtNestedErrorRetainsDependencyFence(t *testing.T) {
	clock := newTestClock()
	store := newFakeStore(clock)
	runtime := newTestRuntime(t, store, clock)
	outer := Options{Name: "outer", Revalidate: testRevalidate}
	_, err := Load(scopedContext(runtime, false), outer, JSONCodec[string](), func(ctx context.Context) (string, error) {
		_, err := Load(ctx, Options{Name: "inner", Revalidate: testRevalidate, Tags: []string{"inner-tag"}}, JSONCodec[string](), func(context.Context) (string, error) { return "", errors.New("origin unavailable") })
		if err == nil {
			t.Fatal("expected inner error")
		}
		if err := RevalidateTag(scopedContext(runtime, true), "inner-tag"); err != nil {
			return "", err
		}
		return "fallback", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	key, _ := DataKey("deploy", "build-1", "outer", nil)
	if _, ok := store.record(key); ok {
		t.Fatal("caught nested error lost dependency fence")
	}
}
