package island

import (
	"strconv"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/internal/engine"
	"github.com/starriv/cloudshuttle/internal/format"
	"github.com/starriv/cloudshuttle/internal/icons"
	"github.com/starriv/cloudshuttle/locales"
)

// bar builds the row as tall as the menu bar: one wing each side of the
// housing, which stays empty between them.
func (i *Island) bar(c *ui.Context, g Geometry, m mode) {
	ui.Row(c).Height(g.BarHeight).FillWidth().AlignItems(ui.Center).Padding(0, 14).Children(func() {
		ui.Row(c).Grow(1).Basis(0).AlignItems(ui.Center).Children(func() { i.leftWing(c) })
		ui.Box(c).Width(g.NotchWidth)
		ui.Row(c).Grow(1).Basis(0).AlignItems(ui.Center).Justify(ui.End).Children(func() { i.rightWing(c) })
	})
}

func (i *Island) leftWing(c *ui.Context) {
	if i.finished != nil && len(i.runs) == 0 {
		statusIcon(c, i.finished.Status)
		return
	}
	spin := ui.Icon(c, icons.Running).TextColor(blue).FontSize(15)
	spin.Rotate(spin.Loop("spin", time.Second, ui.Linear) * 360)
}

func (i *Island) rightWing(c *ui.Context) {
	if len(i.runs) == 0 {
		return
	}
	text := percent(i.runs[0].Progress)
	if n := len(i.runs); n > 1 {
		text = locales.IslandMore(n - 1)
	}
	ui.Text(c, text).FontSize(12).FontWeight(500).FontFeatures("tnum").SingleLine()
}

// body builds what the open pill adds under the bar: the run starting or in
// progress, or the run that ended.
func (i *Island) body(c *ui.Context) {
	ui.Column(c).FillWidth().Padding(4, 18, 14, 18).Gap(6).
		Transition(ui.ElementTransition{Enter: &ui.Motion{Y: -4}, Exit: &ui.Motion{}}).Children(func() {
		if i.finished != nil && len(i.runs) == 0 {
			i.ended(c, *i.finished)
			return
		}
		run := i.runs[0]
		p := run.Progress
		ui.Text(c, run.Run.TaskName).FontSize(14).FontWeight(500).SingleLine().Ellipsis("…")
		bar(c, p)
		ui.Text(c, detail(p)).FontSize(11).FontFeatures("tnum").TextColor(muted).SingleLine()
	})
}

func (i *Island) ended(c *ui.Context, r domain.Run) {
	title := locales.IslandFinished(r.TaskName)
	switch r.Status {
	case domain.StatusFailed:
		title = locales.NotifyFailedTitle(r.TaskName)
	case domain.StatusCanceled:
		title = locales.IslandStopped(r.TaskName)
	}
	ui.Text(c, title).FontSize(14).FontWeight(500).SingleLine().Ellipsis("…")
	sub := locales.Transferred(int(r.Stats.Transfers), format.Bytes(r.Stats.Bytes))
	if r.Status == domain.StatusFailed && r.Message != "" {
		sub = r.Message
	}
	ui.Text(c, sub).FontSize(11).TextColor(muted).SingleLine().Ellipsis("…")
}

// bar is a thin track filling as the run goes on, gliding between updates.
func bar(c *ui.Context, p engine.Progress) {
	box := ui.Box(c).Height(4).FillWidth().Radius(2).Background(track).ClipX()
	fill := float32(0)
	if p.TotalBytes > 0 {
		fill = float32(p.Bytes) / float32(p.TotalBytes)
	}
	shown := box.AnimateTo("fill", fill, ui.Smooth)
	box.Children(func() {
		ui.Box(c).HeightPercent(100).WidthPercent(shown * 100).Radius(2).Background(blue)
	})
}

func percent(p engine.Progress) string {
	if p.TotalBytes <= 0 {
		return "…"
	}
	return strconv.Itoa(int(100*p.Bytes/p.TotalBytes)) + "%"
}

func detail(p engine.Progress) string {
	text := locales.Progress(format.Bytes(p.Bytes), format.Bytes(p.TotalBytes))
	if p.Speed > 0 {
		text += "  " + locales.Speed(format.Bytes(int64(p.Speed)))
	}
	return text
}

func statusIcon(c *ui.Context, s domain.Status) {
	switch s {
	case domain.StatusSuccess:
		ui.Icon(c, icons.Success).TextColor(green).FontSize(15)
	case domain.StatusFailed:
		ui.Icon(c, icons.Failed).TextColor(red).FontSize(15)
	default:
		ui.Icon(c, icons.Stopped).TextColor(muted).FontSize(15)
	}
}
