package cache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestMemoRequiresRequestScope(t *testing.T) {
	_, err := Memo(context.Background(), "nav", func(context.Context) (string, error) {
		return "nav", nil
	})
	if !errors.Is(err, ErrNoRequestScope) {
		t.Fatalf("err = %v, want ErrNoRequestScope", err)
	}
}

func TestMemoCachesAcrossSequentialCalls(t *testing.T) {
	ctx := WithRequestScope(context.Background(), NewRequestScope(false))
	var calls atomic.Int32
	fn := func(context.Context) (string, error) {
		calls.Add(1)
		return "nav", nil
	}
	for i := 0; i < 5; i++ {
		got, err := Memo(ctx, "nav", fn)
		if err != nil || got != "nav" {
			t.Fatalf("Memo() = (%q, %v)", got, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("fn called %d times, want 1", calls.Load())
	}
}

// TestMemoDedupesConcurrentCalls proves the in-flight singleflight behavior:
// many goroutines calling Memo with the same key concurrently must observe
// fn execute exactly once and all receive its result.
func TestMemoDedupesConcurrentCalls(t *testing.T) {
	ctx := WithRequestScope(context.Background(), NewRequestScope(false))
	var calls atomic.Int32
	start := make(chan struct{})
	fn := func(context.Context) (int, error) {
		<-start
		calls.Add(1)
		return 42, nil
	}

	const goroutines = 50
	var wg sync.WaitGroup
	results := make([]int, goroutines)
	errs := make([]error, goroutines)
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = Memo(ctx, "answer", fn)
		}(i)
	}
	close(start)
	wg.Wait()

	if calls.Load() != 1 {
		t.Fatalf("fn called %d times, want 1", calls.Load())
	}
	for i := range results {
		if errs[i] != nil || results[i] != 42 {
			t.Fatalf("goroutine %d: Memo() = (%d, %v)", i, results[i], errs[i])
		}
	}
}

func TestMemoDifferentKeysAreIndependent(t *testing.T) {
	ctx := WithRequestScope(context.Background(), NewRequestScope(false))
	a, err := Memo(ctx, "a", func(context.Context) (int, error) { return 1, nil })
	if err != nil || a != 1 {
		t.Fatalf("a = (%d, %v)", a, err)
	}
	b, err := Memo(ctx, "b", func(context.Context) (int, error) { return 2, nil })
	if err != nil || b != 2 {
		t.Fatalf("b = (%d, %v)", b, err)
	}
}

func TestMemoTypeCollisionReturnsError(t *testing.T) {
	ctx := WithRequestScope(context.Background(), NewRequestScope(false))
	if _, err := Memo(ctx, "shared", func(context.Context) (int, error) { return 1, nil }); err != nil {
		t.Fatalf("first Memo() error = %v", err)
	}
	_, err := Memo(ctx, "shared", func(context.Context) (string, error) { return "x", nil })
	if err == nil {
		t.Fatal("expected a type collision error")
	}
}

func TestMemoPropagatesAndCachesError(t *testing.T) {
	ctx := WithRequestScope(context.Background(), NewRequestScope(false))
	wantErr := errors.New("boom")
	var calls atomic.Int32
	fn := func(context.Context) (int, error) {
		calls.Add(1)
		return 0, wantErr
	}
	for i := 0; i < 3; i++ {
		_, err := Memo(ctx, "failing", fn)
		if !errors.Is(err, wantErr) {
			t.Fatalf("err = %v, want %v", err, wantErr)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("fn called %d times, want 1", calls.Load())
	}
}

func TestMemoScopesAreIndependentAcrossRequests(t *testing.T) {
	var calls atomic.Int32
	fn := func(context.Context) (int, error) {
		calls.Add(1)
		return int(calls.Load()), nil
	}
	first := WithRequestScope(context.Background(), NewRequestScope(false))
	second := WithRequestScope(context.Background(), NewRequestScope(false))
	a, err := Memo(first, "nav", fn)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Memo(second, "nav", fn)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("expected independent memo bags per RequestScope, got a=%d b=%d", a, b)
	}
	if calls.Load() != 2 {
		t.Fatalf("fn called %d times, want 2", calls.Load())
	}
}
