package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"
)

// Options describes one cached value: what it is, what it was computed from,
// how long it stays fresh, and which tags invalidate it.
type Options struct {
	// Profile supplies a named duration when Revalidate is zero. Revalidate
	// remains available for precise application-specific windows.
	Profile Profile
	// Name identifies the value across the whole deploy, e.g.
	// "catalog.product". Two call sites sharing a Name share cache entries, so
	// it must be unique per logical value, not per package.
	Name string
	// Args are the inputs the value depends on. They are canonically encoded
	// into the key (see DataKey), so they must be JSON-compatible and must
	// include everything fn reads that varies.
	Args []any
	// Revalidate is how long a computed value stays fresh. A non-positive
	// Revalidate disables caching for the call: Load computes the value and
	// returns it without touching the store. Caching until a tag bump is
	// deliberately not the zero-value behaviour - an accidentally empty
	// Options must not pin data in the cache forever.
	Revalidate time.Duration
	// Tags are the invalidation handles for this value. RevalidateTag bumps
	// them; RevalidatePath bumps PathTag(path), so an entry that must react to
	// a path revalidation has to carry that tag too.
	Tags []string
}

// Codec converts a cached value to and from the bytes a Store holds. Encoding
// is explicit rather than reflective because entries outlive the process that
// wrote them: the encoding is a wire format shared across instances of one
// build.
type Codec[T any] interface {
	Encode(value T) ([]byte, error)
	Decode(data []byte) (T, error)
}

// JSONCodec returns a Codec backed by encoding/json. It is the right default
// for plain data values; types whose JSON round trip is lossy (or that carry
// renderplan.SafeHTML, which must be re-trusted on decode) need their own
// codec.
func JSONCodec[T any]() Codec[T] { return jsonCodec[T]{} }

type jsonCodec[T any] struct{}

func (jsonCodec[T]) Encode(value T) ([]byte, error) { return json.Marshal(value) }

func (jsonCodec[T]) Decode(data []byte) (T, error) {
	var value T
	if err := json.Unmarshal(data, &value); err != nil {
		return value, err
	}
	return value, nil
}

// Load returns the cached value for options, computing it with fn on a miss.
//
// Load is shared computation caching: cookies and authorization do not disable
// it. Args must include every varying input (including tenant/user identity
// where relevant), and callers must authorize before EVERY call, including
// hits. Callbacks receive cancellation/deadlines and a fresh computation scope,
// not incoming context values or request memo state. Closures must not capture
// undeclared request inputs. This contract also applies to uncached fallbacks.
// Without a runtime or a positive duration the callback simply computes once.
//
// A hit past its revalidate deadline but inside the runtime's stale window is
// returned immediately while one background goroutine refreshes it, guarded by
// a distributed lease so the refresh runs on one instance rather than all of
// them.
func Load[T any](ctx context.Context, options Options, codec Codec[T], fn func(context.Context) (T, error)) (T, error) {
	var zero T
	if fn == nil {
		return zero, errors.New("cache: Load requires a non-nil fn")
	}
	if codec == nil {
		return zero, errors.New("cache: Load requires a non-nil codec")
	}
	if options.Name == "" {
		return zero, errors.New("cache: Load requires a non-empty Options.Name")
	}
	if scope, ok := RequestScopeFrom(ctx); ok {
		// Dependency tracking is independent of whether this particular data
		// value is cached. A cached route still needs to be invalidated when its
		// loader reads an intentionally uncached value.
		scope.RecordDependencyTags(options.Tags...)
	}
	runtime := dataRuntime(ctx)
	revalidate := options.Revalidate
	if revalidate <= 0 {
		revalidate = options.Profile.Duration()
	}
	if runtime == nil || revalidate <= 0 {
		result, err := compute(ctx, runtime, entry{name: options.Name, tags: normalizeTags(options.Tags), shared: true}, codec, nil, fn)
		return consume(ctx, result), err
	}
	key, err := DataKeyWithGeneration(runtime.deployPrefix, runtime.buildID, runtime.generation, options.Name, options.Args)
	if err != nil {
		return zero, err
	}
	return loadEntry(ctx, runtime, entry{
		name:       options.Name,
		shared:     true,
		key:        key,
		revalidate: revalidate,
		tags:       normalizeTags(options.Tags),
	}, codec, nil, fn)
}

// entry is one resolved cache entry: everything the fill, write-back, and
// refresh paths need once a caller's options have been turned into a key.
// cache.Load and the route coordinator differ only in how they build it.
type entry struct {
	// name labels the entry in logs. It is the caller's Options.Name for data
	// entries and the route ID for route entries.
	name       string
	shared     bool
	key        string
	revalidate time.Duration
	tags       []string
}

// loadEntry is the shared read path: return a fresh hit, return a stale hit
// while refreshing it in the background, or fill.
//
// storable, when non-nil, is consulted after fn computes a value and decides
// whether that value may be written to the shared store. It exists for the
// route coordinator, where privacy and the loader's result kind are only known
// once the loader has run.
func loadEntry[T any](ctx context.Context, runtime *Runtime, item entry, codec Codec[T], storable func(T) bool, fn func(context.Context) (T, error)) (T, error) {
	record, hit, err := runtime.store.Get(ctx, item.key)
	if err != nil {
		runtime.logger.Warn("cache read failed", "name", item.name, "error", err)
	}
	if hit {
		value, decodeErr := codec.Decode(record.Value)
		if decodeErr == nil {
			observeDependencies(ctx, dependencySnapshot{versions: record.TagVersions})
			if runtime.now().Before(record.FreshUntil) {
				return value, nil
			}
			refreshInBackground(ctx, runtime, item, codec, storable, fn)
			return value, nil
		}
		runtime.logger.Warn("cache entry could not be decoded", "name", item.name, "error", decodeErr)
	}
	return fill(ctx, runtime, item, codec, storable, fn)
}

// loadRuntime resolves the installed cache handle for this request, or nil
// when the request must not be served from cache. Privacy is checked here, on
// the Get gate, so a private request never even reads a shared entry.
func loadRuntime(ctx context.Context) *Runtime {
	scope, ok := RequestScopeFrom(ctx)
	if !ok || scope.Private() || scope.computation {
		return nil
	}
	return scope.runtime
}

// dataRuntime deliberately does not apply request privacy. Data is explicitly
// keyed computation; route responses retain the separate loadRuntime gate.
func dataRuntime(ctx context.Context) *Runtime {
	scope, ok := RequestScopeFrom(ctx)
	if !ok {
		return nil
	}
	return scope.runtime
}

func fill[T any](ctx context.Context, runtime *Runtime, item entry, codec Codec[T], storable func(T) bool, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	result, err := runtime.flight.do(item.key, func() (any, error) {
		// A previous leader may have populated the key after this request's
		// initial miss but before it acquired the flight slot. Reuse that fresh
		// record; stale entries must still be recomputed for refreshes.
		if record, hit, readErr := runtime.store.Get(ctx, item.key); readErr == nil && hit && runtime.now().Before(record.FreshUntil) {
			if value, decodeErr := codec.Decode(record.Value); decodeErr == nil {
				return computed[T]{value: value, dependencies: dependencySnapshot{versions: record.TagVersions}}, nil
			}
		}
		return compute(ctx, runtime, item, codec, storable, fn)
	})
	if result == nil && err != nil {
		return zero, err
	}
	value, ok := result.(computed[T])
	if !ok {
		return zero, fmt.Errorf("cache: load %q returned a value that is not %s; two call sites are sharing one Name with different types", item.name, reflect.TypeFor[T]())
	}
	return consume(ctx, value), err
}

// compute retains the versions observed BEFORE each dependency was read. A
// nested hit contributes its stored versions; a nested fill its prefill fence.
// A later read must never replace those versions and bless stale bytes.
func compute[T any](ctx context.Context, runtime *Runtime, item entry, codec Codec[T], storable func(T) bool, fn func(context.Context) (T, error)) (computed[T], error) {
	deps := &dependencies{}
	if runtime != nil && len(item.tags) > 0 {
		versions, err := runtime.store.TagVersions(ctx, item.tags)
		if err != nil {
			runtime.logger.Warn("cache tag versions unavailable", "name", item.name, "error", err)
		}
		deps.merge(dependencySnapshot{versions: versions, unsafe: err != nil})
	}
	callbackCtx := context.WithValue(ctx, dependencyContextKey{}, deps)
	if item.shared {
		callbackCtx = computationContext(ctx, runtime, deps)
	}
	value, err := fn(callbackCtx)
	result := computed[T]{value: value, dependencies: deps.snapshot()}
	if err != nil {
		return result, err
	}
	if runtime == nil || item.revalidate <= 0 || result.dependencies.unsafe || (storable != nil && !storable(value)) {
		return result, nil
	}
	encoded, err := codec.Encode(value)
	if err != nil {
		runtime.logger.Warn("cache entry could not be encoded", "name", item.name, "error", err)
		return result, nil
	}
	record := Record{Value: encoded, TagVersions: result.dependencies.versions, FreshUntil: runtime.now().Add(item.revalidate)}
	if err := runtime.store.Set(ctx, item.key, record, item.revalidate+runtime.maxStale); err != nil && !errors.Is(err, ErrStaleWrite) {
		runtime.logger.Warn("cache write failed", "name", item.name, "error", err)
	}
	return result, nil
}

// refreshInBackground recomputes a stale entry without making the current
// request wait. The goroutine runs on a context detached from the request (the
// response is already on its way out) but bounded by the runtime's refresh
// timeout, recovers panics so a failing loader cannot take the process down,
// and goes through the same singleflight group as a foreground fill so a
// concurrent miss joins it instead of duplicating the work.
func refreshInBackground[T any](ctx context.Context, runtime *Runtime, item entry, codec Codec[T], storable func(T) bool, fn func(context.Context) (T, error)) {
	if !runtime.refreshing.enter(item.key) {
		return
	}
	detached := context.WithoutCancel(ctx)
	// Refresh never records dependencies or refresh instructions on the request
	// that already received stale bytes. Shared callbacks are further isolated
	// by compute; route callbacks retain their ordinary request context values.
	detached = WithRequestScope(detached, NewRequestScope(false, WithRuntimeHandle(runtime)))
	detached = context.WithValue(detached, dependencyContextKey{}, (*dependencies)(nil))
	go func() {
		defer runtime.refreshing.leave(item.key)
		defer func() {
			if recovered := recover(); recovered != nil {
				runtime.logger.Error("cache refresh panicked", "name", item.name, "panic", recovered)
			}
		}()
		refreshCtx, cancel := context.WithTimeout(detached, runtime.refreshTimeout)
		defer cancel()
		if !runtime.acquireRefreshLease(refreshCtx, item.key) {
			return
		}
		if _, err := fill(refreshCtx, runtime, item, codec, storable, fn); err != nil {
			runtime.logger.Warn("cache refresh failed", "name", item.name, "error", err)
		}
	}()
}
