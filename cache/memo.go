package cache

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
)

// ErrNoRequestScope is returned by Memo when ctx has no RequestScope
// attached. Memo cannot fall back to an unscoped bag: the memo bag's
// lifetime is the request's, so there must be a request to scope it to.
var ErrNoRequestScope = errors.New("cache: context has no RequestScope; the runtime must call cache.WithRequestScope at the request entry point")

// memoEntry is the untyped slot stored in a RequestScope's memo bag. once
// gives Memo its singleflight behavior for free: concurrent callers for the
// same key block on the same Once until the first caller's fn returns, then
// all callers (including the first) observe the same value/err.
type memoEntry struct {
	once  sync.Once
	typ   reflect.Type
	value any
	err   error
}

// Memo runs fn at most once per (RequestScope, key) pair and returns its
// result to every caller for that key during the request, deduplicating
// concurrent calls. Memo is a package function, not a method, because Go
// forbids type parameters on methods (Locked decision 2).
//
// Reusing key with a different T on the same RequestScope is a programming
// error: it returns a descriptive error rather than silently corrupting the
// cached value's type.
func Memo[T any](ctx context.Context, key string, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	scope, ok := RequestScopeFrom(ctx)
	if !ok {
		return zero, ErrNoRequestScope
	}
	if fn == nil {
		return zero, errors.New("cache: Memo requires a non-nil fn")
	}
	wantType := reflect.TypeFor[T]()

	scope.mu.Lock()
	entry, exists := scope.memo[key]
	if !exists {
		entry = &memoEntry{typ: wantType}
		scope.memo[key] = entry
	}
	scope.mu.Unlock()

	if entry.typ != wantType {
		return zero, fmt.Errorf("cache: memo key %q already used for type %s, got %s", key, entry.typ, wantType)
	}

	entry.once.Do(func() {
		entry.value, entry.err = fn(ctx)
	})
	if entry.err != nil {
		return zero, entry.err
	}
	value, ok := entry.value.(T)
	if !ok {
		return zero, fmt.Errorf("cache: memo key %q held an incompatible value for type %s", key, wantType)
	}
	return value, nil
}
