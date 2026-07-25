package renderer

import (
	"strings"
	"testing"

	"github.com/Origens-Dev/gobeyond/renderplan"
)

func TestWithNavigationSeedsPathnameLocals(t *testing.T) {
	plan := &renderplan.Plan{
		APIVersion: renderplan.APIVersionV1Alpha1,
		RouteID:    "nav",
		Root: &renderplan.Text{
			Kind: "text",
			Value: &renderplan.Path{
				Kind: "path",
				Path: []renderplan.PathSegment{
					renderplan.Property("__gobeyond"),
					renderplan.Property("pathname"),
				},
			},
		},
	}
	html, err := New().WithNavigation(NavigationMeta{
		RouteID:  "nav",
		Pathname: "/products/widget",
	}).Render(plan, map[string]any{"title": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(html, "/products/widget") {
		t.Fatalf("expected pathname in output, got %q", html)
	}
}
