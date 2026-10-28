package project

import (
	"fmt"
	goversion "go/version"
)

// MinimumGoVersion follows the public module's required language version.
// The qualified build compiler is pinned by go.mod's toolchain directive.
const MinimumGoVersion = "1.26.0"

func runtimeModuleGoVersion(requested string) (string, error) {
	if !goversion.IsValid("go" + requested) {
		return "", fmt.Errorf("invalid project Go version %q", requested)
	}
	if goversion.Compare("go"+requested, "go"+MinimumGoVersion) < 0 {
		return MinimumGoVersion, nil
	}
	return requested, nil
}
