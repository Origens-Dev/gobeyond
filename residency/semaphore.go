package residency

import (
	"container/list"
	"sync"
)

// weightedSemaphore bounds the summed weight of concurrent cold loads. It is
// FIFO: a heavy waiter at the head blocks lighter waiters behind it, which
// keeps a stream of small decodes from starving a large one. Acquisition
// never gives up — load contexts are detached from caller cancellation, so a
// queued load always runs and its result is shared via the cache.
type weightedSemaphore struct {
	size int64

	mu      sync.Mutex
	cur     int64
	waiters list.List // of *semWaiter
}

type semWaiter struct {
	n     int64
	ready chan struct{}
}

func newWeightedSemaphore(size int64) *weightedSemaphore {
	return &weightedSemaphore{size: size}
}

// clamp bounds a requested weight to [1, size] so a single load whose
// estimated peak exceeds the whole budget still runs (alone) instead of
// deadlocking. Callers must acquire and release the same clamped value.
func (s *weightedSemaphore) clamp(n int64) int64 {
	if n < 1 {
		n = 1
	}
	if n > s.size {
		n = s.size
	}
	return n
}

func (s *weightedSemaphore) acquire(n int64) {
	s.mu.Lock()
	if s.waiters.Len() == 0 && s.cur+n <= s.size {
		s.cur += n
		s.mu.Unlock()
		return
	}
	w := &semWaiter{n: n, ready: make(chan struct{})}
	s.waiters.PushBack(w)
	s.mu.Unlock()
	<-w.ready
}

func (s *weightedSemaphore) release(n int64) {
	s.mu.Lock()
	s.cur -= n
	for {
		front := s.waiters.Front()
		if front == nil {
			break
		}
		w := front.Value.(*semWaiter)
		if s.cur+w.n > s.size {
			break
		}
		s.waiters.Remove(front)
		s.cur += w.n
		close(w.ready)
	}
	s.mu.Unlock()
}
