package memstore

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Origens-Dev/gobeyond/cache"
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

func mustSet(t *testing.T, store *Store, key string, record cache.Record, ttl time.Duration) {
	t.Helper()
	if err := store.Set(context.Background(), key, record, ttl); err != nil {
		t.Fatalf("Set(%q) error = %v", key, err)
	}
}

func mustGet(t *testing.T, store *Store, key string) (cache.Record, bool) {
	t.Helper()
	record, hit, err := store.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("Get(%q) error = %v", key, err)
	}
	return record, hit
}

func TestStoreRoundTrip(t *testing.T) {
	store := New(Options{Clock: newTestClock().Now})
	mustSet(t, store, "k", cache.Record{Value: []byte("v")}, time.Minute)

	record, hit := mustGet(t, store, "k")
	if !hit || string(record.Value) != "v" {
		t.Fatalf("Get() = (%q, %v)", record.Value, hit)
	}
	if err := store.Delete(context.Background(), "k"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, hit := mustGet(t, store, "k"); hit {
		t.Fatal("Get() returned a deleted entry")
	}
}

func TestStoreExpiresEntries(t *testing.T) {
	clock := newTestClock()
	store := New(Options{Clock: clock.Now, MaxTTL: time.Hour})
	mustSet(t, store, "k", cache.Record{Value: []byte("v")}, time.Minute)

	clock.advance(59 * time.Second)
	if _, hit := mustGet(t, store, "k"); !hit {
		t.Fatal("entry expired early")
	}
	clock.advance(2 * time.Second)
	if _, hit := mustGet(t, store, "k"); hit {
		t.Fatal("expired entry was served")
	}
	if store.Len() != 0 {
		t.Fatalf("Len() = %d, want the expired entry dropped", store.Len())
	}
}

// TestStoreClampsTTL is the bound on how long this process can serve an entry
// invalidated on another instance whose broadcast never arrived.
func TestStoreClampsTTL(t *testing.T) {
	clock := newTestClock()
	store := New(Options{Clock: clock.Now, MaxTTL: 30 * time.Second})
	mustSet(t, store, "k", cache.Record{Value: []byte("v")}, time.Hour)

	clock.advance(31 * time.Second)
	if _, hit := mustGet(t, store, "k"); hit {
		t.Fatal("entry outlived the store's TTL bound")
	}
}

func TestStoreEvictsLeastRecentlyUsedByCount(t *testing.T) {
	store := New(Options{MaxEntries: 3, Clock: newTestClock().Now})
	for i := 0; i < 3; i++ {
		mustSet(t, store, strconv.Itoa(i), cache.Record{Value: []byte("v")}, time.Minute)
	}
	if _, hit := mustGet(t, store, "0"); !hit {
		t.Fatal("entry 0 missing before eviction")
	}
	mustSet(t, store, "3", cache.Record{Value: []byte("v")}, time.Minute)

	if store.Len() != 3 {
		t.Fatalf("Len() = %d, want 3", store.Len())
	}
	if _, hit := mustGet(t, store, "1"); hit {
		t.Fatal("expected the least recently used entry to be evicted")
	}
	for _, key := range []string{"0", "2", "3"} {
		if _, hit := mustGet(t, store, key); !hit {
			t.Fatalf("entry %q was evicted, want it retained", key)
		}
	}
}

func TestStoreEvictsByByteBudget(t *testing.T) {
	store := New(Options{MaxBytes: 64, MaxEntries: 100, Clock: newTestClock().Now})
	for i := 0; i < 8; i++ {
		mustSet(t, store, strconv.Itoa(i), cache.Record{Value: make([]byte, 16)}, time.Minute)
	}
	if store.Bytes() > 64 {
		t.Fatalf("Bytes() = %d, want at most 64", store.Bytes())
	}
	if _, hit := mustGet(t, store, "0"); hit {
		t.Fatal("expected the oldest entries to be evicted under the byte budget")
	}
}

func TestStoreRefusesOversizedRecords(t *testing.T) {
	store := New(Options{MaxBytes: 32, Clock: newTestClock().Now})
	mustSet(t, store, "k", cache.Record{Value: make([]byte, 64)}, time.Minute)
	if store.Len() != 0 || store.Bytes() != 0 {
		t.Fatalf("oversized record stored: len=%d bytes=%d", store.Len(), store.Bytes())
	}
}

// TestBumpTagDropsEntriesSynchronously is Locked decision 13: an action that
// revalidates a tag must be able to return knowing this process will not serve
// the invalidated entries again, without waiting for a TTL.
func TestBumpTagDropsEntriesSynchronously(t *testing.T) {
	store := New(Options{Clock: newTestClock().Now})
	ctx := context.Background()
	mustSet(t, store, "widget", cache.Record{Value: []byte("v"), TagVersions: map[string]int64{"products": 0, "product:widget": 0}}, time.Minute)
	mustSet(t, store, "gadget", cache.Record{Value: []byte("v"), TagVersions: map[string]int64{"products": 0, "product:gadget": 0}}, time.Minute)

	if err := store.BumpTag(ctx, "product:widget"); err != nil {
		t.Fatalf("BumpTag() error = %v", err)
	}

	if _, hit := mustGet(t, store, "widget"); hit {
		t.Fatal("the invalidated entry was still served")
	}
	if _, hit := mustGet(t, store, "gadget"); !hit {
		t.Fatal("a bump must only drop the entries carrying that tag")
	}
	if store.Len() != 1 {
		t.Fatalf("Len() = %d, want the invalidated entry dropped eagerly rather than on read", store.Len())
	}
}

// TestGetRejectsStaleTagVersions covers the other half of Locked decision 13:
// even an entry the bump did not drop - one written under an older version
// afterwards - is refused on read.
func TestGetRejectsStaleTagVersions(t *testing.T) {
	store := New(Options{Clock: newTestClock().Now})
	ctx := context.Background()
	if err := store.BumpTag(ctx, "products"); err != nil {
		t.Fatalf("BumpTag() error = %v", err)
	}

	if err := store.Set(ctx, "k", cache.Record{Value: []byte("v"), TagVersions: map[string]int64{"products": 0}}, time.Minute); !errors.Is(err, cache.ErrStaleWrite) {
		t.Fatalf("Set() error = %v, want ErrStaleWrite", err)
	}
	if _, hit := mustGet(t, store, "k"); hit {
		t.Fatal("an entry fenced against an older tag version must never be served")
	}
}

// TestSetAdoptsNewerTagVersions is how L1 learns about bumps it never saw: the
// shared tier's versions arrive on the records written through this store.
func TestSetAdoptsNewerTagVersions(t *testing.T) {
	store := New(Options{Clock: newTestClock().Now})
	ctx := context.Background()
	mustSet(t, store, "old", cache.Record{Value: []byte("old"), TagVersions: map[string]int64{"products": 3}}, time.Minute)
	mustSet(t, store, "new", cache.Record{Value: []byte("new"), TagVersions: map[string]int64{"products": 4}}, time.Minute)

	versions, err := store.TagVersions(ctx, []string{"products"})
	if err != nil {
		t.Fatalf("TagVersions() error = %v", err)
	}
	if versions["products"] != 4 {
		t.Fatalf("products version = %d, want the adopted 4", versions["products"])
	}
	if _, hit := mustGet(t, store, "old"); hit {
		t.Fatal("adopting a newer version must drop the entries built under the older one")
	}
	if _, hit := mustGet(t, store, "new"); !hit {
		t.Fatal("the entry that carried the newer version was dropped")
	}
}

func TestAdoptTagVersionIsMonotonic(t *testing.T) {
	store := New(Options{Clock: newTestClock().Now})
	ctx := context.Background()
	store.AdoptTagVersion("products", 5)
	store.AdoptTagVersion("products", 2)

	versions, _ := store.TagVersions(ctx, []string{"products"})
	if versions["products"] != 5 {
		t.Fatalf("products version = %d, want 5", versions["products"])
	}
}

// TestAdoptTagVersionDropsEntries is the pub/sub purge path: a broadcast from
// another instance drops this instance's copies immediately.
func TestAdoptTagVersionDropsEntries(t *testing.T) {
	store := New(Options{Clock: newTestClock().Now})
	mustSet(t, store, "k", cache.Record{Value: []byte("v"), TagVersions: map[string]int64{"products": 0}}, time.Minute)

	store.AdoptTagVersion("products", 1)

	if store.Len() != 0 {
		t.Fatalf("Len() = %d, want the broadcast to have dropped the entry", store.Len())
	}
}

func TestTagVersionsReportsUnknownTagsAsZero(t *testing.T) {
	store := New(Options{Clock: newTestClock().Now})
	versions, err := store.TagVersions(context.Background(), []string{"never-seen"})
	if err != nil {
		t.Fatalf("TagVersions() error = %v", err)
	}
	version, present := versions["never-seen"]
	if !present || version != 0 {
		t.Fatalf("versions = %v, want never-seen at 0", versions)
	}
}

func TestAcquireLease(t *testing.T) {
	clock := newTestClock()
	store := New(Options{Clock: clock.Now})
	ctx := context.Background()

	granted, err := store.AcquireLease(ctx, "k#refresh", time.Second)
	if err != nil || !granted {
		t.Fatalf("AcquireLease() = (%v, %v)", granted, err)
	}
	granted, _ = store.AcquireLease(ctx, "k#refresh", time.Second)
	if granted {
		t.Fatal("a held lease must not be granted twice")
	}
	clock.advance(2 * time.Second)
	granted, _ = store.AcquireLease(ctx, "k#refresh", time.Second)
	if !granted {
		t.Fatal("an expired lease must be reacquirable")
	}
}

func TestStoreIsConcurrencySafe(t *testing.T) {
	store := New(Options{MaxEntries: 64, Clock: newTestClock().Now})
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := strconv.Itoa(i % 8)
			record := cache.Record{Value: []byte("v"), TagVersions: map[string]int64{"products": 0}}
			for j := 0; j < 200; j++ {
				_ = store.Set(ctx, key, record, time.Minute)
				_, _, _ = store.Get(ctx, key)
				if j%50 == 0 {
					_ = store.BumpTag(ctx, "products")
				}
			}
		}(i)
	}
	wg.Wait()
}
