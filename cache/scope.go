package cache

import (
	"context"
	"sync"
)

// RequestScope is the per-request bag every cache primitive in this package
// requires. See the package doc for what it holds and where the runtime
// attaches it.
type RequestScope struct {
	private bool
	runtime *Runtime

	mu           sync.Mutex
	memo         map[string]*memoEntry
	refreshPaths []string
	refreshTags  []string
}

// ScopeOption configures a RequestScope at creation time.
type ScopeOption func(*RequestScope)

// WithRuntimeHandle installs the server's cache handle on the scope, which is
// what lets Load and the Revalidate* functions reach the byte store and the
// deploy prefix / BuildID that namespace every key. The handle rides on the
// scope rather than a package-level global so a process can serve two servers
// (or a test can run two builds) without them sharing a cache; a nil handle is
// a no-op, leaving cache.Load to fall through to its uncached path.
func WithRuntimeHandle(runtime *Runtime) ScopeOption {
	return func(scope *RequestScope) { scope.runtime = runtime }
}

// NewRequestScope creates a RequestScope. private is the Get-gate result
// computed from the inbound request's headers (see IsPrivateRequest) -
// callers should compute it once, before any middleware or loader runs, and
// pass it here.
func NewRequestScope(private bool, options ...ScopeOption) *RequestScope {
	scope := &RequestScope{private: private, memo: make(map[string]*memoEntry)}
	for _, option := range options {
		option(scope)
	}
	return scope
}

// Private reports the request's Get-gate privacy flag captured at scope
// creation. Cache layers must treat a private RequestScope as fail-closed:
// skip reads and skip writes regardless of an otherwise-public CachePolicy.
func (s *RequestScope) Private() bool {
	if s == nil {
		return true
	}
	return s.private
}

// RecordRefreshPath accumulates a path a later RevalidatePath call wants the
// client to refresh. It is safe for concurrent use.
func (s *RequestScope) RecordRefreshPath(path string) {
	if s == nil || path == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshPaths = append(s.refreshPaths, path)
}

// RecordRefreshTag accumulates a tag a later RevalidateTag call wants the
// client to refresh. It is safe for concurrent use.
func (s *RequestScope) RecordRefreshTag(tag string) {
	if s == nil || tag == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshTags = append(s.refreshTags, tag)
}

// RefreshPaths returns a copy of the paths recorded so far on this scope.
func (s *RequestScope) RefreshPaths() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.refreshPaths...)
}

// RefreshTags returns a copy of the tags recorded so far on this scope.
func (s *RequestScope) RefreshTags() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.refreshTags...)
}

type requestScopeContextKey struct{}

// WithRequestScope attaches scope to ctx. Runtime request entry points call
// this once per request; every derived context (including
// context.WithTimeout children created for loader/action/API deadlines)
// resolves the same scope via RequestScopeFrom.
func WithRequestScope(ctx context.Context, scope *RequestScope) context.Context {
	return context.WithValue(ctx, requestScopeContextKey{}, scope)
}

// RequestScopeFrom retrieves the RequestScope attached to ctx, if any.
func RequestScopeFrom(ctx context.Context) (*RequestScope, bool) {
	scope, ok := ctx.Value(requestScopeContextKey{}).(*RequestScope)
	return scope, ok
}
