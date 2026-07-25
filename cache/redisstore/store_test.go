package redisstore

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Origens-Dev/gobeyond/cache"
)

func TestGetMiss(t *testing.T) {
	fake := newFakeCommander(time.Now)
	store := newTestStore(t, fake, Options{})

	_, ok, err := store.Get(context.Background(), "missing")
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if ok {
		t.Fatal("expected a miss for an absent key")
	}
}

func TestGetHitDerivesExpiresAtFromTTL(t *testing.T) {
	clock := newManualClock(time.Unix(1_700_000_000, 0))
	fake := newFakeCommander(clock.Now)
	store := newTestStore(t, fake, Options{Namespace: "ns", Clock: clock.Now})

	record := cache.Record{Value: []byte("v1"), TagVersions: map[string]int64{"a": 1}}
	payload, err := encodeRecord(record)
	if err != nil {
		t.Fatalf("encodeRecord: %v", err)
	}
	fake.seed("k", string(payload), 30*time.Second)
	fake.seed(tagKey("ns", "a"), "1", 0)

	got, ok, err := store.Get(context.Background(), "k")
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if !ok {
		t.Fatal("expected a hit")
	}
	if string(got.Value) != "v1" {
		t.Fatalf("Value = %q, want %q", got.Value, "v1")
	}
	wantExpiresAt := clock.Now().Add(30 * time.Second)
	if !got.ExpiresAt.Equal(wantExpiresAt) {
		t.Fatalf("ExpiresAt = %v, want %v", got.ExpiresAt, wantExpiresAt)
	}
}

func TestGetTagInvalidatedIsMiss(t *testing.T) {
	clock := newManualClock(time.Unix(1_700_000_000, 0))
	fake := newFakeCommander(clock.Now)
	store := newTestStore(t, fake, Options{Namespace: "ns", Clock: clock.Now})

	record := cache.Record{Value: []byte("stale"), TagVersions: map[string]int64{"a": 1}}
	payload, err := encodeRecord(record)
	if err != nil {
		t.Fatalf("encodeRecord: %v", err)
	}
	fake.seed("k", string(payload), 30*time.Second)
	fake.seed(tagKey("ns", "a"), "2", 0) // tag bumped past the version this entry was built under

	_, ok, err := store.Get(context.Background(), "k")
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if ok {
		t.Fatal("expected a miss for a tag-invalidated entry")
	}

	waitFor(t, time.Second, func() bool {
		_, found, _, _ := fake.getWithTTL(context.Background(), "k")
		return !found
	})
}

func TestSetPersistsWhenTagVersionsMatch(t *testing.T) {
	fake := newFakeCommander(time.Now)
	store := newTestStore(t, fake, Options{Namespace: "ns"})
	ctx := context.Background()

	if err := store.BumpTag(ctx, "a"); err != nil { // version becomes 1
		t.Fatalf("BumpTag: %v", err)
	}
	record := cache.Record{Value: []byte("v1"), TagVersions: map[string]int64{"a": 1}}
	if err := store.Set(ctx, "k", record, time.Minute); err != nil {
		t.Fatalf("Set: %v", err)
	}

	waitFor(t, time.Second, func() bool { return store.Stats().Persisted == 1 })
	if store.Stats().Rejected != 0 {
		t.Fatalf("Rejected = %d, want 0", store.Stats().Rejected)
	}

	got, ok, err := store.Get(ctx, "k")
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if !ok || string(got.Value) != "v1" {
		t.Fatalf("Get = (%+v, %v), want a hit with value v1", got, ok)
	}
}

func TestSetRejectedByCASWhenTagBumpedConcurrently(t *testing.T) {
	fake := newFakeCommander(time.Now)
	fake.evalGate = make(chan struct{})
	fake.evalStarted = make(chan struct{}, 1)
	store := newTestStore(t, fake, Options{Namespace: "ns"})
	ctx := context.Background()

	if err := store.BumpTag(ctx, "a"); err != nil { // version becomes 1; the write below reads this version
		t.Fatalf("BumpTag: %v", err)
	}
	record := cache.Record{Value: []byte("v1"), TagVersions: map[string]int64{"a": 1}}
	if err := store.Set(ctx, "k", record, time.Minute); err != nil {
		t.Fatalf("Set: %v", err)
	}

	select {
	case <-fake.evalStarted:
	case <-time.After(time.Second):
		t.Fatal("worker never picked up the queued write")
	}

	if err := store.BumpTag(ctx, "a"); err != nil { // version becomes 2 while the write above is in flight
		t.Fatalf("BumpTag: %v", err)
	}
	close(fake.evalGate)

	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if got := store.Stats().Rejected; got != 1 {
		t.Fatalf("Rejected = %d, want 1", got)
	}
	if got := store.Stats().Persisted; got != 0 {
		t.Fatalf("Persisted = %d, want 0", got)
	}
	if _, ok, _ := store.Get(ctx, "k"); ok {
		t.Fatal("a CAS-rejected write must not be visible")
	}
}

func TestWritePoolDropsWhenQueueFullAndDrainsOnClose(t *testing.T) {
	fake := newFakeCommander(time.Now)
	fake.evalGate = make(chan struct{})
	fake.evalStarted = make(chan struct{}, 1)
	store := newTestStore(t, fake, Options{WriteWorkers: 1, WriteQueue: 1})
	ctx := context.Background()

	set := func(key string) {
		done := make(chan struct{})
		go func() {
			if err := store.Set(ctx, key, cache.Record{Value: []byte(key)}, time.Minute); err != nil {
				t.Errorf("Set(%q): %v", key, err)
			}
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(200 * time.Millisecond):
			t.Fatalf("Set(%q) did not return promptly", key)
		}
	}

	set("k1") // picked up by the sole worker immediately, which then blocks on evalGate
	select {
	case <-fake.evalStarted:
	case <-time.After(time.Second):
		t.Fatal("worker never picked up k1")
	}

	set("k2") // fills the size-1 queue
	set("k3") // queue full and worker busy: must be dropped, not block

	if got := store.Stats().Enqueued; got != 2 {
		t.Fatalf("Enqueued = %d, want 2", got)
	}
	if got := store.Stats().Dropped; got != 1 {
		t.Fatalf("Dropped = %d, want 1", got)
	}

	close(fake.evalGate)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := store.Stats().Persisted; got != 2 {
		t.Fatalf("Persisted after Close = %d, want 2 (Close must drain the queue)", got)
	}
}

func TestSetUsesDetachedContextForTheQueuedWrite(t *testing.T) {
	fake := newFakeCommander(time.Now)
	store := newTestStore(t, fake, Options{WriteTimeout: 100 * time.Millisecond})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // parent is already done before Set is even called

	if err := store.Set(ctx, "k", cache.Record{Value: []byte("v")}, time.Minute); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if got := store.Stats().Persisted; got != 1 {
		t.Fatalf("Persisted = %d, want 1: a Set queued with an already-canceled context must still write", got)
	}
	if got := store.Stats().Failed; got != 0 {
		t.Fatalf("Failed = %d, want 0", got)
	}
}

func TestSetRequiresPositiveTTL(t *testing.T) {
	fake := newFakeCommander(time.Now)
	store := newTestStore(t, fake, Options{})
	if err := store.Set(context.Background(), "k", cache.Record{}, 0); err == nil {
		t.Fatal("expected an error for a non-positive ttl")
	}
}

func TestTagVersionsAbsentIsZero(t *testing.T) {
	fake := newFakeCommander(time.Now)
	store := newTestStore(t, fake, Options{})
	ctx := context.Background()

	versions, err := store.TagVersions(ctx, []string{"a", "b"})
	if err != nil {
		t.Fatalf("TagVersions error: %v", err)
	}
	if versions["a"] != 0 || versions["b"] != 0 {
		t.Fatalf("versions = %v, want both 0", versions)
	}

	if err := store.BumpTag(ctx, "a"); err != nil {
		t.Fatalf("BumpTag: %v", err)
	}
	versions, err = store.TagVersions(ctx, []string{"a", "b"})
	if err != nil {
		t.Fatalf("TagVersions error: %v", err)
	}
	if versions["a"] != 1 {
		t.Fatalf("versions[a] = %d, want 1", versions["a"])
	}
	if versions["b"] != 0 {
		t.Fatalf("versions[b] = %d, want 0", versions["b"])
	}
}

func TestBumpTagPublishFailureDoesNotFailTheBump(t *testing.T) {
	fake := newFakeCommander(time.Now)
	fake.publishErr = context.DeadlineExceeded
	store := newTestStore(t, fake, Options{})
	ctx := context.Background()

	if err := store.BumpTag(ctx, "a"); err != nil {
		t.Fatalf("BumpTag returned an error despite a publish failure: %v", err)
	}
	versions, err := store.TagVersions(ctx, []string{"a"})
	if err != nil {
		t.Fatalf("TagVersions error: %v", err)
	}
	if versions["a"] != 1 {
		t.Fatalf("versions[a] = %d, want 1: the counter must still increment", versions["a"])
	}
}

func TestSubscribeTagBumpsDecodesAndSkipsMalformedMessages(t *testing.T) {
	fake := newFakeCommander(time.Now)
	store := newTestStore(t, fake, Options{Namespace: "ns"})
	ctx, cancel := context.WithCancel(context.Background())

	var mu sync.Mutex
	type bump struct {
		tag     string
		version int64
	}
	var got []bump

	done := make(chan error, 1)
	go func() {
		done <- store.SubscribeTagBumps(ctx, func(tag string, version int64) {
			mu.Lock()
			got = append(got, bump{tag, version})
			mu.Unlock()
		})
	}()

	waitFor(t, time.Second, func() bool { return fake.subscriberCount(tagBumpChannel("ns")) > 0 })

	if err := fake.publish(ctx, tagBumpChannel("ns"), "not json"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := store.BumpTag(ctx, "widgets"); err != nil {
		t.Fatalf("BumpTag: %v", err)
	}

	waitFor(t, time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) == 1
	})

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SubscribeTagBumps returned %v, want nil on clean cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SubscribeTagBumps did not return after ctx was canceled")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0].tag != "widgets" || got[0].version != 1 {
		t.Fatalf("got = %+v, want exactly one bump for widgets@1", got)
	}
}

func TestAcquireLeaseTrueThenFalse(t *testing.T) {
	fake := newFakeCommander(time.Now)
	store := newTestStore(t, fake, Options{})
	ctx := context.Background()

	acquired, err := store.AcquireLease(ctx, "lease:k", time.Minute)
	if err != nil {
		t.Fatalf("AcquireLease error: %v", err)
	}
	if !acquired {
		t.Fatal("expected the first AcquireLease to succeed")
	}

	acquired, err = store.AcquireLease(ctx, "lease:k", time.Minute)
	if err != nil {
		t.Fatalf("AcquireLease error: %v", err)
	}
	if acquired {
		t.Fatal("expected the second AcquireLease to fail while the lease is held")
	}
}

func TestDeleteRemovesEntry(t *testing.T) {
	fake := newFakeCommander(time.Now)
	store := newTestStore(t, fake, Options{})
	ctx := context.Background()
	fake.seed("k", "v", time.Minute)

	if err := store.Delete(ctx, "k"); err != nil {
		t.Fatalf("Delete error: %v", err)
	}
	if _, ok, _ := store.Get(ctx, "k"); ok {
		t.Fatal("expected the entry to be gone after Delete")
	}
	if err := store.Delete(ctx, "missing"); err != nil {
		t.Fatalf("Delete of a missing key must not error: %v", err)
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	fake := newFakeCommander(time.Now)
	store := newTestStore(t, fake, Options{})
	if err := store.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
