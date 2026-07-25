package cache

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testRevalidate = 30 * time.Second

func newTestRuntime(t *testing.T, store Store, clock *testClock) *Runtime {
	t.Helper()
	runtime, err := NewRuntime(RuntimeConfig{
		DeployPrefix:   "deploy",
		BuildID:        "build-1",
		Store:          store,
		MaxStale:       60 * time.Second,
		RefreshTimeout: 2 * time.Second,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		Clock:          clock.Now,
	})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	return runtime
}

// scopedContext mimics what the runtime server does per request: a fresh
// RequestScope carrying the privacy flag and the shared cache handle.
func scopedContext(runtime *Runtime, private bool) context.Context {
	return WithRequestScope(context.Background(), NewRequestScope(private, WithRuntimeHandle(runtime)))
}

func productOptions() Options {
	return Options{
		Name:       "catalog.product",
		Args:       []any{"widget"},
		Revalidate: testRevalidate,
		Tags:       []string{"products", "product:widget"},
	}
}

func counterLoader(calls *atomic.Int32) func(context.Context) (string, error) {
	return func(context.Context) (string, error) {
		return fmt.Sprintf("v%d", calls.Add(1)), nil
	}
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestLoadWithoutRequestScopeComputesWithoutCaching(t *testing.T) {
	var calls atomic.Int32
	for i := 0; i < 2; i++ {
		value, err := Load(context.Background(), productOptions(), JSONCodec[string](), counterLoader(&calls))
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if want := fmt.Sprintf("v%d", i+1); value != want {
			t.Fatalf("Load() = %q, want %q", value, want)
		}
	}
}

// TestLoadOnPrivateRequestSkipsTheStore is the fail-closed Get gate: a request
// carrying viewer identity must not read or write a shared entry, and must
// still succeed.
func TestLoadOnPrivateRequestSkipsTheStore(t *testing.T) {
	clock := newTestClock()
	store := newFakeStore(clock)
	runtime := newTestRuntime(t, store, clock)
	var calls atomic.Int32

	for i := 0; i < 2; i++ {
		if _, err := Load(scopedContext(runtime, true), productOptions(), JSONCodec[string](), counterLoader(&calls)); err != nil {
			t.Fatalf("Load() error = %v", err)
		}
	}

	if calls.Load() != 2 {
		t.Fatalf("loader called %d times, want 2", calls.Load())
	}
	if gets, sets, _ := store.counts(); gets != 0 || sets != 0 {
		t.Fatalf("store touched on a private request: gets=%d sets=%d", gets, sets)
	}
}

func TestLoadWithoutRuntimeHandleSkipsTheStore(t *testing.T) {
	ctx := WithRequestScope(context.Background(), NewRequestScope(false))
	var calls atomic.Int32
	if _, err := Load(ctx, productOptions(), JSONCodec[string](), counterLoader(&calls)); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if _, err := Load(ctx, productOptions(), JSONCodec[string](), counterLoader(&calls)); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("loader called %d times, want 2", calls.Load())
	}
}

func TestLoadNonPositiveRevalidateSkipsTheStore(t *testing.T) {
	clock := newTestClock()
	store := newFakeStore(clock)
	runtime := newTestRuntime(t, store, clock)
	options := productOptions()
	options.Revalidate = 0
	var calls atomic.Int32

	if _, err := Load(scopedContext(runtime, false), options, JSONCodec[string](), counterLoader(&calls)); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if gets, sets, _ := store.counts(); gets != 0 || sets != 0 {
		t.Fatalf("store touched without a revalidate window: gets=%d sets=%d", gets, sets)
	}
}

func TestLoadRejectsMisconfiguration(t *testing.T) {
	clock := newTestClock()
	runtime := newTestRuntime(t, newFakeStore(clock), clock)
	ctx := scopedContext(runtime, false)
	loader := func(context.Context) (string, error) { return "", nil }

	if _, err := Load(ctx, Options{Revalidate: testRevalidate}, JSONCodec[string](), loader); err == nil {
		t.Fatal("expected an error for an empty Name")
	}
	if _, err := Load(ctx, productOptions(), nil, loader); err == nil {
		t.Fatal("expected an error for a nil codec")
	}
	if _, err := Load[string](ctx, productOptions(), JSONCodec[string](), nil); err == nil {
		t.Fatal("expected an error for a nil fn")
	}
	options := productOptions()
	options.Args = []any{make(chan int)}
	if _, err := Load(ctx, options, JSONCodec[string](), loader); err == nil {
		t.Fatal("expected an error for args that cannot be canonically encoded")
	}
}

// TestLoadCachesAcrossRequests proves the entry outlives the RequestScope that
// created it: the second request is a different scope on the same runtime.
func TestLoadCachesAcrossRequests(t *testing.T) {
	clock := newTestClock()
	store := newFakeStore(clock)
	runtime := newTestRuntime(t, store, clock)
	var calls atomic.Int32

	first, err := Load(scopedContext(runtime, false), productOptions(), JSONCodec[string](), counterLoader(&calls))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	second, err := Load(scopedContext(runtime, false), productOptions(), JSONCodec[string](), counterLoader(&calls))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if first != "v1" || second != "v1" {
		t.Fatalf("Load() = %q then %q, want v1 twice", first, second)
	}
	if calls.Load() != 1 {
		t.Fatalf("loader called %d times, want 1", calls.Load())
	}
}

func TestLoadKeyCarriesDeployPrefixAndBuildID(t *testing.T) {
	clock := newTestClock()
	store := newFakeStore(clock)
	runtime := newTestRuntime(t, store, clock)
	var calls atomic.Int32

	if _, err := Load(scopedContext(runtime, false), productOptions(), JSONCodec[string](), counterLoader(&calls)); err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	key, err := DataKey("deploy", "build-1", "catalog.product", []any{"widget"})
	if err != nil {
		t.Fatalf("DataKey() error = %v", err)
	}
	if _, stored := store.record(key); !stored {
		t.Fatalf("no entry stored under %q", key)
	}
}

func TestLoadExpiredEntryIsRecomputed(t *testing.T) {
	clock := newTestClock()
	store := newFakeStore(clock)
	runtime := newTestRuntime(t, store, clock)
	var calls atomic.Int32

	if _, err := Load(scopedContext(runtime, false), productOptions(), JSONCodec[string](), counterLoader(&calls)); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	clock.advance(testRevalidate + runtime.maxStale + time.Second)
	value, err := Load(scopedContext(runtime, false), productOptions(), JSONCodec[string](), counterLoader(&calls))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if value != "v2" {
		t.Fatalf("Load() = %q, want v2", value)
	}
}

// TestLoadServesStaleWhileRefreshing covers the stale-while-revalidate path:
// the request past the revalidate deadline is answered from the stale entry,
// and the refreshed value lands without any request having waited for it.
func TestLoadServesStaleWhileRefreshing(t *testing.T) {
	clock := newTestClock()
	store := newFakeStore(clock)
	runtime := newTestRuntime(t, store, clock)
	var calls atomic.Int32
	loader := counterLoader(&calls)

	if _, err := Load(scopedContext(runtime, false), productOptions(), JSONCodec[string](), loader); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	clock.advance(testRevalidate + time.Second)

	stale, err := Load(scopedContext(runtime, false), productOptions(), JSONCodec[string](), loader)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if stale != "v1" {
		t.Fatalf("Load() = %q, want the stale v1", stale)
	}

	key, _ := DataKey("deploy", "build-1", "catalog.product", []any{"widget"})
	waitFor(t, "the background refresh to store v2", func() bool {
		record, stored := store.record(key)
		return stored && string(record.Value) == `"v2"`
	})

	fresh, err := Load(scopedContext(runtime, false), productOptions(), JSONCodec[string](), loader)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if fresh != "v2" {
		t.Fatalf("Load() = %q, want the refreshed v2", fresh)
	}
	if calls.Load() != 2 {
		t.Fatalf("loader called %d times, want 2", calls.Load())
	}
}

// TestLoadStaleRefreshRunsOncePerBurst keeps a burst of stale reads from
// turning into a burst of refresh goroutines.
func TestLoadStaleRefreshRunsOncePerBurst(t *testing.T) {
	clock := newTestClock()
	store := newFakeStore(clock)
	runtime := newTestRuntime(t, store, clock)
	var calls atomic.Int32
	loader := counterLoader(&calls)

	if _, err := Load(scopedContext(runtime, false), productOptions(), JSONCodec[string](), loader); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	clock.advance(testRevalidate + time.Second)
	for i := 0; i < 5; i++ {
		if _, err := Load(scopedContext(runtime, false), productOptions(), JSONCodec[string](), loader); err != nil {
			t.Fatalf("Load() error = %v", err)
		}
	}

	key, _ := DataKey("deploy", "build-1", "catalog.product", []any{"widget"})
	waitFor(t, "the background refresh to store v2", func() bool {
		record, stored := store.record(key)
		return stored && string(record.Value) == `"v2"`
	})
	if calls.Load() != 2 {
		t.Fatalf("loader called %d times, want 2", calls.Load())
	}
}

// TestLoadStaleRefreshIsLeasedAcrossInstances uses two runtimes over one
// store, the way two tasks behind a load balancer share ElastiCache: the
// second instance still answers from its stale entry, but the lease keeps it
// from recomputing a value the first instance is already recomputing.
func TestLoadStaleRefreshIsLeasedAcrossInstances(t *testing.T) {
	clock := newTestClock()
	store := newFakeStore(clock)
	first := newTestRuntime(t, store, clock)
	second := newTestRuntime(t, store, clock)
	var calls atomic.Int32
	release := make(chan struct{})
	loader := func(context.Context) (string, error) {
		version := calls.Add(1)
		if version == 2 {
			<-release
		}
		return fmt.Sprintf("v%d", version), nil
	}

	if _, err := Load(scopedContext(first, false), productOptions(), JSONCodec[string](), loader); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	clock.advance(testRevalidate + time.Second)

	if _, err := Load(scopedContext(first, false), productOptions(), JSONCodec[string](), loader); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	waitFor(t, "the first instance to start refreshing", func() bool { return calls.Load() == 2 })

	stale, err := Load(scopedContext(second, false), productOptions(), JSONCodec[string](), loader)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if stale != "v1" {
		t.Fatalf("Load() = %q, want the stale v1", stale)
	}
	waitFor(t, "the second instance to consult the lease", func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		return store.leaseCalls == 2
	})
	if calls.Load() != 2 {
		t.Fatalf("loader called %d times, want 2: the lease must stop the second refresh", calls.Load())
	}

	close(release)
	key, _ := DataKey("deploy", "build-1", "catalog.product", []any{"widget"})
	waitFor(t, "the refreshed value to land", func() bool {
		record, stored := store.record(key)
		return stored && string(record.Value) == `"v2"`
	})
}

func TestLoadTagBumpInvalidatesEntry(t *testing.T) {
	clock := newTestClock()
	store := newFakeStore(clock)
	runtime := newTestRuntime(t, store, clock)
	var calls atomic.Int32
	loader := counterLoader(&calls)

	if _, err := Load(scopedContext(runtime, false), productOptions(), JSONCodec[string](), loader); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if err := RevalidateTag(scopedContext(runtime, false), "product:widget"); err != nil {
		t.Fatalf("RevalidateTag() error = %v", err)
	}
	value, err := Load(scopedContext(runtime, false), productOptions(), JSONCodec[string](), loader)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if value != "v2" {
		t.Fatalf("Load() = %q, want v2 after the tag bump", value)
	}
}

// TestLoadDoesNotPersistAValueInvalidatedMidFill is the write-time fence: a
// tag bumped while the loader ran must reject the write, or the cache would
// resurrect exactly the data the bump invalidated.
func TestLoadDoesNotPersistAValueInvalidatedMidFill(t *testing.T) {
	clock := newTestClock()
	store := newFakeStore(clock)
	runtime := newTestRuntime(t, store, clock)
	var calls atomic.Int32
	loader := func(ctx context.Context) (string, error) {
		version := calls.Add(1)
		if version == 1 {
			if err := store.BumpTag(ctx, "product:widget"); err != nil {
				return "", err
			}
		}
		return fmt.Sprintf("v%d", version), nil
	}

	value, err := Load(scopedContext(runtime, false), productOptions(), JSONCodec[string](), loader)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if value != "v1" {
		t.Fatalf("Load() = %q, want the computed v1", value)
	}

	key, _ := DataKey("deploy", "build-1", "catalog.product", []any{"widget"})
	if _, stored := store.record(key); stored {
		t.Fatal("a value invalidated while it was computed must not be persisted")
	}
	store.mu.Lock()
	staleWrites := store.staleWrites
	store.mu.Unlock()
	if staleWrites != 1 {
		t.Fatalf("stale writes = %d, want 1", staleWrites)
	}
}

func TestLoadSkipsTheWriteWhenTagVersionsAreUnavailable(t *testing.T) {
	clock := newTestClock()
	store := newFakeStore(clock)
	store.versionsErr = errors.New("cache unreachable")
	runtime := newTestRuntime(t, store, clock)
	var calls atomic.Int32

	value, err := Load(scopedContext(runtime, false), productOptions(), JSONCodec[string](), counterLoader(&calls))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if value != "v1" {
		t.Fatalf("Load() = %q, want v1", value)
	}
	if _, sets, _ := store.counts(); sets != 0 {
		t.Fatalf("store writes = %d, want 0 when the entry cannot be fenced", sets)
	}
}

func TestLoadSurvivesAFailingStore(t *testing.T) {
	clock := newTestClock()
	store := newFakeStore(clock)
	store.getErr = errors.New("cache unreachable")
	store.setErr = errors.New("cache unreachable")
	runtime := newTestRuntime(t, store, clock)
	var calls atomic.Int32

	value, err := Load(scopedContext(runtime, false), productOptions(), JSONCodec[string](), counterLoader(&calls))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if value != "v1" {
		t.Fatalf("Load() = %q, want v1", value)
	}
}

func TestLoadDedupesConcurrentFills(t *testing.T) {
	clock := newTestClock()
	store := newFakeStore(clock)
	runtime := newTestRuntime(t, store, clock)
	var calls atomic.Int32
	release := make(chan struct{})
	loader := func(context.Context) (string, error) {
		<-release
		return fmt.Sprintf("v%d", calls.Add(1)), nil
	}

	const goroutines = 20
	var wg sync.WaitGroup
	values := make([]string, goroutines)
	errs := make([]error, goroutines)
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			values[i], errs[i] = Load(scopedContext(runtime, false), productOptions(), JSONCodec[string](), loader)
		}(i)
	}
	close(release)
	wg.Wait()

	for i := range values {
		if errs[i] != nil {
			t.Fatalf("goroutine %d: Load() error = %v", i, errs[i])
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("loader called %d times, want 1", calls.Load())
	}
}

func TestLoadPropagatesLoaderErrors(t *testing.T) {
	clock := newTestClock()
	store := newFakeStore(clock)
	runtime := newTestRuntime(t, store, clock)
	wantErr := errors.New("origin down")

	_, err := Load(scopedContext(runtime, false), productOptions(), JSONCodec[string](), func(context.Context) (string, error) {
		return "", wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Load() error = %v, want %v", err, wantErr)
	}
	if _, sets, _ := store.counts(); sets != 0 {
		t.Fatalf("store writes = %d, want 0 after a loader error", sets)
	}
}

func TestJSONCodecRoundTrip(t *testing.T) {
	type product struct {
		Slug  string `json:"slug"`
		Price int    `json:"price"`
	}
	codec := JSONCodec[product]()
	encoded, err := codec.Encode(product{Slug: "widget", Price: 12})
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	decoded, err := codec.Decode(encoded)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if decoded.Slug != "widget" || decoded.Price != 12 {
		t.Fatalf("Decode() = %+v", decoded)
	}
}
