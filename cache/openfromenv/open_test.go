package openfromenv

import (
	"sync"
	"testing"
)

func TestOpenFromEnvWithoutRedis(t *testing.T) {
	t.Setenv("GOBEYOND_CACHE_ENDPOINT", "")
	t.Setenv("GOBEYOND_CACHE_KEY_PREFIX", "")

	config, closeFn, err := OpenFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := closeFn(); err != nil {
			t.Fatal(err)
		}
	}()
	if config == nil || config.Store == nil {
		t.Fatal("OpenFromEnv returned nil config or store")
	}
	if config.DeployPrefix != "local" {
		t.Fatalf("DeployPrefix = %q, want local", config.DeployPrefix)
	}
	if err := closeFn(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestOpenFromEnvUsesDeployPrefix(t *testing.T) {
	t.Setenv("GOBEYOND_CACHE_ENDPOINT", "")
	t.Setenv("GOBEYOND_CACHE_KEY_PREFIX", "acme-prod")

	config, closeFn, err := OpenFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	if config.DeployPrefix != "acme-prod" {
		t.Fatalf("DeployPrefix = %q, want acme-prod", config.DeployPrefix)
	}
}

func TestOpenFromEnvCloseIsSafeConcurrent(t *testing.T) {
	t.Setenv("GOBEYOND_CACHE_ENDPOINT", "")
	_, closeFn, err := OpenFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_ = closeFn()
		}()
	}
	wait.Wait()
}
