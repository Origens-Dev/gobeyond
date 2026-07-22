// Package account owns request-time props for /account.
package account

import (
	"net/http"
	"strings"

	gb "github.com/gobeyond-dev/gobeyond"
	contract "github.com/gobeyond-dev/gobeyond/examples/seo-site/internal/gobeyondgen/contracts/routes/r_account_441bb226"
	"github.com/gobeyond-dev/gobeyond/examples/seo-site/internal/site"
	gbruntime "github.com/gobeyond-dev/gobeyond/runtime"
)

type Params struct{}

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
