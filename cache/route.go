package cache

import (
	"context"
	"errors"
	"time"
)

// RouteOptions describes one page route's cached entry: which route and URL it
// belongs to, how long the origin may reuse it, and what invalidates it.
//
// RouteID, Path, RawQuery, and PublicOrigin are the identity half; they go
// into RouteKey together with the deploy prefix and BuildID the installed
// Runtime owns, so two builds, two origins, or two query strings never share
// an entry. Revalidate and Tags come from definePage({ revalidate, tags }).
type RouteOptions struct {
	// Profile supplies a named duration when Revalidate is zero.
	Profile Profile
	RouteID string
	// Path is the public request path this entry was computed for, not the
	// route's pattern: a route serves many paths and each caches separately.
	Path         string
	RawQuery     string
	PublicOrigin string
	// Revalidate is how long a computed entry stays fresh. A non-positive
	// Revalidate disables route caching for the call, matching Load: an
	// accidentally empty RouteOptions must not pin a page in the cache until
	// something happens to bump one of its tags.
	Revalidate time.Duration
	// Tags are the route's author-declared invalidation handles. PathTag(Path)
	// is always added on top, so cache.RevalidatePath("/products/widget")
	// drops exactly that page without touching the rest of the route.
	Tags []string
}

// LoadRoute returns the cached value for one route path, computing it with fn
// on a miss. It is the props-ISR coordinator behind runtime page loads; the
// runtime, not this package, decides what a "value" is (props, metadata,
// status, and kind - never response headers or rendered HTML).
//
// Like Load, it fails open: without a RequestScope, on a private request, with
// no cache runtime installed, or with a non-positive Revalidate, it simply
// calls fn. Unlike Load, the write is additionally gated on storable, because
// a page's cacheability is only known after the loader ran: the runtime uses
// it to keep responses that mint a cookie, and results that are not a plain
// OK, out of a store other visitors read.
//
// A hit past its revalidate deadline but inside the runtime's stale window is
// served immediately while one leased background goroutine refreshes it.
func LoadRoute[T any](ctx context.Context, options RouteOptions, codec Codec[T], storable func(T) bool, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	if fn == nil {
		return zero, errors.New("cache: LoadRoute requires a non-nil fn")
	}
	if codec == nil {
		return zero, errors.New("cache: LoadRoute requires a non-nil codec")
	}
	if options.RouteID == "" {
		return zero, errors.New("cache: LoadRoute requires a non-empty RouteOptions.RouteID")
	}
	runtime := loadRuntime(ctx)
	revalidate := options.Revalidate
	if revalidate <= 0 {
		revalidate = options.Profile.Duration()
	}
	if runtime == nil || revalidate <= 0 {
		return fn(ctx)
	}
	key, err := RouteKeyWithGeneration(runtime.deployPrefix, runtime.buildID, runtime.generation, options.RouteID, options.Path, options.RawQuery, options.PublicOrigin)
	if err != nil {
		return zero, err
	}
	pathTag, err := PathTag(options.Path)
	if err != nil {
		return zero, err
	}
	return loadEntry(ctx, runtime, entry{
		name:       options.RouteID,
		key:        key,
		revalidate: revalidate,
		tags:       normalizeTags(append(append([]string(nil), options.Tags...), pathTag)),
	}, codec, storable, fn)
}
