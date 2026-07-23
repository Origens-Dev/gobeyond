// Package seosite wires the website-first React fixture to GoBeyond's Go runtime.
package seosite

import (
	"errors"
	"net/http"
	"strings"

	gb "github.com/holbrookab/gobeyond"
	"github.com/holbrookab/gobeyond/browserassets"
	apitime "github.com/holbrookab/gobeyond/examples/seo-site/internal/gobeyondgen/api/r_api_time_066a4b03"
	actioncontract "github.com/holbrookab/gobeyond/examples/seo-site/internal/gobeyondgen/contracts/actions/r_products_slug_3e2e8eb9_add_to_cart"
	generatedroutes "github.com/holbrookab/gobeyond/examples/seo-site/internal/gobeyondgen/routes"
	pageaccount "github.com/holbrookab/gobeyond/examples/seo-site/internal/gobeyondgen/routes/r_account_441bb226"
	pagearticle "github.com/holbrookab/gobeyond/examples/seo-site/internal/gobeyondgen/routes/r_articles__slug_c2f99372"
	pagecategory "github.com/holbrookab/gobeyond/examples/seo-site/internal/gobeyondgen/routes/r_category__page_05ecbf63"
	pageenglish "github.com/holbrookab/gobeyond/examples/seo-site/internal/gobeyondgen/routes/r_en_articles__slug_2da8a82a"
	pagefrench "github.com/holbrookab/gobeyond/examples/seo-site/internal/gobeyondgen/routes/r_fr_articles__slug_fee2939e"
	pagelocation "github.com/holbrookab/gobeyond/examples/seo-site/internal/gobeyondgen/routes/r_locations__slug_730658f7"
	productroute "github.com/holbrookab/gobeyond/examples/seo-site/internal/gobeyondgen/routes/r_products__slug_3e2e8eb9"
	"github.com/holbrookab/gobeyond/examples/seo-site/internal/site"
	gbmiddleware "github.com/holbrookab/gobeyond/middleware"
	"github.com/holbrookab/gobeyond/renderplan"
	"github.com/holbrookab/gobeyond/router"
	gbruntime "github.com/holbrookab/gobeyond/runtime"
)

const (
	HomeRouteID           = generatedroutes.RouteRoot
	AccountRouteID        = generatedroutes.RouteAccount
	ArticleRouteID        = generatedroutes.RouteArticlesSlug
	CategoryRouteID       = generatedroutes.RouteCategoryPage
	EnglishArticleRouteID = generatedroutes.RouteEnArticlesSlug
	FrenchArticleRouteID  = generatedroutes.RouteFrArticlesSlug
	LocationRouteID       = generatedroutes.RouteLocationsSlug
	ProductRouteID        = generatedroutes.RouteProductsSlug
)

// Site is the live acceptance-site handler. The wrapper owns the two crawler
// control documents which are normally served from the static CDN, while the
// embedded runtime owns documents, actions, and APIs.
type Site struct {
	runtime      *gbruntime.Server
	publicOrigin string
}

// AssetConfig is emitted by gobeyond build and loaded by the production
// entrypoint. Keeping it outside the render plan lets Vite retain
// content-hashed stylesheet names while every dynamic document links the
// exact same browser assets as a static document from the build.
type AssetConfig struct {
	ClientScript string   `json:"clientScript"`
	Styles       []string `json:"styles"`
}

func (s *Site) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	switch request.URL.Path {
	case "/robots.txt":
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		writer.Header().Set("Cache-Control", "public, max-age=300")
		_, _ = writer.Write([]byte("User-agent: *\nAllow: /\nDisallow: /account\n\nSitemap: " + s.publicOrigin + "/sitemap.xml\n"))
	case "/sitemap.xml":
		writer.Header().Set("Content-Type", "application/xml; charset=utf-8")
		writer.Header().Set("Cache-Control", "public, max-age=300")
		_, _ = writer.Write([]byte(sitemapXML(s.publicOrigin)))
	default:
		s.runtime.ServeHTTP(writer, request)
	}
}

func New(buildID, publicOrigin string, plans map[string]*renderplan.Plan) (*Site, error) {
	return NewWithAssets(buildID, publicOrigin, plans, AssetConfig{
		ClientScript: "/_gobeyond/assets/" + buildID + "/app.js",
		Styles:       []string{},
	})
}

// NewWithAssets creates the acceptance site with the browser assets emitted
// by the same build. Tests may continue to use New when they do not bundle CSS.
func NewWithAssets(buildID, publicOrigin string, plans map[string]*renderplan.Plan, assets AssetConfig) (*Site, error) {
	return newWithStaticLoader(buildID, publicOrigin, plans, nil, assets, func(*gb.PageContext) (gbruntime.LoadedPage, error) {
		return *homePage(publicOrigin), nil
	})
}

// NewWithStaticStore uses route-aware browser assets when the build emitted a
// runtime manifest. A nil manifest is the one-release compatibility path for
// older builds and uses the legacy page-level asset fields.
func NewWithStaticStore(buildID, publicOrigin string, plans map[string]*renderplan.Plan, assets *browserassets.Manifest, staticStore *gbruntime.StaticStore) (*Site, error) {
	if staticStore == nil {
		return nil, errors.New("SEO site requires packaged static data")
	}
	legacyAssets := AssetConfig{}
	if assets == nil {
		legacyAssets = AssetConfig{ClientScript: "/_gobeyond/assets/" + buildID + "/app.js", Styles: []string{}}
	}
	return newWithStaticLoader(buildID, publicOrigin, plans, assets, legacyAssets, staticStore.Loader(HomeRouteID))
}

func newWithStaticLoader(buildID, publicOrigin string, plans map[string]*renderplan.Plan, browserAssets *browserassets.Manifest, legacyAssets AssetConfig, homeLoader gbruntime.PageLoader) (*Site, error) {
	required := []string{
		HomeRouteID, AccountRouteID, ArticleRouteID, CategoryRouteID,
		EnglishArticleRouteID, FrenchArticleRouteID, LocationRouteID, ProductRouteID,
	}
	for _, routeID := range required {
		if plans[routeID] == nil {
			return nil, errors.New("SEO site requires render plan " + routeID)
		}
	}
	runtime, err := gbruntime.New(gbruntime.Config{
		BuildID:       buildID,
		PublicOrigin:  publicOrigin,
		BrowserAssets: browserAssets,
		Pages: []gbruntime.PageRoute{
			{
				Route:        router.Route{ID: HomeRouteID, Pattern: "/", Mode: router.ModeStatic},
				Plan:         plans[HomeRouteID],
				Load:         homeLoader,
				Indexable:    true,
				ClientScript: legacyAssets.ClientScript,
				Styles:       legacyAssets.Styles,
			},
			{
				Route: router.Route{ID: AccountRouteID, Pattern: "/account", Mode: router.ModeDynamic},
				Plan:  plans[AccountRouteID],
				Load: func(ctx *gb.PageContext) (gbruntime.LoadedPage, error) {
					withPublicOrigin(ctx, publicOrigin)
					return pageaccount.Page(ctx, pageaccount.Params{})
				},
				Indexable:    false,
				ClientScript: legacyAssets.ClientScript,
				Styles:       legacyAssets.Styles,
			},
			{
				Route: router.Route{ID: ArticleRouteID, Pattern: "/articles/[slug]", Mode: router.ModeDynamic},
				Plan:  plans[ArticleRouteID],
				Load: func(ctx *gb.PageContext) (gbruntime.LoadedPage, error) {
					withPublicOrigin(ctx, publicOrigin)
					return pagearticle.Page(ctx, pagearticle.Params{Slug: ctx.Params["slug"]})
				},
				Indexable:    true,
				ClientScript: legacyAssets.ClientScript,
				Styles:       legacyAssets.Styles,
			},
			{
				Route: router.Route{ID: CategoryRouteID, Pattern: "/category/[page]", Mode: router.ModeDynamic},
				Plan:  plans[CategoryRouteID],
				Load: func(ctx *gb.PageContext) (gbruntime.LoadedPage, error) {
					withPublicOrigin(ctx, publicOrigin)
					return pagecategory.Page(ctx, pagecategory.Params{Page: ctx.Params["page"]})
				},
				Indexable:    true,
				ClientScript: legacyAssets.ClientScript,
				Styles:       legacyAssets.Styles,
			},
			{
				Route: router.Route{ID: EnglishArticleRouteID, Pattern: "/en/articles/[slug]", Mode: router.ModeDynamic},
				Plan:  plans[EnglishArticleRouteID],
				Load: func(ctx *gb.PageContext) (gbruntime.LoadedPage, error) {
					withPublicOrigin(ctx, publicOrigin)
					return pageenglish.Page(ctx, pageenglish.Params{Slug: ctx.Params["slug"]})
				},
				Indexable:    true,
				ClientScript: legacyAssets.ClientScript,
				Styles:       legacyAssets.Styles,
			},
			{
				Route: router.Route{ID: FrenchArticleRouteID, Pattern: "/fr/articles/[slug]", Mode: router.ModeDynamic},
				Plan:  plans[FrenchArticleRouteID],
				Load: func(ctx *gb.PageContext) (gbruntime.LoadedPage, error) {
					withPublicOrigin(ctx, publicOrigin)
					return pagefrench.Page(ctx, pagefrench.Params{Slug: ctx.Params["slug"]})
				},
				Indexable:    true,
				ClientScript: legacyAssets.ClientScript,
				Styles:       legacyAssets.Styles,
			},
			{
				Route: router.Route{ID: LocationRouteID, Pattern: "/locations/[slug]", Mode: router.ModeDynamic},
				Plan:  plans[LocationRouteID],
				Load: func(ctx *gb.PageContext) (gbruntime.LoadedPage, error) {
					withPublicOrigin(ctx, publicOrigin)
					return pagelocation.Page(ctx, pagelocation.Params{Slug: ctx.Params["slug"]})
				},
				Indexable:    true,
				ClientScript: legacyAssets.ClientScript,
				Styles:       legacyAssets.Styles,
			},
			{
				Route: router.Route{ID: ProductRouteID, Pattern: "/products/[slug]", Mode: router.ModeDynamic},
				Plan:  plans[ProductRouteID],
				Load: func(ctx *gb.PageContext) (gbruntime.LoadedPage, error) {
					withPublicOrigin(ctx, publicOrigin)
					return productroute.Page(ctx, productroute.Params{Slug: ctx.Params["slug"]})
				},
				Indexable:    true,
				ClientScript: legacyAssets.ClientScript,
				Styles:       legacyAssets.Styles,
			},
		},
		Actions: []gbruntime.Action{actioncontract.Register(productroute.AddToCart)},
		APIs: []gbruntime.APIRoute{{
			Route: router.Route{ID: "api_time", Pattern: "/api/time", Mode: router.ModeAPI},
			Methods: map[string]gb.Handler{
				http.MethodGet: apitime.GET,
			},
		}},
		Middleware: []gbmiddleware.Rule{{
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
		}},
	})
	if err != nil {
		return nil, err
	}
	return &Site{runtime: runtime, publicOrigin: strings.TrimSuffix(publicOrigin, "/")}, nil
}

func homePage(origin string) *gbruntime.LoadedPage {
	canonical := origin + "/"
	return &gbruntime.LoadedPage{
		Kind: gb.ResultOK, Status: http.StatusOK, Cache: gb.CachePolicy{Mode: gb.CachePublic, MaxAge: 300},
		Props: map[string]any{"featuredArticleHref": "/articles/portable-react", "featuredProductHref": "/products/trail-pack"},
		Metadata: shared.PublicMetadata("en", "GoBeyond Field Guide", "Practical notes and equipment for building beyond the usual path.", canonical, "website", origin+"/social/home.svg", gb.JSONLD{
			"@context": "https://schema.org", "@type": "WebSite", "name": "GoBeyond Field Guide", "url": canonical,
		}),
	}
}

func withPublicOrigin(ctx *gb.PageContext, origin string) {
	if ctx.Values == nil {
		ctx.Values = map[string]any{}
	}
	ctx.Values[shared.PublicOriginValue] = strings.TrimSuffix(origin, "/")
}

func sitemapXML(origin string) string {
	paths := []string{"/", "/articles/portable-react", "/products/trail-pack", "/category/1", "/category/2", "/locations/seattle", "/en/articles/portable-react", "/fr/articles/react-portable"}
	var output strings.Builder
	output.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?><urlset xmlns=\"http://www.sitemaps.org/schemas/sitemap/0.9\">")
	for _, path := range paths {
		output.WriteString("<url><loc>")
		output.WriteString(origin)
		output.WriteString(path)
		output.WriteString("</loc></url>")
	}
	output.WriteString("</urlset>")
	return output.String()
}
