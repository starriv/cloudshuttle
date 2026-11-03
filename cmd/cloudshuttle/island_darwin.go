//go:build darwin

package main

import (
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/internal/island"
	"github.com/starriv/cloudshuttle/internal/service"
)

// nsStatusWindowLevel is the level of the menu bar's status items, one above
// the menu bar's own: the pill shows over the bar, as part of it.
const nsStatusWindowLevel = 25

// NSWindowCollectionBehavior: on every Space, still while Spaces switch,
// beside full-screen apps, and out of the window cycle.
const collectionBehavior = 1<<0 | 1<<4 | 1<<6 | 1<<8

type nsRect struct{ X, Y, W, H float64 }

// CGRect is NSRect under the name AppKit's methods take it by.
type CGRect struct{ X, Y, W, H float64 }

type nsEdgeInsets struct{ Top, Left, Bottom, Right float64 }

// notchIsland is the island's window, at the top of the main display.
type notchIsland struct {
	state  *island.Island
	win    *mygo.Window
	w, h   int
	active atomic.Bool // whether a run was in progress, last told
}

// newIsland opens the island's window. Call it once the app is ready.
func newIsland() *notchIsland {
	g := measureNotch()
	n := &notchIsland{state: island.New(g)}
	w, h := g.Window()
	n.win = mygo.NewWindow(mygo.WindowOptions{
		Width:       w,
		Height:      h,
		Frameless:   true,
		Transparent: true,
		Panel:       true,
		Hidden:      true,
		Content:     ui.View(n.state.View),
	})
	n.win.SetHasShadow(false)
	n.win.SetIgnoreMouseEvents(true) // clicks go to the menu bar under it
	n.w, n.h = w, h
	n.win.ShowInactive()
	n.raise()
	n.place()

	mygo.Screen.OnDisplaysChanged(func() {
		g := measureNotch()
		w, h := g.Window()
		n.win.Update(func() { n.state.SetGeometry(g) })
		mygo.RunOnMain(func() { n.w, n.h = w, h })
		n.raise()
		n.place()
	})
	return n
}

// place puts the window at the top edge of the display with the menu bar,
// centered, over the bar and the camera housing: once shown, as displays
// change, and as a run begins, before the pill shows again.
func (n *notchIsland) place() {
	mygo.RunOnMain(func() {
		win := objc.ID(n.win.NativeHandle())
		if win == 0 {
			return
		}
		sel := objc.RegisterName
		screens := objc.ID(objc.GetClass("NSScreen")).Send(sel("screens"))
		if objc.Send[uint](screens, sel("count")) == 0 {
			return
		}
		screen := objc.Send[nsRect](screens.Send(sel("objectAtIndex:"), 0), sel("frame"))
		w, h := float64(n.w), float64(n.h)
		frame := nsRect{X: screen.X + (screen.W-w)/2, Y: screen.Y + screen.H - h, W: w, H: h}
		if objc.Send[nsRect](win, sel("frame")) != frame {
			win.Send(sel("setFrame:display:"), frame, true)
		}
	})
}

// raise lifts the window over the menu bar and onto every Space, which no
// window option does, and lets it stay over the bar.
func (n *notchIsland) raise() {
	mygo.RunOnMain(func() {
		win := objc.ID(n.win.NativeHandle())
		if win == 0 {
			return
		}
		keepOverMenuBar(win)
		win.Send(objc.RegisterName("setLevel:"), nsStatusWindowLevel)
		win.Send(objc.RegisterName("setCollectionBehavior:"), collectionBehavior)
	})
}

var (
	islandWindow atomic.Uintptr // the objc.ID of the island's window
	overBarOnce  sync.Once
)

// keepOverMenuBar lets win overlap the menu bar. AppKit constrains a
// window's frame to below the menu bar as it shows it, as the frame is set
// and as it places the windows again (on waking, as displays change),
// through constrainFrameRect:toScreen:. The island overrides that method
// for its window alone: in the window's class, as KVO tells it, the method
// keeps the island's frame as asked and leaves every other window's to the
// implementation it replaces. The window keeps its class: changing the
// class of a window AppKit observes with KVO breaks its deallocation.
func keepOverMenuBar(win objc.ID) {
	islandWindow.Store(uintptr(win))
	overBarOnce.Do(func() {
		lib, err := purego.Dlopen("/usr/lib/libobjc.A.dylib", purego.RTLD_GLOBAL|purego.RTLD_NOW)
		if err != nil {
			slog.Warn("island: load the Objective-C runtime", "err", err)
			return
		}
		var (
			getInstanceMethod func(cls objc.Class, sel objc.SEL) uintptr
			typeEncoding      func(method uintptr) string
			getImplementation func(cls objc.Class, sel objc.SEL) uintptr
			replaceMethod     func(cls objc.Class, sel objc.SEL, imp objc.IMP, types string) uintptr
		)
		purego.RegisterLibFunc(&getInstanceMethod, lib, "class_getInstanceMethod")
		purego.RegisterLibFunc(&typeEncoding, lib, "method_getTypeEncoding")
		purego.RegisterLibFunc(&getImplementation, lib, "class_getMethodImplementation")
		purego.RegisterLibFunc(&replaceMethod, lib, "class_replaceMethod")

		sel := objc.RegisterName("constrainFrameRect:toScreen:")
		cls := objc.Send[objc.Class](win, objc.RegisterName("class"))
		method := getInstanceMethod(cls, sel)
		if method == 0 {
			slog.Warn("island: the window does not constrain its frame")
			return
		}
		var constrain func(self objc.ID, cmd objc.SEL, frame CGRect, screen objc.ID) CGRect
		purego.RegisterFunc(&constrain, getImplementation(cls, sel))
		replaceMethod(cls, sel, objc.NewIMP(func(self objc.ID, cmd objc.SEL, frame CGRect, screen objc.ID) CGRect {
			if uintptr(self) == islandWindow.Load() {
				return frame
			}
			return constrain(self, cmd, frame, screen)
		}), typeEncoding(method))
	})
}

// SetRuns gives the island the runs in progress.
func (n *notchIsland) SetRuns(runs []service.Active) {
	if active := len(runs) > 0; n.active.Swap(active) != active && active {
		n.place() // the pill is to show: back over the bar, if AppKit moved it
	}
	n.win.Update(func() { n.state.SetRuns(runs) })
}

// Finished tells the island a run ended.
func (n *notchIsland) Finished(r domain.Run) {
	n.win.Update(func() { n.state.Finished(r) })
}

// measureNotch reads the camera housing of the main display, which macOS
// tells as the room the menu bar leaves on either side of it, and the
// height of the menu bar.
func measureNotch() island.Geometry {
	var g island.Geometry
	mygo.RunOnMain(func() {
		sel := objc.RegisterName
		statusBar := objc.ID(objc.GetClass("NSStatusBar")).Send(sel("systemStatusBar"))
		g.BarHeight = float32(objc.Send[float64](statusBar, sel("thickness")))

		screens := objc.ID(objc.GetClass("NSScreen")).Send(sel("screens"))
		if objc.Send[uint](screens, sel("count")) == 0 {
			return
		}
		screen := screens.Send(sel("objectAtIndex:"), 0) // the one with the menu bar
		if !objc.Send[bool](screen, sel("respondsToSelector:"), sel("safeAreaInsets")) {
			return // before macOS 12: no housing to know of
		}
		insets := objc.Send[nsEdgeInsets](screen, sel("safeAreaInsets"))
		if insets.Top <= 0 {
			return
		}
		frame := objc.Send[nsRect](screen, sel("frame"))
		left := objc.Send[nsRect](screen, sel("auxiliaryTopLeftArea"))
		right := objc.Send[nsRect](screen, sel("auxiliaryTopRightArea"))
		g.BarHeight = float32(insets.Top)
		if notch := frame.W - left.W - right.W; notch > 0 && left.W > 0 && right.W > 0 {
			g.NotchWidth = float32(notch)
		}
	})
	if g.BarHeight <= 0 {
		g.BarHeight = 24
	}
	return g
}
