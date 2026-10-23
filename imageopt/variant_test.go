package imageopt

import (
	"bytes"
	"context"
	"image"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVariantContract(t *testing.T) {
	for _, width := range []string{"1", "31", "640", "960", "1280", "1920", "3840"} {
		if _, err := ParseVariant("url=%2Fphoto.png&w="+width, ""); err != nil {
			t.Fatalf("width %s: %v", width, err)
		}
	}
	for _, query := range []string{"w=0", "w=3841", "w=3x", "w=1e2", "w=%2B32", "w=32&w=64", "w=32&q=0", "w=32&q=101", "w=32&q=", "w=32&q=75x", "w=32&f=avif", "w=32&f=png&f=jpeg", "w=32&v=3", "w=32&url=%2Fother.png"} {
		if _, err := ParseVariant("url=%2Fphoto.png&"+query, ""); err == nil {
			t.Fatalf("accepted %s", query)
		}
	}
	for _, tt := range []struct{ accept, want string }{
		{"", "jpeg"}, {"image/webp", "webp"}, {"image/webp;q=0,image/jpeg", "jpeg"},
		{"image/webp;q=0,*/*;q=1", "jpeg"}, {"image/webp;q=0.5,image/jpeg;q=1", "jpeg"},
		{"image/*", "webp"}, {"*/*", "webp"}, {"image/webp;q=NaN,image/jpeg", "jpeg"},
	} {
		v, err := ParseVariant("url=%2Fphoto.png&w=32&f=auto", tt.accept)
		if err != nil || v.Format != tt.want || !v.Negotiated {
			t.Fatalf("Accept %q: %+v %v", tt.accept, v, err)
		}
	}
	if _, err := ParseVariant("url=%2Fphoto.png&w=32", "image/webp;q=0,image/jpeg;q=0"); err == nil {
		t.Fatal("accepted excluded formats")
	}
}

type publicFixtureLoader struct {
	t     *testing.T
	bytes []byte
}

func (l publicFixtureLoader) Open(ctx context.Context, _ string) (io.ReadCloser, error) {
	if ctx.Value(fixtureIdentityKey{}) != nil {
		l.t.Fatal("viewer context leaked into public source loader")
	}
	return io.NopCloser(bytes.NewReader(l.bytes)), nil
}

type fixtureIdentityKey struct{}

func TestPortablePublicImageFormatsAndCredentials(t *testing.T) {
	var fixture bytes.Buffer
	// Existing test fixture helper encodes a PNG to a temporary source.
	root := t.TempDir()
	writeTestPNG(t, root+"/source.png", 8, 4)
	source, err := (DiskLoader{Root: root}).Open(context.Background(), "/source.png")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(&fixture, source)
	source.Close()
	handler := Handler(publicFixtureLoader{t, fixture.Bytes()})
	for _, format := range []string{"jpeg", "png", "webp", "auto"} {
		t.Run(format, func(t *testing.T) {
			req := httptest.NewRequest("GET", Route+"?v=2&url=%2Fsource.png&w=31&q=75&f="+format, nil)
			req.Header.Set("Accept", "image/webp,image/jpeg;q=0.8")
			req.Header.Set("Cookie", "session=viewer")
			req.Header.Set("Authorization", "Bearer viewer")
			req = req.WithContext(context.WithValue(req.Context(), fixtureIdentityKey{}, "viewer"))
			out := httptest.NewRecorder()
			handler.ServeHTTP(out, req)
			if out.Code != http.StatusOK {
				t.Fatalf("%d %s", out.Code, out.Body.String())
			}
			decoded, actual, err := image.Decode(bytes.NewReader(out.Body.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			want := format
			if format == "auto" {
				want = "webp"
			}
			if actual != want || decoded.Bounds().Dx() != 31 || !strings.Contains(out.Header().Get("Cache-Control"), "public") {
				t.Fatalf("%s %v %v", actual, decoded.Bounds(), out.Header())
			}
			if (out.Header().Get("Vary") == "Accept") != (format == "auto") {
				t.Fatalf("Vary=%q", out.Header().Get("Vary"))
			}
		})
	}
	for _, query := range []string{"url=%2Fsource.png&w=bad", "url=%2Fmissing&w=32&f=avif"} {
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, httptest.NewRequest("GET", Route+"?"+query, nil))
		if out.Code < 400 || out.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%d %v", out.Code, out.Header())
		}
	}
}
