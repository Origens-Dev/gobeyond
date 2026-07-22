// Package articles_slug owns request-time props for /articles/[slug].
package articles_slug

import (
	gb "github.com/gobeyond-dev/gobeyond"
	contract "github.com/gobeyond-dev/gobeyond/examples/seo-site/internal/gobeyondgen/contracts/routes/r_articles_slug_c2f99372"
	"github.com/gobeyond-dev/gobeyond/examples/seo-site/internal/site"
	gbruntime "github.com/gobeyond-dev/gobeyond/runtime"
	"net/http"
	"time"
)

type Params struct{ Slug string }

func Page(ctx *gb.PageContext, params Params) (gbruntime.LoadedPage, error) {
	origin, err := shared.PublicOrigin(ctx)
	if err != nil {
		return gbruntime.LoadedPage{}, err
	}
	if params.Slug != "portable-react" {
		return gbruntime.LoadedPage{Kind: gb.ResultNotFound, Status: http.StatusNotFound, Cache: gb.CachePolicy{Mode: gb.CachePrivateNoStore}, Metadata: gb.Metadata{Lang: "en", Title: "Article not found"}, Props: contract.Props{Slug: params.Slug, Title: "Article not found", Description: "The requested article does not exist.", AuthorName: "GoBeyond", PublishedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), PublishedLabel: "January 1, 2026", Paragraphs: []string{}}}, nil
	}
	canonical := origin + "/articles/portable-react"
	props := contract.Props{Slug: "portable-react", Title: "Portable React rendered by Go", Description: "A crawler-visible React article with a Go runtime.", AuthorName: "GoBeyond contributors", PublishedAt: time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC), PublishedLabel: "July 22, 2026", Canonical: canonical, AlternateEnglish: origin + "/en/articles/portable-react", AlternateFrench: origin + "/fr/articles/react-portable", SocialImage: origin + "/social/article.svg", Paragraphs: []string{"React owns the website and Go owns request-time rendering.", "The browser hydrates the same semantic HTML."}}
	return gbruntime.LoadedPage{Kind: gb.ResultOK, Status: http.StatusOK, Cache: gb.CachePolicy{Mode: gb.CachePublic, MaxAge: 60}, Props: props, Metadata: shared.PublicMetadata("en", props.Title, props.Description, canonical, "article", props.SocialImage, gb.JSONLD{"@context": "https://schema.org", "@type": "Article", "headline": props.Title, "datePublished": "2026-07-22"})}, nil
}
