package redisstore

import (
	"context"
	"time"
)

// commander is the narrow slice of Redis commands Store needs, factored out
// behind an interface so tests can exercise Store's logic (encoding,
// namespacing, the write pool, CAS decisions) against a hand-written
// in-memory fake instead of a real Redis server or a hundred-plus-method
// fake of redis.UniversalClient.
type commander interface {
	// getWithTTL fetches key's raw payload and remaining TTL in one round
	// trip. found is false, with the other return values meaningless, when
	// the key does not exist. ttl is <= 0 when the key exists but carries no
	// expiry (should not happen for entries this store wrote, since every
	// write sets a PX TTL, but is handled rather than assumed away).
	getWithTTL(ctx context.Context, key string) (payload string, found bool, ttl time.Duration, err error)
	// mget returns, for each key, whether it existed and its value if so.
	// The two result slices are the same length as keys and index-aligned
	// with it.
	mget(ctx context.Context, keys []string) (values []string, found []bool, err error)
	del(ctx context.Context, key string) error
	incr(ctx context.Context, key string) (int64, error)
	setNX(ctx context.Context, key, value string, ttl time.Duration) (bool, error)
	publish(ctx context.Context, channel, payload string) error
	// evalCAS runs the write-time compare-and-set (see casScript):
	// tagKeys[i] is checked against expectedVersions[i] for every i, and the
	// payload is stored under key with the given PX ttl only when every pair
	// matches. persisted reports which branch ran.
	evalCAS(ctx context.Context, key, payload string, ttl time.Duration, tagKeys []string, expectedVersions []int64) (persisted bool, err error)
	// subscribe blocks delivering channel's messages to onMessage until ctx
	// is done (returning nil) or the subscription itself fails (returning
	// that error). It must not let a delivery-side problem with one message
	// abort the loop.
	subscribe(ctx context.Context, channel string, onMessage func(payload string)) error
	close() error
}
