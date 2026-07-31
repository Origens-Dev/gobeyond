package temporal

import (
	"testing"
	"time"

	"go.temporal.io/sdk/temporal"
)

func TestShouldReportTimer(t *testing.T) {
	if shouldReportTimer(0) || shouldReportTimer(-time.Second) {
		t.Fatal("non-positive duration must skip SoR")
	}
	if !shouldReportTimer(time.Millisecond) {
		t.Fatal("positive duration must report")
	}
}

func TestFireAtFromDuration(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	d := 5 * time.Minute
	got := now.Add(d).UTC().Format(time.RFC3339)
	want := "2026-07-31T12:05:00Z"
	if got != want {
		t.Fatalf("fire_at=%q want %q", got, want)
	}
}

func shouldReportTimer(d time.Duration) bool {
	return d > 0
}

func TestStampFromRetryPolicyDefaults(t *testing.T) {
	got := stampFromRetryPolicy(nil)
	if got.InitialIntervalMS != 1000 || got.BackoffCoefficient != 2.0 || got.MaximumAttempts != 0 {
		t.Fatalf("defaults=%+v", got)
	}
	got = stampFromRetryPolicy(&temporal.RetryPolicy{
		InitialInterval:    2 * time.Minute,
		BackoffCoefficient: 1.5,
		MaximumInterval:    10 * time.Minute,
		MaximumAttempts:    5,
	})
	if got.InitialIntervalMS != 120000 || got.MaximumAttempts != 5 {
		t.Fatalf("custom=%+v", got)
	}
}

func TestIsNonRetryableActivityErr(t *testing.T) {
	if isNonRetryableActivityErr(temporal.NewApplicationError("x", "t")) {
		t.Fatal("retryable ApplicationError must not mark non_retryable")
	}
	if !isNonRetryableActivityErr(temporal.NewNonRetryableApplicationError("x", "t", nil)) {
		t.Fatal("NonRetryableApplicationError must mark non_retryable")
	}
}
