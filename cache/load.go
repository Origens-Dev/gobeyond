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
// Caching is opt-in and fails closed. When ctx carries no RequestScope, when
// the request is private (see IsPrivateRequest), when no cache runtime is
// installed, or when Revalidate is non-positive, Load simply calls fn: a
// missing or untrustworthy caching context degrades to the uncached behaviour
// instead of erroring, so adding cache.Load to a loader can never turn a
// working page into a failing one. Misconfiguration that is always a bug - an
// empty Name, a nil codec or fn, arguments that cannot be canonically encoded
// - does error, everywhere, so it surfaces on the first run rather than only
// on cache-enabled deployments.
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
	runtime := loadRuntime(ctx)
	if runtime == nil || options.Revalidate <= 0 {
		return fn(ctx)
	}
	key, err := DataKey(runtime.deployPrefix, runtime.buildID, options.Name, options.Args)
	if err != nil {
		return zero, err
	}
	return loadEntry(ctx, runtime, entry{
		name:       options.Name,
		key:        key,
		revalidate: options.Revalidate,
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
	if !ok || scope.Private() {
		return nil
	}
	return scope.runtime
}

func fill[T any](ctx context.Context, runtime *Runtime, item entry, codec Codec[T], storable func(T) bool, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	result, err := runtime.flight.do(item.key, func() (any, error) {
		return compute(ctx, runtime, item, codec, storable, fn)
	})
	if err != nil {
		return zero, err
	}
	value, ok := result.(T)
	if !ok {
		return zero, fmt.Errorf("cache: load %q returned a value that is not %s; two call sites are sharing one Name with different types", item.name, reflect.TypeFor[T]())
	}
	return value, nil
}

// compute runs fn and writes the result back, fenced by the tag versions read
// before fn started. Reading the versions first is what makes the store's
// write-time compare-and-set meaningful: a bump that lands while fn is running
// changes the current versions, and the write is then rejected instead of
// resurrecting data the bump invalidated.
func compute[T any](ctx context.Context, runtime *Runtime, item entry, codec Codec[T], storable func(T) bool, fn func(context.Context) (T, error)) (any, error) {
	var versions map[string]int64
	fenced := true
	if len(item.tags) > 0 {
		read, err := runtime.store.TagVersions(ctx, item.tags)
		if err != nil {
			runtime.logger.Warn("cache tag versions unavailable", "name", item.name, "error", err)
			fenced = false
		}
		versions = read
	}
	value, err := fn(ctx)
	if err != nil {
		return nil, err
	}
	if !fenced || (storable != nil && !storable(value)) {
		return value, nil
	}
	encoded, err := codec.Encode(value)
	if err != nil {
		runtime.logger.Warn("cache entry could not be encoded", "name", item.name, "error", err)
		return value, nil
	}
	now := runtime.now()
	record := Record{
		Value:       encoded,
		TagVersions: versions,
		FreshUntil:  now.Add(item.revalidate),
	}
	if err := runtime.store.Set(ctx, item.key, record, item.revalidate+runtime.maxStale); err != nil && !errors.Is(err, ErrStaleWrite) {
		runtime.logger.Warn("cache write failed", "name", item.name, "error", err)
	}
	return value, nil
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
