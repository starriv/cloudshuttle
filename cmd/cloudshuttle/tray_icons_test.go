package main

import (
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
