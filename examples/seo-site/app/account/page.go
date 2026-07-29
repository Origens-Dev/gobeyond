// Package account owns request-time props for /account.
package account

import (
	"net/http"
	"strings"

	gb "github.com/Origens-Dev/gobeyond"
	contract "github.com/Origens-Dev/gobeyond/examples/seo-site/.generated/contracts/routes/r_account_441bb226"
	"github.com/Origens-Dev/gobeyond/examples/seo-site/internal/site"
	gbruntime "github.com/Origens-Dev/gobeyond/runtime"
)

type Params struct{}

var Indexable = false

type Props struct {
	DisplayName string `json:"displayName"`
}

func Page(ctx *gb.PageContext, _ Params) (gbruntime.LoadedPage, error) {
	origin, err := shared.PublicOrigin(ctx)
	if err != nil {
		return gbruntime.LoadedPage{}, err
	}
	name := "Guest"
	if cookie, cookieErr := ctx.Request.Cookie("gobeyond_account"); cookieErr == nil && strings.TrimSpace(cookie.Value) != "" {
		name = cookie.Value
	}
	return gbruntime.LoadedPage{
		Kind: gb.ResultOK, Status: http.StatusOK, Cache: gb.CachePolicy{Mode: gb.CachePrivateNoStore},
		Props:    contract.Props{DisplayName: name},
		Metadata: gb.Metadata{Lang: "en", Title: "Your account", Description: "Private GoBeyond account", Canonical: origin + "/account", Robots: "noindex, nofollow"},
	}, nil
}
