package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectEnvironmentPrecedence(t *testing.T) {
	root := t.TempDir()
	writeEnvFixture(t, root, ".env", "FROM_BASE=base\nSHARED=base\nPROCESS_WINS=file\n")
	writeEnvFixture(t, root, ".env.production", "FROM_MODE=production\nSHARED=mode\n")
	writeEnvFixture(t, root, ".env.local", "FROM_LOCAL=local\nSHARED=local\n")
	writeEnvFixture(t, root, ".env.production.local", "FROM_MODE_LOCAL=local-production\nSHARED=mode-local\n")
	t.Setenv("PROCESS_WINS", "process")

	environment, err := projectEnvironment(root, "production")
	if err != nil {
		t.Fatal(err)
	}
	values := environmentValues(environment)
	for key, want := range map[string]string{
		"FROM_BASE":       "base",
		"FROM_MODE":       "production",
		"FROM_LOCAL":      "local",
		"FROM_MODE_LOCAL": "local-production",
		"SHARED":          "mode-local",
		"PROCESS_WINS":    "process",
	} {
		if got := values[key]; got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestProjectEnvironmentAllowsAbsentDotenvFiles(t *testing.T) {
	environment, err := projectEnvironment(t.TempDir(), "development")
	if err != nil {
		t.Fatalf("absent dotenv files must be ignored: %v", err)
	}
	if len(environment) == 0 {
		t.Fatal("process environment was lost")
	}
}

func TestProjectEnvironmentLoadsContentfulSecretsForGoWithoutBrowserPrefix(t *testing.T) {
	root := t.TempDir()
	writeEnvFixture(t, root, ".env.production", "CONTENTFUL_SPACE_ID=space_123\nCONTENTFUL_DELIVERY_ACCESS_TOKEN='contentful-delivery-secret'\nVITE_CONTENTFUL_SPACE_ID=space_123\n")
	environment, err := projectEnvironment(root, "production")
	if err != nil {
		t.Fatal(err)
	}
	values := environmentValues(environment)
	if values["CONTENTFUL_DELIVERY_ACCESS_TOKEN"] != "contentful-delivery-secret" {
		t.Fatal("Go/runtime secret was not loaded")
	}
	if values["VITE_CONTENTFUL_SPACE_ID"] != "space_123" {
		t.Fatal("public Contentful identifier was not loaded")
	}
}

func TestViteHonorsProjectPostCSSAndDoesNotExposeUnprefixedSecret(t *testing.T) {
	root := t.TempDir()
	vite := filepath.Join(workspaceRoot(t), "node_modules", ".bin", "vite")
	if _, err := os.Stat(vite); err != nil {
		t.Skip("workspace Vite is unavailable")
	}
	writeEnvFixture(t, root, ".env.production", "CONTENTFUL_DELIVERY_ACCESS_TOKEN=contentful-delivery-secret\nVITE_CONTENTFUL_SPACE_ID=space_123\n")
	writeEnvFixture(t, root, "entry.js", "import './site.css'\nglobalThis.__PUBLIC_SPACE__ = import.meta.env.VITE_CONTENTFUL_SPACE_ID\nglobalThis.__SECRET__ = import.meta.env.CONTENTFUL_DELIVERY_ACCESS_TOKEN\n")
	writeEnvFixture(t, root, "site.css", ":root { --tooling-test: source; }\n")
	writeEnvFixture(t, root, "postcss-plugin.mjs", "export default { postcssPlugin: 'gobeyond-test-plugin', Declaration(declaration) { if (declaration.prop === '--tooling-test') declaration.value = 'processed' } }\n")
	writeEnvFixture(t, root, "postcss.config.mjs", "import plugin from './postcss-plugin.mjs'\nexport default { plugins: [plugin] }\n")
	writeEnvFixture(t, root, "vite.config.ts", "export default { publicDir: false, build: { outDir: process.env.GOBEYOND_STATIC_OUT, emptyOutDir: false, rollupOptions: { input: process.env.GOBEYOND_CLIENT_ENTRY, output: { entryFileNames: 'app.js', assetFileNames: 'assets/[name]-[hash][extname]' } } } }\n")

	environment, err := projectEnvironment(root, "production")
	if err != nil {
		t.Fatal(err)
	}
	staticDir := filepath.Join(root, "output")
	if err := buildBrowserAssets(workspaceRoot(t), root, staticDir, "test", filepath.Join(root, "entry.js"), environment, "production"); err != nil {
		t.Fatal(err)
	}
	assets := readTree(t, filepath.Join(staticDir, "_gobeyond", "assets", "test"))
	if !strings.Contains(assets, "processed") {
		t.Fatal("Vite did not apply the project PostCSS config")
	}
	if !strings.Contains(assets, "space_123") {
		t.Fatal("Vite did not expose the VITE_ variable")
	}
	if strings.Contains(assets, "contentful-delivery-secret") {
		t.Fatal("unprefixed Contentful secret was emitted into browser assets")
	}
}

func workspaceRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(directory, "..", ".."))
}

func writeEnvFixture(t *testing.T, root, name, contents string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func environmentValues(environment []string) map[string]string {
	values := make(map[string]string, len(environment))
	for _, entry := range environment {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = value
	}
	return values
}

func readTree(t *testing.T, root string) string {
	t.Helper()
	var contents strings.Builder
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		contents.Write(data)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return contents.String()
}
