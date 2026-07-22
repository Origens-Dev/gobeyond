// Package locations_slug owns request-time props for /locations/[slug].
package locations_slug

import (
	gb "github.com/gobeyond-dev/gobeyond"
	contract "github.com/gobeyond-dev/gobeyond/examples/seo-site/internal/gobeyondgen/contracts/routes/r_locations_slug_730658f7"
	"github.com/gobeyond-dev/gobeyond/examples/seo-site/internal/site"
	gbruntime "github.com/gobeyond-dev/gobeyond/runtime"
	"net/http"
)

type Params struct{ Slug string }

func Page(ctx *gb.PageContext, params Params) (gbruntime.LoadedPage, error) {
	origin, err := shared.PublicOrigin(ctx)
	if err != nil {
		return gbruntime.LoadedPage{}, err
	}
	if params.Slug != "seattle" {
		return gbruntime.LoadedPage{Kind: gb.ResultNotFound, Status: http.StatusNotFound, Cache: gb.CachePolicy{Mode: gb.CachePrivateNoStore}, Metadata: gb.Metadata{Lang: "en", Title: "Location not found"}, Props: contract.Props{Hours: []string{}}}, nil
	}
	canonical := origin + "/locations/seattle"
	props := contract.Props{Name: "GoBeyond Seattle", Description: "A field office for portable web architecture.", Canonical: canonical, StreetAddress: "500 Pine Street", Locality: "Seattle", Region: "WA", PostalCode: "98101", Phone: "+1 206 555 0100", PhoneHref: "tel:+12065550100", Hours: []string{"Monday–Friday: 09:00–17:00"}, MapHref: "https://maps.google.com/?q=500+Pine+Street+Seattle"}
	return gbruntime.LoadedPage{Kind: gb.ResultOK, Status: http.StatusOK, Cache: gb.CachePolicy{Mode: gb.CachePublic, MaxAge: 300}, Props: props, Metadata: shared.PublicMetadata("en", props.Name, props.Description, canonical, "website", origin+"/social/seattle.svg", gb.JSONLD{"@context": "https://schema.org", "@type": "LocalBusiness", "name": props.Name, "description": props.Description, "url": canonical, "telephone": props.Phone, "address": map[string]any{"@type": "PostalAddress", "streetAddress": props.StreetAddress, "addressLocality": props.Locality, "addressRegion": props.Region, "postalCode": props.PostalCode}})}, nil
}
