package redisstore

import (
	"context"
	"strconv"
	"sync"
	"time"
)

// fakeCommander is a hand-written, in-memory commander used only by this
// package's tests. It exists so Store's logic (namespacing, encoding, the
// write pool, and the CAS decision) can be exercised without a real Redis
// server and without faking redis.UniversalClient's much larger surface.
//
// evalCAS deliberately reuses casAllows - the same function the real Lua
// script mirrors - so a test failure here would also indicate the Lua script
// (which this package cannot exercise without a live Redis) disagrees with
// its documented semantics.
type fakeCommander struct {
	mu     sync.Mutex
	clock  func() time.Time
	values map[string]string
	expiry map[string]time.Time
	subs   map[string][]chan string

	publishErr error

	// evalStarted, when non-nil, receives a value each time evalCAS begins,
	// letting a test observe that a worker has picked up a job before the
	// test proceeds. evalGate, when non-nil, blocks evalCAS until the test
	// closes or sends on it, letting a test hold a job "in flight".
	evalStarted chan struct{}
	evalGate    chan struct{}
}

var _ commander = (*fakeCommander)(nil)

func newFakeCommander(clock func() time.Time) *fakeCommander {
	return &fakeCommander{
		clock:  clock,
		values: make(map[string]string),
		expiry: make(map[string]time.Time),
		subs:   make(map[string][]chan string),
	}
}

// seed populates an entry directly, bypassing evalCAS, for tests that only
// care about Get's behavior against a fixture.
func (f *fakeCommander) seed(key, value string, ttl time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.setLocked(key, value, ttl)
}

func (f *fakeCommander) getLocked(key string) (string, bool) {
	value, ok := f.values[key]
	if !ok {
		return "", false
	}
	if expiry, has := f.expiry[key]; has && !f.clock().Before(expiry) {
		delete(f.values, key)
		delete(f.expiry, key)
		return "", false
	}
	return value, true
}

func (f *fakeCommander) setLocked(key, value string, ttl time.Duration) {
	f.values[key] = value
	if ttl > 0 {
		f.expiry[key] = f.clock().Add(ttl)
	} else {
		delete(f.expiry, key)
	}
}

func (f *fakeCommander) getWithTTL(_ context.Context, key string) (string, bool, time.Duration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	value, ok := f.getLocked(key)
	if !ok {
		return "", false, 0, nil
	}
	var ttl time.Duration
	if expiry, has := f.expiry[key]; has {
		if ttl = expiry.Sub(f.clock()); ttl < 0 {
			ttl = 0
		}
	}
	return value, true, ttl, nil
}

func (f *fakeCommander) mget(_ context.Context, keys []string) ([]string, []bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	values := make([]string, len(keys))
	found := make([]bool, len(keys))
	for i, key := range keys {
		if value, ok := f.getLocked(key); ok {
			values[i] = value
			found[i] = true
		}
	}
	return values, found, nil
}

func (f *fakeCommander) del(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.values, key)
	delete(f.expiry, key)
	return nil
}

func (f *fakeCommander) incr(_ context.Context, key string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var current int64
	if value, ok := f.getLocked(key); ok {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return 0, err
		}
		current = parsed
	}
	current++
	f.values[key] = strconv.FormatInt(current, 10)
	delete(f.expiry, key)
	return current, nil
}

func (f *fakeCommander) setNX(_ context.Context, key, value string, ttl time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.getLocked(key); ok {
		return false, nil
	}
	f.setLocked(key, value, ttl)
	return true, nil
}

func (f *fakeCommander) publish(_ context.Context, channel, payload string) error {
	if f.publishErr != nil {
		return f.publishErr
	}
	f.mu.Lock()
	subs := append([]chan string(nil), f.subs[channel]...)
	f.mu.Unlock()
	for _, sub := range subs {
		select {
		case sub <- payload:
		default:
		}
	}
	return nil
}

func (f *fakeCommander) evalCAS(ctx context.Context, key, payload string, ttl time.Duration, tagKeys []string, expectedVersions []int64) (bool, error) {
	if f.evalStarted != nil {
		select {
		case f.evalStarted <- struct{}{}:
		default:
		}
	}
	if f.evalGate != nil {
		select {
		case <-f.evalGate:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	current := make(map[string]string, len(tagKeys))
	expected := make(map[string]int64, len(tagKeys))
	for i, tagKey := range tagKeys {
		expected[tagKey] = expectedVersions[i]
		if value, ok := f.getLocked(tagKey); ok {
			current[tagKey] = value
		}
	}
	if !casAllows(current, expected) {
		return false, nil
	}
	f.setLocked(key, payload, ttl)
	return true, nil
}

func (f *fakeCommander) subscribe(ctx context.Context, channel string, onMessage func(payload string)) error {
	ch := make(chan string, 16)
	f.mu.Lock()
	f.subs[channel] = append(f.subs[channel], ch)
	f.mu.Unlock()
	defer f.removeSub(channel, ch)

	for {
		select {
		case <-ctx.Done():
			return nil
		case payload := <-ch:
			onMessage(payload)
		}
	}
}

func (f *fakeCommander) removeSub(channel string, ch chan string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	subs := f.subs[channel]
	for i, candidate := range subs {
		if candidate == ch {
			f.subs[channel] = append(subs[:i], subs[i+1:]...)
			return
		}
	}
}

func (f *fakeCommander) subscriberCount(channel string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.subs[channel])
}

func (f *fakeCommander) close() error {
	return nil
}
