package redisstore

import (
	"context"
	"sync/atomic"
	"time"
)

// Defaults for the write-behind pool. They favor bounding worst-case memory
// and Redis load over never dropping a write: a dropped write just means the
// entry stays a cache miss, which every reader already handles.
const (
	DefaultWriteWorkers = 4
	DefaultWriteQueue   = 256
	DefaultWriteTimeout = 2 * time.Second
)

// writeJob is one queued Set, holding everything a worker needs to run the
// write-time CAS without touching Store's fields (so processJob needs no
// lock). ctx is the caller's original context, kept only for its values -
// see detachedWriteContext for why it is never used for cancellation.
type writeJob struct {
	ctx              context.Context
	key              string
	payload          string
	ttl              time.Duration
	tagKeys          []string
	expectedVersions []int64
}

// Stats snapshots the write-behind pool's lifetime counters. It exists so
// tests (and, eventually, metrics) can observe pool behavior that Set's
// fire-and-forget return value hides on purpose.
type Stats struct {
	// Enqueued counts writes accepted onto the queue.
	Enqueued int64
	// Dropped counts writes discarded because the queue was full.
	Dropped int64
	// Persisted counts writes whose CAS matched and were stored.
	Persisted int64
	// Rejected counts writes whose CAS did not match: some tag was bumped
	// between the version read and the write running.
	Rejected int64
	// Failed counts writes that errored talking to Redis.
	Failed int64
}

type poolStats struct {
	enqueued  atomic.Int64
	dropped   atomic.Int64
	persisted atomic.Int64
	rejected  atomic.Int64
	failed    atomic.Int64
}

func (s *poolStats) snapshot() Stats {
	return Stats{
		Enqueued:  s.enqueued.Load(),
		Dropped:   s.dropped.Load(),
		Persisted: s.persisted.Load(),
		Rejected:  s.rejected.Load(),
		Failed:    s.failed.Load(),
	}
}

// startWorkers launches n workers draining s.jobs. It is called once from
// New, before the Store is returned, so there is no race with Set or Close.
func (s *Store) startWorkers(n int) {
	s.workers.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer s.workers.Done()
			for job := range s.jobs {
				s.runWrite(job)
			}
		}()
	}
}

// detachedWriteContext derives the context a queued write actually runs
// with: it keeps parent's values (for logging/tracing) but not its
// cancellation, because a request context canceling when the HTTP response
// is already on the wire must not abort a write that is only now getting a
// worker. The detached context still gets its own bound, DefaultWriteTimeout
// or Options.WriteTimeout, so a wedged connection cannot hold a worker
// forever.
func (s *Store) detachedWriteContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), s.writeTimeout)
}

func (s *Store) runWrite(job writeJob) {
	ctx, cancel := s.detachedWriteContext(job.ctx)
	defer cancel()
	persisted, err := s.client.evalCAS(ctx, job.key, job.payload, job.ttl, job.tagKeys, job.expectedVersions)
	switch {
	case err != nil:
		s.stats.failed.Add(1)
		s.logger.Warn("redisstore: write-behind Set failed", "key", job.key, "error", err)
	case persisted:
		s.stats.persisted.Add(1)
	default:
		s.stats.rejected.Add(1)
	}
}
