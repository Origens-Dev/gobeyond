package cache

import (
	"context"
	"sync"
	"time"
)

// testClock is a manually advanced clock shared by a test's Runtime and its
// store, so freshness and expiry are decided by the same instant.
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

// fakeStore is an instrumented in-package Store. The root package cannot
// import cache/memstore in its own tests (that package imports this one), and
// these tests need to inject transport failures and observe every call anyway.
type fakeStore struct {
	clock *testClock

	mu          sync.Mutex
	records     map[string]Record
	versions    map[string]int64
	leases      map[string]time.Time
	getErr      error
	setErr      error
	versionsErr error
	bumpErr     error

	gets         int
	sets         int
	staleWrites  int
	bumps        int
	versionReads int
	leaseCalls   int
}

var (
	_ Store  = (*fakeStore)(nil)
	_ Leaser = (*fakeStore)(nil)
)

func newFakeStore(clock *testClock) *fakeStore {
	return &fakeStore{
		clock:    clock,
		records:  make(map[string]Record),
		versions: make(map[string]int64),
		leases:   make(map[string]time.Time),
	}
}

func (s *fakeStore) Get(_ context.Context, key string) (Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gets++
	if s.getErr != nil {
		return Record{}, false, s.getErr
	}
	record, exists := s.records[key]
	if !exists {
		return Record{}, false, nil
	}
	if !s.clock.Now().Before(record.ExpiresAt) {
		delete(s.records, key)
		return Record{}, false, nil
	}
	for tag, version := range record.TagVersions {
		if s.versions[tag] != version {
			delete(s.records, key)
			return Record{}, false, nil
		}
	}
	return record, true, nil
}

func (s *fakeStore) Set(_ context.Context, key string, record Record, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sets++
	if s.setErr != nil {
		return s.setErr
	}
	for tag, version := range record.TagVersions {
		if s.versions[tag] != version {
			s.staleWrites++
			return ErrStaleWrite
		}
	}
	record.ExpiresAt = s.clock.Now().Add(ttl)
	s.records[key] = record
	return nil
}

func (s *fakeStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.records, key)
	return nil
}

func (s *fakeStore) TagVersions(_ context.Context, tags []string) (map[string]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.versionReads++
	if s.versionsErr != nil {
		return nil, s.versionsErr
	}
	versions := make(map[string]int64, len(tags))
	for _, tag := range tags {
		versions[tag] = s.versions[tag]
	}
	return versions, nil
}

func (s *fakeStore) BumpTag(_ context.Context, tag string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bumps++
	if s.bumpErr != nil {
		return s.bumpErr
	}
	s.versions[tag]++
	for key, record := range s.records {
		if _, tagged := record.TagVersions[tag]; tagged {
			delete(s.records, key)
		}
	}
	return nil
}

func (s *fakeStore) AcquireLease(_ context.Context, key string, ttl time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.leaseCalls++
	now := s.clock.Now()
	if expiry, held := s.leases[key]; held && now.Before(expiry) {
		return false, nil
	}
	s.leases[key] = now.Add(ttl)
	return true, nil
}

func (s *fakeStore) record(key string) (Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, exists := s.records[key]
	return record, exists
}

func (s *fakeStore) counts() (gets, sets, bumps int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gets, s.sets, s.bumps
}
