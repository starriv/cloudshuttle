package main

import (
	"bytes"
	"image"
	"image/png"
	"log/slog"
	"sync"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/starriv/cloudshuttle/internal/icons"
)

// Native menus tint these template images for light, dark and selected rows.
// Rasterize the shared Tabler SVGs once at Retina resolution.
var menuIcons = sync.OnceValue(func() map[*ui.SVG]*mygo.MenuIcon {
	result := make(map[*ui.SVG]*mygo.MenuIcon)
	for _, svg := range []*ui.SVG{icons.Overview, icons.Tasks, icons.History, icons.Success, icons.Failed, icons.Stopped, icons.Running, icons.Untested, icons.Idle} {
		var data bytes.Buffer
		if err := png.Encode(&data, menuIconImage(svg)); err != nil {
			// A missing icon leaves the menu usable, so it is not worth a
			// crash on the main thread.
			slog.Warn("render a menu icon", "err", err)
			continue
		}
		icon, err := mygo.NewMenuIcon(data.Bytes(), true)
		if err != nil {
			slog.Warn("make a menu icon", "err", err)
			continue
		}
		result[svg] = icon
	}
	return result
})

func menuIconImage(svg *ui.SVG) *image.RGBA {
	return ui.Render(func(c *ui.Context) {
		c.Root().Background(ui.Transparent)
		ui.Icon(c, svg).Size(16, 16).TextColor(c.Theme().Text)
	}, 16, 16, 2)
}
