//go:build whatsapp_native

package whatsapp

import (
	"image"
	_ "image/png"
	"os"
	"testing"
)

func TestSaveQRCodePNG(t *testing.T) {
	out := "/tmp/qrcode_test.png"

	// Use a realistic WhatsApp QR payload
	code := "2@testqrpayload123456,abcdefghijklmnopqrstuvwxyz,ABCDEFGHIJKLMNOPQRSTUVWXYZ,12345678"
	if err := saveQRCodePNG(code, out); err != nil {
		t.Fatalf("saveQRCodePNG returned error: %v", err)
	}

	f, err := os.Open(out)
	if err != nil {
		t.Fatalf("could not open output file: %v", err)
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		t.Fatalf("could not decode PNG: %v", err)
	}

	bounds := img.Bounds()
	if bounds.Dx() != qrImageSize || bounds.Dy() != qrImageSize {
		t.Errorf("expected image size %dx%d, got %dx%d", qrImageSize, qrImageSize, bounds.Dx(), bounds.Dy())
	}

	// Count black and white pixels to verify QR data is actually drawn
	var black, white int
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, _, _, _ := img.At(x, y).RGBA()
			if r < 0x8000 {
				black++
			} else {
				white++
			}
		}
	}

	t.Logf("Image %dx%d: %d black pixels, %d white pixels", bounds.Dx(), bounds.Dy(), black, white)

	if black == 0 {
		t.Error("no black pixels found — QR code was not drawn (all white image)")
	}
	// A valid QR code should have a meaningful mix; at least 5% black
	total := black + white
	pct := float64(black) / float64(total) * 100
	if pct < 5.0 {
		t.Errorf("only %.1f%% black pixels — QR code appears too sparse (likely not drawn correctly)", pct)
	}
	t.Logf("QR code coverage: %.1f%% black pixels — looks good", pct)
}
