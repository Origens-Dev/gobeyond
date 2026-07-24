package main

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateAppIcons(t *testing.T) {
	projectRoot := t.TempDir()
	staticDir := filepath.Join(t.TempDir(), "static")
	if err := os.MkdirAll(filepath.Join(projectRoot, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(staticDir, 0o755); err != nil {
		t.Fatal(err)
	}

	source := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			source.SetNRGBA(x, y, color.NRGBA{R: uint8(x * 4), G: uint8(y * 4), B: 32, A: 255})
		}
	}
	sourceFile, err := os.Create(filepath.Join(projectRoot, "app", "icon.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(sourceFile, source); err != nil {
		t.Fatal(err)
	}
	if err := sourceFile.Close(); err != nil {
		t.Fatal(err)
	}

	paths, err := generateAppIcons(projectRoot, staticDir)
	if err != nil {
		t.Fatal(err)
	}
	wantPaths := []string{"/apple-touch-icon.png", "/favicon-16x16.png", "/favicon-32x32.png"}
	if got := mergeAssetPaths(paths); !equalStrings(got, wantPaths) {
		t.Fatalf("generated paths = %v, want %v", got, wantPaths)
	}
	for _, output := range generatedIcons {
		file, openErr := os.Open(filepath.Join(staticDir, output.name))
		if openErr != nil {
			t.Fatal(openErr)
		}
		config, decodeErr := png.DecodeConfig(file)
		file.Close()
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if config.Width != output.size || config.Height != output.size {
			t.Fatalf("%s dimensions = %dx%d, want %dx%d", output.name, config.Width, config.Height, output.size, output.size)
		}
	}
}

func TestGenerateAppIconsWithoutSource(t *testing.T) {
	paths, err := generateAppIcons(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 0 {
		t.Fatalf("generated paths without app/icon.png: %v", paths)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
