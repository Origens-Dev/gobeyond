package runtime

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gb "github.com/Origens-Dev/gobeyond"
	"github.com/Origens-Dev/gobeyond/pack"
	"github.com/Origens-Dev/gobeyond/renderplan"
	"github.com/Origens-Dev/gobeyond/router"
)

func storePlanJSON(routeID, text string) []byte {
	return []byte(`{"apiVersion":"gobeyond.render/v1alpha1","routeId":"` + routeID + `","root":{"kind":"element","tag":"main","children":[{"kind":"text","value":{"kind":"literal","value":"` + text + `"}}]}}`)
}

func writePlanPack(t *testing.T, buildID string, plans map[string][]byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "render-plans"+pack.ExtPlans)
	if err := pack.WritePlans(path, buildID, plans); err != nil {
		t.Fatal(err)
	}
	return path
}

func openTestPlanStore(t *testing.T, buildID string, plans map[string][]byte) *PackPlanStore {
	t.Helper()
	store, err := OpenPlanStore(writePlanPack(t, buildID, plans))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func openTestStaticStore(t *testing.T, buildID string, entries map[string][]byte, contracts string) *PackStaticStore {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "static-build"+pack.ExtStatic)
	if err := pack.WriteStatic(path, buildID, entries); err != nil {
		t.Fatal(err)
	}
	contractsPath := filepath.Join(dir, "contracts.json")
	if err := os.WriteFile(contractsPath, []byte(contracts), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStaticStore(path, contractsPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func staticPage(title string) *LoadedPage {
	return &LoadedPage{Kind: gb.ResultOK, Status: http.StatusOK, Props: map[string]any{}, Metadata: gb.Metadata{Lang: "en", Title: title}}
}

func serveText(t *testing.T, server *Server, url string) (int, string) {
	t.Helper()
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, url, nil))
	return recorder.Code, recorder.Body.String()
}

func TestNewRejectsPageWithoutPlanOrStore(t *testing.T) {
	page := PageRoute{
		Route:  router.Route{ID: "home", Pattern: "/", Mode: router.ModeStatic},
		Static: staticPage("Home"),
	}
	if _, err := New(Config{BuildID: "build-1", PublicOrigin: "https://example.com", Pages: []PageRoute{page}}); err == nil {
		t.Fatal("expected a page without an inline plan or a plan store to be rejected")
	}

	// A store that does not carry the route is as good as no store.
	other := openTestPlanStore(t, "build-1", map[string][]byte{"about": storePlanJSON("about", "about page")})
	_, err := New(Config{BuildID: "build-1", PublicOrigin: "https://example.com", PlanStore: other, Pages: []PageRoute{page}})
	if err == nil || !strings.Contains(err.Error(), "home") {
		t.Fatalf("expected the missing route to be named, got %v", err)
	}
}

func TestNewRejectsPlanStoreBuildMismatch(t *testing.T) {
	store := openTestPlanStore(t, "build-2", map[string][]byte{"home": storePlanJSON("home", "home page")})
	_, err := New(Config{
		BuildID:      "build-1",
		PublicOrigin: "https://example.com",
		PlanStore:    store,
		Pages: []PageRoute{{
			Route:  router.Route{ID: "home", Pattern: "/", Mode: router.ModeStatic},
			Static: staticPage("Home"),
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "plan store build ID mismatch") {
		t.Fatalf("expected a plan store build ID mismatch, got %v", err)
	}
}

func TestNewRejectsStaticEntriesBuildMismatch(t *testing.T) {
	entries := map[string][]byte{
		pack.StaticEntryKey("home", nil): []byte(`{"props":{},"metadata":{"lang":"en","title":"Home"}}`),
	}
	contracts := `{"apiVersion":"gobeyond.contract/v1alpha1","routes":[{"routeId":"home","props":{"kind":"object","shape":{}}}],"actions":[]}`
	store := openTestStaticStore(t, "build-2", entries, contracts)
	page := PageRoute{Route: router.Route{ID: "home", Pattern: "/", Mode: router.ModeStatic}, Plan: &renderplan.Plan{APIVersion: gb.RenderAPIVersion, RouteID: "home", Root: &renderplan.Element{Kind: "element", Tag: "main"}}}
	_, err := New(Config{BuildID: "build-1", PublicOrigin: "https://example.com", Static: store, Pages: []PageRoute{page}})
	if err == nil || !strings.Contains(err.Error(), "static entry store build ID mismatch") {
		t.Fatalf("expected a static entry store build ID mismatch, got %v", err)
	}

	// A build-agnostic adapter (empty build ID) is accepted: the eager JSON
	// store carries no build header.
	eager := &StaticStore{routes: map[string][]LoadedPageEntry{
		"home": {{Params: map[string]any{}, Page: *staticPage("Home")}},
	}}
	if eager.BuildID() != "" {
		t.Fatalf("eager store build ID = %q, want empty", eager.BuildID())
	}
	if _, err := New(Config{BuildID: "build-1", PublicOrigin: "https://example.com", Static: eager, Pages: []PageRoute{page}}); err != nil {
		t.Fatalf("build-agnostic static store rejected: %v", err)
	}
}

// TestPlanStoreServesColdPlanOnFirstRender proves the two lazy-residency
// guarantees at once: New verifies membership without decoding anything, and
// the first document request performs exactly one cold decode which then
// stays resident for the next request.
func TestPlanStoreServesColdPlanOnFirstRender(t *testing.T) {
	store := openTestPlanStore(t, "build-1", map[string][]byte{"home": storePlanJSON("home", "home from store")})
	server, err := New(Config{
		BuildID:      "build-1",
		PublicOrigin: "https://example.com",
		PlanStore:    store,
		Pages: []PageRoute{{
			Route:  router.Route{ID: "home", Pattern: "/", Mode: router.ModeStatic},
			Static: staticPage("Home"),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats := store.Stats(); stats.Entries != 0 || stats.Misses != 0 {
		t.Fatalf("New must not decode plans: %+v", stats)
	}

	status, body := serveText(t, server, "https://example.com/")
	if status != http.StatusOK || !strings.Contains(body, "home from store") {
		t.Fatalf("status=%d body=%s", status, body)
	}
	if stats := store.Stats(); stats.Entries != 1 || stats.Misses != 1 {
		t.Fatalf("first render must cold-load exactly once: %+v", stats)
	}

	if status, _ := serveText(t, server, "https://example.com/"); status != http.StatusOK {
		t.Fatalf("second request status=%d", status)
	}
	if stats := store.Stats(); stats.Misses != 1 || stats.Hits != 1 {
		t.Fatalf("second render must hit the resident plan: %+v", stats)
	}
}

// TestInlinePlanWinsOverStore covers mixed mode: a page may carry an inline
// plan even when the store shadows the same route, and pages without one
// fall through to the store.
func TestInlinePlanWinsOverStore(t *testing.T) {
	store := openTestPlanStore(t, "build-1", map[string][]byte{
		"home":  storePlanJSON("home", "store copy"),
		"about": storePlanJSON("about", "about from store"),
	})
	inline, err := renderplan.Parse(storePlanJSON("home", "inline copy"))
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Config{
		BuildID:      "build-1",
		PublicOrigin: "https://example.com",
		PlanStore:    store,
		Pages: []PageRoute{
			{Route: router.Route{ID: "home", Pattern: "/", Mode: router.ModeStatic}, Plan: inline, Static: staticPage("Home")},
			{Route: router.Route{ID: "about", Pattern: "/about", Mode: router.ModeStatic}, Static: staticPage("About")},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	status, body := serveText(t, server, "https://example.com/")
	if status != http.StatusOK || !strings.Contains(body, "inline copy") || strings.Contains(body, "store copy") {
		t.Fatalf("inline plan must win: status=%d body=%s", status, body)
	}
	if stats := store.Stats(); stats.Misses != 0 {
		t.Fatalf("rendering an inline-plan page must not touch the store: %+v", stats)
	}

	status, body = serveText(t, server, "https://example.com/about")
	if status != http.StatusOK || !strings.Contains(body, "about from store") {
		t.Fatalf("store-backed page: status=%d body=%s", status, body)
	}
	if stats := store.Stats(); stats.Misses != 1 {
		t.Fatalf("store-backed page must cold-load once: %+v", stats)
	}
}

func TestPackStaticStoreRestoresSafeHTMLAndReportsMisses(t *testing.T) {
	entries := map[string][]byte{
		pack.StaticEntryKey("article", map[string]string{"slug": "hello"}): []byte(`{"props":{"body":"<p>Sanitized</p>"},"metadata":{"lang":"en","title":"Hello","robots":"noindex, nofollow"}}`),
	}
	contracts := `{"apiVersion":"gobeyond.contract/v1alpha1","routes":[{"routeId":"article","props":{"kind":"object","shape":{"body":{"kind":"safeHtml"}}}}],"actions":[]}`
	store := openTestStaticStore(t, "build-1", entries, contracts)

	if !store.Has("article") || store.Has("missing") {
		t.Fatalf("Has: article=%v missing=%v", store.Has("article"), store.Has("missing"))
	}
	page, ok, err := store.Entry(context.Background(), "article", map[string]string{"slug": "hello"})
	if err != nil || !ok {
		t.Fatalf("Entry: ok=%v err=%v", ok, err)
	}
	body, isSafe := page.Props.(map[string]any)["body"].(renderplan.SafeHTML)
	if !isSafe || body.String() != "<p>Sanitized</p>" {
		t.Fatalf("props=%#v", page.Props)
	}
	if page.Kind != gb.ResultOK || page.Metadata.Title != "Hello" {
		t.Fatalf("page=%+v", page)
	}

	missing, ok, err := store.Entry(context.Background(), "article", map[string]string{"slug": "missing"})
	if err != nil || ok {
		t.Fatalf("missing entry must be a plain miss: ok=%v err=%v page=%+v", ok, err, missing)
	}
}

// TestStaticEntryStoreBacksPagesWithoutLoaders proves the server-side wiring:
// a page with neither inline static data nor a loader is served from the
// static entry store, a URL without a packaged entry renders not-found, and
// Config.Contracts defaults from the store.
func TestStaticEntryStoreBacksPagesWithoutLoaders(t *testing.T) {
	entries := map[string][]byte{
		pack.StaticEntryKey("article", map[string]string{"slug": "hello"}): []byte(`{"props":{"body":"<p>Sanitized</p>"},"metadata":{"lang":"en","title":"Hello","robots":"noindex, nofollow"}}`),
	}
	contracts := `{"apiVersion":"gobeyond.contract/v1alpha1","routes":[{"routeId":"article","props":{"kind":"object","shape":{"body":{"kind":"safeHtml"}}}}],"actions":[]}`
	store := openTestStaticStore(t, "build-1", entries, contracts)

	plan, err := renderplan.Parse(storePlanJSON("article", "article page"))
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(Config{
		BuildID:      "build-1",
		PublicOrigin: "https://example.com",
		Static:       store,
		Pages: []PageRoute{{
			Route: router.Route{ID: "article", Pattern: "/articles/[slug]", Mode: router.ModeStatic},
			Plan:  plan,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if server.config.Contracts == nil {
		t.Fatal("Config.Contracts must default from the static store")
	}

	status, body := serveText(t, server, "https://example.com/articles/hello")
	if status != http.StatusOK || !strings.Contains(body, "<title>Hello</title>") {
		t.Fatalf("hit: status=%d body=%s", status, body)
	}
	status, body = serveText(t, server, "https://example.com/articles/other")
	if status != http.StatusNotFound || !strings.Contains(body, "Not found") {
		t.Fatalf("miss must render not-found: status=%d body=%s", status, body)
	}
}

// TestPlanStoreNegativeCachesImmutableFailures corrupts one stored record
// byte: the digest check fails, the failure is marked immutable, and the
// second request is answered from the negative cache instead of re-reading
// bytes that cannot heal within this build.
func TestPlanStoreNegativeCachesImmutableFailures(t *testing.T) {
	path := writePlanPack(t, "build-1", map[string][]byte{"home": storePlanJSON("home", "home page")})
	probe, err := pack.Open(path, pack.ContentPlans)
	if err != nil {
		t.Fatal(err)
	}
	record, _ := probe.Record("home")
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data[record.Offset] ^= 0xff
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	store, err := OpenPlanStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Plan(context.Background(), "home"); !errors.Is(err, pack.ErrDigestMismatch) {
		t.Fatalf("expected a digest mismatch, got %v", err)
	}
	if stats := store.Stats(); stats.NegativeEntries != 1 {
		t.Fatalf("immutable failure must be negative-cached: %+v", stats)
	}
	if _, err := store.Plan(context.Background(), "home"); !errors.Is(err, pack.ErrDigestMismatch) {
		t.Fatalf("expected the remembered digest mismatch, got %v", err)
	}
	if stats := store.Stats(); stats.NegativeHits != 1 {
		t.Fatalf("second failure must come from the negative cache: %+v", stats)
	}
}
