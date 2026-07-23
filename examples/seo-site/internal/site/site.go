// Package shared contains the runtime-independent policy shared by the
// example's typed page loaders. Each loader receives the configured public
// origin through its PageContext; it never trusts the request Host header.
package shared

import (
	"errors"

	gb "github.com/holbrookab/gobeyond"
)

const PublicOriginValue = "gobeyond.public_origin"

func PublicOrigin(ctx *gb.PageContext) (string, error) {
	origin, ok := ctx.Values[PublicOriginValue].(string)
	if !ok || origin == "" {
		return "", errors.New("configured public origin is missing from page context")
	}
	return origin, nil
}

func PublicMetadata(lang, title, description, canonical, kind, image string, jsonLD gb.JSONLD, alternates ...gb.Alternate) gb.Metadata {
	return gb.Metadata{
		Lang: lang, Title: title, Description: description, Canonical: canonical, Robots: "index, follow", Alternates: alternates,
		OpenGraph: gb.OpenGraph{Type: kind, Title: title, Description: description, URL: canonical, Images: []string{image}},
		Twitter:   gb.Twitter{Card: "summary_large_image", Title: title, Description: description, Images: []string{image}},
		JSONLD:    []gb.JSONLD{jsonLD},
	}
}
