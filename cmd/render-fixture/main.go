// Command render-fixture is a test-only bridge used by cross-language
// hydration conformance tests. It is not part of the production server.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/holbrookab/gobeyond/renderer"
	"github.com/holbrookab/gobeyond/renderplan"
)

func main() {
	var input struct {
		Plan  json.RawMessage `json:"plan"`
		Props any             `json:"props"`
	}
	if err := json.NewDecoder(os.Stdin).Decode(&input); err != nil {
		fmt.Fprintln(os.Stderr, "decode input:", err)
		os.Exit(1)
	}
	plan, err := renderplan.Parse(input.Plan)
	if err != nil {
		fmt.Fprintln(os.Stderr, "decode plan:", err)
		os.Exit(1)
	}
	output, err := renderer.Render(plan, input.Props)
	if err != nil {
		fmt.Fprintln(os.Stderr, "render:", err)
		os.Exit(1)
	}
	fmt.Print(output)
}
