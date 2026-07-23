// Package products_slug implements actions declared by the product route.
package products_slug

import (
	"errors"

	gb "github.com/Origens-Dev/gobeyond"
	contract "github.com/Origens-Dev/gobeyond/examples/seo-site/internal/gobeyondgen/contracts/actions/r_products_slug_3e2e8eb9_add_to_cart"
)

func AddToCart(_ *gb.ActionContext, input contract.Input) (contract.Output, error) {
	if input.ProductSlug == "" || input.Quantity < 1 || input.Quantity > 20 {
		return contract.Output{}, errors.New("productSlug and a quantity from 1 to 20 are required")
	}
	return contract.Output{Saved: true, CartItemCount: input.Quantity}, nil
}
