package view

import (
	"image"
	"image/draw"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/starriv/cloudshuttle/locales"
)

// TestPageScrollBarShowsInThePageMargin checks a page's scroll bar is drawn
// in the page's margin, beside its content, rather than over it: a bar
// floats over what it scrolls and takes no room, so the page's padding has
// to be inside the scroll container, which reaches the content area's edges.
func TestPageScrollBarShowsInThePageMargin(t *testing.T) {
	h := newHarness(t)
	h.click(locales.NavSettings())
	h.tt.SetSize(760, 620) // short enough that the settings page scrolls
	h.tt.Advance(500 * time.Millisecond)

	// The pointer is over the page's title bar: no scroll bar shows.
	h.tt.Move(500, 20)
	h.tt.Frame()
	hidden := snapshot(h.tt.Image())

	// The pointer is over the page: its scroll bar shows.
	h.tt.Move(500, 400)
	h.tt.Frame()
	shown := h.tt.Image()

	// All the scroll bar paints lies in the page's margin: no card reaches
	// it, and the bar is narrower than the margin.
	margin := ui.LightTheme().Space(pagePadding)
	w := shown.Rect.Dx()
	var bar, over int
	for y := 0; y < shown.Rect.Dy(); y++ {
		for x := 0; x < w; x++ {
			if shown.RGBAAt(x, y) == hidden.RGBAAt(x, y) {
				continue
			}
			bar++
			if float32(x) < float32(w)-margin {
				over++
			}
		}
	}
	if bar == 0 {
		t.Fatal("no scroll bar shows on the settings page")
	}
	if over > 0 {
		t.Fatalf("%d of the scroll bar's %d pixels are over the page's content, not in its margin", over, bar)
	}
}

// snapshot copies a frame, which the tester paints the next one over.
func snapshot(img *image.RGBA) *image.RGBA {
	out := image.NewRGBA(img.Rect)
	draw.Draw(out, out.Rect, img, img.Rect.Min, draw.Src)
	return out
}
