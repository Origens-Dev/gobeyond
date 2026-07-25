package cache

import (
	"context"
	"errors"
	"time"
)

// ErrStaleWrite is returned by a Store.Set whose Record carries tag versions
// that no longer match the store's current versions: some Revalidate* call
// bumped one of the entry's tags while the caller was computing the value, so
// persisting it would resurrect data the bump was meant to invalidate. It is
// an expected outcome of the write-time compare-and-set, not a failure -
// callers log it at most and move on with the value they computed.
var ErrStaleWrite = errors.New("cache: entry was invalidated while it was being computed")

// Record is one cache entry: opaque bytes plus the metadata every tier needs
// to decide whether the bytes may still be served.
//
// TagVersions are the tag counters observed immediately before the value was
// computed. They are the fence for both directions of staleness: a Store must
// refuse to persist a Record whose versions have moved on (ErrStaleWrite), and
// must refuse to return one on Get (Locked decision 13 - L1 invalidation is
// never TTL-only). A tag absent from the map is treated as version 0.
//
// FreshUntil bounds the stale-while-revalidate window's fresh half and is
// carried through the store untouched; only cache.Load interprets it.
// ExpiresAt is the entry's hard expiry: stores populate it on Get and ignore
// whatever a caller puts there on Set, where the ttl argument is authoritative.
type Record struct {
	Value       []byte
	TagVersions map[string]int64
	FreshUntil  time.Time
	ExpiresAt   time.Time
}

// Store is the byte-level cache tier cache.Load and (in a later phase) route
// ISR are built on. Implementations live in cache/memstore (bounded in-process
// L1) and cache/redisstore (shared L2); Tiered composes them.
//
// Implementations must be safe for concurrent use, must never return an entry
// that is hard-expired or whose tag versions are stale, and must treat a
// transport failure as an error rather than a miss so callers can tell "not
// cached" from "cache unreachable".
type Store interface {
	// Get returns the record stored under key. The boolean reports a usable
	// hit; a missing, expired, or tag-invalidated entry is (Record{}, false,
	// nil).
	Get(ctx context.Context, key string) (Record, bool, error)
	// Set stores record under key for at most ttl, subject to the store's own
	// TTL bound, and returns ErrStaleWrite when record.TagVersions no longer
	// match the store's current tag versions.
	Set(ctx context.Context, key string, record Record, ttl time.Duration) error
	// Delete removes key. Deleting a missing key is not an error.
	Delete(ctx context.Context, key string) error
	// TagVersions returns the current version of each requested tag. Tags that
	// were never bumped report 0 and are still present in the result.
	TagVersions(ctx context.Context, tags []string) (map[string]int64, error)
	// BumpTag increments a tag's version, invalidating every record that was
	// built under an older version. It is synchronous: once it returns without
	// error, no tier this store fronts may serve an entry fenced by the old
	// version.
	BumpTag(ctx context.Context, tag string) error
}

// Leaser is implemented by stores that can hand out short-lived exclusive
// leases. cache.Load uses one to keep a stale-while-revalidate refresh from
// running on every instance at once; a store that does not implement it simply
// falls back to in-process deduplication.
type Leaser interface {
	// AcquireLease reports whether the caller now holds key's lease for ttl.
	// The lease is never released explicitly - it expires - so ttl must bound
	// the work it guards.
	AcquireLease(ctx context.Context, key string, ttl time.Duration) (bool, error)
}

// TagBumpPublisher is implemented by shared stores that broadcast their tag
// bumps to other instances. Delivery is best-effort and must never be relied
// on for correctness: it only shortens the window in which another instance's
// L1 still holds an entry its own TTL and version check would eventually
// reject anyway.
type TagBumpPublisher interface {
	SubscribeTagBumps(ctx context.Context, onBump func(tag string, version int64)) error
}

// TagVersionAdopter is implemented by local stores that can be told a tag's
// authoritative version out of band, e.g. from a TagBumpPublisher broadcast.
type TagVersionAdopter interface {
	AdoptTagVersion(tag string, version int64)
}

// WatchTagBumps forwards l2's tag-bump broadcasts into l1 so a bump on another
// instance drops this instance's L1 entries early. It blocks until ctx is done
// and returns nil when either store does not support broadcasting, because the
// pub/sub path is an optimization on top of L1's TTL bound and per-Get version
// check, not a correctness requirement.
func WatchTagBumps(ctx context.Context, l1, l2 Store) error {
	publisher, ok := l2.(TagBumpPublisher)
	if !ok {
		return nil
	}
	adopter, ok := l1.(TagVersionAdopter)
	if !ok {
		return nil
	}
	return publisher.SubscribeTagBumps(ctx, adopter.AdoptTagVersion)
}

// normalizeTags removes empty and duplicate tags while keeping the caller's
// order, so that tag sets built from user input produce stable version maps.
func normalizeTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(tags))
	unique := make([]string, 0, len(tags))
	for _, tag := range tags {
		if tag == "" {
			continue
		}
		if _, exists := seen[tag]; exists {
			continue
		}
		seen[tag] = struct{}{}
		unique = append(unique, tag)
	}
	if len(unique) == 0 {
		return nil
	}
	return unique
}
