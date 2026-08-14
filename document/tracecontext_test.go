package document

import "testing"

func TestNormalizeTraceParent(t *testing.T) {
	valid := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	if got := NormalizeTraceParent(" " + valid + " "); got != valid {
		t.Fatalf("valid parent = %q", got)
	}
	if got := NormalizeTraceParent("00-00000000000000000000000000000000-00f067aa0ba902b7-01"); got != "" {
		t.Fatalf("zero trace id must drop, got %q", got)
	}
	if got := NormalizeTraceParent(`00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"><script>`); got != "" {
		t.Fatalf("injected parent must drop, got %q", got)
	}
}

func TestNormalizeTraceState(t *testing.T) {
	if got := NormalizeTraceState(" vendor=opaque "); got != "vendor=opaque" {
		t.Fatalf("valid state = %q", got)
	}
	if got := NormalizeTraceState(`vendor="><script>`); got != "" {
		t.Fatalf("quoted state must drop, got %q", got)
	}
}
