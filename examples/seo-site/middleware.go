// Optional request middleware for the generated site registry.
// Patterns stay product-scoped so unrelated static pages remain static.
package middleware

import (
	"net/http"

	gb "github.com/Origens-Dev/gobeyond"
	gbmiddleware "github.com/Origens-Dev/gobeyond/middleware"
)

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
