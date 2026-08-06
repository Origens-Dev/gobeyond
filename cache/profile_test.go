package cache

import (
	"testing"
	"time"
)

func TestProfilesAreExplicitAndBounded(t *testing.T) {
	if Profile("").Valid() {
		t.Fatal("empty profile must be uncached")
	}
	if got := ProfileUntilInvalidated.Duration(); got != SafetyTTL {
		t.Fatalf("until-invalidated duration = %s, want %s", got, SafetyTTL)
	}
	if SafetyTTL != 31*24*time.Hour {
		t.Fatalf("safety TTL changed: %s", SafetyTTL)
	}
}

func TestGenerationChangesCacheKeys(t *testing.T) {
	a, err := DataKeyWithGeneration("deploy", "build", "1", "content", []any{"x"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := DataKeyWithGeneration("deploy", "build", "2", "content", []any{"x"})
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("cache generations must isolate data keys")
	}
}
