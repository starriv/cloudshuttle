package view

import (
	"context"
	"errors"

	"github.com/egoist/mygo/ui"
	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/internal/icons"
	"github.com/starriv/cloudshuttle/locales"
)

// runDeletesView is what a sync the delete check stopped would have
// deleted, for the run the history shows in detail.
type runDeletesView struct {
	run     string // the run's ID
	deletes domain.StoppedDeletes
	missing bool   // none kept: the run is older than the list
	failed  string // why reading it failed
	list    ui.ListState
}

// wouldDeleteLines is how many paths show as lines: more scroll in a list.
const wouldDeleteLines = 8

// loadRunDeletes reads what the run with id would have deleted, from the
// local database, as the run's log is.
func (a *App) loadRunDeletes(id string) {
	d := &a.runDeletes
	*d = runDeletesView{run: id}
	deletes, err := a.svc.RunDeletes(context.Background(), id)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		d.missing = true
	case err != nil:
		d.failed = locales.HistoryWouldDeleteFailed(err.Error())
	default:
		d.deletes = deletes
	}
}

// wouldDelete lists the files a stopped sync would have deleted, so that a
// person looks them over before running it anyway.
func (a *App) wouldDelete(c *ui.Context, r domain.Run) {
	p := paletteFor(c.Theme())
	if a.runDeletes.run != r.ID {
		a.loadRunDeletes(r.ID)
	}
	d := &a.runDeletes
	if d.missing {
		return
	}
	ui.Column(c).Gap(7).Children(func() {
		detailLabel(c, locales.HistoryWouldDelete())
		if d.failed != "" {
			result(c, d.failed, true)
			return
		}
		paths := d.deletes.Paths
		line := func(path string) {
			ui.Text(c, path).Font("monospace").FontSize(sizeSmall).TextColor(p.text).
				Selectable().SingleLine().Ellipsis("…").Padding(1, 10).FillWidth()
		}
		if len(paths) <= wouldDeleteLines {
			well := ui.Column(c).FillWidth().PaddingY(4).Radius(10).
				Background(p.cardAlt).Border(1, p.hairline)
			well.Children(func() {
				for _, path := range paths {
					line(path)
				}
			})
		} else {
			well := ui.Column(c).FillWidth().Clip().Radius(10).
				Background(p.cardAlt).Border(1, p.hairline).PaddingY(4)
			well.Children(func() {
				ui.List(c, &d.list, len(paths), func(i int) { line(paths[i]) }).
					FillWidth().Height(160).ScrollbarInsets(3, 4, 3).
					Label(locales.HistoryWouldDelete())
			})
		}
		if more := d.deletes.Total - len(paths); more > 0 {
			ui.Text(c, locales.HistoryWouldDeleteMore(more)).FontSize(sizeMicro).TextColor(p.muted)
		}
		a.runAnywayButton(c, r)
	})
}

// runAnywayButton runs the stopped sync's task anyway, after the same
// question as the task's menu: while the task is there, still a sync, and
// not running.
func (a *App) runAnywayButton(c *ui.Context, r domain.Run) {
	for _, task := range a.tasks {
		if task.ID != r.TaskID || task.Mode != domain.ModeSync {
			continue
		}
		_, running := a.active[task.ID]
		ui.Row(c).Children(func() {
			if accentButton(c, "run-anyway", icons.Run, locales.TaskRunAnyway(), buttonSoft).
				Disabled(running).Clicked() {
				a.confirmRunAnyway(task)
			}
		})
		return
	}
}
