package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"sort"
)

var generatedIcons = []struct {
	name string
	size int
}{
	{name: "favicon-16x16.png", size: 16},
	{name: "favicon-32x32.png", size: 32},
	{name: "apple-touch-icon.png", size: 180},
}

func generateAppIcons(projectRoot, staticDir string) ([]string, error) {
	sourcePath := filepath.Join(projectRoot, "app", "icon.png")
	sourceFile, err := os.Open(sourcePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open app icon: %w", err)
	}
	defer sourceFile.Close()

	source, err := png.Decode(sourceFile)
	if err != nil {
		return nil, fmt.Errorf("decode app/icon.png: %w", err)
	}
	bounds := source.Bounds()
	if bounds.Dx() != bounds.Dy() || bounds.Dx() == 0 {
		return nil, fmt.Errorf("app/icon.png must be a non-empty square PNG")
	}

	paths := make([]string, 0, len(generatedIcons))
	for _, output := range generatedIcons {
		destination := filepath.Join(staticDir, output.name)
		file, createErr := os.Create(destination)
		if createErr != nil {
			return nil, fmt.Errorf("create generated icon %s: %w", output.name, createErr)
		}
		encodeErr := png.Encode(file, resizeSquare(source, output.size))
		closeErr := file.Close()
		if encodeErr != nil {
			return nil, fmt.Errorf("encode generated icon %s: %w", output.name, encodeErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("close generated icon %s: %w", output.name, closeErr)
		}
		paths = append(paths, "/"+output.name)
	}
	return paths, nil
}

func resizeSquare(source image.Image, size int) *image.NRGBA {
	target := image.NewNRGBA(image.Rect(0, 0, size, size))
	bounds := source.Bounds()
	scale := float64(bounds.Dx()) / float64(size)
	for y := 0; y < size; y++ {
		sourceY := (float64(y)+0.5)*scale - 0.5
		sourceY = math.Max(0, math.Min(sourceY, float64(bounds.Dy()-1)))
		y0 := int(math.Floor(sourceY))
		y1 := y0 + 1
		wy := sourceY - float64(y0)
		y0 = clamp(y0, 0, bounds.Dy()-1)
		y1 = clamp(y1, 0, bounds.Dy()-1)
		for x := 0; x < size; x++ {
			sourceX := (float64(x)+0.5)*scale - 0.5
			sourceX = math.Max(0, math.Min(sourceX, float64(bounds.Dx()-1)))
			x0 := int(math.Floor(sourceX))
			x1 := x0 + 1
			wx := sourceX - float64(x0)
			x0 = clamp(x0, 0, bounds.Dx()-1)
			x1 = clamp(x1, 0, bounds.Dx()-1)

			topLeft := color.NRGBAModel.Convert(source.At(bounds.Min.X+x0, bounds.Min.Y+y0)).(color.NRGBA)
			topRight := color.NRGBAModel.Convert(source.At(bounds.Min.X+x1, bounds.Min.Y+y0)).(color.NRGBA)
			bottomLeft := color.NRGBAModel.Convert(source.At(bounds.Min.X+x0, bounds.Min.Y+y1)).(color.NRGBA)
			bottomRight := color.NRGBAModel.Convert(source.At(bounds.Min.X+x1, bounds.Min.Y+y1)).(color.NRGBA)
			target.SetNRGBA(x, y, color.NRGBA{
				R: interpolate(topLeft.R, topRight.R, bottomLeft.R, bottomRight.R, wx, wy),
				G: interpolate(topLeft.G, topRight.G, bottomLeft.G, bottomRight.G, wx, wy),
				B: interpolate(topLeft.B, topRight.B, bottomLeft.B, bottomRight.B, wx, wy),
				A: interpolate(topLeft.A, topRight.A, bottomLeft.A, bottomRight.A, wx, wy),
			})
		}
	}
	return target
}

func interpolate(topLeft, topRight, bottomLeft, bottomRight uint8, x, y float64) uint8 {
	top := float64(topLeft)*(1-x) + float64(topRight)*x
	bottom := float64(bottomLeft)*(1-x) + float64(bottomRight)*x
	return uint8(math.Round(top*(1-y) + bottom*y))
}

func clamp(value, minimum, maximum int) int {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}

func mergeAssetPaths(groups ...[]string) []string {
	seen := make(map[string]struct{})
	var merged []string
	for _, group := range groups {
		for _, path := range group {
			if _, exists := seen[path]; exists {
				continue
			}
			seen[path] = struct{}{}
			merged = append(merged, path)
		}
	}
	sort.Strings(merged)
	return merged
}
