//go:build windows

package tray

import (
	"image/color"
	"testing"
)

func TestRenderXHasContent(t *testing.T) {
	img := RenderX(color.RGBA{R: 0xff, G: 0x45, B: 0x45, A: 0xff}, 16, 16)
	if img.Bounds().Dx() != 16 || img.Bounds().Dy() != 16 {
		t.Fatalf("bounds = %v", img.Bounds())
	}
	var opaque int
	for _, p := range img.Pix {
		if p != 0 {
			opaque++
		}
	}
	if opaque == 0 {
		t.Fatal("RenderX produced an empty image")
	}
}

func TestRenderSparklineBounds(t *testing.T) {
	img := RenderSparkline([]float64{0, 0.5, 1}, color.RGBA{A: 0xff}, 16, 16)
	if got := img.Bounds(); got.Dx() != 16 || got.Dy() != 16 {
		t.Fatalf("bounds = %v", got)
	}
}

func TestRenderMemoryBarBounds(t *testing.T) {
	img := RenderMemoryBar(0.5, 0.25, color.RGBA{A: 0xff}, 16, 16)
	if got := img.Bounds(); got.Dx() != 16 || got.Dy() != 16 {
		t.Fatalf("bounds = %v", got)
	}
}
