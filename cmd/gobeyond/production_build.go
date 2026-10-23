package main

import (
	"debug/elf"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const productionBuildTag = "nodynamic"

// All production entry points share these flags. CGO_ENABLED=0 alone does not
// exclude purego's cgo_import_dynamic directives and their ELF interpreter.
func buildProductionExecutable(root string, environment []string, output, target string) error {
	environment = withEnvironment(environment, "CGO_ENABLED=0", "GOFLAGS=")
	if err := runCommandWithEnvironment(root, environment, "go", "build", "-tags="+productionBuildTag, "-trimpath", "-ldflags=-s -w", "-o", output, target); err != nil {
		return err
	}
	if productionTargetOS(environment) != "linux" {
		return nil
	}
	if err := verifyStaticELF(output); err != nil {
		_ = os.Remove(output)
		return err
	}
	return nil
}

func productionTargetOS(environment []string) string {
	goos := runtime.GOOS
	for _, value := range environment {
		if strings.HasPrefix(value, "GOOS=") {
			goos = strings.TrimPrefix(value, "GOOS=")
			if goos == "" {
				goos = runtime.GOOS
			}
		}
	}
	return goos
}

func verifyStaticELF(path string) error {
	binary, err := elf.Open(path)
	if err != nil {
		return fmt.Errorf("production executable is not a valid ELF: %w", err)
	}
	defer binary.Close()
	for _, program := range binary.Progs {
		if program.Type == elf.PT_INTERP {
			return fmt.Errorf("production executable %s requires a dynamic interpreter", path)
		}
	}
	libraries, err := binary.ImportedLibraries()
	if err != nil {
		return fmt.Errorf("inspect production executable dependencies: %w", err)
	}
	if len(libraries) != 0 {
		return fmt.Errorf("production executable %s requires dynamic libraries", path)
	}
	return nil
}

// Recheck the complete existing output tree before declaring artifacts ready.
// This covers worker outputs copied/restored into dist alongside fresh output.
func verifyProductionArtifacts(environment []string, dist string) error {
	if productionTargetOS(environment) != "linux" {
		return nil
	}
	paths := []string{filepath.Join(dist, "server", "gobeyond-server")}
	workers, err := filepath.Glob(filepath.Join(dist, "workers", "*", "gobeyond-worker"))
	if err != nil {
		return err
	}
	paths = append(paths, workers...)
	for _, path := range paths {
		if err := verifyStaticELF(path); err != nil {
			return err
		}
	}
	return nil
}
