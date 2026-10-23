package cache

import (
	"context"
	"errors"
	"sync"
)

// ErrSharedComputation means a request-only operation was attempted inside a
// shared data computation. Authorize before Load; callbacks compute values
// from explicit inputs and must not mutate requests or invalidate caches.
var ErrSharedComputation = errors.New("cache: request operations are unavailable inside a shared computation")

// valueFreeContext preserves cancellation and deadlines, but exposes none of
// the caller's context values (including identity, request memo, and headers).
// Closures can still capture request state: this is an API boundary, not a sandbox.
type valueFreeContext struct{ context.Context }

func (valueFreeContext) Value(any) any { return nil }

type dependencyContextKey struct{}

type dependencies struct {
	mu       sync.Mutex
	versions map[string]int64
	unsafe   bool
}

type dependencySnapshot struct {
	versions map[string]int64
	unsafe   bool
}

func (d *dependencies) merge(snapshot dependencySnapshot) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.versions == nil {
		d.versions = make(map[string]int64)
	}
	d.unsafe = d.unsafe || snapshot.unsafe
	for tag, version := range snapshot.versions {
		if previous, ok := d.versions[tag]; ok && previous != version {
			d.unsafe = true
			continue
		}
		d.versions[tag] = version
	}
}

func (d *dependencies) snapshot() dependencySnapshot {
	d.mu.Lock()
	defer d.mu.Unlock()
	result := dependencySnapshot{versions: make(map[string]int64, len(d.versions)), unsafe: d.unsafe}
	for tag, version := range d.versions {
		result.versions[tag] = version
	}
	return result
}

func computationContext(ctx context.Context, runtime *Runtime, deps *dependencies) context.Context {
	scope := NewRequestScope(true, WithRuntimeHandle(runtime))
	scope.computation = true
	ctx = WithRequestScope(valueFreeContext{ctx}, scope)
	return context.WithValue(ctx, dependencyContextKey{}, deps)
}

func isSharedComputation(ctx context.Context) bool {
	scope, ok := RequestScopeFrom(ctx)
	return ok && scope.computation
}

func observeDependencies(ctx context.Context, snapshot dependencySnapshot) {
	if parent, ok := ctx.Value(dependencyContextKey{}).(*dependencies); ok && parent != nil {
		parent.merge(snapshot)
	}
	if scope, ok := RequestScopeFrom(ctx); ok {
		tags := make([]string, 0, len(snapshot.versions))
		for tag := range snapshot.versions {
			tags = append(tags, tag)
		}
		scope.RecordDependencyTags(tags...)
	}
}

// computed is also the singleflight result: every waiter must receive the
// dependencies of the bytes it consumed, not just the fill leader's request.
type computed[T any] struct {
	value        T
	dependencies dependencySnapshot
}

func consume[T any](ctx context.Context, result computed[T]) T {
	observeDependencies(ctx, result.dependencies)
	return result.value
}
