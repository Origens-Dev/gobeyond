// Package products_slug owns request-time props for /products/[slug].
package products_slug

import (
	gb "github.com/holbrookab/gobeyond"
	contract "github.com/holbrookab/gobeyond/examples/seo-site/internal/gobeyondgen/contracts/routes/r_products_slug_3e2e8eb9"
	"github.com/holbrookab/gobeyond/examples/seo-site/internal/site"
	gbruntime "github.com/holbrookab/gobeyond/runtime"
	"net/http"
)

type Params struct{ Slug string }

func Page(ctx *gb.PageContext, params Params) (gbruntime.LoadedPage, error) {
	origin, err := shared.PublicOrigin(ctx)
	if err != nil {
		return gbruntime.LoadedPage{}, err
	}
	if params.Slug != "trail-pack" {
		return gbruntime.LoadedPage{Kind: gb.ResultNotFound, Status: http.StatusNotFound, Cache: gb.CachePolicy{Mode: gb.CachePrivateNoStore}, Metadata: gb.Metadata{Lang: "en", Title: "Product not found"}, Props: contract.Props{Slug: params.Slug, Name: "Product not found", Image: "/missing.jpg", Currency: "USD", Availability: contract.PropsAvailabilityOutOfStock}}, nil
	}
	canonical := origin + "/products/trail-pack"
	props := contract.Props{Slug: "trail-pack", Name: "Trail Pack", Description: "A durable pack for portable React.", Canonical: canonical, Image: origin + "/images/trail-pack.svg", ImageAlt: "Blue Trail Pack", Price: 129, PriceLabel: "$129.00", Currency: "USD", Availability: contract.PropsAvailabilityInStock}
	return gbruntime.LoadedPage{Kind: gb.ResultOK, Status: http.StatusOK, Cache: gb.CachePolicy{Mode: gb.CachePublic, MaxAge: 60}, Props: props, Metadata: shared.PublicMetadata("en", "Trail Pack", "A durable pack for portable React.", canonical, "product", origin+"/images/trail-pack.svg", gb.JSONLD{"@context": "https://schema.org", "@type": "Product", "name": "Trail Pack", "image": origin + "/images/trail-pack.svg", "offers": map[string]any{"@type": "Offer", "price": 129, "priceCurrency": "USD", "availability": "https://schema.org/InStock"}})}, nil
}
