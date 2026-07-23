// Package fr_articles_slug owns request-time props for /fr/articles/[slug].
package fr_articles_slug

import (
	gb "github.com/holbrookab/gobeyond"
	contract "github.com/holbrookab/gobeyond/examples/seo-site/internal/gobeyondgen/contracts/routes/r_fr_articles_slug_fee2939e"
	"github.com/holbrookab/gobeyond/examples/seo-site/internal/site"
	gbruntime "github.com/holbrookab/gobeyond/runtime"
	"net/http"
	"time"
)

type Params struct{ Slug string }

func Page(ctx *gb.PageContext, params Params) (gbruntime.LoadedPage, error) {
	origin, err := shared.PublicOrigin(ctx)
	if err != nil {
		return gbruntime.LoadedPage{}, err
	}
	if params.Slug != "react-portable" {
		return gbruntime.LoadedPage{Kind: gb.ResultNotFound, Status: http.StatusNotFound, Cache: gb.CachePolicy{Mode: gb.CachePrivateNoStore}, Metadata: gb.Metadata{Lang: "fr", Title: "Article introuvable"}, Props: contract.Props{Slug: params.Slug, Title: "Article introuvable", Paragraphs: []string{}}}, nil
	}
	canonical := origin + "/fr/articles/react-portable"
	english, french := origin+"/en/articles/portable-react", canonical
	props := contract.Props{Slug: params.Slug, Title: "React portable rendu par Go", Description: "Un article React visible par les robots avec une exécution Go.", AuthorName: "Contributeurs GoBeyond", PublishedAt: time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC), PublishedLabel: "22 juillet 2026", Canonical: canonical, AlternateEnglish: english, AlternateFrench: french, SocialImage: origin + "/social/article.svg", Paragraphs: []string{"React possède le site et Go gère le rendu à la demande.", "Le navigateur hydrate le même HTML sémantique."}}
	return gbruntime.LoadedPage{Kind: gb.ResultOK, Status: http.StatusOK, Cache: gb.CachePolicy{Mode: gb.CachePublic, MaxAge: 60}, Props: props, Metadata: shared.PublicMetadata("fr", props.Title, props.Description, canonical, "article", props.SocialImage, gb.JSONLD{"@context": "https://schema.org", "@type": "Article", "headline": props.Title, "datePublished": "2026-07-22", "inLanguage": "fr"}, gb.Alternate{Language: "en", URL: english}, gb.Alternate{Language: "fr", URL: french})}, nil
}
