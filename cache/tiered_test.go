package cache

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestTieredReadsL1First(t *testing.T) {
	clock := newTestClock()
	l1, l2 := newFakeStore(clock), newFakeStore(clock)
	store := Tiered(l1, l2, TieredOptions{Clock: clock.Now})
	ctx := context.Background()

	if err := l1.Set(ctx, "k", Record{Value: []byte("local")}, time.Minute); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	record, hit, err := store.Get(ctx, "k")
	if err != nil || !hit {
		t.Fatalf("Get() = (%v, %v, %v)", record, hit, err)
	}
	if string(record.Value) != "local" {
		t.Fatalf("Get() value = %q, want local", record.Value)
	}
	if gets, _, _ := l2.counts(); gets != 0 {
		t.Fatalf("L2 reads = %d, want 0 on an L1 hit", gets)
	}
}

// TestTieredWritesBackToL1 is what makes the shared tier worth having: one
// instance's miss populates its local tier for the next request.
func TestTieredWritesBackToL1(t *testing.T) {
	clock := newTestClock()
	l1, l2 := newFakeStore(clock), newFakeStore(clock)
	store := Tiered(l1, l2, TieredOptions{Clock: clock.Now})
	ctx := context.Background()

	if err := l2.Set(ctx, "k", Record{Value: []byte("shared")}, time.Minute); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if _, hit, err := store.Get(ctx, "k"); err != nil || !hit {
		t.Fatalf("Get() = (%v, %v)", hit, err)
	}

	record, cached := l1.record("k")
	if !cached {
		t.Fatal("an L2 hit must populate L1")
	}
	if string(record.Value) != "shared" {
		t.Fatalf("L1 value = %q, want shared", record.Value)
	}
	if !record.ExpiresAt.Equal(clock.Now().Add(time.Minute)) {
		t.Fatalf("L1 expiry = %v, want the remaining L2 lifetime", record.ExpiresAt)
	}
}

func TestTieredWritesBothTiers(t *testing.T) {
	clock := newTestClock()
	l1, l2 := newFakeStore(clock), newFakeStore(clock)
	store := Tiered(l1, l2, TieredOptions{Clock: clock.Now})

	if err := store.Set(context.Background(), "k", Record{Value: []byte("v")}, time.Minute); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if _, cached := l1.record("k"); !cached {
		t.Fatal("L1 was not written")
	}
	if _, cached := l2.record("k"); !cached {
		t.Fatal("L2 was not written")
	}
}

// TestTieredWithoutL2IsL1Only is the degraded mode of a deployment with no
// shared cache endpoint: everything above the store behaves identically.
func TestTieredWithoutL2IsL1Only(t *testing.T) {
	clock := newTestClock()
	l1 := newFakeStore(clock)
	store := Tiered(l1, nil, TieredOptions{Clock: clock.Now})
	ctx := context.Background()

	if err := store.Set(ctx, "k", Record{Value: []byte("v")}, time.Minute); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	record, hit, err := store.Get(ctx, "k")
	if err != nil || !hit || string(record.Value) != "v" {
		t.Fatalf("Get() = (%q, %v, %v)", record.Value, hit, err)
	}
	if err := store.BumpTag(ctx, "products"); err != nil {
		t.Fatalf("BumpTag() error = %v", err)
	}
	versions, err := store.TagVersions(ctx, []string{"products"})
	if err != nil || versions["products"] != 1 {
		t.Fatalf("TagVersions() = (%v, %v)", versions, err)
	}
}

// TestTieredTagVersionsComeFromL2 matters for the write fence: the versions a
// fill is fenced with must be the ones the shared tier's compare-and-set will
// check it against.
func TestTieredTagVersionsComeFromL2(t *testing.T) {
	clock := newTestClock()
	l1, l2 := newFakeStore(clock), newFakeStore(clock)
	store := Tiered(l1, l2, TieredOptions{Clock: clock.Now})
	ctx := context.Background()

	if err := l2.BumpTag(ctx, "products"); err != nil {
		t.Fatalf("BumpTag() error = %v", err)
	}
	versions, err := store.TagVersions(ctx, []string{"products"})
	if err != nil {
		t.Fatalf("TagVersions() error = %v", err)
	}
	if versions["products"] != 1 {
		t.Fatalf("products version = %d, want the shared tier's 1", versions["products"])
	}
}

func TestTieredBumpsBothTiers(t *testing.T) {
	clock := newTestClock()
	l1, l2 := newFakeStore(clock), newFakeStore(clock)
	store := Tiered(l1, l2, TieredOptions{Clock: clock.Now})
	ctx := context.Background()

	if err := store.BumpTag(ctx, "products"); err != nil {
		t.Fatalf("BumpTag() error = %v", err)
	}
	local, _ := l1.TagVersions(ctx, []string{"products"})
	shared, _ := l2.TagVersions(ctx, []string{"products"})
	if local["products"] != 1 || shared["products"] != 1 {
		t.Fatalf("versions after bump: local=%d shared=%d, want 1 and 1", local["products"], shared["products"])
	}
}

func TestTieredReportsBumpFailures(t *testing.T) {
	clock := newTestClock()
	l1, l2 := newFakeStore(clock), newFakeStore(clock)
	l2.bumpErr = errors.New("cache unreachable")
	store := Tiered(l1, l2, TieredOptions{Clock: clock.Now})

	if err := store.BumpTag(context.Background(), "products"); err == nil {
		t.Fatal("expected the shared tier's failure to surface")
	}
	local, _ := l1.TagVersions(context.Background(), []string{"products"})
	if local["products"] != 1 {
		t.Fatal("a failing shared tier must not stop the local drop")
	}
}

// TestTieredFallsBackToL2OnL1Failure keeps a broken local tier from turning
// every read into a miss.
func TestTieredFallsBackToL2OnL1Failure(t *testing.T) {
	clock := newTestClock()
	l1, l2 := newFakeStore(clock), newFakeStore(clock)
	l1.getErr = errors.New("local failure")
	store := Tiered(l1, l2, TieredOptions{Clock: clock.Now})
	ctx := context.Background()

	if err := l2.Set(ctx, "k", Record{Value: []byte("shared")}, time.Minute); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	record, hit, err := store.Get(ctx, "k")
	if err != nil || !hit || string(record.Value) != "shared" {
		t.Fatalf("Get() = (%q, %v, %v)", record.Value, hit, err)
	}
}

func TestTieredLeasePrefersL2(t *testing.T) {
	clock := newTestClock()
	l1, l2 := newFakeStore(clock), newFakeStore(clock)
	store := Tiered(l1, l2, TieredOptions{Clock: clock.Now}).(Leaser)
	ctx := context.Background()

	granted, err := store.AcquireLease(ctx, "k#refresh", time.Second)
	if err != nil || !granted {
		t.Fatalf("AcquireLease() = (%v, %v)", granted, err)
	}
	if l2.leaseCalls != 1 || l1.leaseCalls != 0 {
		t.Fatalf("lease calls: l1=%d l2=%d, want the shared tier consulted", l1.leaseCalls, l2.leaseCalls)
	}
	granted, err = store.AcquireLease(ctx, "k#refresh", time.Second)
	if err != nil || granted {
		t.Fatalf("second AcquireLease() = (%v, %v), want denied", granted, err)
	}
}
