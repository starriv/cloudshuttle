package main

import (
	"bytes"
	"image/png"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/starriv/cloudshuttle/internal/icons"
)

func TestMenuIconsAreTransparentRetinaTemplates(t *testing.T) {
	for _, svg := range []*ui.SVG{icons.Overview, icons.Tasks, icons.History, icons.Success, icons.Failed, icons.Stopped, icons.Running, icons.Untested} {
		img := menuIconImage(svg)
		if img.Bounds().Dx() != 32 || img.Bounds().Dy() != 32 {
			t.Fatal("menu icon must be 32 by 32 pixels")
		}
		transparent, ink := 0, 0
		for y := 0; y < 32; y++ {
			for x := 0; x < 32; x++ {
				if img.RGBAAt(x, y).A == 0 {
					transparent++
				} else {
					ink++
				}
			}
		}
		if transparent == 0 || ink == 0 {
			t.Fatalf("expected a visible outline on a transparent background, got %d clear and %d painted pixels", transparent, ink)
		}
		if menuIcons()[svg] == nil {
			t.Fatal("missing native menu icon")
		}
	}
}

// Template export must preserve both visible ink and transparent cutouts.
// A luminance-mask exporter can otherwise silently produce an empty PNG.
func TestBrandTrayImages(t *testing.T) {
	for _, tc := range []struct {
		name     string
		data     []byte
		size     int
		template bool
	}{{"macOS", trayIcon, 44, true}, {"color", trayColorIcon, 64, false}} {
		t.Run(tc.name, func(t *testing.T) {
			img, err := png.Decode(bytes.NewReader(tc.data))
			if err != nil {
				t.Fatal(err)
			}
			b := img.Bounds()
			if b.Dx() != tc.size || b.Dy() != tc.size {
				t.Fatalf("unexpected size: %v", b)
			}
			ink, clear, color := 0, 0, 0
			for y := b.Min.Y; y < b.Max.Y; y++ {
				for x := b.Min.X; x < b.Max.X; x++ {
					r, g, bl, a := img.At(x, y).RGBA()
					if a == 0 {
						clear++
						continue
					}
					ink++
					if r != 0 || g != 0 || bl != 0 {
						color++
					}
				}
			}
			if ink < 100 || clear < 100 {
				t.Fatalf("expected visible art and transparency: ink=%d clear=%d", ink, clear)
			}
			if tc.template && color != 0 {
				t.Fatal("macOS template must be black and transparent")
			}
			if !tc.template && color == 0 {
				t.Fatal("color tray icon has no color")
			}
		})
	}
}
