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
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
)

// DefaultWidths bounds the variants the runtime will generate.
var DefaultWidths = []int{16, 32, 48, 64, 96, 128, 256, 384, 640, 750, 828, 1080, 1200, 1920, 2048, 3840}

var (
	ErrInvalidSource = errors.New("invalid same-site image source")
	ErrNotFound      = errors.New("image source not found")
)

// Loader opens a same-site static path. Implementations must not interpret it
// as a remote URL.
type Loader interface {
	Open(context.Context, string) (io.ReadCloser, error)
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
	if root, ok := DiskRootFromEnvironment(diskRoot); ok {
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
		if request.Method != http.MethodGet {
			writer.Header().Set("Allow", http.MethodGet)
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if loader == nil {
			http.Error(writer, "image optimizer source unavailable", http.StatusServiceUnavailable)
			return
		}
		source := request.URL.Query().Get("url")
		if _, err := validateSource(source); err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		width, err := strconv.Atoi(request.URL.Query().Get("w"))
		if err != nil {
			http.Error(writer, "invalid image width", http.StatusBadRequest)
			return
		}
		if _, ok := widths[width]; !ok {
			http.Error(writer, "unsupported image width", http.StatusBadRequest)
			return
		}
		quality := defaultQuality
		if raw := request.URL.Query().Get("q"); raw != "" {
			quality, err = strconv.Atoi(raw)
			if err != nil {
				http.Error(writer, "invalid image quality", http.StatusBadRequest)
				return
			}
		}
		quality = max(1, min(quality, 100))
		format := strings.ToLower(request.URL.Query().Get("f"))
		if format == "jpg" {
			format = "jpeg"
		}
		if format != "" && format != "jpeg" && format != "png" {
			http.Error(writer, "unsupported image format", http.StatusBadRequest)
			return
		}

		input, err := loader.Open(request.Context(), source)
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, ErrInvalidSource) {
				status = http.StatusBadRequest
			} else if errors.Is(err, ErrNotFound) || errors.Is(err, os.ErrNotExist) {
				status = http.StatusNotFound
			}
			http.Error(writer, http.StatusText(status), status)
			return
		}
		defer input.Close()
		data, err := io.ReadAll(io.LimitReader(input, maxSourceBytes+1))
		if err != nil {
			http.Error(writer, "read image source", http.StatusInternalServerError)
			return
		}
		if len(data) > maxSourceBytes {
			http.Error(writer, "image source too large", http.StatusRequestEntityTooLarge)
			return
		}
		output, contentType, err := optimize(data, width, quality, format)
		if err != nil {
			http.Error(writer, err.Error(), http.StatusUnsupportedMediaType)
			return
		}
		writer.Header().Set("Cache-Control", "public, max-age=3600")
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

func optimize(data []byte, width, quality int, requestedFormat string) ([]byte, string, error) {
	config, sourceFormat, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, "", errors.New("unsupported image source")
	}
	if config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > maxSourcePixels {
		return nil, "", errors.New("image dimensions are unsupported")
	}
	if sourceFormat != "jpeg" && sourceFormat != "png" {
		return nil, "", errors.New("image source must be JPEG or PNG")
	}
	height := max(1, int(math.Round(float64(config.Height)*float64(width)/float64(config.Width))))
	if int64(width)*int64(height) > maxSourcePixels {
		return nil, "", errors.New("output image dimensions are unsupported")
	}
	source, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", errors.New("decode image source")
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
