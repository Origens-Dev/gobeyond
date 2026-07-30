// Package durables supplies request-time props for app/durables/page.tsx.
package durables

import (
	gb "github.com/Origens-Dev/gobeyond"
)

// Local Temporal dogfood — not meant for public indexing.
var Indexable = false

// Props is the JSON payload passed from this Go loader to React.
type Props struct{}

// Page loads the durables demo page.
func Page(_ *gb.PageContext) (gb.PageResult[Props], error) {
	return gb.OK(Props{}, gb.Metadata{
		Lang:  "en",
		Title: "Durables",
	}), nil
}
