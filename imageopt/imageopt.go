// Package imageopt provides the Node-free GoBeyond runtime image optimizer.
//
// This package is AWS-free by design: it owns the Loader interface, the disk
// source, the HTTP handler, and the resize/re-encode path. The S3-backed
// source lives in the nested module
// github.com/Origens-Dev/gobeyond/imageopt/s3, so only deployments that read
// images from S3 pull the AWS SDK into their module graph.
package imageopt

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// Route is the same-site runtime image optimization endpoint.
	Route = "/_gobeyond/image"

	defaultQuality  = 75
	maxSourceBytes  = 32 << 20
	maxSourcePixels = 40_000_000

	// ImageSourceBucketEnv and ImageSourcePrefixEnv configure production S3
	// source loading. GOBEYOND_STATIC_DIR takes precedence for local development.
	ImageSourceBucketEnv = "GOBEYOND_IMAGE_SOURCE_BUCKET"
	ImageSourcePrefixEnv = "GOBEYOND_IMAGE_SOURCE_PREFIX"
	// ImageRemoteDomainsEnv is a comma-separated list of exact domains or
	// subdomain patterns (for example, images.example.com,*.ctfassets.net)
	// that the remote image loader may fetch.
	ImageRemoteDomainsEnv = "GOBEYOND_IMAGE_REMOTE_DOMAINS"
	// ImageErrorHeader identifies validation or source failures without
	// exposing the requested URL, which may contain sensitive query values.
	ImageErrorHeader = "X-GoBeyond-Image-Error"
)

// DefaultWidths bounds the variants the runtime will generate.
var DefaultWidths = []int{16, 32, 48, 64, 96, 128, 256, 384, 640, 750, 828, 1080, 1200, 1920, 2048, 3840}

var (
	ErrInvalidSource  = errors.New("invalid same-site image source")
	ErrNotFound       = errors.New("image source not found")
	errSourceTooLarge = errors.New("image source too large")
)

// Loader opens a same-site static path. Implementations must not interpret it
// as a remote URL.
type Loader interface {
	Open(context.Context, string) (io.ReadCloser, error)
}

// RouterLoader selects a remote loader for absolute HTTPS URLs and a local
// loader for same-site paths. It lets a deployment optimize both its own
// static assets and explicitly allowlisted public remote assets.
type RouterLoader struct {
	Local  Loader
	Remote Loader
}

func (loader RouterLoader) Open(ctx context.Context, source string) (io.ReadCloser, error) {
	if isRemoteSource(source) {
		if loader.Remote == nil {
			return nil, ErrInvalidSource
		}
		return loader.Remote.Open(ctx, source)
	}
	if loader.Local == nil {
		return nil, ErrNotFound
	}
	return loader.Local.Open(ctx, source)
}

// RemoteLoader fetches public HTTPS images from an explicit domain allowlist.
// It intentionally does not forward viewer headers or credentials.
type RemoteLoader struct {
	Client         *http.Client
	AllowedDomains []string
}

var _ Loader = RemoteLoader{}

// NewRemoteLoader creates a remote loader with the default SSRF-safe client.
func NewRemoteLoader(domains []string) (RemoteLoader, error) {
	normalized, err := NormalizeRemoteDomains(domains)
	if err != nil {
		return RemoteLoader{}, err
	}
	return RemoteLoader{
		Client:         remoteHTTPClient(normalized),
		AllowedDomains: normalized,
	}, nil
}

// NewRemoteLoaderFromEnvironment reads ImageRemoteDomainsEnv. It returns nil
// when remote loading is not configured.
func NewRemoteLoaderFromEnvironment() (*RemoteLoader, error) {
	raw := strings.TrimSpace(os.Getenv(ImageRemoteDomainsEnv))
	if raw == "" {
		return nil, nil
	}
	domains := strings.Split(raw, ",")
	loader, err := NewRemoteLoader(domains)
	if err != nil {
		return nil, fmt.Errorf("invalid %s: %w", ImageRemoteDomainsEnv, err)
	}
	return &loader, nil
}

// NormalizeRemoteDomains validates and canonicalizes domain allowlist entries.
func NormalizeRemoteDomains(domains []string) ([]string, error) {
	seen := make(map[string]struct{}, len(domains))
	result := make([]string, 0, len(domains))
	for _, raw := range domains {
		domain := strings.ToLower(strings.TrimSpace(raw))
		if domain == "" {
			continue
		}
		if strings.HasPrefix(domain, "*.") {
			if len(domain) <= 2 || strings.Contains(domain[2:], "/") || strings.Contains(domain[2:], ":") {
				return nil, fmt.Errorf("invalid remote domain %q", raw)
			}
		} else if strings.Contains(domain, "*") || strings.Contains(domain, "/") || strings.Contains(domain, ":") {
			return nil, fmt.Errorf("invalid remote domain %q", raw)
		}
		domain = strings.TrimSuffix(domain, ".")
		if _, ok := seen[domain]; ok {
			continue
		}
		seen[domain] = struct{}{}
		result = append(result, domain)
	}
	if len(result) == 0 {
		return nil, errors.New("at least one remote domain is required")
	}
	return result, nil
}

// ValidateRemoteURL canonicalizes a remote source and checks its domain
// allowlist. Remote source URLs may not carry query strings or fragments.
func ValidateRemoteURL(source string, allowedDomains []string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(source))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Port() != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", ErrInvalidSource
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if !remoteDomainAllowed(host, allowedDomains) {
		return "", ErrInvalidSource
	}
	if parsed.Path == "" || strings.Contains(parsed.Path, "\\") || strings.Contains(parsed.Path, "\x00") {
		return "", ErrInvalidSource
	}
	for _, segment := range strings.Split(strings.TrimPrefix(parsed.Path, "/"), "/") {
		if segment == "." || segment == ".." {
			return "", ErrInvalidSource
		}
	}
	parsed.Host = host
	return parsed.String(), nil
}

func (loader RemoteLoader) Open(ctx context.Context, source string) (io.ReadCloser, error) {
	canonical, err := ValidateRemoteURL(source, loader.AllowedDomains)
	if err != nil {
		return nil, err
	}
	client := loader.Client
	if client == nil {
		client = remoteHTTPClient(loader.AllowedDomains)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, canonical, nil)
	if err != nil {
		return nil, ErrInvalidSource
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_ = response.Body.Close()
		if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusGone {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("remote image returned HTTP %d", response.StatusCode)
	}
	return &limitedReadCloser{Reader: io.LimitReader(response.Body, maxSourceBytes+1), closer: response.Body}, nil
}

type limitedReadCloser struct {
	io.Reader
	closer io.Closer
}

func (reader *limitedReadCloser) Close() error { return reader.closer.Close() }

func remoteHTTPClient(allowedDomains []string) *http.Client {
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
			if err != nil {
				return nil, err
			}
			for _, ip := range ips {
				if !isPublicIP(ip) {
					return nil, errors.New("remote image host resolves to a private address")
				}
			}
			if len(ips) == 0 {
				return nil, errors.New("remote image host has no address")
			}
			return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
		},
	}
	return &http.Client{
		Transport: transport,
		Timeout:   10 * time.Second,
		CheckRedirect: func(request *http.Request, _ []*http.Request) error {
			_, err := ValidateRemoteURL(request.URL.String(), allowedDomains)
			return err
		},
	}
}

func remoteDomainAllowed(host string, allowedDomains []string) bool {
	for _, raw := range allowedDomains {
		domain := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
		if strings.HasPrefix(domain, "*.") {
			base := strings.TrimPrefix(domain, "*.")
			if host != base && strings.HasSuffix(host, "."+base) {
				return true
			}
		} else if host == domain {
			return true
		}
	}
	return false
}

func isPublicIP(ip net.IP) bool {
	return !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() && !ip.IsUnspecified()
}

func isRemoteSource(source string) bool {
	parsed, err := url.Parse(strings.TrimSpace(source))
	return err == nil && parsed.Scheme != "" && parsed.Host != ""
}

// DiskLoader reads static files beneath Root.
type DiskLoader struct {
	Root string
}

// NewLoaderFromEnvironment selects a disk source when diskRoot (or
// GOBEYOND_STATIC_DIR) is set, and reports no loader when nothing is
// configured. This package is deliberately AWS-free: S3-backed sources live in
// the nested github.com/Origens-Dev/gobeyond/imageopt/s3 module, whose
// s3.NewLoaderFromEnvironment adds the S3 branch to this same environment
// contract.
func NewLoaderFromEnvironment(_ context.Context, diskRoot string) (Loader, error) {
	remote, err := NewRemoteLoaderFromEnvironment()
	if err != nil {
		return nil, err
	}
	if root, ok := DiskRootFromEnvironment(diskRoot); ok {
		if remote != nil {
			return RouterLoader{Local: DiskLoader{Root: root}, Remote: remote}, nil
		}
		return DiskLoader{Root: root}, nil
	}
	configured, err := S3SourceFromEnvironment()
	if err != nil {
		return nil, err
	}
	if configured {
		return nil, fmt.Errorf(
			"%s/%s are configured but this build has no S3 image source: import github.com/Origens-Dev/gobeyond/imageopt/s3 and call s3.NewLoaderFromEnvironment",
			ImageSourceBucketEnv, ImageSourcePrefixEnv)
	}
	if remote != nil {
		return remote, nil
	}
	return nil, nil
}

// DiskRootFromEnvironment resolves the disk image source: diskRoot when set,
// otherwise GOBEYOND_STATIC_DIR. ok is false when neither is configured.
func DiskRootFromEnvironment(diskRoot string) (root string, ok bool) {
	if strings.TrimSpace(diskRoot) == "" {
		diskRoot = os.Getenv("GOBEYOND_STATIC_DIR")
	}
	if strings.TrimSpace(diskRoot) == "" {
		return "", false
	}
	return diskRoot, true
}

// S3SourceFromEnvironment reports whether a complete, valid S3 image source is
// configured. It errors when the bucket and prefix disagree about being set or
// when the prefix is unsafe, so the imageopt/s3 module and AWS-free builds
// report the same misconfiguration.
func S3SourceFromEnvironment() (configured bool, err error) {
	bucket := strings.TrimSpace(os.Getenv(ImageSourceBucketEnv))
	prefix := strings.TrimSpace(os.Getenv(ImageSourcePrefixEnv))
	if bucket == "" && prefix == "" {
		return false, nil
	}
	if bucket == "" || prefix == "" {
		return false, fmt.Errorf("%s and %s must be configured together", ImageSourceBucketEnv, ImageSourcePrefixEnv)
	}
	if _, err := ValidatePrefix(prefix); err != nil {
		return false, fmt.Errorf("invalid %s: %w", ImageSourcePrefixEnv, err)
	}
	return true, nil
}

// Open securely resolves source beneath the configured static root.
func (loader DiskLoader) Open(_ context.Context, source string) (io.ReadCloser, error) {
	relative, err := validateSource(source)
	if err != nil {
		return nil, err
	}
	root, err := filepath.Abs(loader.Root)
	if err != nil || strings.TrimSpace(loader.Root) == "" {
		return nil, fmt.Errorf("resolve static root: %w", ErrInvalidSource)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	candidate := filepath.Join(resolvedRoot, filepath.FromSlash(relative))
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if !withinRoot(resolvedRoot, resolved) {
		return nil, ErrInvalidSource
	}
	file, err := os.Open(resolved)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, ErrNotFound
	}
	return file, nil
}

// Handler returns an HTTP handler for Route.
func Handler(loader Loader) http.Handler {
	widths := make(map[int]struct{}, len(DefaultWidths))
	for _, width := range DefaultWidths {
		widths[width] = struct{}{}
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		reject := func(status int, code, message string) {
			writer.Header().Set(ImageErrorHeader, code)
			http.Error(writer, message, status)
		}
		if request.Method != http.MethodGet {
			writer.Header().Set("Allow", http.MethodGet)
			reject(http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		if loader == nil {
			reject(http.StatusServiceUnavailable, "source_unavailable", "image optimizer source unavailable")
			return
		}
		source := request.URL.Query().Get("url")
		width, err := strconv.Atoi(request.URL.Query().Get("w"))
		if err != nil {
			reject(http.StatusBadRequest, "invalid_width", "invalid image width")
			return
		}
		if _, ok := widths[width]; !ok {
			reject(http.StatusBadRequest, "unsupported_width", "unsupported image width")
			return
		}
		quality := defaultQuality
		if raw := request.URL.Query().Get("q"); raw != "" {
			quality, err = strconv.Atoi(raw)
			if err != nil {
				reject(http.StatusBadRequest, "invalid_quality", "invalid image quality")
				return
			}
		}
		quality = max(1, min(quality, 100))
		format := strings.ToLower(request.URL.Query().Get("f"))
		if format == "jpg" {
			format = "jpeg"
		}
		if format != "" && format != "jpeg" && format != "png" {
			reject(http.StatusBadRequest, "unsupported_format", "unsupported image format")
			return
		}

		input, err := loader.Open(request.Context(), source)
		if err != nil {
			status := http.StatusInternalServerError
			code := "source_open_failed"
			if errors.Is(err, ErrInvalidSource) {
				status = http.StatusBadRequest
				code = "invalid_source"
			} else if errors.Is(err, ErrNotFound) || errors.Is(err, os.ErrNotExist) {
				status = http.StatusNotFound
				code = "source_not_found"
			}
			reject(status, code, http.StatusText(status))
			return
		}
		defer input.Close()
		output, contentType, err := optimize(&sourceLimitReader{Reader: input, Remaining: maxSourceBytes}, width, quality, format)
		if err != nil {
			if errors.Is(err, errSourceTooLarge) {
				reject(http.StatusRequestEntityTooLarge, "source_too_large", "image source too large")
				return
			}
			reject(http.StatusUnsupportedMediaType, "unsupported_source", err.Error())
			return
		}
		writer.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", CacheSecondsFromEnvironment()))
		writer.Header().Set("Content-Type", contentType)
		writer.Header().Set("Content-Length", strconv.Itoa(len(output)))
		_, _ = writer.Write(output)
	})
}

// ValidateSource normalizes a same-site image source into a safe relative
// path. Loader implementations outside this package (e.g. imageopt/s3) must
// call it before touching any storage.
func ValidateSource(source string) (string, error) { return validateSource(source) }

// ValidatePrefix normalizes a storage prefix, rejecting traversal and empty
// segments. It is exported for out-of-package Loader implementations.
func ValidatePrefix(prefix string) (string, error) { return validatePrefix(prefix) }

func validateSource(source string) (string, error) {
	if source == "" || !strings.HasPrefix(source, "/") || strings.HasPrefix(source, "//") || strings.Contains(source, "\\") {
		return "", ErrInvalidSource
	}
	parsed, err := url.Parse(source)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", ErrInvalidSource
	}
	decoded, err := url.PathUnescape(parsed.EscapedPath())
	if err != nil || decoded != parsed.Path || strings.Contains(decoded, "\x00") {
		return "", ErrInvalidSource
	}
	segments := strings.Split(strings.TrimPrefix(decoded, "/"), "/")
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." {
			return "", ErrInvalidSource
		}
	}
	return strings.Join(segments, "/"), nil
}

func validatePrefix(prefix string) (string, error) {
	prefix = strings.Trim(prefix, "/")
	if prefix == "" || strings.Contains(prefix, "\\") {
		return "", ErrInvalidSource
	}
	for _, segment := range strings.Split(prefix, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", ErrInvalidSource
		}
	}
	return prefix, nil
}

func withinRoot(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

type sourceLimitReader struct {
	io.Reader
	Remaining int64
}

func (r *sourceLimitReader) Read(p []byte) (int, error) {
	if r.Remaining <= 0 {
		return 0, errSourceTooLarge
	}
	if int64(len(p)) > r.Remaining {
		p = p[:r.Remaining]
	}
	n, err := r.Reader.Read(p)
	r.Remaining -= int64(n)
	return n, err
}

func optimize(reader io.Reader, width, quality int, requestedFormat string) ([]byte, string, error) {
	source, sourceFormat, err := image.Decode(reader)
	if err != nil {
		if errors.Is(err, errSourceTooLarge) {
			return nil, "", errSourceTooLarge
		}
		return nil, "", errors.New("unsupported image source")
	}
	bounds := source.Bounds()
	sourceWidth, sourceHeight := bounds.Dx(), bounds.Dy()
	if sourceWidth <= 0 || sourceHeight <= 0 || int64(sourceWidth)*int64(sourceHeight) > maxSourcePixels {
		return nil, "", errors.New("image dimensions are unsupported")
	}
	if sourceFormat != "jpeg" && sourceFormat != "png" {
		return nil, "", errors.New("image source must be JPEG or PNG")
	}
	height := max(1, int(math.Round(float64(sourceHeight)*float64(width)/float64(sourceWidth))))
	if int64(width)*int64(height) > maxSourcePixels {
		return nil, "", errors.New("output image dimensions are unsupported")
	}
	resized := resizeBilinear(source, width, height)
	format := requestedFormat
	if format == "" {
		format = sourceFormat
	}
	var output bytes.Buffer
	switch format {
	case "jpeg":
		opaque := image.NewRGBA(resized.Bounds())
		draw.Draw(opaque, opaque.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
		draw.Draw(opaque, opaque.Bounds(), resized, image.Point{}, draw.Over)
		if err := jpeg.Encode(&output, opaque, &jpeg.Options{Quality: quality}); err != nil {
			return nil, "", err
		}
		return output.Bytes(), "image/jpeg", nil
	case "png":
		encoder := png.Encoder{CompressionLevel: png.DefaultCompression}
		if err := encoder.Encode(&output, resized); err != nil {
			return nil, "", err
		}
		return output.Bytes(), "image/png", nil
	default:
		return nil, "", errors.New("unsupported output format")
	}
}

func resizeBilinear(source image.Image, width, height int) *image.NRGBA {
	target := image.NewNRGBA(image.Rect(0, 0, width, height))
	bounds := source.Bounds()
	sourceWidth, sourceHeight := bounds.Dx(), bounds.Dy()
	for y := 0; y < height; y++ {
		sourceY := (float64(y)+0.5)*float64(sourceHeight)/float64(height) - 0.5
		y0 := max(0, min(int(math.Floor(sourceY)), sourceHeight-1))
		y1 := min(y0+1, sourceHeight-1)
		fy := sourceY - math.Floor(sourceY)
		if sourceY < 0 {
			fy = 0
		}
		for x := 0; x < width; x++ {
			sourceX := (float64(x)+0.5)*float64(sourceWidth)/float64(width) - 0.5
			x0 := max(0, min(int(math.Floor(sourceX)), sourceWidth-1))
			x1 := min(x0+1, sourceWidth-1)
			fx := sourceX - math.Floor(sourceX)
			if sourceX < 0 {
				fx = 0
			}
			c00 := color.NRGBAModel.Convert(source.At(bounds.Min.X+x0, bounds.Min.Y+y0)).(color.NRGBA)
			c10 := color.NRGBAModel.Convert(source.At(bounds.Min.X+x1, bounds.Min.Y+y0)).(color.NRGBA)
			c01 := color.NRGBAModel.Convert(source.At(bounds.Min.X+x0, bounds.Min.Y+y1)).(color.NRGBA)
			c11 := color.NRGBAModel.Convert(source.At(bounds.Min.X+x1, bounds.Min.Y+y1)).(color.NRGBA)
			target.SetNRGBA(x, y, color.NRGBA{
				R: interpolate(c00.R, c10.R, c01.R, c11.R, fx, fy),
				G: interpolate(c00.G, c10.G, c01.G, c11.G, fx, fy),
				B: interpolate(c00.B, c10.B, c01.B, c11.B, fx, fy),
				A: interpolate(c00.A, c10.A, c01.A, c11.A, fx, fy),
			})
		}
	}
	return target
}

func interpolate(c00, c10, c01, c11 uint8, fx, fy float64) uint8 {
	top := float64(c00)*(1-fx) + float64(c10)*fx
	bottom := float64(c01)*(1-fx) + float64(c11)*fx
	return uint8(math.Round(top*(1-fy) + bottom*fy))
}
