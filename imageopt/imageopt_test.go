package imageopt

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
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

func TestRemoteLoaderAllowlistAndCanonicalURL(t *testing.T) {
	loader, err := NewRemoteLoader([]string{"*.ctfassets.net"})
	if err != nil {
		t.Fatal(err)
	}
	loader.Client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if got := request.URL.String(); got != "https://images.ctfassets.net/space/asset/photo.jpg" {
			t.Fatalf("URL = %q", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("image")),
			Header:     make(http.Header),
			Request:    request,
		}, nil
	})}
	body, err := loader.Open(context.Background(), "https://images.ctfassets.net/space/asset/photo.jpg")
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	data, err := io.ReadAll(body)
	if err != nil || string(data) != "image" {
		t.Fatalf("body = %q, err = %v", data, err)
	}
}

func TestLoadDeploymentConfig(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GOBEYOND_IMAGE_REMOTE_DOMAINS", "")
	if _, ok, err := LoadDeploymentConfig(root); err != nil || ok {
		t.Fatalf("missing config = (%v, %v), want (false, nil)", ok, err)
	}

	configPath := filepath.Join(root, DeploymentConfigPath)
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(`{"remoteDomains":["images.ctfassets.net"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	config, ok, err := LoadDeploymentConfig(root)
	if err != nil || !ok || !reflect.DeepEqual(config.RemoteDomains, []string{"images.ctfassets.net"}) || config.CacheSeconds != DefaultCacheSeconds {
		t.Fatalf("config = (%+v, %v, %v)", config, ok, err)
	}
}

func TestRemoteLoaderRejectsUnsafeSources(t *testing.T) {
	loader, err := NewRemoteLoader([]string{"images.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{
		"http://images.example.com/photo.jpg",
		"https://other.example.com/photo.jpg",
		"https://images.example.com:443/photo.jpg",
		"https://images.example.com/photo.jpg?token=secret",
		"https://images.example.com/photo.jpg#fragment",
	} {
		t.Run(source, func(t *testing.T) {
			if _, err := loader.Open(context.Background(), source); !errors.Is(err, ErrInvalidSource) {
				t.Fatalf("error = %v, want ErrInvalidSource", err)
			}
		})
	}
}

func TestRouterLoaderUsesRemoteForAbsoluteURL(t *testing.T) {
	remote, err := NewRemoteLoader([]string{"images.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	remote.Client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("remote")), Header: make(http.Header), Request: request}, nil
	})}
	loader := RouterLoader{Local: fakeLoader{body: "local"}, Remote: remote}
	body, err := loader.Open(context.Background(), "https://images.example.com/photo.jpg")
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	data, _ := io.ReadAll(body)
	if string(data) != "remote" {
		t.Fatalf("body = %q", data)
	}
}

func TestNewLoaderFromEnvironmentConfiguresRemoteSource(t *testing.T) {
	t.Setenv("GOBEYOND_STATIC_DIR", "")
	t.Setenv(ImageSourceBucketEnv, "")
	t.Setenv(ImageSourcePrefixEnv, "")
	t.Setenv(ImageRemoteDomainsEnv, "images.example.com")
	loader, err := NewLoaderFromEnvironment(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := loader.(*RemoteLoader); !ok {
		t.Fatalf("loader = %#v, want *RemoteLoader", loader)
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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }

type fakeLoader struct{ body string }

func (loader fakeLoader) Open(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(loader.body)), nil
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

func TestHandlerAllowsConfiguredRemoteImage(t *testing.T) {
	var source bytes.Buffer
	if err := png.Encode(&source, image.NewRGBA(image.Rect(0, 0, 80, 40))); err != nil {
		t.Fatal(err)
	}
	remote, err := NewRemoteLoader([]string{"images.ctfassets.net"})
	if err != nil {
		t.Fatal(err)
	}
	remote.Client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://images.ctfassets.net/space/asset/photo.png" {
			t.Fatalf("URL = %q", request.URL.String())
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(bytes.NewReader(source.Bytes())),
			Header:     make(http.Header),
			Request:    request,
		}, nil
	})}
	recorder := httptest.NewRecorder()
	Handler(remote).ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodGet, Route+"?url=https%3A%2F%2Fimages.ctfassets.net%2Fspace%2Fasset%2Fphoto.png&w=32", nil),
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	decoded, format, err := image.Decode(bytes.NewReader(recorder.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if format != "png" || decoded.Bounds().Dx() != 32 {
		t.Fatalf("format=%s bounds=%v", format, decoded.Bounds())
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
		code   string
	}{
		{http.MethodPost, "url=%2Fbrand.png&w=32", http.StatusMethodNotAllowed, "method_not_allowed"},
		{http.MethodGet, "url=https%3A%2F%2Fexample.com%2Fbrand.png&w=32", http.StatusBadRequest, "invalid_source"},
		{http.MethodGet, "url=%2Fbrand.png&w=31", http.StatusBadRequest, "unsupported_width"},
		{http.MethodGet, "url=%2Fbrand.png&w=32&q=bad", http.StatusBadRequest, "invalid_quality"},
		{http.MethodGet, "url=%2Fbrand.png&w=32&f=webp", http.StatusBadRequest, "unsupported_format"},
		{http.MethodGet, "url=%2Fmissing.png&w=32", http.StatusNotFound, "source_not_found"},
	}
	for _, test := range tests {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(test.method, Route+"?"+test.query, nil))
		if recorder.Code != test.status {
			t.Fatalf("%s %s: status=%d body=%s", test.method, test.query, recorder.Code, recorder.Body.String())
		}
		if got := recorder.Header().Get(ImageErrorHeader); got != test.code {
			t.Fatalf("%s %s: error code=%q, want %q", test.method, test.query, got, test.code)
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
