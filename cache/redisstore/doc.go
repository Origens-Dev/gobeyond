// Package redisstore implements GoBeyond's shared L2 cache tier on top of
// Redis (ElastiCache Serverless in the reference deployment): a
// cache.Store/cache.Leaser/cache.TagBumpPublisher backed by one Redis
// endpoint, shared across every instance behind a deploy.
//
// # Write-behind, not write-through
//
// Set never talks to Redis synchronously: it hands the record to a bounded
// worker pool and returns immediately, so a slow or unreachable L2 never adds
// latency to the request that produced the value. The tradeoff is that Set
// cannot report ErrStaleWrite - the compare-and-set that would produce it
// happens later, on a worker, against whatever tag versions are current at
// that point, not the ones current when Set was called. Losing a queued
// write (timeout, dropped-because-full, transport error) is treated the same
// as never having cached the entry: correctness rests on the version check
// every Get performs, not on Set succeeding.
//
// # Tag versions are the fence, not the TTL
//
// Every entry is written with a Lua script that re-checks its tags'
// versions immediately before the SET (write-time CAS), and every Get
// re-checks them again against the current counters (read-time
// invalidation), mirroring cache.Store's contract that a tag bump must win
// over a not-yet-expired TTL. Both checks share one decision function,
// casAllows, so the Lua script and the Go read path can never disagree
// about what counts as stale.
//
// # Namespacing
//
// Options.Namespace scopes tag-version keys and the tag-bump pub/sub channel
// to one deploy inside a possibly shared Redis instance; entry keys are not
// touched here because callers (cache.RouteKey / cache.DataKey) already
// namespace them with a deploy prefix before Get/Set ever see them.
package redisstore
