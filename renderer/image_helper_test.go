package renderer

import (
	"testing"

	"github.com/Origens-Dev/gobeyond/renderplan"
)

func TestImageSrcHelperMatchesReactHelper(t *testing.T) {
	expression := &renderplan.Helper{
		Kind: "helper",
		Name: "imageSrc",
		Arguments: []renderplan.Expression{
			&renderplan.Literal{Kind: "literal", Value: "/brand/logo mark.png"},
			&renderplan.Literal{Kind: "literal", Value: map[string]any{"w": 640, "q": 82, "f": "png"}},
		},
	}
	value, err := evalHelper(expression, environment{}, "image")
	if err != nil {
		t.Fatal(err)
	}
	const want = "/_gobeyond/image?url=%2Fbrand%2Flogo+mark.png&w=640&q=82&f=png"
	if value != want {
		t.Fatalf("imageSrc = %q, want %q", value, want)
	}
}

func TestImageSrcHelperDefaultsQuality(t *testing.T) {
	expression := &renderplan.Helper{
		Kind: "helper",
		Name: "imageSrc",
		Arguments: []renderplan.Expression{
			&renderplan.Literal{Kind: "literal", Value: "/brand/logo.png"},
			&renderplan.Literal{Kind: "literal", Value: map[string]any{"w": 32}},
		},
	}
	value, err := evalHelper(expression, environment{}, "image")
	if err != nil {
		t.Fatal(err)
	}
	const want = "/_gobeyond/image?url=%2Fbrand%2Flogo.png&w=32&q=75"
	if value != want {
		t.Fatalf("imageSrc = %q, want %q", value, want)
	}
}
