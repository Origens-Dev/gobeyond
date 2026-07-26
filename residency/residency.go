// Package residency implements the bounded in-process residency cache from
// ADR 004 (lazy route residency). It keeps lazily decoded, immutable build
// artifacts — render plans and packaged static entries — resident between
// requests, bounded by entry count and by estimated decoded bytes.
//
// The cache is a segmented LRU (SLRU): entries land in a probation segment on
// first load and move to a protected segment on their next hit, so a one-off
// scan of cold routes cannot flush the working set. Concurrent loads for the
// same key are coalesced into a single decode, and total in-flight cold-load
// work is bounded by a weighted semaphore over estimated peak decode weight.
// A canceled waiter returns promptly without canceling the shared decode; the
// finished result still populates the cache for the next request.
//
// Eviction, idle expiration, and Trim drop only the cache's own reference.
// Values already returned to callers remain valid for as long as those
// callers hold them; reclamation is left entirely to the garbage collector
// and the cache never forces a collection.
//
// The package deliberately knows nothing about the pack container format.
// PlanStore and StaticEntries implementations wrap a Cache and supply the
// decode step as a LoadFunc.
package residency

import (
	"container/list"
	"context"
	"errors"
	"sync"
	"time"
)

// Defaults from ADR 004. They are per-cache: a plan store and a static entry
// store each get their own budgets.
const (
	// DefaultMaxEntries bounds resident entries when Options.MaxEntries is
	// zero. ADR 004 uses 64 for plans and 128 for static entries; the plan
	// figure is the package default.
	DefaultMaxEntries = 64
	// DefaultMaxResidentBytes bounds total estimated decoded bytes when
	// Options.MaxResidentBytes is zero.
	DefaultMaxResidentBytes = 32 << 20
	// DefaultIdleExpiry drops entries that have not been read for this long.
	DefaultIdleExpiry = 10 * time.Minute
	// DefaultDecodeSemaphoreBytes bounds the summed estimated peak weight of
	// concurrent cold loads when Options.DecodeSemaphoreBytes is zero.
	DefaultDecodeSemaphoreBytes = 32 << 20
	// DefaultNegativeMaxEntries bounds the negative cache when
	// Options.NegativeMaxEntries is zero.
	DefaultNegativeMaxEntries = 256
	// DefaultNegativeTTL bounds how long an immutable failure is remembered
	// when Options.NegativeTTL is zero.
	DefaultNegativeTTL = 5 * time.Minute
)

// ProtectedSegmentFraction is the share of the entry and byte budgets
// reserved for the protected SLRU segment. It is a design-locked constant
// (ADR 004), not an option.
const ProtectedSegmentFraction = 0.8

// ErrClosed is returned by Get after Close.
var ErrClosed = errors.New("residency: cache closed")

// errLoadPanicked is handed to waiters when a load panicked. The panic keeps
// unwinding in the loader goroutine; waiters must not receive a zero value as
// if the load had succeeded.
var errLoadPanicked = errors.New("residency: load panicked")

// LoadFunc performs one cold load: read, verify, and decode a record. It
// returns the decoded value, the estimated resident weight of the decoded
// value in bytes, and the estimated peak transient weight of the decode
// itself. The context it receives is detached from any single caller's
// cancellation because the result is shared; loads should be bounded by their
// own I/O deadlines, not by the first requester's patience.
type LoadFunc[V any] func(ctx context.Context) (value V, decodedWeight, peakWeight int64, err error)

// Options configures a Cache. The zero value is valid and yields the
// defaults above. For the duration and count fields, zero means "use the
// default" and a negative value disables the mechanism entirely.
type Options struct {
	// MaxEntries bounds the number of resident entries. Zero or negative
	// yields DefaultMaxEntries.
	MaxEntries int
	// MaxResidentBytes bounds the summed estimated decoded weight of
	// resident entries. It also fixes the oversized threshold: a value whose
	// decoded weight exceeds MaxResidentBytes/8 is returned to the caller
	// but never inserted. Zero or negative yields DefaultMaxResidentBytes.
	MaxResidentBytes int64
	// IdleExpiry drops entries not read for this long. Zero yields
	// DefaultIdleExpiry; negative disables idle expiration.
	IdleExpiry time.Duration
	// DecodeSemaphoreBytes bounds the summed estimated peak weight of
	// concurrent cold loads. Zero yields DefaultDecodeSemaphoreBytes;
	// negative disables the bound.
	DecodeSemaphoreBytes int64
	// NegativeMaxEntries bounds the negative cache. Zero yields
	// DefaultNegativeMaxEntries; negative disables negative caching.
	NegativeMaxEntries int
	// NegativeTTL bounds how long an immutable failure is remembered. Zero
	// yields DefaultNegativeTTL; negative disables negative caching.
	NegativeTTL time.Duration
	// Clock overrides time.Now, for tests.
	Clock func() time.Time
}

// Stats is a point-in-time snapshot of cache state and counters.
type Stats struct {
	// Entries and ResidentBytes cover both SLRU segments.
	Entries       int
	ResidentBytes int64
	// ProtectedEntries and ProtectedBytes cover the protected segment only.
	ProtectedEntries int
	ProtectedBytes   int64

	Hits   uint64
	Misses uint64
	// Evictions counts entries dropped for capacity, including by Trim.
	Evictions uint64
	// IdleExpirations counts entries dropped for exceeding IdleExpiry.
	IdleExpirations uint64
	// OversizedLoads counts successful loads returned but not inserted
	// because their decoded weight exceeded MaxResidentBytes/8.
	OversizedLoads uint64

	NegativeEntries int
	NegativeHits    uint64

	// InFlight is the number of loads currently running or being awaited.
	InFlight int
}

// Cache is a bounded SLRU residency cache. It is safe for concurrent use.
type Cache[V any] struct {
	maxEntries          int
	maxBytes            int64
	oversizeBytes       int64
	protectedMaxEntries int
	protectedMaxBytes   int64
	idle                time.Duration
	negativeMax         int
	negativeTTL         time.Duration
	now                 func() time.Time
	sem                 *weightedSemaphore

	mu             sync.Mutex
	closed         bool
	entries        map[string]*entry[V]
	probation      *list.List // front = most recently used
	protected      *list.List // front = most recently used
	bytes          int64
	protectedBytes int64
	flights        map[string]*flight[V]
	negative       map[string]*negativeEntry
	negativeOrder  *list.List // front = oldest; element values are keys

	hits            uint64
	misses          uint64
	evictions       uint64
	idleExpirations uint64
	oversizedLoads  uint64
	negativeHits    uint64

	stopJanitor chan struct{}
	janitorDone chan struct{}
}

type flight[V any] struct {
	done  chan struct{}
	value V
	err   error
}

// New builds a Cache from opts.
func New[V any](opts Options) *Cache[V] {
	maxEntries := opts.MaxEntries
	if maxEntries <= 0 {
		maxEntries = DefaultMaxEntries
	}
	maxBytes := opts.MaxResidentBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxResidentBytes
	}
	idle := opts.IdleExpiry
	if idle == 0 {
		idle = DefaultIdleExpiry
	} else if idle < 0 {
		idle = 0
	}
	semBytes := opts.DecodeSemaphoreBytes
	if semBytes == 0 {
		semBytes = DefaultDecodeSemaphoreBytes
	}
	negativeMax := opts.NegativeMaxEntries
	if negativeMax == 0 {
		negativeMax = DefaultNegativeMaxEntries
	} else if negativeMax < 0 {
		negativeMax = 0
	}
	negativeTTL := opts.NegativeTTL
	if negativeTTL == 0 {
		negativeTTL = DefaultNegativeTTL
	} else if negativeTTL < 0 {
		negativeMax = 0
		negativeTTL = 0
	}
	now := opts.Clock
	if now == nil {
		now = time.Now
	}

	protectedMaxEntries := int(float64(maxEntries) * ProtectedSegmentFraction)
	if protectedMaxEntries < 1 {
		protectedMaxEntries = 1
	}

	c := &Cache[V]{
		maxEntries:          maxEntries,
		maxBytes:            maxBytes,
		oversizeBytes:       maxBytes / 8,
		protectedMaxEntries: protectedMaxEntries,
		protectedMaxBytes:   int64(float64(maxBytes) * ProtectedSegmentFraction),
		idle:                idle,
		negativeMax:         negativeMax,
		negativeTTL:         negativeTTL,
		now:                 now,
		entries:             make(map[string]*entry[V]),
		probation:           list.New(),
		protected:           list.New(),
		flights:             make(map[string]*flight[V]),
		negative:            make(map[string]*negativeEntry),
		negativeOrder:       list.New(),
	}
	if semBytes > 0 {
		c.sem = newWeightedSemaphore(semBytes)
	}
	if c.idle > 0 {
		c.startJanitor()
	}
	return c
}

// Get returns the value for key, loading it with load on a miss. Concurrent
// callers for the same key share one load. estimatedDecoded and estimatedPeak
// come from the caller's index (pack index weights per ADR 004) and are used
// for the decode semaphore before the load has run; the entry's resident
// weight uses the decodedWeight the load itself reports, falling back to
// estimatedDecoded when the load reports nothing.
//
// If ctx is canceled while waiting, Get returns ctx.Err() promptly; the
// shared load keeps running and its result still populates the cache.
func (c *Cache[V]) Get(ctx context.Context, key string, estimatedDecoded, estimatedPeak int64, load LoadFunc[V]) (V, error) {
	var zero V
	if err := ctx.Err(); err != nil {
		return zero, err
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return zero, ErrClosed
	}
	if e, ok := c.entries[key]; ok {
		now := c.now()
		if c.idle > 0 && now.Sub(e.lastAccess) > c.idle {
			c.removeEntryLocked(e)
			c.idleExpirations++
		} else {
			c.hits++
			e.lastAccess = now
			c.touchLocked(e)
			value := e.value
			c.mu.Unlock()
			return value, nil
		}
	}
	if err, ok := c.negativeLookupLocked(key); ok {
		c.negativeHits++
		c.mu.Unlock()
		return zero, err
	}
	c.misses++
	f, ok := c.flights[key]
	if !ok {
		f = &flight[V]{done: make(chan struct{})}
		c.flights[key] = f
		// The load context is detached from this caller's cancellation
		// (but keeps its values) because the result is shared with every
		// waiter and with future requests via the cache.
		go c.runLoad(context.WithoutCancel(ctx), key, estimatedDecoded, estimatedPeak, load, f)
	}
	c.mu.Unlock()

	select {
	case <-f.done:
		return f.value, f.err
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}

func (c *Cache[V]) runLoad(ctx context.Context, key string, estimatedDecoded, estimatedPeak int64, load LoadFunc[V], f *flight[V]) {
	if c.sem != nil {
		n := c.sem.clamp(max(estimatedPeak, estimatedDecoded, 1))
		c.sem.acquire(n)
		defer c.sem.release(n)
	}
	completed := false
	defer func() {
		if !completed {
			var zero V
			c.finishLoad(key, f, zero, 0, errLoadPanicked)
		}
	}()
	value, decodedWeight, _, err := load(ctx)
	completed = true
	if decodedWeight <= 0 {
		decodedWeight = estimatedDecoded
	}
	if decodedWeight < 0 {
		decodedWeight = 0
	}
	c.finishLoad(key, f, value, decodedWeight, err)
}

func (c *Cache[V]) finishLoad(key string, f *flight[V], value V, weight int64, err error) {
	c.mu.Lock()
	if c.flights[key] == f {
		delete(c.flights, key)
	}
	switch {
	case err != nil:
		if !c.closed && IsImmutable(err) {
			c.storeNegativeLocked(key, err)
		}
	case c.closed:
		// Late load after Close: hand the value to waiters, keep nothing.
	case weight > c.oversizeBytes:
		c.oversizedLoads++
	default:
		c.removeNegativeLocked(key)
		c.insertLocked(key, value, weight)
	}
	c.mu.Unlock()
	f.value, f.err = value, err
	close(f.done)
}

// Stats returns a snapshot of cache state and counters.
func (c *Cache[V]) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Stats{
		Entries:          len(c.entries),
		ResidentBytes:    c.bytes,
		ProtectedEntries: c.protected.Len(),
		ProtectedBytes:   c.protectedBytes,
		Hits:             c.hits,
		Misses:           c.misses,
		Evictions:        c.evictions,
		IdleExpirations:  c.idleExpirations,
		OversizedLoads:   c.oversizedLoads,
		NegativeEntries:  len(c.negative),
		NegativeHits:     c.negativeHits,
		InFlight:         len(c.flights),
	}
}

// Trim evicts least-recently-used entries (probation first) until estimated
// resident bytes are at or below targetBytes. Trim(0) clears every evictable
// entry. Values already returned to callers remain valid.
func (c *Cache[V]) Trim(targetBytes int64) {
	if targetBytes < 0 {
		targetBytes = 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for c.bytes > targetBytes || (targetBytes == 0 && len(c.entries) > 0) {
		if !c.evictOneLocked() {
			return
		}
	}
}

// Close releases the cache's references and stops its janitor. Loads already
// in flight complete and return to their waiters but are not retained. Close
// is idempotent; Get after Close returns ErrClosed.
func (c *Cache[V]) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.entries = make(map[string]*entry[V])
	c.probation.Init()
	c.protected.Init()
	c.bytes = 0
	c.protectedBytes = 0
	c.negative = make(map[string]*negativeEntry)
	c.negativeOrder.Init()
	stop, done := c.stopJanitor, c.janitorDone
	c.mu.Unlock()
	if stop != nil {
		close(stop)
		<-done
	}
	return nil
}

// startJanitor sweeps idle-expired entries in the background so residency
// shrinks even without traffic. Get also expires lazily, so the janitor is a
// reclamation floor, not a correctness requirement.
func (c *Cache[V]) startJanitor() {
	interval := c.idle / 4
	if interval < time.Second {
		interval = time.Second
	}
	if interval > time.Minute {
		interval = time.Minute
	}
	c.stopJanitor = make(chan struct{})
	c.janitorDone = make(chan struct{})
	go func() {
		defer close(c.janitorDone)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-c.stopJanitor:
				return
			case <-ticker.C:
				c.sweepIdle()
			}
		}
	}()
}

func (c *Cache[V]) sweepIdle() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.idle <= 0 {
		return
	}
	now := c.now()
	for _, e := range c.entries {
		if now.Sub(e.lastAccess) > c.idle {
			c.removeEntryLocked(e)
			c.idleExpirations++
		}
	}
	for key, ne := range c.negative {
		if !now.Before(ne.expires) {
			c.removeNegativeLocked(key)
		}
	}
}
