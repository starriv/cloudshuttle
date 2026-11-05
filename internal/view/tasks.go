package view

import (
	"context"
	"slices"

	"github.com/egoist/mygo/ui"
	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/internal/format"
	"github.com/starriv/cloudshuttle/internal/icons"
	"github.com/starriv/cloudshuttle/internal/service"
	"github.com/starriv/cloudshuttle/locales"
)

func (a *App) tasksPage(c *ui.Context) {
	addTask := func() {
		if iconButton(c, icons.Add, locales.TasksAdd(), true).Clicked() {
			a.openTaskForm(nil)
		}
	}
	header(c, locales.NavTasks(), addTask)
	if len(a.tasks) == 0 {
		empty(c, icons.Tasks, locales.TasksEmpty(), nil)
		return
	}
	if slices.ContainsFunc(a.tasks, func(t domain.Task) bool { return t.Schedule.On() }) {
		a.blockedNote(c)
	}

	t := c.Theme()
	cols := []ui.TableColumn{
		{Title: locales.TasksColumnName(), ID: "name", MinWidth: 100},
		{Title: locales.TasksColumnRoute(), ID: "route", MinWidth: 160},
		{Title: locales.TasksColumnSchedule(), ID: "schedule", Width: 180},
		{Title: locales.TasksColumnLastRun(), ID: "last", Width: 130},
		{Title: "", ID: "actions", Width: 148, Fixed: true, Align: ui.End},
	}
	a.taskList.Label = func(i int) string { return a.tasks[i].Name }
	ui.Table(c, &a.taskList, cols, len(a.tasks), func(row, col int) {
		task := a.tasks[row]
		switch col {
		case 0:
			ui.Text(c, task.Name).SingleLine()
		case 1:
			ui.Column(c).MinWidth(0).Children(func() {
				ui.Text(c, a.locationText(task.Source)).SingleLine().Ellipsis("…")
				route := locales.TasksRouteCopy(a.locationText(task.Destination))
				if task.Mode == domain.ModeSync {
					route = locales.TasksRouteSync(a.locationText(task.Destination))
				}
				ui.Text(c, route).SingleLine().Ellipsis("…").TextColor(t.TextMuted).FontSize(11)
			})
		case 2:
			a.scheduleCell(c, task)
		case 3:
			a.lastRunCell(c, task)
		case 4:
			a.taskActions(c, task)
		}
	}).Grow(1)
}

func (a *App) locationText(l domain.Location) string {
	if l.Local() {
		return l.Path
	}
	for _, s := range a.storages {
		if s.ID == l.StorageID {
			if l.Path == "" {
				return s.Name + ":"
			}
			return s.Name + ": " + l.Path
		}
	}
	return l.Path
}

func (a *App) scheduleCell(c *ui.Context, task domain.Task) {
	if !task.Schedule.On() {
		ui.Text(c, locales.TasksManual()).TextColor(c.Theme().TextMuted).SingleLine()
		return
	}
	ui.Column(c).MinWidth(0).Children(func() {
		ui.Text(c, scheduleText(c, task.Schedule)).SingleLine().Ellipsis("…")
		if next := a.svc.NextRun(task); !next.IsZero() {
			ui.Text(c, locales.TasksNextRun(formatTime(c, next))).
				TextColor(c.Theme().TextMuted).FontSize(11).SingleLine().Ellipsis("…")
		}
	})
}

func (a *App) lastRunCell(c *ui.Context, task domain.Task) {
	if run, ok := a.active[task.ID]; ok {
		ui.Column(c).Gap(4).FillWidth().Children(func() {
			if run.Queued {
				ui.Text(c, locales.TaskWaiting()).TextColor(c.Theme().TextMuted).SingleLine()
				return
			}
			progressBar(c, task.Name, run.Progress.Bytes, run.Progress.TotalBytes)
			ui.Text(c, format.Bytes(run.Progress.Bytes)).FontSize(11).FontFeatures("tnum").
				TextColor(c.Theme().TextMuted).SingleLine()
		})
		return
	}
	last, ok := a.lastRuns[task.ID]
	if !ok {
		ui.Text(c, locales.TasksNever()).TextColor(c.Theme().TextMuted).SingleLine()
		return
	}
	ui.Column(c).Children(func() {
		status(c, last.Status)
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Text(c, formatTime(c, last.StartedAt)).TextColor(c.Theme().TextMuted).FontSize(11).SingleLine()
			if last.Stats.Deletes > 0 {
				ui.Text(c, locales.HistoryDeletedShort(int(last.Stats.Deletes))).
					TextColor(c.Theme().TextMuted).FontSize(11).SingleLine()
			}
		})
	})
}

func (a *App) taskActions(c *ui.Context, task domain.Task) {
	ui.Row(c).Gap(6).Justify(ui.End).Children(func() {
		if _, running := a.active[task.ID]; running {
			if iconButton(c, icons.Stop, locales.TaskStop(), false).Clicked() {
				a.stop(task.ID)
			}
		} else if iconButton(c, icons.Run, locales.TaskRun(), false).Clicked() {
			a.run(task, false, false)
		}
		ui.Button(c, "").Label(locales.TaskActions(task.Name)).Children(func() {
			ui.Icon(c, icons.More)
		}).Menu(func(m *ui.Menu) {
			_, running := a.active[task.ID]
			if m.Item(locales.TaskDryRun()).Disabled(running).Chosen() {
				a.run(task, true, false)
			}
			if task.Mode == domain.ModeSync && m.Item(locales.TaskRunAnyway()).Disabled(running).Chosen() {
				a.confirmRunAnyway(task)
			}
			m.Separator()
			if m.Item(locales.TaskEdit()).Chosen() {
				a.openTaskForm(&task)
			}
			if m.Item(locales.TaskDelete()).Disabled(running).Chosen() {
				a.confirmDeleteTask(task)
			}
		})
	})
}

func (a *App) run(task domain.Task, dryRun, force bool) {
	opts := service.RunOptions{Trigger: domain.TriggerManual, DryRun: dryRun, Force: force}
	if _, err := a.svc.Run(context.Background(), task.ID, opts); err != nil {
		a.toastError(err)
		return
	}
	a.toast(locales.TaskStarted(task.Name))
}

// confirmRunAnyway asks before a sync that skips the check on deletes: it
// may delete most of the destination, or all of it when the source is empty.
func (a *App) confirmRunAnyway(task domain.Task) {
	message := locales.TaskRunAnywayMessage()
	if days := task.Trash(a.settings); days > 0 {
		message += " " + locales.TaskRunAnywayTrash(days)
	}
	a.confirm = confirmation{
		open:    true,
		title:   locales.TaskRunAnywayTitle(task.Name),
		message: message,
		ok:      locales.TaskRunAnywayButton(),
		action:  func() { a.run(task, false, true) },
	}
}

func (a *App) confirmDeleteTask(task domain.Task) {
	a.confirm = confirmation{
		open:    true,
		title:   locales.TaskDeleteTitle(task.Name),
		message: locales.TaskDeleteMessage(),
		action: func() {
			if err := a.svc.DeleteTask(context.Background(), task.ID); err != nil {
				a.toastError(err)
				return
			}
			a.toast(locales.TaskDeleted())
		},
	}
}
