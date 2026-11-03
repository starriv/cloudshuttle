package view

import (
	"math"
	"strconv"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/internal/format"
	"github.com/starriv/cloudshuttle/internal/icons"
	"github.com/starriv/cloudshuttle/internal/service"
	"github.com/starriv/cloudshuttle/locales"
)

// The overview sums up the app's state in cards, each leading to where it
// is in full: the counts to their pages, a run to its details and log in
// the history, a task to its runs.
func (a *App) overviewPage(c *ui.Context) {
	header(c, locales.NavOverview(), nil)

	ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
		ui.Column(c).Gap(24).PaddingY(4).Children(func() {
			a.overviewStats(c)
			if len(a.running) > 0 {
				section(c, locales.OverviewRunning(), func() {
					ui.Column(c).Gap(8).Children(func() {
						for _, run := range a.running {
							a.activeCard(c, run)
						}
					})
				})
			}
			if len(a.tasks) > 0 {
				section(c, locales.NavTasks(), func() {
					// As many columns as the window is wide for: each card
					// is at least taskCardWidth, and they share the row.
					// Empty cells after the last card keep it as wide as the
					// others; they wrap onto a line of no height, whose gap
					// the margin takes back.
					ui.Row(c).Wrap().Gap(12).Margin(0, 0, -12, 0).AlignItems(ui.Stretch).Children(func() {
						for _, task := range a.tasks {
							ui.Column(c.Key(task.ID)).Basis(taskCardWidth).Grow(1).MinWidth(0).Children(func() {
								a.taskCard(c, task)
							})
						}
						for i := range taskCardFillers {
							ui.Box(c.Key(i)).Basis(taskCardWidth).Grow(1).MinWidth(0).Height(0)
						}
					})
				})
			}
			section(c, locales.OverviewRecent(), func() { a.recentRuns(c) })
		})
	})
}

// taskCardWidth is the narrowest a task card of the overview gets, and
// taskCardFillers the most columns past the first a window is wide for.
const (
	taskCardWidth   = 360
	taskCardFillers = 4
)

func section(c *ui.Context, title string, body func()) {
	ui.Column(c).Gap(12).Children(func() {
		ui.Text(c, title).FontSize(16).FontWeight(500)
		body()
	})
}

// overviewStats are the counts at the top, each a way to its page.
func (a *App) overviewStats(c *ui.Context) {
	t := c.Theme()
	scheduled, failing := 0, 0
	for _, task := range a.tasks {
		if task.Schedule.On() {
			scheduled++
		}
		if a.lastRuns[task.ID].Status == domain.StatusFailed {
			failing++
		}
	}
	working, broken := 0, 0
	for _, s := range a.storages {
		switch {
		case !s.Check.Checked():
		case s.Check.OK:
			working++
		default:
			broken++
		}
	}
	weekFailed := 0
	for _, r := range a.week {
		if r.Status == domain.StatusFailed {
			weekFailed++
		}
	}

	ui.Grid(c).ColumnTracks(ui.Fr(1), ui.Fr(1), ui.Fr(1), ui.Fr(1)).Gap(12).Children(func() {
		a.statCard(c, statCard{
			icon: icons.Tasks, label: locales.OverviewTasks(), n: len(a.tasks),
			note: locales.OverviewScheduledOf(scheduled), open: func() { a.page = pageTasks },
		})
		storageNote := locales.OverviewWorking(working)
		if broken > 0 {
			storageNote = locales.OverviewBroken(broken)
		}
		a.statCard(c, statCard{
			icon: icons.Storages, label: locales.OverviewStorages(), n: len(a.storages),
			note: storageNote, open: func() { a.page = pageStorages },
		})
		weekNote := locales.OverviewNoneFailed()
		if weekFailed > 0 {
			weekNote = locales.OverviewSomeFailed(weekFailed)
		}
		a.statCard(c, statCard{
			icon: icons.History, label: locales.OverviewWeek(), n: len(a.week),
			note: weekNote, open: func() { a.openHistory("", "", "") },
		})
		failColor := t.Text
		failNote := locales.OverviewAllWell()
		if failing > 0 {
			failColor, failNote = t.Danger, locales.OverviewFailingNote()
		}
		a.statCard(c, statCard{
			icon: icons.Failed, label: locales.OverviewFailing(), n: failing, color: failColor,
			note: failNote, open: func() { a.openHistory("", domain.StatusFailed, "") },
		})
	})
}

type statCard struct {
	icon  *ui.SVG
	label string
	n     int
	color ui.Color // of the number; the text color when zero
	note  string   // what the number is made of, below it
	open  func()   // shows the number in full, as there is something to show
}

// statCard shows a count: a button leading to its page while it is not
// zero, plain otherwise, as there is nothing to see there.
func (a *App) statCard(c *ui.Context, s statCard) {
	t := c.Theme()
	color := s.color
	if color == (ui.Color{}) {
		color = t.Text
	}
	content := func(card ui.Element) {
		// The number counts to a new value rather than jumping.
		shown := card.AnimateTo("n", float32(s.n), ui.Smooth)
		ui.Column(c).Gap(6).Grow(1).MinWidth(0).Children(func() {
			ui.Row(c).Gap(6).AlignItems(ui.Center).TextColor(t.TextMuted).Children(func() {
				ui.Icon(c, s.icon)
				ui.Text(c, s.label).SingleLine().Ellipsis("…").MinWidth(0)
			})
			ui.Text(c, strconv.Itoa(int(math.Round(float64(shown))))).
				FontSize(28).FontWeight(500).TextColor(color)
			ui.Text(c, s.note).FontSize(12).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
		})
	}
	if s.n == 0 || s.open == nil {
		card := ui.Row(c).MinWidth(0)
		cardLook(c, card, false)
		card.Children(func() { content(card) })
		return
	}
	card := ui.ButtonBase(c).Label(s.label + " " + strconv.Itoa(s.n)).Tooltip(s.note)
	cardLook(c, card, true)
	card.Children(func() { content(card) })
	if card.Clicked() {
		s.open()
	}
}

// cardLook gives a card its face: a quiet border, filled as the pointer is
// on one that leads somewhere, and more as it is pressed.
func cardLook(c *ui.Context, card ui.Element, leads bool) {
	t := c.Theme()
	card.Justify(ui.Start).AlignItems(ui.Stretch).Padding(14, 16).Radius(12).Border(1, t.Border).
		Background(t.Background).Transition(ui.ElementTransition{Colors: true, Duration: 120 * time.Millisecond})
	if !leads {
		return
	}
	switch {
	case card.Pressed():
		card.Background(t.SurfacePressed)
	case card.Hovered():
		card.Background(t.SurfaceHover)
	}
}

// activeCard shows a run in progress: its task, how far it is, a button
// that stops it, and, clicked, its details and log in the history.
func (a *App) activeCard(c *ui.Context, run service.Active) {
	t := c.Theme()
	p := run.Progress
	ui.Row(c.Key(run.Run.ID)).AlignItems(ui.Center).Gap(12).Transition(listTransition).Children(func() {
		card := ui.ButtonBase(c).Grow(1).MinWidth(0).Label(locales.OverviewOpenRun(run.Run.TaskName))
		cardLook(c, card, true)
		card.Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(6).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, run.Run.TaskName).FontWeight(500).SingleLine().Ellipsis("…").MinWidth(0)
					if run.Run.DryRun {
						ui.Badge(c, locales.DryRunBadge())
					}
				})
				if run.Queued {
					ui.Text(c, locales.TaskWaiting()).TextColor(t.TextMuted).SingleLine()
					return
				}
				progressBar(c, run.Run.TaskName, p.Bytes, p.TotalBytes)
				ui.Row(c).Gap(12).TextColor(t.TextMuted).Children(func() {
					ui.Text(c, locales.Progress(format.Bytes(p.Bytes), format.Bytes(p.TotalBytes))).FontFeatures("tnum")
					if p.Speed > 0 {
						ui.Text(c, locales.Speed(format.Bytes(int64(p.Speed)))).FontFeatures("tnum")
					}
				})
			})
		})
		if card.Clicked() {
			a.openHistory("", "", run.Run.ID)
		}
		if iconButton(c, icons.Stop, locales.TaskStop(), false).Clicked() {
			a.stop(run.Run.TaskID)
		}
	})
}

// taskCard shows a task: where it goes, how it last ran and when it runs
// next. Clicked, it shows the task's runs; its button runs it.
func (a *App) taskCard(c *ui.Context, task domain.Task) {
	t := c.Theme()
	_, running := a.active[task.ID]
	ui.Column(c).Gap(10).Padding(14, 16).Radius(12).Border(1, t.Border).MinWidth(0).Children(func() {
		head := ui.ButtonBase(c).Justify(ui.Start).AlignItems(ui.Stretch).Radius(8).MinWidth(0).
			Label(locales.OverviewOpenTask(task.Name)).
			Transition(ui.ElementTransition{Colors: true, Duration: 120 * time.Millisecond})
		switch {
		case head.Pressed():
			head.Background(t.SurfacePressed)
		case head.Hovered():
			head.Background(t.SurfaceHover)
		}
		head.Children(func() {
			ui.Column(c).Gap(4).Grow(1).MinWidth(0).Padding(4, 6).Children(func() {
				ui.Text(c, task.Name).FontWeight(500).SingleLine().Ellipsis("…")
				ui.Text(c, a.locationText(task.Source)).FontSize(12).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
				route := locales.TasksRouteCopy(a.locationText(task.Destination))
				if task.Mode == domain.ModeSync {
					route = locales.TasksRouteSync(a.locationText(task.Destination))
				}
				ui.Text(c, route).FontSize(12).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
			})
		})
		if head.Clicked() {
			a.openHistory(task.ID, "", "")
		}
		ui.Row(c).Gap(12).AlignItems(ui.Center).PaddingX(6).Children(func() {
			ui.Column(c).Gap(2).Grow(1).MinWidth(0).Children(func() {
				if last, ok := a.lastRuns[task.ID]; ok && !running {
					ui.Row(c).Gap(8).AlignItems(ui.Center).MinWidth(0).Children(func() {
						status(c, last.Status)
						ui.Text(c, formatTime(c, last.StartedAt)).FontSize(12).TextColor(t.TextMuted).
							SingleLine().Ellipsis("…").MinWidth(0)
						if last.Stats.Deletes > 0 {
							ui.Text(c, locales.HistoryDeletedShort(int(last.Stats.Deletes))).FontSize(12).
								TextColor(t.TextMuted).SingleLine()
						}
					})
				} else if !running {
					ui.Text(c, locales.TasksNever()).TextColor(t.TextMuted).SingleLine()
				} else {
					status(c, domain.StatusRunning)
				}
				next := locales.TasksManual()
				if n := a.svc.NextRun(task); !n.IsZero() {
					next = locales.TasksNextRun(formatTime(c, n))
				}
				ui.Text(c, next).FontSize(12).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
			})
			if iconButton(c, icons.Run, locales.TaskRun(), false).Disabled(running).Clicked() {
				a.run(task, false, false)
			}
		})
	})
}

// recentRuns lists the latest runs, each leading to its details and log.
func (a *App) recentRuns(c *ui.Context) {
	t := c.Theme()
	if len(a.recent) == 0 {
		ui.Text(c, locales.OverviewNoRuns()).TextColor(t.TextMuted)
		return
	}
	ui.Column(c).Radius(12).Border(1, t.Border).Children(func() {
		for i, r := range a.recent {
			// A run that starts slides in at the top; the others make room.
			row := ui.ButtonBase(c.Key(r.ID)).Justify(ui.Start).Gap(16).Padding(10, 16).
				Label(locales.OverviewOpenRun(r.TaskName)).Transition(listTransition)
			if i > 0 {
				row.BorderWidth(1, 0, 0, 0).BorderColor(t.Border)
			}
			switch {
			case row.Pressed():
				row.Background(t.SurfacePressed)
			case row.Hovered():
				row.Background(t.SurfaceHover)
			}
			row.Children(func() {
				ui.Column(c).Width(110).Children(func() { status(c, r.Status) })
				ui.Row(c).Gap(8).AlignItems(ui.Center).Grow(1).MinWidth(0).Children(func() {
					ui.Text(c, r.TaskName).SingleLine().Ellipsis("…").MinWidth(0)
					if r.DryRun {
						ui.Badge(c, locales.DryRunBadge())
					}
				})
				ui.Text(c, transferred(r.Stats)).TextColor(t.TextMuted).SingleLine().FontFeatures("tnum")
				if r.Stats.Deletes > 0 {
					ui.Text(c, locales.HistoryDeletedShort(int(r.Stats.Deletes))).
						TextColor(t.TextMuted).SingleLine().FontFeatures("tnum")
				}
				ui.Text(c, formatTime(c, r.StartedAt)).TextColor(t.TextMuted).SingleLine().
					Width(170).TextAlign(ui.End)
			})
			if row.Clicked() {
				a.openHistory("", "", r.ID)
			}
		}
	})
}

// openHistory shows the history of the task with taskID, or of every task,
// of a status, or of all, with the run with runID chosen, when given.
func (a *App) openHistory(taskID string, s domain.Status, runID string) {
	a.page = pageHistory
	taskOptions, statusOptions := a.historyTaskOptions(), a.historyStatusOptions()
	a.historyTask, a.historyStatus = taskOptions[0], statusOptions[0]
	for i, task := range a.tasks {
		if task.ID == taskID {
			a.historyTask = taskOptions[i+1]
		}
	}
	for i, hs := range historyStatuses {
		if hs == s {
			a.historyStatus = statusOptions[i+1]
		}
	}
	a.reloadHistory()
	a.historyChosen.Clear()
	a.historyRow = -1
	for i, r := range a.history {
		if r.ID == runID {
			a.historyChosen.Add(r.ID)
			a.historyRow = i
			a.historyList.ScrollIntoView(i)
		}
	}
}

// listTransition moves rows to their places, and brings new ones in.
var listTransition = ui.ElementTransition{
	Animation: &ui.Snappy,
	Position:  true,
	Enter:     &ui.Motion{Y: -6, Collapse: true},
	Exit:      &ui.Motion{Collapse: true},
}

// progressBar shows done of total, gliding between the updates the engine
// sends twice a second, or an indeterminate bar while the total is not yet
// known.
func progressBar(c *ui.Context, label string, done, total int64) {
	box := ui.Column(c).FillWidth()
	value := -1.0
	if total > 0 {
		value = float64(box.AnimateTo("p", float32(done)/float32(total), ui.Smooth))
	}
	box.Children(func() { ui.Progress(c, value).Label(label) })
}

func (a *App) stop(taskID string) {
	if err := a.svc.Cancel(taskID); err != nil && err != domain.ErrNotActive {
		a.toastError(err)
	}
}
