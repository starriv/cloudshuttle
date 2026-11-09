package view

import (
	"context"
	"errors"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/internal/icons"
	"github.com/starriv/cloudshuttle/locales"
)

// runLogView is the log of the run the history shows in detail: the app's
// lines and rclone's, as they were written.
type runLogView struct {
	run     string // the run's ID
	version int    // the lines kept when read last
	lines   []string
	missing bool   // the run has none: it ran before logs were kept
	failed  string // why reading it failed
	list    ui.ListState
}

// runLogHeight is how high the log is: it scrolls beyond.
const runLogHeight = 220

// loadRunLog reads the log of the run with id. Reading it from the local
// database is quick, as reading the history is.
func (a *App) loadRunLog(id string) {
	l := &a.runLog
	if l.run != id {
		*l = runLogView{run: id}
		// A run in progress shows its latest lines as they come.
		l.list.FollowEnd = true
	}
	log, version, err := a.svc.RunLog(context.Background(), id)
	// A run in progress is read again twice a second: its log is split
	// again only when a line was added.
	if err == nil && version == l.version && l.lines != nil {
		return
	}
	l.version = version
	l.lines, l.missing, l.failed = nil, false, ""
	switch {
	case errors.Is(err, domain.ErrNotFound):
		l.missing = true
	case err != nil:
		l.failed = locales.HistoryLogFailed(err.Error())
	default:
		l.lines = strings.Split(strings.TrimRight(log, "\n"), "\n")
	}
}

// refreshRunLog reads again the log shown, while its run is in progress.
func (a *App) refreshRunLog() {
	if a.runLog.run == "" {
		return
	}
	for _, r := range a.running {
		if r.Run.ID == a.runLog.run {
			a.loadRunLog(r.Run.ID)
			return
		}
	}
}

// runLogSection is the log of the run, in a quiet well a step into the
// details panel: the lines as they were written, the end in view while the
// run goes on, and a copy for the whole of it.
func (a *App) runLogSection(c *ui.Context, r domain.Run) {
	p := paletteFor(c.Theme())
	if a.runLog.run != r.ID {
		a.loadRunLog(r.ID)
	}
	l := &a.runLog
	ui.Column(c).Gap(7).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, locales.HistoryLog()).FontSize(sizeSmall).FontWeight(620).
				LetterSpacing(0.2).TextColor(p.muted).Grow(1)
			if len(l.lines) > 0 {
				if accentButton(c, "copy-log", icons.Copy, locales.HistoryLogCopy(), buttonGhost).Clicked() {
					c.WriteClipboard(strings.Join(l.lines, "\n") + "\n")
					a.toast(locales.HistoryLogCopied())
				}
			}
		})
		switch {
		case l.failed != "":
			result(c, l.failed, true)
		case l.missing:
			detailWell(c).Children(func() {
				ui.Text(c, locales.HistoryLogNone()).FontSize(sizeSmall).TextColor(p.muted).LineHeight(1.45)
			})
		default:
			well := ui.Column(c).FillWidth().Clip().Radius(10).
				Background(p.cardAlt).Border(1, p.hairline).PaddingY(5)
			well.Children(func() {
				ui.List(c, &l.list, len(l.lines), func(i int) {
					line := l.lines[i]
					text := ui.Text(c, line).Font("monospace").FontSize(11.5).Selectable().
						Padding(1, 10).FillWidth()
					// The level is in the line as a word; the color only
					// helps it stand out.
					switch {
					case strings.Contains(line, " ERROR "):
						text.TextColor(p.danger)
					case strings.Contains(line, " WARN "):
						text.TextColor(p.warning)
					default:
						text.TextColor(p.text)
					}
				}).FillWidth().Height(runLogHeight).ScrollbarInsets(3, 4, 3).
					Label(locales.HistoryLog())
			})
		}
	})
}
