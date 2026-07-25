package cache

import (
	"context"
	"testing"
	"time"
)

func TestRequestScopeRoundTrip(t *testing.T) {
	scope := NewRequestScope(false)
	ctx := WithRequestScope(context.Background(), scope)
	got, ok := RequestScopeFrom(ctx)
	if !ok || got != scope {
		t.Fatalf("RequestScopeFrom = (%v, %v), want (%v, true)", got, ok, scope)
	}
}

func TestRequestScopeAbsentByDefault(t *testing.T) {
	if _, ok := RequestScopeFrom(context.Background()); ok {
		t.Fatal("expected no RequestScope on a bare context")
	}
}

// TestRequestScopeSurvivesTimeoutChild proves that a timeout child context
// (as runtime.loadPage creates via context.WithTimeout) still resolves the
// RequestScope attached at the request entry point, since context value
// lookups walk the parent chain.
func TestRequestScopeSurvivesTimeoutChild(t *testing.T) {
	scope := NewRequestScope(true)
	parent := WithRequestScope(context.Background(), scope)
	child, cancel := context.WithTimeout(parent, time.Minute)
	defer cancel()
	got, ok := RequestScopeFrom(child)
	if !ok || got != scope {
		t.Fatalf("RequestScopeFrom(child) = (%v, %v), want (%v, true)", got, ok, scope)
	}
	if !got.Private() {
		t.Fatal("expected the private flag to survive through the timeout child")
	}
}

func TestRequestScopePrivateNilScope(t *testing.T) {
	var scope *RequestScope
	if !scope.Private() {
		t.Fatal("a nil RequestScope must fail closed to private")
	}
}

func TestRequestScopeRefreshRecorder(t *testing.T) {
	scope := NewRequestScope(false)
	scope.RecordRefreshPath("/products/widget")
	scope.RecordRefreshPath("/products/widget2")
	scope.RecordRefreshTag("products")

	if got := scope.RefreshPaths(); len(got) != 2 || got[0] != "/products/widget" || got[1] != "/products/widget2" {
		t.Fatalf("RefreshPaths = %v", got)
	}
	if got := scope.RefreshTags(); len(got) != 1 || got[0] != "products" {
		t.Fatalf("RefreshTags = %v", got)
	}

	// Mutating the returned slice must not affect the scope's internal state.
	paths := scope.RefreshPaths()
	paths[0] = "mutated"
	if got := scope.RefreshPaths(); got[0] != "/products/widget" {
		t.Fatalf("RefreshPaths was mutated externally: %v", got)
	}
}

func TestRequestScopeRefreshRecorderIgnoresEmptyValues(t *testing.T) {
	scope := NewRequestScope(false)
	scope.RecordRefreshPath("")
	scope.RecordRefreshTag("")
	if got := scope.RefreshPaths(); len(got) != 0 {
		t.Fatalf("RefreshPaths = %v, want empty", got)
	}
	if got := scope.RefreshTags(); len(got) != 0 {
		t.Fatalf("RefreshTags = %v, want empty", got)
	}
}

func TestRequestScopeRefreshRecorderNilScopeIsNoop(t *testing.T) {
	var scope *RequestScope
	scope.RecordRefreshPath("/x")
	scope.RecordRefreshTag("y")
	if got := scope.RefreshPaths(); got != nil {
		t.Fatalf("RefreshPaths on nil scope = %v", got)
	}
	if got := scope.RefreshTags(); got != nil {
		t.Fatalf("RefreshTags on nil scope = %v", got)
	}
}
