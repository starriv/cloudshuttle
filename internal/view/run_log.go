package view

import (
	"context"
	"errors"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/starriv/cloudshuttle/internal/domain"
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

func (a *App) runLogSection(c *ui.Context, r domain.Run) {
	t := c.Theme()
	if a.runLog.run != r.ID {
		a.loadRunLog(r.ID)
	}
	l := &a.runLog
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, locales.HistoryLog()).FontWeight(500).Grow(1)
		if len(l.lines) > 0 && ui.Button(c, locales.HistoryLogCopy()).Clicked() {
			c.WriteClipboard(strings.Join(l.lines, "\n") + "\n")
			a.toast(locales.HistoryLogCopied())
		}
	})
	switch {
	case l.failed != "":
		result(c, l.failed, true)
	case l.missing:
		ui.Text(c, locales.HistoryLogNone()).TextColor(t.TextMuted)
	default:
		ui.List(c, &l.list, len(l.lines), func(i int) {
			line := l.lines[i]
			text := ui.Text(c, line).Font("monospace").FontSize(12).Selectable().
				Padding(1, t.Space(2)).FillWidth()
			// The level is in the line as a word; the color only helps it
			// stand out.
			switch {
			case strings.Contains(line, " ERROR "):
				text.TextColor(t.Danger)
			case strings.Contains(line, " WARN "):
				text.TextColor(t.Warning)
			}
		}).Label(locales.HistoryLog()).FillWidth().Height(runLogHeight).
			Border(1, t.Border).Radius(t.Radius)
	}
}
