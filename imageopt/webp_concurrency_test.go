package imageopt

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"sync"
	"testing"
)

func TestWebPConcurrentFirstUseAndAlpha(t *testing.T) {
	source := image.NewNRGBA(image.Rect(0, 0, 9, 7))
	for y := 0; y < 7; y++ {
		for x := 0; x < 9; x++ {
			source.SetNRGBA(x, y, color.NRGBA{R: 150, G: 12, B: 55, A: 128})
		}
	}
	var fixture bytes.Buffer
	if err := png.Encode(&fixture, source); err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for i := 0; i < 12; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			output, kind, err := optimize(bytes.NewReader(fixture.Bytes()), 31, 75, "webp")
			if err != nil {
				t.Error(err)
				return
			}
			decoded, format, err := image.Decode(bytes.NewReader(output))
			if err != nil {
				t.Error(err)
				return
			}
			_, _, _, alpha := decoded.At(0, 0).RGBA()
			if kind != "image/webp" || format != "webp" || decoded.Bounds().Dx() != 31 || decoded.Bounds().Dy() != 24 || alpha != 128*257 {
				t.Errorf("kind=%s format=%s bounds=%v alpha=%d", kind, format, decoded.Bounds(), alpha)
			}
		}()
	}
	workers.Wait()
}
