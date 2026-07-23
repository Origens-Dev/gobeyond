package middleware

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	gb "github.com/holbrookab/gobeyond"
)

func TestChainAppliesMatchingRulesInOrder(t *testing.T) {
	var calls []string
	wrap := func(name string) gb.Middleware {
		return func(next gb.Handler) gb.Handler {
			return func(ctx *gb.RequestContext) (gb.Response, error) {
				calls = append(calls, name+":before")
				response, err := next(ctx)
				calls = append(calls, name+":after")
				return response, err
			}
		}
	}
	handler, err := Chain([]Rule{
		{Name: "all", Middleware: wrap("all")},
		{Name: "products", Config: gb.MiddlewareConfig{Patterns: []string{"/products/[slug]"}}, Middleware: wrap("products")},
		{Name: "post", Config: gb.MiddlewareConfig{Methods: []string{http.MethodPost}}, Middleware: wrap("post")},
	}, func(*gb.RequestContext) (gb.Response, error) {
		calls = append(calls, "handler")
		return gb.Response{Status: http.StatusOK}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/products/widget", nil)
	if _, err := handler(&gb.RequestContext{Request: request}); err != nil {
		t.Fatal(err)
	}
	want := []string{"all:before", "products:before", "handler", "products:after", "all:after"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}
