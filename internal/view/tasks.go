package view

import (
	"context"
	"slices"

	"github.com/egoist/mygo/ui"
	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/internal/icons"
	"github.com/starriv/cloudshuttle/internal/service"
	"github.com/starriv/cloudshuttle/locales"
)

// The tasks page: what each task copies and where it goes, when it runs
// next and how it last went. The page's own card holds the rows, one task
// each, and the row says the task's state before it says anything else.

// The tasks card's columns: the task and its source, where it goes, when it
// runs, its last run, and the buttons that act on it. The name and the
// route share the room the others leave; the rest keep their width, so the
// schedule, the status words and the buttons never clip.
const (
	taskColSchedule = 170
	taskColLast     = 130
	taskColStatus   = 132
	taskColActions  = 82
	taskRowHeight   = 62
	taskHeadHeight  = 34
	taskRowPad      = rowPadX
	taskActionWidth = 30
)

func (a *App) tasksPage(c *ui.Context) {
	addTask := func() {
		if iconButton(c, icons.Add, locales.TasksAdd(), true).Clicked() {
			a.openTaskForm(nil)
		}
	}
	pageHeader(c, locales.NavTasks(), locales.TasksSubtitle(), addTask)

	if len(a.tasks) == 0 {
		emptyPage(c, icons.Tasks, locales.TasksEmptyTitle(), locales.TasksEmpty(), addTask)
		return
	}

	a.tasksToolbar(c)
	if slices.ContainsFunc(a.tasks, func(t domain.Task) bool { return t.Schedule.On() }) {
		ui.Box(c).Padding(2, 0, 0, 0).Children(func() { a.blockedNote(c) })
	}

	ui.Box(c).Grow(1).MinHeight(0).Margin(0, 0, pageBottom, 0).Children(func() {
		a.tasksCard(c)
	})
}

// tasksToolbar says how many tasks there are, how many run on a schedule,
// and whether anything needs a look now: the quiet answer under the title.
func (a *App) tasksToolbar(c *ui.Context) {
	p := paletteFor(c.Theme())
	scheduled, failing := 0, 0
	for _, task := range a.tasks {
		if task.Schedule.On() {
			scheduled++
		}
		if a.lastRuns[task.ID].Status == domain.StatusFailed {
			failing++
		}
	}
	ui.Row(c).Height(34).AlignItems(ui.Center).Gap(10).Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icons.Tasks).Size(12, 12).TextColor(p.faint)
			ui.Text(c, locales.OverviewFailingTasks(len(a.tasks))).FontSize(sizeSmall).
				TextColor(p.muted).SingleLine()
		})
		ui.Box(c).Size(3, 3).Radius(1.5).Background(p.faint)
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icons.Calendar).Size(12, 12).TextColor(p.faint)
			ui.Text(c, locales.OverviewScheduledOf(scheduled)).FontSize(sizeSmall).
				TextColor(p.muted).SingleLine()
		})
		ui.Spacer(c)
		switch {
		case len(a.running) > 0:
			// The status column already marks the run as moving; this
			// summary stays a quiet count.
			pill(c, "running", nil, locales.SidebarTransferring(len(a.running)),
				p.accent, p.accentSoft)
		case failing > 0:
			pill(c, "failing", icons.Warning, locales.OverviewAttentionTitle(failing),
				p.warning, p.warningSoft)
		}
	})
}

// tasksCard holds the rows, headed by the columns' names.
func (a *App) tasksCard(c *ui.Context) {
	p := paletteFor(c.Theme())
	a.taskList.Label = func(i int) string { return a.tasks[i].Name }
	card(c).Grow(1).MinHeight(0).Clip().Children(func() {
		ui.Row(c).Height(taskHeadHeight).Padding(0, taskRowPad).AlignItems(ui.Center).
			Background(p.cardAlt).Children(func() {
			a.taskColumns(c,
				func() { taskHeadText(c, locales.TasksColumnName()) },
				func() { taskHeadText(c, locales.TasksColumnRoute()) },
				func() { taskHeadText(c, locales.TasksColumnSchedule()) },
				func() { taskHeadText(c, locales.TasksColumnLastRun()) },
				func() { taskHeadText(c, locales.HistoryColumnStatus()) },
				func() {})
		})
		ui.List(c, &a.taskList, len(a.tasks), func(i int) {
			a.taskRow(c, a.tasks[i], i)
		}).Grow(1).MinHeight(0).ScrollbarInsets(gapSmall, 4, gapSmall)
	})
}

// taskHeadText is the name of a column of the tasks card.
func taskHeadText(c *ui.Context, text string) {
	ui.Text(c, text).FontSize(sizeMicro).FontWeight(600).LetterSpacing(0.2).
		TextColor(paletteFor(c.Theme()).muted).SingleLine().Ellipsis("…")
}

// taskColumns lays out a row of the tasks card: the header's and each
// task's. The columns that grow share the room the fixed ones leave.
func (a *App) taskColumns(c *ui.Context, name, route, schedule, last, status, actions func()) {
	scheduleW, lastW, statusW, actionsW := a.taskColumnWidths(c)
	ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
		a.taskDataColumns(c, scheduleW, lastW, statusW, name, route, schedule, last, status)
		ui.Box(c).Width(actionsW).Shrink(0).AlignItems(ui.End).Children(actions)
	})
}

// taskColumnWidths are the fixed widths of the tasks card, which the header
// and the rows share. A window as narrow as the smallest one gives the
// fixed columns less: the name and where the task sends the files matter
// more than the exact widths of the rest.
func (a *App) taskColumnWidths(c *ui.Context) (schedule, last, status, actions float32) {
	w, _ := c.Size()
	schedule, last, status, actions = taskColSchedule, taskColLast, taskColStatus, taskColActions
	if w < 1040 {
		schedule, last, status, actions = 116, 88, 116, 72
	}
	return schedule, last, status, actions
}

// taskDataColumns lays out the columns a task's row shows beside its
// buttons: the buttons sit outside the row that opens the editor.
func (a *App) taskDataColumns(c *ui.Context, scheduleW, lastW, statusW float32, name, route, schedule, last, status func()) {
	ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
		ui.Box(c).Grow(1).Basis(0).MinWidth(100).Children(name)
		ui.Box(c).Grow(1).Basis(0).MinWidth(100).Padding(0, 10, 0, 10).Children(route)
		ui.Box(c).Width(scheduleW).Shrink(0).Padding(0, 10, 0, 0).Children(schedule)
		ui.Box(c).Width(lastW).Shrink(0).Children(last)
		ui.Box(c).Width(statusW).Shrink(0).Padding(0, 10, 0, 14).AlignItems(ui.Start).Children(status)
	})
}

// taskRow is one task: what it moves, where to, when it runs, how it last
// went, and what one can do with it now.
func (a *App) taskRow(c *ui.Context, task domain.Task, i int) {
	p := paletteFor(c.Theme())
	row := ui.Row(c).Height(taskRowHeight).Padding(0, taskRowPad).AlignItems(ui.Center)
	row.Transition(quickTransition)
	if row.Hovered() {
		row.Background(p.hover)
	}
	// The task itself opens its editor; the buttons beside it keep their
	// own clicks, which is why they sit outside the row-wide button.
	row.Children(func() {
		body := ui.ButtonBase(c.Key(task.ID + "-open")).Grow(1).MinWidth(0).Shrink(1).
			Justify(ui.Start).AlignItems(ui.Center).Label(locales.TaskOpen(task.Name))
		body.Children(func() {
			scheduleW, lastW, statusW, _ := a.taskColumnWidths(c)
			a.taskDataColumns(c, scheduleW, lastW, statusW,
				func() { a.taskNameCell(c, task) },
				func() { a.taskRouteCell(c, task) },
				func() { a.scheduleCell(c, task) },
				func() { a.lastRunCell(c, task) },
				func() { a.taskStatusCell(c, task) })
		})
		if body.Clicked() {
			a.openTaskForm(&task)
		}
		actionsW := func() float32 {
			_, _, _, w := a.taskColumnWidths(c)
			return w
		}()
		ui.Box(c).Width(actionsW).Shrink(0).AlignItems(ui.End).Children(func() {
			a.taskActions(c, task)
		})
	})
}

// taskNameCell is the task's name over the folder it moves.
func (a *App) taskNameCell(c *ui.Context, task domain.Task) {
	p := paletteFor(c.Theme())
	icon := icons.Copy
	if task.Mode == domain.ModeSync {
		icon = icons.Transfer
	}
	ui.Row(c).Gap(10).AlignItems(ui.Center).MinWidth(0).Children(func() {
		iconTile(c, icon, 30, p.accent, p.accentSoft)
		ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
			ui.Text(c, task.Name).FontSize(sizeBody).FontWeight(570).TextColor(p.text).
				SingleLine().Ellipsis("…").Tooltip(task.Name)
			source := a.locationText(task.Source)
			ui.Text(c, source).FontSize(sizeMicro).TextColor(p.muted).
				SingleLine().Ellipsis("…").Tooltip(source)
		})
	})
}

// taskRouteCell says where the task sends the files, and how.
func (a *App) taskRouteCell(c *ui.Context, task domain.Task) {
	p := paletteFor(c.Theme())
	route := locales.TasksRouteCopy(a.locationText(task.Destination))
	if task.Mode == domain.ModeSync {
		route = locales.TasksRouteSync(a.locationText(task.Destination))
	}
	ui.Text(c, route).FontSize(sizeMicro).TextColor(p.muted).SingleLine().Ellipsis("…").Tooltip(route)
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

// scheduleCell says how often the task runs, and when it does so next.
func (a *App) scheduleCell(c *ui.Context, task domain.Task) {
	p := paletteFor(c.Theme())
	if !task.Schedule.On() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icons.Idle).Size(11, 11).TextColor(p.faint)
			ui.Text(c, locales.TasksManual()).TextColor(p.muted).FontSize(sizeSmall).SingleLine()
		})
		return
	}
	ui.Column(c).MinWidth(0).Gap(1).Children(func() {
		ui.Text(c, scheduleText(c, task.Schedule)).FontSize(sizeSmall).TextColor(p.text).
			SingleLine().Ellipsis("…")
		if next := a.svc.NextRun(task); !next.IsZero() {
			ui.Text(c, locales.TasksNextRun(compactDateTime(c, next))).
				FontSize(sizeMicro).TextColor(p.muted).SingleLine().Ellipsis("…").
				FontFeatures("tnum")
		}
	})
}

// lastRunCell says when the task last ran; the status has a column of its
// own, so a run starting or ending never changes this cell's height.
func (a *App) lastRunCell(c *ui.Context, task domain.Task) {
	p := paletteFor(c.Theme())
	last, ok := a.lastRuns[task.ID]
	if !ok {
		ui.Text(c, locales.TasksNever()).TextColor(p.muted).FontSize(sizeSmall).SingleLine()
		return
	}
	ui.Text(c, formatTime(c, last.StartedAt)).TextColor(p.muted).FontSize(sizeMicro).
		SingleLine().Ellipsis("…").FontFeatures("tnum")
}

// taskStatusCell shows how the task's latest run went, or how the run in
// progress is going. Only the status capsule takes room in the row, so it
// is centered with the other columns; the progress bar floats below it
// without changing the row's height.
func (a *App) taskStatusCell(c *ui.Context, task domain.Task) {
	p := paletteFor(c.Theme())
	run, running := a.active[task.ID]
	last, ran := a.lastRuns[task.ID]
	if !running && !ran {
		return
	}
	ui.Column(c).AlignItems(ui.Start).Children(func() {
		switch {
		case running && run.Queued:
			pill(c, "queued-"+task.ID, icons.Clock, locales.TaskWaiting(), p.muted, p.neutralSoft)
		case running:
			statusPill(c, domain.StatusRunning)
		default:
			statusPill(c, last.Status)
		}
		if running && !run.Queued {
			taskProgress(c, task.Name, run.Progress.Bytes, run.Progress.TotalBytes).
				Absolute().Left(0).Top(26)
		}
	})
}

// taskProgress is the thin bar under a running task's status: the same
// height whether it shows or not.
func taskProgress(c *ui.Context, label string, done, total int64) ui.Element {
	p := paletteFor(c.Theme())
	track := ui.Box(c).Height(4).Width(92).Radius(2).Background(p.neutralSoft).Clip()
	v := float32(0)
	if total > 0 {
		v = track.AnimateTo("p", float32(done)/float32(total), ui.Smooth)
	}
	track.Children(func() {
		ui.Box(c).HeightPercent(100).WidthPercent(max(v, 0) * 100).Radius(2).
			Background(p.accent)
	})
	return track.Label(label)
}

func (a *App) taskActions(c *ui.Context, task domain.Task) {
	ui.Row(c).Gap(4).Justify(ui.End).Children(func() {
		if _, running := a.active[task.ID]; running {
			if iconAction(c, task.ID+"-stop", icons.Stop, locales.TaskStop()).Clicked() {
				a.stop(task.ID)
			}
		} else if iconAction(c, task.ID+"-run", icons.Run, locales.TaskRun()).Clicked() {
			a.run(task, false, false)
		}
		a.taskMenu(c, task)
	})
}

// taskMenu is the row's menu of the other things one can do with a task.
func (a *App) taskMenu(c *ui.Context, task domain.Task) {
	p := paletteFor(c.Theme())
	more := ui.ButtonBase(c).Size(taskActionWidth, taskActionWidth).Radius(8).Center().
		Label(locales.TaskActions(task.Name)).TextColor(p.muted)
	hoverRow(c, more)
	more.Children(func() { ui.Icon(c, icons.More).Size(15, 15) })
	more.Menu(func(m *ui.Menu) {
		_, running := a.active[task.ID]
		if m.Item(locales.TaskDryRun()).Disabled(running).Chosen() {
			a.run(task, true, false)
		}
		if task.Mode == domain.ModeSync && m.Item(locales.TaskRunAnyway()).Disabled(running).Chosen() {
			a.confirmRunAnyway(task)
		}
		m.Separator()
		if m.Item(locales.TaskDelete()).Disabled(running).Chosen() {
			a.confirmDeleteTask(task)
		}
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
