package runtime

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/Origens-Dev/gobeyond/buildpaths"
)

const (
	// EnvStaticDir names the environment variable pointing at the built
	// static directory (dist/static). Preview and origin servers use it when
	// CloudFront/S3 is not in front of the process.
	EnvStaticDir = "GOBEYOND_STATIC_DIR"

	immutableCacheControl = "public, max-age=31536000, immutable"
)

// StaticFiles serves build artifacts and public files from directory (typically
// GOBEYOND_STATIC_DIR), then falls through to next for everything else.
//
// Content-addressed build paths under /_gobeyond/builds/.../assets|manifest|static
// get Cache-Control: public, max-age=31536000, immutable. Non-hashed public/
// files are served without that header. Compressible responses (JS, CSS, SVG,
// JSON, HTML, source maps) are gzip-compressed when the client accepts gzip,
// matching document/API gzip in Server.ServeHTTP.
//
// An empty directory disables static serving and returns next unchanged.
func StaticFiles(directory string, next http.Handler) http.Handler {
	if strings.TrimSpace(directory) == "" || next == nil {
		return next
	}
	files := http.FileServer(http.Dir(directory))
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !buildpaths.IsStaticArtifact(request.URL.Path) && !staticFileExists(directory, request.URL.Path) {
			next.ServeHTTP(writer, request)
			return
		}
		if buildpaths.IsStaticArtifact(request.URL.Path) {
			writer.Header().Set("Cache-Control", immutableCacheControl)
		}
		if acceptsGzip(request) && staticPathCompressible(request.URL.Path) {
			compressed := &gzipResponseWriter{ResponseWriter: writer}
			files.ServeHTTP(compressed, request)
			_ = compressed.Close()
			return
		}
		files.ServeHTTP(writer, request)
	})
}

// StaticFilesFromEnv is StaticFiles(os.Getenv(EnvStaticDir), next).
func StaticFilesFromEnv(next http.Handler) http.Handler {
	return StaticFiles(os.Getenv(EnvStaticDir), next)
}

func staticFileExists(directory, requestPath string) bool {
	cleaned := filepath.Clean("/" + requestPath)
	if cleaned == "/" || strings.Contains(cleaned, "..") {
		return false
	}
	path := filepath.Join(directory, filepath.FromSlash(strings.TrimPrefix(cleaned, "/")))
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func staticPathCompressible(path string) bool {
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".js"),
		strings.HasSuffix(lower, ".css"),
		strings.HasSuffix(lower, ".svg"),
		strings.HasSuffix(lower, ".json"),
		strings.HasSuffix(lower, ".html"),
		strings.HasSuffix(lower, ".htm"),
		strings.HasSuffix(lower, ".map"):
		return true
	default:
		return false
	}
}
