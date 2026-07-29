package temporal

import "testing"

func TestOptionsFromEnv(t *testing.T) {
	t.Setenv(EnvAddress, "temporal:7233")
	t.Setenv(EnvNamespace, "default")
	t.Setenv(EnvTaskQueue, "demo__local")
	got := optionsFromEnv(Options{})
	if got.Address != "temporal:7233" || got.Namespace != "default" || got.TaskQueue != "demo__local" {
		t.Fatalf("got %#v", got)
	}
}
