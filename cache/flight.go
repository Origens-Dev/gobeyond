package cache

import (
	"errors"
	"sync"
)

// errFillPanicked is handed to callers waiting on a fill whose leader
// panicked. The panic itself keeps unwinding in the leader's goroutine; the
// waiters must not silently receive a zero value as if the fill had succeeded.
var errFillPanicked = errors.New("cache: fill panicked")

// flightGroup deduplicates concurrent fills for the same key inside one
// process. It is the in-process half of fill deduplication; the distributed
// half is the Leaser lease a stale-while-revalidate refresh takes before it
// starts (see Runtime.acquireRefreshLease).
//
// It differs from the RequestScope memo bag in lifetime: the memo bag lives
// for one request, this group lives for the process and only exists while a
// fill is actually running.
type flightGroup struct {
	mu    sync.Mutex
	calls map[string]*flightCall
}

type flightCall struct {
	done  chan struct{}
	value any
	err   error
}

// do runs fn unless an identical key is already in flight, in which case it
// waits for that call and returns its result.
func (g *flightGroup) do(key string, fn func() (any, error)) (any, error) {
	call, leader := g.begin(key)
	if !leader {
		<-call.done
		return call.value, call.err
	}
	completed := false
	defer func() {
		if !completed {
			g.end(key, call, nil, errFillPanicked)
		}
	}()
	value, err := fn()
	completed = true
	g.end(key, call, value, err)
	return value, err
}

func (g *flightGroup) begin(key string) (*flightCall, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.calls == nil {
		g.calls = make(map[string]*flightCall)
	}
	if existing, running := g.calls[key]; running {
		return existing, false
	}
	call := &flightCall{done: make(chan struct{})}
	g.calls[key] = call
	return call, true
}

func (g *flightGroup) end(key string, call *flightCall, value any, err error) {
	g.mu.Lock()
	if g.calls[key] == call {
		delete(g.calls, key)
	}
	g.mu.Unlock()
	call.value, call.err = value, err
	close(call.done)
}
