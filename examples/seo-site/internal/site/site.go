// Package shared holds app helpers used by typed page loaders.
// It is ordinary developer code under internal/, not a gobeyond hook surface.
package shared

import (
	"errors"
	"strings"

	gb "github.com/Origens-Dev/gobeyond"
)

func PublicOrigin(ctx *gb.PageContext) (string, error) {
	if ctx == nil || strings.TrimSpace(ctx.PublicOrigin) == "" {
		return "", errors.New("configured public origin is missing from page context")
	}
	return strings.TrimSuffix(ctx.PublicOrigin, "/"), nil
}

func PublicMetadata(lang, title, description, canonical, kind, image string, jsonLD gb.JSONLD, alternates ...gb.Alternate) gb.Metadata {
	return gb.Metadata{
		Lang: lang, Title: title, Description: description, Canonical: canonical, Robots: "index, follow", Alternates: alternates,
		OpenGraph: gb.OpenGraph{Type: kind, Title: title, Description: description, URL: canonical, Images: []string{image}},
		Twitter:   gb.Twitter{Card: "summary_large_image", Title: title, Description: description, Images: []string{image}},
		JSONLD:    []gb.JSONLD{jsonLD},
	}
}
