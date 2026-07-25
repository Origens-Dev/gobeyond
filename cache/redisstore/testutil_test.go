package redisstore

import (
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// manualClock is a controllable time source shared between a Store under
// test and the fakeCommander backing it, so Get/Set TTL behavior can be
// tested deterministically.
type manualClock struct {
	mu  sync.Mutex
	now time.Time
}

func newManualClock(start time.Time) *manualClock {
	return &manualClock{now: start}
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// newTestStore builds a Store around a fakeCommander, applying the same
// defaults New would but without going through Options.Client (a
// fakeCommander is not a redis.UniversalClient). Tests own client's
// lifecycle expectations directly, so Close is registered as cleanup.
func newTestStore(t *testing.T, client commander, opts Options) *Store {
	t.Helper()
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	clock := opts.Clock
	if clock == nil {
		clock = time.Now
	}
	workers := opts.WriteWorkers
	if workers <= 0 {
		workers = DefaultWriteWorkers
	}
	queue := opts.WriteQueue
	if queue <= 0 {
		queue = DefaultWriteQueue
	}
	writeTimeout := opts.WriteTimeout
	if writeTimeout <= 0 {
		writeTimeout = DefaultWriteTimeout
	}
	store := &Store{
		client:       client,
		namespace:    opts.Namespace,
		logger:       logger,
		clock:        clock,
		writeTimeout: writeTimeout,
		jobs:         make(chan writeJob, queue),
	}
	store.startWorkers(workers)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// waitFor polls cond until it reports true or the deadline passes, failing
// the test in the latter case. It exists because the write pool runs on its
// own goroutines: tests need to observe their effects without a fixed sleep.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("condition not met within %s", timeout)
		}
		time.Sleep(time.Millisecond)
	}
}
