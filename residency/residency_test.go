package residency

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func newTestClock() *testClock {
	return &testClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// constLoad returns a LoadFunc yielding value with the given decoded weight
// and counting invocations.
func constLoad(value string, weight int64, calls *atomic.Int32) LoadFunc[string] {
	return func(context.Context) (string, int64, int64, error) {
		calls.Add(1)
		return value, weight, weight, nil
	}
}

func mustGet(t *testing.T, c *Cache[string], key string, weight int64, load LoadFunc[string]) string {
	t.Helper()
	v, err := c.Get(context.Background(), key, weight, weight, load)
	if err != nil {
		t.Fatalf("Get(%q): %v", key, err)
	}
	return v
}

func TestHitAndMiss(t *testing.T) {
	c := New[string](Options{})
	defer c.Close()

	var calls atomic.Int32
	load := constLoad("v1", 100, &calls)

	if got := mustGet(t, c, "k1", 100, load); got != "v1" {
		t.Fatalf("first Get = %q, want v1", got)
	}
	if got := mustGet(t, c, "k1", 100, load); got != "v1" {
		t.Fatalf("second Get = %q, want v1", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("load calls = %d, want 1", calls.Load())
	}

	stats := c.Stats()
	if stats.Hits != 1 || stats.Misses != 1 {
		t.Fatalf("hits/misses = %d/%d, want 1/1", stats.Hits, stats.Misses)
	}
	if stats.Entries != 1 || stats.ResidentBytes != 100 {
		t.Fatalf("entries/bytes = %d/%d, want 1/100", stats.Entries, stats.ResidentBytes)
	}
	if stats.InFlight != 0 {
		t.Fatalf("in-flight = %d, want 0", stats.InFlight)
	}
}

func TestSingleflightCoalescing(t *testing.T) {
	c := New[string](Options{})
	defer c.Close()

	var calls atomic.Int32
	gate := make(chan struct{})
	started := make(chan struct{})
	var startOnce sync.Once
	load := func(context.Context) (string, int64, int64, error) {
		calls.Add(1)
		startOnce.Do(func() { close(started) })
		<-gate
		return "shared", 10, 10, nil
	}

	const waiters = 8
	results := make(chan string, waiters)
	errs := make(chan error, waiters)
	for i := 0; i < waiters; i++ {
		go func() {
			v, err := c.Get(context.Background(), "k", 10, 10, load)
			results <- v
			errs <- err
		}()
	}

	<-started
	close(gate)

	for i := 0; i < waiters; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("waiter error: %v", err)
		}
		if v := <-results; v != "shared" {
			t.Fatalf("waiter value = %q, want shared", v)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("load calls = %d, want 1 (coalesced)", calls.Load())
	}
}

func TestCanceledWaiterDoesNotCancelLoad(t *testing.T) {
	c := New[string](Options{})
	defer c.Close()

	var calls atomic.Int32
	gate := make(chan struct{})
	started := make(chan struct{})
	var loadCtxErr atomic.Value
	load := func(ctx context.Context) (string, int64, int64, error) {
		calls.Add(1)
		close(started)
		<-gate
		loadCtxErr.Store(fmt.Sprintf("%v", ctx.Err()))
		return "v", 10, 10, nil
	}

	// The leader itself uses a cancelable context: canceling it must not
	// cancel the shared load either.
	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	leaderDone := make(chan error, 1)
	go func() {
		_, err := c.Get(leaderCtx, "k", 10, 10, load)
		leaderDone <- err
	}()
	<-started

	// A second waiter joins the in-flight load, then cancels.
	waiterCtx, cancelWaiter := context.WithCancel(context.Background())
	waiterDone := make(chan error, 1)
	go func() {
		_, err := c.Get(waiterCtx, "k", 10, 10, load)
		waiterDone <- err
	}()
	time.Sleep(10 * time.Millisecond) // let the waiter join the flight
	cancelWaiter()
	select {
	case err := <-waiterDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waiter error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled waiter did not return promptly while load in flight")
	}

	cancelLeader()
	select {
	case err := <-leaderDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("leader error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled leader did not return promptly")
	}

	// The load was never canceled and its result populates the cache.
	close(gate)
	deadline := time.Now().Add(2 * time.Second)
	for c.Stats().Entries == 0 {
		if time.Now().After(deadline) {
			t.Fatal("load result never populated the cache")
		}
		time.Sleep(time.Millisecond)
	}
	if got := loadCtxErr.Load(); got != "<nil>" {
		t.Fatalf("load ctx err = %v, want <nil>", got)
	}
	if got := mustGet(t, c, "k", 10, load); got != "v" {
		t.Fatalf("post-load Get = %q, want v", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("load calls = %d, want 1", calls.Load())
	}
}

func TestOversizedLoadedButNotCached(t *testing.T) {
	// Threshold = MaxResidentBytes/8 = 100.
	c := New[string](Options{MaxResidentBytes: 800})
	defer c.Close()

	var calls atomic.Int32
	load := constLoad("big", 101, &calls)

	if got := mustGet(t, c, "big", 101, load); got != "big" {
		t.Fatalf("Get = %q, want big", got)
	}
	if got := mustGet(t, c, "big", 101, load); got != "big" {
		t.Fatalf("Get = %q, want big", got)
	}
	if calls.Load() != 2 {
		t.Fatalf("load calls = %d, want 2 (oversized never cached)", calls.Load())
	}
	stats := c.Stats()
	if stats.Entries != 0 || stats.ResidentBytes != 0 {
		t.Fatalf("entries/bytes = %d/%d, want 0/0", stats.Entries, stats.ResidentBytes)
	}
	if stats.OversizedLoads != 2 {
		t.Fatalf("oversized loads = %d, want 2", stats.OversizedLoads)
	}

	// At exactly the threshold the value is cached.
	var borderCalls atomic.Int32
	border := constLoad("border", 100, &borderCalls)
	mustGet(t, c, "border", 100, border)
	mustGet(t, c, "border", 100, border)
	if borderCalls.Load() != 1 {
		t.Fatalf("border load calls = %d, want 1", borderCalls.Load())
	}
}

func TestIdleExpiration(t *testing.T) {
	clock := newTestClock()
	c := New[string](Options{Clock: clock.Now}) // default idle expiry 10m
	defer c.Close()

	var calls atomic.Int32
	load := constLoad("v", 10, &calls)

	mustGet(t, c, "k", 10, load)
	clock.advance(9 * time.Minute)
	mustGet(t, c, "k", 10, load)
	if calls.Load() != 1 {
		t.Fatalf("load calls after 9m = %d, want 1", calls.Load())
	}

	clock.advance(10*time.Minute + time.Second)
	mustGet(t, c, "k", 10, load)
	if calls.Load() != 2 {
		t.Fatalf("load calls after idle window = %d, want 2", calls.Load())
	}
	if stats := c.Stats(); stats.IdleExpirations != 1 {
		t.Fatalf("idle expirations = %d, want 1", stats.IdleExpirations)
	}
}

func TestSweepIdleReclaimsWithoutTraffic(t *testing.T) {
	clock := newTestClock()
	c := New[string](Options{Clock: clock.Now})
	defer c.Close()

	var calls atomic.Int32
	mustGet(t, c, "k", 10, constLoad("v", 10, &calls))

	clock.advance(10*time.Minute + time.Second)
	c.sweepIdle()

	stats := c.Stats()
	if stats.Entries != 0 || stats.ResidentBytes != 0 {
		t.Fatalf("entries/bytes after sweep = %d/%d, want 0/0", stats.Entries, stats.ResidentBytes)
	}
	if stats.IdleExpirations != 1 {
		t.Fatalf("idle expirations = %d, want 1", stats.IdleExpirations)
	}
}

func TestTrim(t *testing.T) {
	c := New[string](Options{MaxResidentBytes: 800})
	defer c.Close()

	var calls atomic.Int32
	for i := 0; i < 4; i++ {
		key := fmt.Sprintf("k%d", i)
		mustGet(t, c, key, 100, constLoad(key, 100, &calls))
	}
	held := mustGet(t, c, "k3", 100, constLoad("k3", 100, &calls))

	c.Trim(250)
	stats := c.Stats()
	if stats.ResidentBytes > 250 {
		t.Fatalf("resident bytes after Trim(250) = %d, want <= 250", stats.ResidentBytes)
	}
	if stats.Entries != 2 {
		t.Fatalf("entries after Trim(250) = %d, want 2", stats.Entries)
	}

	// Trim evicts from the LRU end: k2 and k3 (touched most recently) stay.
	before := calls.Load()
	mustGet(t, c, "k3", 100, constLoad("k3", 100, &calls))
	if calls.Load() != before {
		t.Fatal("Trim evicted the most recently used entry")
	}

	c.Trim(0)
	stats = c.Stats()
	if stats.Entries != 0 || stats.ResidentBytes != 0 {
		t.Fatalf("entries/bytes after Trim(0) = %d/%d, want 0/0", stats.Entries, stats.ResidentBytes)
	}

	// The value returned before the trim is still valid for its holder.
	if held != "k3" {
		t.Fatalf("held value corrupted: %q", held)
	}
}

func TestWeightEviction(t *testing.T) {
	// 8 entries of weight 100 fill the 800-byte budget exactly; the 9th
	// evicts the probation LRU entry (k0).
	c := New[string](Options{MaxResidentBytes: 800})
	defer c.Close()

	var calls atomic.Int32
	for i := 0; i < 9; i++ {
		key := fmt.Sprintf("k%d", i)
		mustGet(t, c, key, 100, constLoad(key, 100, &calls))
	}

	stats := c.Stats()
	if stats.Entries != 8 || stats.ResidentBytes != 800 {
		t.Fatalf("entries/bytes = %d/%d, want 8/800", stats.Entries, stats.ResidentBytes)
	}
	if stats.Evictions != 1 {
		t.Fatalf("evictions = %d, want 1", stats.Evictions)
	}

	before := calls.Load()
	mustGet(t, c, "k0", 100, constLoad("k0", 100, &calls))
	if calls.Load() != before+1 {
		t.Fatal("k0 should have been evicted and reloaded")
	}
}

func TestEntryCountEviction(t *testing.T) {
	c := New[string](Options{MaxEntries: 2})
	defer c.Close()

	var calls atomic.Int32
	mustGet(t, c, "a", 1, constLoad("a", 1, &calls))
	mustGet(t, c, "b", 1, constLoad("b", 1, &calls))
	mustGet(t, c, "c", 1, constLoad("c", 1, &calls))

	if stats := c.Stats(); stats.Entries != 2 || stats.Evictions != 1 {
		t.Fatalf("entries/evictions = %d/%d, want 2/1", stats.Entries, stats.Evictions)
	}
	before := calls.Load()
	mustGet(t, c, "a", 1, constLoad("a", 1, &calls))
	if calls.Load() != before+1 {
		t.Fatal("oldest entry a should have been evicted")
	}
}

func TestProtectedSegmentSurvivesScan(t *testing.T) {
	c := New[string](Options{MaxResidentBytes: 800})
	defer c.Close()

	var calls atomic.Int32
	for i := 0; i < 8; i++ {
		key := fmt.Sprintf("k%d", i)
		mustGet(t, c, key, 100, constLoad(key, 100, &calls))
	}
	// Promote k0 to the protected segment.
	mustGet(t, c, "k0", 100, constLoad("k0", 100, &calls))
	if stats := c.Stats(); stats.ProtectedEntries != 1 || stats.ProtectedBytes != 100 {
		t.Fatalf("protected entries/bytes = %d/%d, want 1/100", stats.ProtectedEntries, stats.ProtectedBytes)
	}

	// A scan of new keys evicts probation entries, never protected k0.
	for i := 8; i < 15; i++ {
		key := fmt.Sprintf("k%d", i)
		mustGet(t, c, key, 100, constLoad(key, 100, &calls))
	}
	before := calls.Load()
	mustGet(t, c, "k0", 100, constLoad("k0", 100, &calls))
	if calls.Load() != before {
		t.Fatal("protected entry k0 was evicted by a probation scan")
	}
}

func TestNegativeCacheImmutableFailuresOnly(t *testing.T) {
	c := New[string](Options{})
	defer c.Close()

	base := errors.New("digest mismatch")
	var immutableCalls atomic.Int32
	immutableLoad := func(context.Context) (string, int64, int64, error) {
		immutableCalls.Add(1)
		return "", 0, 0, ImmutableError(base)
	}
	if _, err := c.Get(context.Background(), "corrupt", 10, 10, immutableLoad); !errors.Is(err, base) {
		t.Fatalf("first error = %v, want wrapped %v", err, base)
	}
	if _, err := c.Get(context.Background(), "corrupt", 10, 10, immutableLoad); !errors.Is(err, base) {
		t.Fatalf("second error = %v, want wrapped %v", err, base)
	}
	if immutableCalls.Load() != 1 {
		t.Fatalf("immutable load calls = %d, want 1 (negative cached)", immutableCalls.Load())
	}

	transient := errors.New("connection reset")
	var transientCalls atomic.Int32
	transientLoad := func(context.Context) (string, int64, int64, error) {
		transientCalls.Add(1)
		return "", 0, 0, transient
	}
	for i := 0; i < 2; i++ {
		if _, err := c.Get(context.Background(), "flaky", 10, 10, transientLoad); !errors.Is(err, transient) {
			t.Fatalf("transient error = %v, want %v", err, transient)
		}
	}
	if transientCalls.Load() != 2 {
		t.Fatalf("transient load calls = %d, want 2 (never negative cached)", transientCalls.Load())
	}

	stats := c.Stats()
	if stats.NegativeEntries != 1 {
		t.Fatalf("negative entries = %d, want 1", stats.NegativeEntries)
	}
	if stats.NegativeHits != 1 {
		t.Fatalf("negative hits = %d, want 1", stats.NegativeHits)
	}
}

func TestNegativeCacheTTL(t *testing.T) {
	clock := newTestClock()
	c := New[string](Options{Clock: clock.Now, NegativeTTL: time.Minute})
	defer c.Close()

	var calls atomic.Int32
	load := func(context.Context) (string, int64, int64, error) {
		calls.Add(1)
		return "", 0, 0, ImmutableError(errors.New("bad record"))
	}
	c.Get(context.Background(), "k", 10, 10, load)
	c.Get(context.Background(), "k", 10, 10, load)
	if calls.Load() != 1 {
		t.Fatalf("load calls before TTL = %d, want 1", calls.Load())
	}

	clock.advance(time.Minute + time.Second)
	c.Get(context.Background(), "k", 10, 10, load)
	if calls.Load() != 2 {
		t.Fatalf("load calls after TTL = %d, want 2", calls.Load())
	}
}

func TestNegativeCacheBounded(t *testing.T) {
	c := New[string](Options{NegativeMaxEntries: 2})
	defer c.Close()

	load := func(context.Context) (string, int64, int64, error) {
		return "", 0, 0, ImmutableError(errors.New("bad"))
	}
	for i := 0; i < 3; i++ {
		c.Get(context.Background(), fmt.Sprintf("k%d", i), 10, 10, load)
	}
	if stats := c.Stats(); stats.NegativeEntries != 2 {
		t.Fatalf("negative entries = %d, want 2", stats.NegativeEntries)
	}

	// The oldest remembered failure (k0) was dropped, so k0 loads again.
	var calls atomic.Int32
	counting := func(context.Context) (string, int64, int64, error) {
		calls.Add(1)
		return "", 0, 0, ImmutableError(errors.New("bad"))
	}
	c.Get(context.Background(), "k0", 10, 10, counting)
	if calls.Load() != 1 {
		t.Fatalf("k0 load calls = %d, want 1 (its negative entry was evicted)", calls.Load())
	}
}

func TestDecodeSemaphoreBoundsConcurrentLoads(t *testing.T) {
	// Two loads of estimated peak 60 cannot overlap under a 100-byte budget.
	c := New[int](Options{DecodeSemaphoreBytes: 100})
	defer c.Close()

	var current, maxSeen atomic.Int32
	load := func(context.Context) (int, int64, int64, error) {
		n := current.Add(1)
		for {
			seen := maxSeen.Load()
			if n <= seen || maxSeen.CompareAndSwap(seen, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		current.Add(-1)
		return 1, 60, 60, nil
	}

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := c.Get(context.Background(), fmt.Sprintf("k%d", i), 60, 60, load); err != nil {
				t.Errorf("Get: %v", err)
			}
		}(i)
	}
	wg.Wait()

	if maxSeen.Load() != 1 {
		t.Fatalf("max concurrent loads = %d, want 1", maxSeen.Load())
	}
}

func TestSemaphoreClampsOversizedPeak(t *testing.T) {
	c := New[string](Options{DecodeSemaphoreBytes: 100})
	defer c.Close()

	var calls atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		// Estimated peak far above the semaphore budget must not deadlock.
		mustGet(t, c, "huge", 1<<30, constLoad("v", 10, &calls))
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("load with peak above semaphore budget deadlocked")
	}
}

func TestCloseAndPreCanceledContext(t *testing.T) {
	c := New[string](Options{})

	var calls atomic.Int32
	mustGet(t, c, "k", 10, constLoad("v", 10, &calls))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Get(ctx, "k", 10, 10, constLoad("v", 10, &calls)); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled Get error = %v, want context.Canceled", err)
	}

	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := c.Get(context.Background(), "k", 10, 10, constLoad("v", 10, &calls)); !errors.Is(err, ErrClosed) {
		t.Fatalf("Get after Close error = %v, want ErrClosed", err)
	}
	if stats := c.Stats(); stats.Entries != 0 {
		t.Fatalf("entries after Close = %d, want 0", stats.Entries)
	}
}

func TestLoadFinishingAfterCloseIsNotRetained(t *testing.T) {
	c := New[string](Options{})

	gate := make(chan struct{})
	started := make(chan struct{})
	load := func(context.Context) (string, int64, int64, error) {
		close(started)
		<-gate
		return "v", 10, 10, nil
	}

	done := make(chan error, 1)
	var value string
	go func() {
		v, err := c.Get(context.Background(), "k", 10, 10, load)
		value = v
		done <- err
	}()
	<-started

	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	close(gate)
	if err := <-done; err != nil {
		t.Fatalf("in-flight Get after Close: %v", err)
	}
	if value != "v" {
		t.Fatalf("in-flight Get value = %q, want v", value)
	}
	if stats := c.Stats(); stats.Entries != 0 {
		t.Fatalf("entries after late load = %d, want 0", stats.Entries)
	}
}
