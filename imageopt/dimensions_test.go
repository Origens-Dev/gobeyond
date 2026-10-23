package imageopt

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/png"
	"testing"

	"github.com/gen2brain/webp"
)

func TestRejectOversizedDimensionsBeforeDecodingPixels(t *testing.T) {
	var pngBytes bytes.Buffer
	if err := png.Encode(&pngBytes, image.NewNRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	p := pngBytes.Bytes()
	binary.BigEndian.PutUint32(p[16:20], 10000)
	binary.BigEndian.PutUint32(p[20:24], 10000)
	binary.BigEndian.PutUint32(p[29:33], crc32.ChecksumIEEE(p[12:29]))
	var webpBytes bytes.Buffer
	if err := webp.Encode(&webpBytes, image.NewNRGBA(image.Rect(0, 0, 8, 8)), webp.Options{Quality: 75}); err != nil {
		t.Fatal(err)
	}
	w := webpBytes.Bytes()
	dimension := 9999
	if string(w[12:16]) == "VP8X" {
		for i := 0; i < 3; i++ {
			w[24+i] = byte((dimension >> (8 * i)) & 255)
			w[27+i] = byte((dimension >> (8 * i)) & 255)
		}
	}
	signature := bytes.Index(w, []byte{0x9d, 0x01, 0x2a})
	if signature < 0 {
		t.Fatal("WebP fixture missing VP8 frame signature")
	}
	binary.LittleEndian.PutUint16(w[signature+3:signature+5], 10000)
	binary.LittleEndian.PutUint16(w[signature+5:signature+7], 10000)
	for name, data := range map[string][]byte{"png": p, "webp": w} {
		t.Run(name, func(t *testing.T) {
			config, _, err := image.DecodeConfig(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			if config.Width != 10000 || config.Height != 10000 {
				t.Fatalf("fixture config=%v", config)
			}
			// The compressed payload only contains 8x8 pixels. Config limits must win
			// before pixel decoding tries to allocate a malicious 100M-pixel canvas.
			if _, _, err := optimize(bytes.NewReader(data), 32, 75, "jpeg"); err == nil || err.Error() != "image dimensions are unsupported" {
				t.Fatalf("error=%v", err)
			}
		})
	}
}
