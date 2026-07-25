package cache

import "testing"

// TestNewRuntimeRequiresNamespaces guards the two values that keep one
// deploy's and one build's entries apart. Defaulting either would silently
// share cached data across boundaries that exist for correctness.
func TestNewRuntimeRequiresNamespaces(t *testing.T) {
	clock := newTestClock()
	store := newFakeStore(clock)
	cases := map[string]RuntimeConfig{
		"no deploy prefix": {BuildID: "build-1", Store: store},
		"no build ID":      {DeployPrefix: "deploy", Store: store},
		"no store":         {DeployPrefix: "deploy", BuildID: "build-1"},
	}
	for name, config := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewRuntime(config); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestNewRuntimeAppliesDefaults(t *testing.T) {
	clock := newTestClock()
	runtime, err := NewRuntime(RuntimeConfig{DeployPrefix: "deploy", BuildID: "build-1", Store: newFakeStore(clock)})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	if runtime.maxStale != DefaultMaxStale || runtime.refreshTimeout != DefaultRefreshTimeout {
		t.Fatalf("defaults = (%v, %v)", runtime.maxStale, runtime.refreshTimeout)
	}
	if runtime.DeployPrefix() != "deploy" || runtime.BuildID() != "build-1" || runtime.Store() == nil {
		t.Fatal("runtime did not expose its configuration")
	}
}

func TestDeployPrefixFromEnv(t *testing.T) {
	t.Setenv(EnvDeployPrefix, "")
	if got := DeployPrefixFromEnv(); got != "" {
		t.Fatalf("DeployPrefixFromEnv() = %q, want empty without the platform variable", got)
	}
	t.Setenv(EnvDeployPrefix, "acme-prod")
	if got := DeployPrefixFromEnv(); got != "acme-prod" {
		t.Fatalf("DeployPrefixFromEnv() = %q", got)
	}
}
