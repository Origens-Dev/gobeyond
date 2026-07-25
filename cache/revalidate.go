package cache

import (
	"context"
	"errors"
)

// PathTagPrefix namespaces the per-path invalidation tags RevalidatePath
// bumps, keeping them from colliding with author-chosen tags.
const PathTagPrefix = "path:"

// PathTag returns the invalidation tag for one route path. Paths are
// normalized the same way RouteKey normalizes them, so "/products/widget/"
// and "/products//widget" revalidate the same entries.
//
// Revalidating a path is deliberately a tag bump on one path rather than a
// wipe of everything a route ever produced: a route serves many paths, and
// publishing one product must not evict the other thousand.
func PathTag(path string) (string, error) {
	normalized, err := NormalizePath(path)
	if err != nil {
		return "", err
	}
	return PathTagPrefix + normalized, nil
}

// RevalidateTag invalidates every cached entry carrying tag and records the
// tag on the request's RequestScope for the action envelope's "refresh" field.
//
// The store bump is synchronous: when RevalidateTag returns without error, no
// tier can serve an entry built under the old version, so an action can safely
// respond immediately after calling it. Invalidation is not gated on privacy -
// a mutation made by an authenticated viewer still invalidates public data.
func RevalidateTag(ctx context.Context, tag string) error {
	if tag == "" {
		return errors.New("cache: RevalidateTag requires a non-empty tag")
	}
	scope, ok := RequestScopeFrom(ctx)
	if !ok {
		return ErrNoRequestScope
	}
	scope.RecordRefreshTag(tag)
	if scope.runtime == nil {
		return nil
	}
	return scope.runtime.store.BumpTag(ctx, tag)
}

// RevalidatePath invalidates the entries tagged with path's PathTag and
// records the normalized path on the request's RequestScope. It has the same
// synchronous guarantee as RevalidateTag.
//
// Only entries that opted into the path tag are affected: a cache.Load whose
// value backs one page must list PathTag for that page in its Options.Tags to
// participate.
func RevalidatePath(ctx context.Context, path string) error {
	normalized, err := NormalizePath(path)
	if err != nil {
		return err
	}
	scope, ok := RequestScopeFrom(ctx)
	if !ok {
		return ErrNoRequestScope
	}
	scope.RecordRefreshPath(normalized)
	if scope.runtime == nil {
		return nil
	}
	return scope.runtime.store.BumpTag(ctx, PathTagPrefix+normalized)
}
