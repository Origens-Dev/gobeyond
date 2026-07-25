package redisstore

import "testing"

func TestFromEnvNoEndpointDegrades(t *testing.T) {
	t.Setenv(EnvEndpoint, "")
	store, ok, err := FromEnv(Options{})
	if err != nil {
		t.Fatalf("FromEnv error: %v", err)
	}
	if ok || store != nil {
		t.Fatalf("FromEnv = (%v, %v), want (nil, false)", store, ok)
	}
}

func TestFromEnvEnablesTLSByDefaultWithEndpointAsServerName(t *testing.T) {
	t.Setenv(EnvEndpoint, "cache.example.internal")
	t.Setenv(EnvPort, "6380")
	t.Setenv(EnvKeyPrefix, "acme")
	t.Setenv(EnvTLS, "")

	store, ok, err := FromEnv(Options{})
	if err != nil {
		t.Fatalf("FromEnv error: %v", err)
	}
	if !ok || store == nil {
		t.Fatal("FromEnv did not build a store for a configured endpoint")
	}
	defer store.Close()

	if store.tlsConfig == nil {
		t.Fatal("expected TLS to be enabled by default")
	}
	if store.tlsConfig.ServerName != "cache.example.internal" {
		t.Fatalf("ServerName = %q, want %q", store.tlsConfig.ServerName, "cache.example.internal")
	}
	if store.tlsConfig.InsecureSkipVerify {
		t.Fatal("TLS config must not skip certificate verification")
	}
	if store.namespace != "acme" {
		t.Fatalf("namespace = %q, want %q", store.namespace, "acme")
	}
}

func TestFromEnvTLSSwitchDisablesTLS(t *testing.T) {
	t.Setenv(EnvEndpoint, "localhost")
	t.Setenv(EnvPort, "6379")
	t.Setenv(EnvTLS, "false")

	store, ok, err := FromEnv(Options{})
	if err != nil {
		t.Fatalf("FromEnv error: %v", err)
	}
	if !ok || store == nil {
		t.Fatal("FromEnv did not build a store for a configured endpoint")
	}
	defer store.Close()

	if store.tlsConfig != nil {
		t.Fatalf("expected TLS to be disabled, got %+v", store.tlsConfig)
	}
}

func TestFromEnvExplicitOptionsWinOverEnv(t *testing.T) {
	t.Setenv(EnvEndpoint, "should-not-be-used.example")
	t.Setenv(EnvKeyPrefix, "should-not-be-used")
	t.Setenv(EnvPort, "9999")

	store, ok, err := FromEnv(Options{Addr: "127.0.0.1:6399", Namespace: "explicit"})
	if err != nil {
		t.Fatalf("FromEnv error: %v", err)
	}
	if !ok || store == nil {
		t.Fatal("FromEnv did not build a store for a configured endpoint")
	}
	defer store.Close()

	if store.namespace != "explicit" {
		t.Fatalf("namespace = %q, want %q", store.namespace, "explicit")
	}
	if store.tlsConfig == nil || store.tlsConfig.ServerName != "127.0.0.1" {
		t.Fatalf("explicit Addr was not used to build the client, tlsConfig=%+v", store.tlsConfig)
	}
}

func TestNewRequiresAddrOrClient(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("expected an error when neither Addr nor Client is set")
	}
}
