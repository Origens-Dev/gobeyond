package shared

import (
	"net/http"
	"strings"

	gb "github.com/Origens-Dev/gobeyond"
	"github.com/Origens-Dev/gobeyond/cache/openfromenv"
	gbmiddleware "github.com/Origens-Dev/gobeyond/middleware"
	gbruntime "github.com/Origens-Dev/gobeyond/runtime"
)

// WithPublicOrigin stores the configured origin for loaders that still read
// Values. Runtime also sets ctx.PublicOrigin.
func WithPublicOrigin(ctx *gb.PageContext, origin string) {
	if ctx.Values == nil {
		ctx.Values = map[string]any{}
	}
	ctx.Values[PublicOriginValue] = strings.TrimSuffix(origin, "/")
}

// Middleware returns request middleware for the generated site registry.
func Middleware() []gbmiddleware.Rule {
	return []gbmiddleware.Rule{{
		Name:   "legacy-article",
		Config: gb.MiddlewareConfig{Patterns: []string{"/articles/old-portable-react"}},
		Middleware: func(next gb.Handler) gb.Handler {
			return func(ctx *gb.RequestContext) (gb.Response, error) {
				return gb.Response{
					Status:  http.StatusPermanentRedirect,
					Headers: http.Header{"Location": {"/articles/portable-react"}},
				}, nil
			}
		},
	}}
}

// Wrap adds robots.txt / sitemap.xml outside the GoBeyond runtime.
func Wrap(next http.Handler, publicOrigin string) http.Handler {
	origin := strings.TrimSuffix(publicOrigin, "/")
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/robots.txt":
			writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
			writer.Header().Set("Cache-Control", "public, max-age=300")
			_, _ = writer.Write([]byte("User-agent: *\nAllow: /\nDisallow: /account\n\nSitemap: " + origin + "/sitemap.xml\n"))
		case "/sitemap.xml":
			writer.Header().Set("Content-Type", "application/xml; charset=utf-8")
			writer.Header().Set("Cache-Control", "public, max-age=300")
			_, _ = writer.Write([]byte(sitemapXML(origin)))
		default:
			next.ServeHTTP(writer, request)
		}
	})
}

// Configure attaches cache settings for packaged builds.
func Configure(cfg *gbruntime.Config) (func() error, error) {
	if cfg == nil || cfg.Static == nil {
		return nil, nil
	}
	cacheConfig, closeFn, err := openfromenv.OpenFromEnv()
	if err != nil {
		return nil, err
	}
	cfg.Cache = cacheConfig
	return closeFn, nil
}

func sitemapXML(origin string) string {
	paths := []string{"/", "/articles/portable-react", "/products/trail-pack", "/category/1", "/category/2", "/locations/seattle", "/en/articles/portable-react", "/fr/articles/react-portable"}
	var output strings.Builder
	output.WriteString(`<?xml version="1.0" encoding="UTF-8"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">`)
	for _, path := range paths {
		output.WriteString("<url><loc>")
		output.WriteString(origin)
		output.WriteString(path)
		output.WriteString("</loc></url>")
	}
	output.WriteString("</urlset>")
	return output.String()
}
