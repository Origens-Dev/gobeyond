// Package category_page owns request-time props for /category/[page].
package category_page

import (
	"net/http"

	gb "github.com/Origens-Dev/gobeyond"
	contract "github.com/Origens-Dev/gobeyond/examples/seo-site/internal/gobeyondgen/contracts/routes/r_category_page_05ecbf63"
	"github.com/Origens-Dev/gobeyond/examples/seo-site/internal/site"
	gbruntime "github.com/Origens-Dev/gobeyond/runtime"
)

type Params struct{ Page string }

func Page(ctx *gb.PageContext, params Params) (gbruntime.LoadedPage, error) {
	origin, err := shared.PublicOrigin(ctx)
	if err != nil {
		return gbruntime.LoadedPage{}, err
	}
	if params.Page != "1" && params.Page != "2" {
		return gbruntime.LoadedPage{Kind: gb.ResultNotFound, Status: http.StatusNotFound, Cache: gb.CachePolicy{Mode: gb.CachePrivateNoStore}, Metadata: gb.Metadata{Lang: "en", Title: "Category page not found"}, Props: contract.Props{Items: []contract.PropsItemsItem{}}}, nil
	}
	canonical := origin + "/category/" + params.Page
	currentPage := int64(1)
	items := []contract.PropsItemsItem{{Href: "/articles/portable-react", Name: "Portable React rendered by Go", Summary: "A crawler-visible React article with a Go runtime."}}
	var previous, next *string
	if params.Page == "1" {
		value := "/category/2"
		next = &value
	} else {
		currentPage = 2
		items = []contract.PropsItemsItem{{Href: "/products/trail-pack", Name: "Trail Pack", Summary: "A durable pack for portable React."}}
		value := "/category/1"
		previous = &value
	}
	return gbruntime.LoadedPage{Kind: gb.ResultOK, Status: http.StatusOK, Cache: gb.CachePolicy{Mode: gb.CachePublic, MaxAge: 60}, Props: contract.Props{CurrentPage: currentPage, Canonical: canonical, Items: items, PreviousHref: previous, NextHref: next}, Metadata: shared.PublicMetadata("en", "Field notes · page "+params.Page, "Browse field notes on page "+params.Page, canonical, "website", origin+"/social/category.svg", gb.JSONLD{"@context": "https://schema.org", "@type": "CollectionPage", "name": "Field notes · page " + params.Page, "url": canonical})}, nil
}
