package runtime

import (
	"encoding/json"
	gb "github.com/Origens-Dev/gobeyond"
	"github.com/Origens-Dev/gobeyond/router"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDeploymentConfigurationOwnsPublicSnapshot(t *testing.T) {
	values := map[string]string{"CLERK_PUBLISHABLE_KEY": "pk_test"}
	cfg := Config{DeploymentRevision: "deployment-1", PublicConfig: values}
	if err := loadDeploymentConfig(&cfg); err != nil {
		t.Fatal(err)
	}
	values["CLERK_PUBLISHABLE_KEY"] = "changed"
	if cfg.PublicConfig["CLERK_PUBLISHABLE_KEY"] != "pk_test" {
		t.Fatal("snapshot mutated")
	}
}
func TestDeploymentConfigurationRejectsMissingRevisionAndNonStringValues(t *testing.T) {
	t.Setenv("GOBEYOND_DEPLOYMENT_REVISION", "")
	cfg := Config{PublicConfig: map[string]string{"PUBLIC": "value"}}
	if err := loadDeploymentConfig(&cfg); err == nil {
		t.Fatal("missing revision accepted")
	}
	t.Setenv("GOBEYOND_PUBLIC_CONFIG", `{"PUBLIC":{"secret":"nested"}}`)
	if err := loadDeploymentConfig(&Config{}); err == nil {
		t.Fatal("non-string public configuration accepted")
	}
}

func TestDeploymentRevisionRejectsStaleActionWithoutExecuting(t *testing.T) {
	calls := 0
	server, err := New(Config{BuildID: "same-build", DeploymentRevision: "new-config", PublicOrigin: "https://example.com", Actions: []Action{testAction("save", func(*gb.ActionContext, json.RawMessage) (any, error) { calls++; return nil, nil })}})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "https://example.com/_gobeyond/builds/same-build/actions/save", strings.NewReader(`{}`))
	request.Header.Set("X-GoBeyond-Deployment", "old-config")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || response.Header().Get("X-GoBeyond-Error") != "deployment_mismatch" || calls != 0 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, calls, response.Body.String())
	}
}

func TestStaticDocumentBootstrapsDeploymentConfiguration(t *testing.T) {
	server, err := New(Config{BuildID: "same-build", DeploymentRevision: "config-2", PublicConfig: map[string]string{"KEY": "</script><script>bad</script>"}, PublicOrigin: "https://example.com", Pages: []PageRoute{{Route: router.Route{ID: "product", Pattern: "/", Mode: router.ModeStatic}, Plan: productPlan(), Static: &LoadedPage{Kind: gb.ResultOK, Status: http.StatusOK, Props: map[string]any{"name": "Product", "available": true}, Metadata: gb.Metadata{Lang: "en", Title: "Product", Canonical: "https://example.com/"}}, ClientScript: "/entry.js"}}})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "https://example.com/", nil))
	body := response.Body.String()
	if response.Code != 200 || !strings.Contains(body, `"deploymentRevision":"config-2"`) || !strings.Contains(body, `"publicConfig":{"KEY":`) {
		t.Fatalf("missing deployment bootstrap: %s", body)
	}
	if strings.Contains(body, "</script><script>bad</script>") {
		t.Fatal("unsafe config serialization")
	}
	if strings.Index(body, "__GOBEYOND_DATA__") > strings.Index(body, `src="/entry.js"`) {
		t.Fatal("configuration after application entry")
	}
}
