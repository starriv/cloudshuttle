// Package island shows the progress of runs at the top of the screen, around
// the camera housing of Macs that have one, as the Dynamic Island of iPhones
// does: a black pill that is the housing's color, so the two read as one.
//
// The pill is compact while runs go on: a turning icon left of the housing
// and the share done right of it. It opens for a moment as a run starts, to
// say which, and as it ends, to say how it went, then closes again. With
// nothing to show, it shrinks into the housing and fades.
//
// The package is the pill's state and view; cmd/cloudshuttle gives it a
// window above the menu bar, on macOS.
package island

import (
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/internal/service"
)

// How long the pill stays open as a run starts, and as one ends.
const (
	startedFor  = 2500 * time.Millisecond
	finishedFor = 4 * time.Second
)

// Geometry is where the pill sits: the camera housing, if any, and the menu
// bar it is as tall as.
type Geometry struct {
	NotchWidth float32 // 0 on screens without a housing
	BarHeight  float32
}

// Window sizes the window the pill is drawn in, room for it open.
func (g Geometry) Window() (width, height int) {
	return int(expandedWidth(g)) + 40, int(g.BarHeight + expandedBody + 24)
}

const (
	wing         = 52  // the width of each side of the compact pill
	expandedMin  = 360 // the width of the open pill, at least
	expandedBody = 68  // the height the open pill adds under the bar
)

func compactWidth(g Geometry) float32 { return g.NotchWidth + 2*wing }

func expandedWidth(g Geometry) float32 { return max(expandedMin, g.NotchWidth+2*wing+80) }

// Island is the pill's state. Its methods run on the main thread.
type Island struct {
	geometry Geometry
	now      func() time.Time

	runs     []service.Active
	seen     map[string]bool // runs already announced
	openTill time.Time       // the pill stays open until then
	finished *domain.Run     // the run the open pill reports, once ended
}

// New makes the pill's state.
func New(g Geometry) *Island {
	return &Island{geometry: g, now: time.Now, seen: map[string]bool{}}
}

// SetGeometry places the pill anew, as the displays change.
func (i *Island) SetGeometry(g Geometry) { i.geometry = g }

// SetRuns gives the runs in progress. A run not seen before opens the pill.
func (i *Island) SetRuns(runs []service.Active) {
	i.runs = runs
	live := map[string]bool{}
	for _, r := range runs {
		live[r.Run.ID] = true
		if !i.seen[r.Run.ID] {
			i.seen[r.Run.ID] = true
			i.finished = nil
			i.openTill = i.now().Add(startedFor)
		}
	}
	for id := range i.seen {
		if !live[id] {
			delete(i.seen, id)
		}
	}
}

// Finished reports a run that ended, which the pill opens to tell.
func (i *Island) Finished(r domain.Run) {
	i.finished = &r
	i.openTill = i.now().Add(finishedFor)
}

type mode int

const (
	hidden mode = iota
	compact
	expanded
)

func (i *Island) mode() mode {
	switch {
	case i.now().Before(i.openTill) && (i.finished != nil || len(i.runs) > 0):
		return expanded
	case len(i.runs) > 0:
		return compact
	}
	return hidden
}

// Visible reports whether the pill shows anything, for the window to hide
// once it has shrunk away.
func (i *Island) Visible() bool { return i.mode() != hidden }

// pillSpring is a little bouncy, as the island's own motion.
var pillSpring = ui.Animation{Spring: ui.SpringOptions{Duration: 450 * time.Millisecond, Bounce: 0.25}}

// View builds the window: the pill at the top, in the middle, on nothing.
func (i *Island) View(c *ui.Context) {
	c.Root().Background(ui.Transparent)
	g := i.geometry
	m := i.mode()
	if m == expanded {
		c.After(i.openTill.Sub(i.now()))
	}

	width, height, opacity := g.NotchWidth, g.BarHeight, float32(0)
	switch m {
	case compact:
		width, opacity = compactWidth(g), 1
	case expanded:
		width, height, opacity = expandedWidth(g), g.BarHeight+expandedBody, 1
	}

	ui.Column(c).Fill().AlignItems(ui.Center).Children(func() {
		pill := ui.Column(c).Background(ui.RGB(0, 0, 0)).ClipX().ClipY().TextColor(white)
		w := pill.AnimateTo("w", width, pillSpring)
		h := pill.AnimateTo("h", height, pillSpring)
		radius := min(h/2, 10+(h-g.BarHeight)/3)
		pill.Size(w, h).Opacity(pill.AnimateTo("o", opacity, ui.Smooth)).
			Radius(0, 0, radius, radius).Children(func() {
			if m == hidden && len(i.runs) == 0 && i.finished == nil {
				return
			}
			i.bar(c, g, m)
			if m == expanded {
				i.body(c)
			}
		})
	})
}

// The pill is black whatever the theme, so its colors are its own.
var (
	white = ui.RGB(255, 255, 255)
	muted = ui.RGBA(255, 255, 255, 0.6)
	track = ui.RGBA(255, 255, 255, 0.18)
	green = ui.RGB(48, 209, 88)  // systemGreen, dark
	red   = ui.RGB(255, 69, 58)  // systemRed, dark
	blue  = ui.RGB(10, 132, 255) // systemBlue, dark
)
