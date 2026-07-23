package runtime

import (
	"os"
	"path/filepath"
	"testing"

	gb "github.com/Origens-Dev/gobeyond"
	"github.com/Origens-Dev/gobeyond/renderplan"
)

func TestStaticStoreLoadsPackagedPropsAndSafeHTML(t *testing.T) {
	root := t.TempDir()
	buildPath := filepath.Join(root, "static-build.json")
	contractPath := filepath.Join(root, "contracts.json")
	writeStaticFixture(t, buildPath, `{"apiVersion":"gobeyond.static-build/v1alpha1","routes":[{"routeId":"article","entries":[{"params":{"slug":"hello"},"props":{"body":"<p>Sanitized</p>"},"metadata":{"lang":"en","title":"Hello","robots":"noindex, nofollow"}}]}]}`)
	writeStaticFixture(t, contractPath, `{"apiVersion":"gobeyond.contract/v1alpha1","routes":[{"routeId":"article","props":{"kind":"object","shape":{"body":{"kind":"safeHtml"}}}}],"actions":[]}`)
	store, err := LoadStaticStore(buildPath, contractPath)
	if err != nil {
		t.Fatal(err)
	}
	page, err := store.Loader("article")(&gb.PageContext{Params: map[string]string{"slug": "hello"}})
	if err != nil {
		t.Fatal(err)
	}
	body, ok := page.Props.(map[string]any)["body"].(renderplan.SafeHTML)
	if !ok || body.String() != "<p>Sanitized</p>" {
		t.Fatalf("props=%#v", page.Props)
	}
	missing, err := store.Loader("article")(&gb.PageContext{Params: map[string]string{"slug": "missing"}})
	if err != nil || missing.Kind != gb.ResultNotFound {
		t.Fatalf("missing=%#v err=%v", missing, err)
	}
}

func writeStaticFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
