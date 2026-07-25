package imageopt

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiskLoaderRejectsUnsafeSources(t *testing.T) {
	root := t.TempDir()
	loader := DiskLoader{Root: root}
	for _, source := range []string{
		"",
		"brand/logo.png",
		"../logo.png",
		"/../logo.png",
		"/%2e%2e/logo.png",
		"/brand//logo.png",
		"/brand\\logo.png",
		"https://example.com/logo.png",
		"//example.com/logo.png",
		"/logo.png?version=1",
	} {
		t.Run(source, func(t *testing.T) {
			if _, err := loader.Open(context.Background(), source); err == nil {
				t.Fatalf("Open(%q) succeeded", source)
			}
		})
	}
}

func TestDiskLoaderRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape.png")); err != nil {
		t.Fatal(err)
	}
	if _, err := (DiskLoader{Root: root}).Open(context.Background(), "/escape.png"); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("error = %v, want ErrInvalidSource", err)
	}
}

func TestNewLoaderFromEnvironmentPrefersDisk(t *testing.T) {
	t.Setenv("GOBEYOND_STATIC_DIR", "")
	t.Setenv(ImageSourceBucketEnv, "bucket")
	t.Setenv(ImageSourcePrefixEnv, "landing")
	root := t.TempDir()

	loader, err := NewLoaderFromEnvironment(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	disk, ok := loader.(DiskLoader)
	if !ok || disk.Root != root {
		t.Fatalf("loader = %#v, want DiskLoader rooted at %q", loader, root)
	}
}

// The AWS-free core must not silently serve nothing when a deployment
// configured an S3 source: it points at the nested imageopt/s3 module instead.
func TestNewLoaderFromEnvironmentDirectsS3ConfigurationToNestedModule(t *testing.T) {
	t.Setenv("GOBEYOND_STATIC_DIR", "")
	t.Setenv(ImageSourceBucketEnv, "bucket")
	t.Setenv(ImageSourcePrefixEnv, "landing")

	_, err := NewLoaderFromEnvironment(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "imageopt/s3") {
		t.Fatalf("error = %v, want guidance toward imageopt/s3", err)
	}
}

func TestNewLoaderFromEnvironmentWithoutConfiguration(t *testing.T) {
	t.Setenv("GOBEYOND_STATIC_DIR", "")
	t.Setenv(ImageSourceBucketEnv, "")
	t.Setenv(ImageSourcePrefixEnv, "")

	loader, err := NewLoaderFromEnvironment(context.Background(), "")
	if loader != nil || err != nil {
		t.Fatalf("loader = %#v, err = %v; want (nil, nil)", loader, err)
	}
}

func TestS3SourceFromEnvironmentRejectsPartialConfiguration(t *testing.T) {
	t.Setenv(ImageSourceBucketEnv, "bucket")
	t.Setenv(ImageSourcePrefixEnv, "")
	if _, err := S3SourceFromEnvironment(); err == nil {
		t.Fatal("S3SourceFromEnvironment accepted a bucket without a prefix")
	}

	t.Setenv(ImageSourcePrefixEnv, "../escape")
	if _, err := S3SourceFromEnvironment(); err == nil {
		t.Fatal("S3SourceFromEnvironment accepted a traversal prefix")
	}
}

func TestHandlerResizesPNGAndConvertsJPEG(t *testing.T) {
	root := t.TempDir()
	writeTestPNG(t, filepath.Join(root, "brand.png"), 80, 40)
	handler := Handler(DiskLoader{Root: root})

	for _, test := range []struct {
		query       string
		contentType string
		format      string
	}{
		{"url=%2Fbrand.png&w=32&q=0", "image/png", "png"},
		{"url=%2Fbrand.png&w=64&q=120&f=jpeg", "image/jpeg", "jpeg"},
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, Route+"?"+test.query, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, body = %s", test.query, recorder.Code, recorder.Body.String())
		}
		if got := recorder.Header().Get("Content-Type"); got != test.contentType {
			t.Fatalf("%s: content type = %q", test.query, got)
		}
		decoded, format, err := image.Decode(bytes.NewReader(recorder.Body.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		if format != test.format || decoded.Bounds().Dx() != widthFromQuery(test.query) {
			t.Fatalf("%s: format=%s bounds=%v", test.query, format, decoded.Bounds())
		}
		if got := recorder.Header().Get("Cache-Control"); got != "public, max-age=3600" {
			t.Fatalf("cache control = %q", got)
		}
	}
}

func TestHandlerValidatesRequest(t *testing.T) {
	root := t.TempDir()
	writeTestPNG(t, filepath.Join(root, "brand.png"), 80, 40)
	handler := Handler(DiskLoader{Root: root})
	tests := []struct {
		method string
		query  string
		status int
	}{
		{http.MethodPost, "url=%2Fbrand.png&w=32", http.StatusMethodNotAllowed},
		{http.MethodGet, "url=https%3A%2F%2Fexample.com%2Fbrand.png&w=32", http.StatusBadRequest},
		{http.MethodGet, "url=%2Fbrand.png&w=31", http.StatusBadRequest},
		{http.MethodGet, "url=%2Fbrand.png&w=32&q=bad", http.StatusBadRequest},
		{http.MethodGet, "url=%2Fbrand.png&w=32&f=webp", http.StatusBadRequest},
		{http.MethodGet, "url=%2Fmissing.png&w=32", http.StatusNotFound},
	}
	for _, test := range tests {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(test.method, Route+"?"+test.query, nil))
		if recorder.Code != test.status {
			t.Fatalf("%s %s: status=%d body=%s", test.method, test.query, recorder.Code, recorder.Body.String())
		}
	}
}

func TestHandlerRejectsUnsupportedSourceFormat(t *testing.T) {
	root := t.TempDir()
	var data bytes.Buffer
	if err := jpeg.Encode(&data, image.NewRGBA(image.Rect(0, 0, 8, 8)), nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "photo.jpg"), data.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := Handler(DiskLoader{Root: root})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, Route+"?url=%2Fphoto.jpg&w=16", nil))
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("status=%d type=%q body=%s", recorder.Code, recorder.Header().Get("Content-Type"), recorder.Body.String())
	}

	if err := os.WriteFile(filepath.Join(root, "text.txt"), []byte("not an image"), 0o600); err != nil {
		t.Fatal(err)
	}
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, Route+"?url=%2Ftext.txt&w=16", nil))
	if recorder.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestHandlerRejectsOversizedOutputDimensions(t *testing.T) {
	root := t.TempDir()
	writeTestPNG(t, filepath.Join(root, "tall.png"), 1, 20_000)
	recorder := httptest.NewRecorder()
	Handler(DiskLoader{Root: root}).ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodGet, Route+"?url=%2Ftall.png&w=3840", nil),
	)
	if recorder.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func writeTestPNG(t *testing.T, path string, width, height int) {
	t.Helper()
	picture := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			picture.SetNRGBA(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: 100, A: 200})
		}
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := png.Encode(file, picture); err != nil {
		t.Fatal(err)
	}
}

func widthFromQuery(query string) int {
	if strings.Contains(query, "w=64") {
		return 64
	}
	return 32
}
