// Package memstore implements GoBeyond's in-process L1 cache tier: a bounded
// TTL + LRU byte store with synchronous writes.
//
// L1 is deliberately not a TTL-only cache. Every Get re-checks the tag
// versions an entry was built under, and every tag bump synchronously drops
// the entries it invalidates, so an entry can never outlive its data just
// because its TTL has not elapsed (Locked decision 13). The store's own TTL
// bound exists for the case the version check cannot help with: a tag bumped
// on another instance whose pub/sub broadcast this process never received.
// Keeping that bound short bounds how long a lost broadcast can matter.
package memstore

import (
	"container/list"
	"context"
	"sync"
	"time"

	"github.com/Origens-Dev/gobeyond/cache"
)

// Default bounds. They are intentionally modest: L1 fronts a shared L2 and a
// cheap origin recompute, so an oversized in-process cache mostly buys
// resident memory and longer windows for cross-instance staleness.
const (
	DefaultMaxEntries = 2048
	DefaultMaxBytes   = 32 << 20
	DefaultMaxTTL     = 60 * time.Second
)

// Options configures a Store. The zero value is valid and yields the defaults
// above.
type Options struct {
	// MaxEntries bounds the number of live entries; the least recently used
	// entry is evicted once the bound is exceeded.
	MaxEntries int
	// MaxBytes bounds the total size of live entries by the same LRU rule. A
	// single record larger than the bound is never stored.
	MaxBytes int64
	// MaxTTL clamps every requested TTL. It is the only thing bounding how
	// long this process can serve an entry invalidated elsewhere, so it should
	// stay short whenever an L2 is in play.
	MaxTTL time.Duration
	// Clock overrides time.Now, for tests.
	Clock func() time.Time
}

// Store is a bounded in-process cache.Store. It is safe for concurrent use.
type Store struct {
	maxEntries int
	maxBytes   int64
	maxTTL     time.Duration
	now        func() time.Time

	mu       sync.Mutex
	order    *list.List
	entries  map[string]*list.Element
	tagIndex map[string]map[string]struct{}
	versions map[string]int64
	leases   map[string]time.Time
	bytes    int64
}

type entry struct {
	key    string
	record cache.Record
	size   int64
}

var (
	_ cache.Store             = (*Store)(nil)
	_ cache.Leaser            = (*Store)(nil)
	_ cache.TagVersionAdopter = (*Store)(nil)
)

// New creates a Store with the given bounds.
func New(options Options) *Store {
	store := &Store{
		maxEntries: options.MaxEntries,
		maxBytes:   options.MaxBytes,
		maxTTL:     options.MaxTTL,
		now:        options.Clock,
		order:      list.New(),
		entries:    make(map[string]*list.Element),
		tagIndex:   make(map[string]map[string]struct{}),
		versions:   make(map[string]int64),
		leases:     make(map[string]time.Time),
	}
	if store.maxEntries <= 0 {
		store.maxEntries = DefaultMaxEntries
	}
	if store.maxBytes <= 0 {
		store.maxBytes = DefaultMaxBytes
	}
	if store.maxTTL <= 0 {
		store.maxTTL = DefaultMaxTTL
	}
	if store.now == nil {
		store.now = time.Now
	}
	return store
}

// Get returns the record stored under key, dropping it instead when it has
// expired or when one of the tags it was built under has since been bumped.
// The returned Value slice is shared with the store and must not be mutated.
func (s *Store) Get(_ context.Context, key string) (cache.Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	element, exists := s.entries[key]
	if !exists {
		return cache.Record{}, false, nil
	}
	held := element.Value.(*entry)
	if !s.now().Before(held.record.ExpiresAt) {
		s.removeElement(element)
		return cache.Record{}, false, nil
	}
	if !s.observeVersions(held.record.TagVersions) {
		s.removeElement(element)
		return cache.Record{}, false, nil
	}
	s.order.MoveToFront(element)
	return held.record, true, nil
}

// Set stores record under key for at most ttl, clamped to the configured
// MaxTTL, and reports ErrStaleWrite when one of the record's tags was bumped
// while the value was being computed. A record larger than MaxBytes is
// silently not stored: refusing it keeps the bound honest, and the caller
// already has the value it was going to cache.
func (s *Store) Set(_ context.Context, key string, record cache.Record, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.observeVersions(record.TagVersions) {
		return cache.ErrStaleWrite
	}
	if ttl <= 0 || ttl > s.maxTTL {
		ttl = s.maxTTL
	}
	record.ExpiresAt = s.now().Add(ttl)
	size := int64(len(key) + len(record.Value))
	if size > s.maxBytes {
		s.deleteLocked(key)
		return nil
	}
	s.deleteLocked(key)
	held := &entry{key: key, record: record, size: size}
	s.entries[key] = s.order.PushFront(held)
	s.bytes += size
	for tag := range record.TagVersions {
		keys, exists := s.tagIndex[tag]
		if !exists {
			keys = make(map[string]struct{})
			s.tagIndex[tag] = keys
		}
		keys[key] = struct{}{}
	}
	s.evictLocked()
	return nil
}

// Delete removes key. Deleting a missing key is not an error.
func (s *Store) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleteLocked(key)
	return nil
}

// TagVersions returns this process's view of each tag's version. Tags never
// seen report 0.
func (s *Store) TagVersions(_ context.Context, tags []string) (map[string]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	versions := make(map[string]int64, len(tags))
	for _, tag := range tags {
		if tag == "" {
			continue
		}
		versions[tag] = s.versions[tag]
	}
	return versions, nil
}

// BumpTag increments a tag's version and synchronously drops every entry
// built under an older one, so an action that revalidates a tag can return
// knowing this process will not serve the invalidated entries again.
func (s *Store) BumpTag(_ context.Context, tag string) error {
	if tag == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setVersionLocked(tag, s.versions[tag]+1)
	return nil
}

// AdoptTagVersion applies a tag version learned out of band - from an L2
// broadcast or from an entry another instance wrote - and drops the entries it
// invalidates. Versions only move forward: an older value is ignored.
func (s *Store) AdoptTagVersion(tag string, version int64) {
	if tag == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if version <= s.versions[tag] {
		return
	}
	s.setVersionLocked(tag, version)
}

// AcquireLease grants key's lease for ttl when no unexpired lease is held.
// Leases are process-local, which is exactly enough when this store is the
// only tier; with an L2 present the distributed leaser takes precedence.
func (s *Store) AcquireLease(_ context.Context, key string, ttl time.Duration) (bool, error) {
	if ttl <= 0 {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if expiry, held := s.leases[key]; held && now.Before(expiry) {
		return false, nil
	}
	s.leases[key] = now.Add(ttl)
	return true, nil
}

// Len reports the number of live entries, including any that a subsequent Get
// would discard as expired or invalidated.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

// Bytes reports the total size of live entries counted against MaxBytes.
func (s *Store) Bytes() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bytes
}

// observeVersions reconciles a record's recorded tag versions with this
// process's counters and reports whether the record is still valid. A recorded
// version ahead of the local counter is adopted rather than rejected: it can
// only come from a writer that saw a bump this process missed, and adopting it
// both validates the record and catches the local counter up.
func (s *Store) observeVersions(recorded map[string]int64) bool {
	for tag, version := range recorded {
		current := s.versions[tag]
		switch {
		case version < current:
			return false
		case version > current:
			s.setVersionLocked(tag, version)
		}
	}
	return true
}

func (s *Store) setVersionLocked(tag string, version int64) {
	s.versions[tag] = version
	for key := range s.tagIndex[tag] {
		element, exists := s.entries[key]
		if !exists {
			continue
		}
		if element.Value.(*entry).record.TagVersions[tag] < version {
			s.removeElement(element)
		}
	}
	if len(s.tagIndex[tag]) == 0 {
		delete(s.tagIndex, tag)
	}
}

func (s *Store) evictLocked() {
	for len(s.entries) > s.maxEntries || s.bytes > s.maxBytes {
		oldest := s.order.Back()
		if oldest == nil {
			return
		}
		s.removeElement(oldest)
	}
}

func (s *Store) deleteLocked(key string) {
	if element, exists := s.entries[key]; exists {
		s.removeElement(element)
	}
}

func (s *Store) removeElement(element *list.Element) {
	held := element.Value.(*entry)
	s.order.Remove(element)
	delete(s.entries, held.key)
	s.bytes -= held.size
	for tag := range held.record.TagVersions {
		keys, exists := s.tagIndex[tag]
		if !exists {
			continue
		}
		delete(keys, held.key)
		if len(keys) == 0 {
			delete(s.tagIndex, tag)
		}
	}
}
