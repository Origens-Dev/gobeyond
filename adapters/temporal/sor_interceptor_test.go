package temporal

import (
	"testing"
	"time"
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
