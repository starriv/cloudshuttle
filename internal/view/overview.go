package view

import (
	"math"
	"strconv"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/internal/format"
	"github.com/starriv/cloudshuttle/internal/icons"
	"github.com/starriv/cloudshuttle/internal/service"
	"github.com/starriv/cloudshuttle/locales"
)

// The overview answers one question at a glance: are the backups well?
// From the top: the counts, each a way to its page; the runs in progress;
// the runs of the last seven days, a day that failed in the danger color;
// the tasks, a row each; the latest runs, repeats of a success folded into
// one row. Healthy data is muted; color goes to what needs attention.
func (a *App) overviewPage(c *ui.Context) {
	header(c, locales.NavOverview(), nil)
	days := a.lastDays()

	pageScroll(c).Children(func() {
		ui.Column(c).Gap(24).PaddingY(4).Children(func() {
			a.overviewStats(c, days)
			if len(a.running) > 0 {
				section(c, locales.OverviewRunning(), func() {
					ui.Column(c).Gap(8).Children(func() {
						for _, run := range a.running {
							a.activeCard(c, run)
						}
					})
				})
			}
			section(c, locales.OverviewLast7Days(), func() { a.weekChart(c, days) })
			if len(a.tasks) > 0 {
				section(c, locales.NavTasks(), func() { a.taskRows(c) })
			}
			section(c, locales.OverviewRecent(), func() { a.recentRuns(c) })
		})
	})
}

func section(c *ui.Context, title string, body func()) {
	ui.Column(c).Gap(12).Children(func() {
		ui.Text(c, title).FontSize(16).FontWeight(500)
		body()
	})
}

// listCard is the frame of a list: rows divided by hairlines inside one
// card, clipped to its corners so a row's hover fill follows them.
func listCard(c *ui.Context) ui.Element {
	return ui.Column(c).Radius(12).Border(1, c.Theme().Border).Clip()
}

// rowLook fills a row that leads somewhere as the pointer is on it, and
// more as it is pressed.
func rowLook(c *ui.Context, row ui.Element) {
	t := c.Theme()
	row.Transition(ui.ElementTransition{Colors: true, Duration: 120 * time.Millisecond})
	switch {
	case row.Pressed():
		row.Background(t.SurfacePressed)
	case row.Hovered():
		row.Background(t.SurfaceHover)
	}
}

// day is a calendar day of the last seven: when it starts, how many runs
// started in it and how many of them failed.
type day struct {
	start        time.Time
	runs, failed int
}

// weekStart is midnight of the first of the seven calendar days the
// overview counts: six days before today.
func weekStart(now time.Time) time.Time {
	y, m, d := now.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, now.Location()).AddDate(0, 0, -6)
}

// lastDays counts the runs of the last seven calendar days, today last.
// The count of the week above and the chart read these, so they agree.
func (a *App) lastDays() []day {
	start := weekStart(a.now())
	days := make([]day, 7)
	for i := range days {
		days[i].start = start.AddDate(0, 0, i)
	}
	for _, r := range a.week {
		for i := len(days) - 1; i >= 0; i-- {
			if !r.StartedAt.Before(days[i].start) {
				days[i].runs++
				if r.Status == domain.StatusFailed {
					days[i].failed++
				}
				break
			}
		}
	}
	return days
}

// overviewStats are the counts at the top, in one card divided by
// hairlines, each a way to its page.
func (a *App) overviewStats(c *ui.Context, days []day) {
	t := c.Theme()
	scheduled, failing, firstFailing := 0, 0, ""
	for _, task := range a.tasks {
		if task.Schedule.On() {
			scheduled++
		}
		if a.lastRuns[task.ID].Status == domain.StatusFailed {
			if failing == 0 {
				firstFailing = task.Name
			}
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
	weekRuns, weekFailed := 0, 0
	for _, d := range days {
		weekRuns += d.runs
		weekFailed += d.failed
	}

	tasks := statCard{
		icon: icons.Tasks, label: locales.OverviewTasks(), n: len(a.tasks),
		note: locales.OverviewScheduledOf(scheduled), open: func() { a.page = pageTasks },
	}
	storage := statCard{
		icon: icons.Storages, label: locales.OverviewStorages(), n: len(a.storages),
		note: locales.OverviewWorking(working), open: func() { a.page = pageStorages },
	}
	if broken > 0 {
		storage.note, storage.noteIcon, storage.noteColor = locales.OverviewBroken(broken), icons.Failed, t.Danger
	}
	// Failures of the week are past: they warn, while a task whose last
	// run failed is the problem, in the danger color.
	week := statCard{
		icon: icons.History, label: locales.OverviewWeek(), n: weekRuns,
		note: locales.OverviewNoneFailed(), open: func() { a.openHistory("", "", "") },
	}
	if weekFailed > 0 {
		week.note, week.noteIcon, week.noteColor = locales.OverviewSomeFailed(weekFailed), icons.Warning, t.Warning
		week.open = func() { a.openHistory("", domain.StatusFailed, "") }
	}
	fail := statCard{
		icon: icons.Failed, label: locales.OverviewFailing(), n: failing,
		note: locales.OverviewAllSucceeded(), tip: locales.OverviewAllWell(),
		open: func() { a.openHistory("", domain.StatusFailed, "") },
	}
	if failing > 0 {
		// One failing task is named; more are counted, so the note never
		// reads as though only the first one failed.
		fail.color, fail.note, fail.noteIcon, fail.noteColor = t.Danger, firstFailing, icons.Failed, t.Danger
		if failing > 1 {
			fail.note = locales.OverviewFailingTasks(failing)
		}
		fail.tip = locales.OverviewFailingNote()
	}

	ui.Grid(c).ColumnTracks(ui.Fr(1), ui.Fr(1), ui.Fr(1), ui.Fr(1)).Radius(12).Border(1, t.Border).Children(func() {
		for i, s := range []statCard{tasks, storage, week, fail} {
			cell := ui.Row(c.Key(i)).Padding(6).MinWidth(0).AlignItems(ui.Stretch)
			if i > 0 {
				cell.BorderWidth(0, 0, 0, 1).BorderColor(t.Border)
			}
			cell.Children(func() { a.statCard(c, s) })
		}
	})
}

type statCard struct {
	icon  *ui.SVG
	label string
	n     int
	color ui.Color // of the number; the text color when zero
	note  string   // what the number is made of, in a word or two, below it
	// noteIcon and noteColor flag a note that needs attention; a note
	// without them is muted.
	noteIcon  *ui.SVG
	noteColor ui.Color
	tip       string // explains the number in full; the note when empty
	open      func() // shows the number in full, as there is something to show
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
				FontSize(28).FontWeight(500).TextColor(color).FontFeatures("tnum")
			noteColor := s.noteColor
			if noteColor == (ui.Color{}) {
				noteColor = t.TextMuted
			}
			ui.Row(c).Gap(4).AlignItems(ui.Center).MinWidth(0).TextColor(noteColor).Children(func() {
				if s.noteIcon != nil {
					ui.Icon(c, s.noteIcon)
				}
				ui.Text(c, s.note).FontSize(12).SingleLine().Ellipsis("…").MinWidth(0)
			})
		})
	}
	tip := s.tip
	if tip == "" {
		tip = s.note
	}
	if s.n == 0 || s.open == nil {
		card := ui.Row(c).Grow(1).MinWidth(0).Padding(8, 10).Tooltip(tip)
		card.Children(func() { content(card) })
		return
	}
	card := ui.ButtonBase(c).Grow(1).MinWidth(0).Justify(ui.Start).AlignItems(ui.Stretch).
		Padding(8, 10).Radius(8).Label(s.label + " " + strconv.Itoa(s.n)).Tooltip(tip)
	rowLook(c, card)
	card.Children(func() { content(card) })
	if card.Clicked() {
		s.open()
	}
}

// cardLook gives a card that leads somewhere its face: a quiet border,
// filled as the pointer is on it, and more as it is pressed.
func cardLook(c *ui.Context, card ui.Element) {
	t := c.Theme()
	card.Justify(ui.Start).AlignItems(ui.Stretch).Padding(14, 16).Radius(12).Border(1, t.Border).
		Background(t.Background)
	rowLook(c, card)
}

// weekChartHeight is the height of the tallest bar of the week.
const weekChartHeight = 72

// weekChart shows the runs of each of the last seven days as a bar, its
// failed part on top in the danger color, the day's counts in its tooltip.
// A day leads nowhere: the history has no filter by day, and the counts
// above lead to the failures.
func (a *App) weekChart(c *ui.Context, days []day) {
	t := c.Theme()
	locale := c.Region().FormatLocale(c.Locale())
	most := 1
	for _, d := range days {
		most = max(most, d.runs)
	}
	ui.Row(c).Gap(4).Padding(8).Radius(12).Border(1, t.Border).Children(func() {
		for i, d := range days {
			date := i18n.FormatDate(locale, d.start, i18n.DateOptions{Skeleton: "MMMEd"})
			tip := locales.OverviewDayRuns(date, d.runs)
			if d.failed > 0 {
				tip = locales.OverviewDayRunsFailed(date, d.runs, d.failed)
			}
			weekday := i18n.FormatDate(locale, d.start, i18n.DateOptions{Skeleton: "E"})
			if i == len(days)-1 {
				weekday = locales.OverviewToday()
			}
			body := func() {
				ui.Column(c).Height(weekChartHeight).FillWidth().Justify(ui.End).AlignItems(ui.Center).Children(func() {
					if d.runs == 0 {
						ui.Box(c).Height(2).FillWidth().MaxWidth(48).Radius(1).Background(t.Border)
						return
					}
					h := max(float32(4), weekChartHeight*float32(d.runs)/float32(most))
					failedH := float32(0)
					if d.failed > 0 {
						failedH = max(float32(3), h*float32(d.failed)/float32(d.runs))
					}
					ui.Column(c).Height(h).FillWidth().MaxWidth(48).Radius(6, 6, 2, 2).Clip().Children(func() {
						if failedH > 0 {
							ui.Box(c).Height(failedH).FillWidth().Background(t.Danger)
						}
						// Selection is the theme's pale accent, set for each
						// appearance: healthy runs stay calm.
						ok := ui.Box(c).Grow(1).FillWidth().Background(t.Selection)
						if failedH == 0 {
							ok.BorderWidth(2, 0, 0, 0).BorderColor(t.Accent)
						}
					})
				})
				ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
					count := "—"
					if d.runs > 0 {
						count = strconv.Itoa(d.runs)
					}
					ui.Text(c, count).FontFeatures("tnum").TextColor(t.Text)
					if d.failed > 0 {
						ui.Row(c).Gap(2).AlignItems(ui.Center).TextColor(t.Danger).Children(func() {
							ui.Icon(c, icons.Failed)
							ui.Text(c, strconv.Itoa(d.failed)).FontSize(12).FontFeatures("tnum")
						})
					}
				})
				label := ui.Text(c, weekday).FontSize(12).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
				if i == len(days)-1 {
					label.TextColor(t.Text).FontWeight(500)
				}
			}
			ui.Column(c.Key(i)).Grow(1).Basis(0).MinWidth(0).Padding(8, 4).Gap(6).
				AlignItems(ui.Center).Justify(ui.End).Tooltip(tip).Children(body)
		}
	})
}

// activeCard shows a run in progress: its task, how far it is, a button
// that stops it, and, clicked, its details and log in the history.
func (a *App) activeCard(c *ui.Context, run service.Active) {
	t := c.Theme()
	p := run.Progress
	ui.Row(c.Key(run.Run.ID)).AlignItems(ui.Center).Gap(12).Transition(listTransition).Children(func() {
		card := ui.ButtonBase(c).Grow(1).MinWidth(0).Label(locales.OverviewOpenRun(run.Run.TaskName))
		cardLook(c, card)
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

// taskRows lists the tasks, a row each: where it goes, how it last ran and
// when it runs next. Clicked, a row shows the task's runs; its button runs
// it.
func (a *App) taskRows(c *ui.Context) {
	t := c.Theme()
	listCard(c).Children(func() {
		for i, task := range a.tasks {
			_, running := a.active[task.ID]
			line := ui.Row(c.Key(task.ID)).AlignItems(ui.Center).Gap(8).Padding(0, 12, 0, 0).Transition(listTransition)
			if i > 0 {
				line.BorderWidth(1, 0, 0, 0).BorderColor(t.Border)
			}
			line.Children(func() {
				row := ui.ButtonBase(c).Grow(1).MinWidth(0).Justify(ui.Start).Gap(16).Padding(10, 16).
					Label(locales.OverviewOpenTask(task.Name))
				rowLook(c, row)
				row.Children(func() {
					ui.Column(c).Gap(2).Grow(2).Basis(0).MinWidth(0).Children(func() {
						ui.Text(c, task.Name).FontWeight(500).SingleLine().Ellipsis("…")
						ui.Text(c, a.locationText(task.Source)).FontSize(12).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
					})
					route := locales.TasksRouteCopy(a.locationText(task.Destination))
					if task.Mode == domain.ModeSync {
						route = locales.TasksRouteSync(a.locationText(task.Destination))
					}
					ui.Text(c, route).FontSize(12).TextColor(t.TextMuted).SingleLine().Ellipsis("…").
						Grow(2).Basis(0).MinWidth(0)
					// Wide enough for a status, a date and a time whole.
					ui.Column(c).Gap(2).Width(280).Children(func() {
						switch last, ok := a.lastRuns[task.ID]; {
						case running:
							status(c, domain.StatusRunning)
						case ok:
							ui.Row(c).Gap(8).AlignItems(ui.Center).MinWidth(0).Children(func() {
								quietStatus(c, last.Status)
								ui.Text(c, formatTime(c, last.StartedAt)).FontSize(12).TextColor(t.TextMuted).
									SingleLine().Ellipsis("…").MinWidth(0)
							})
						default:
							ui.Text(c, locales.TasksNever()).TextColor(t.TextMuted).SingleLine()
						}
						next := locales.TasksManual()
						if n := a.svc.NextRun(task); !n.IsZero() {
							next = locales.TasksNextRun(formatTime(c, n))
						}
						ui.Text(c, next).FontSize(12).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
					})
				})
				if row.Clicked() {
					a.openHistory(task.ID, "", "")
				}
				if iconButton(c, icons.Run, locales.TaskRun(), false).Disabled(running).Clicked() {
					a.run(task, false, false)
				}
			})
		}
	})
}

// runGroup is a row of the latest runs: one run, or successes of one task
// in a row, newest first, folded so a task that runs every hour doesn't
// fill the list with the same line.
type runGroup []domain.Run

// recentGroups folds the latest runs into at most recentRuns rows.
func (a *App) recentGroups() []runGroup {
	var groups []runGroup
	for _, r := range a.recent {
		if n := len(groups); n > 0 {
			last := groups[n-1][0]
			if r.Status == domain.StatusSuccess && last.Status == domain.StatusSuccess &&
				r.TaskID == last.TaskID && r.DryRun == last.DryRun {
				groups[n-1] = append(groups[n-1], r)
				continue
			}
		}
		if len(groups) == recentRuns {
			break
		}
		groups = append(groups, runGroup{r})
	}
	return groups
}

// recentRuns lists the latest runs, each leading to its details and log;
// a folded row leads to the newest of its runs.
func (a *App) recentRuns(c *ui.Context) {
	t := c.Theme()
	groups := a.recentGroups()
	if len(groups) == 0 {
		ui.Text(c, locales.OverviewNoRuns()).TextColor(t.TextMuted)
		return
	}
	listCard(c).Children(func() {
		for i, g := range groups {
			r := g[0]
			// A run that starts slides in at the top; the others make room.
			// A folded row is keyed by its oldest run, which a new success
			// joining it leaves in place, so the row stays rather than
			// arriving again.
			label := locales.OverviewOpenRun(r.TaskName)
			if len(g) > 1 {
				// The row shows the totals of the folded runs and leads to
				// all of them, not only the newest.
				label = locales.OverviewOpenTask(r.TaskName)
			}
			row := ui.ButtonBase(c.Key(g[len(g)-1].ID)).Justify(ui.Start).Gap(16).Padding(10, 16).
				Label(label).Transition(listTransition)
			if i > 0 {
				row.BorderWidth(1, 0, 0, 0).BorderColor(t.Border)
			}
			rowLook(c, row)
			var sum domain.Stats
			for _, run := range g {
				sum.Transfers += run.Stats.Transfers
				sum.Bytes += run.Stats.Bytes
				sum.Deletes += run.Stats.Deletes
			}
			row.Children(func() {
				ui.Column(c).Width(110).Children(func() { quietStatus(c, r.Status) })
				ui.Row(c).Gap(8).AlignItems(ui.Center).Grow(1).MinWidth(0).Children(func() {
					ui.Text(c, r.TaskName).SingleLine().Ellipsis("…").MinWidth(0)
					if r.DryRun {
						ui.Badge(c, locales.DryRunBadge())
					}
					if len(g) > 1 {
						ui.Text(c, locales.OverviewRunCount(len(g))).TextColor(t.TextMuted).SingleLine().FontFeatures("tnum")
					}
				})
				ui.Row(c).Gap(16).Justify(ui.End).TextColor(t.TextMuted).Children(func() {
					if sum.Deletes > 0 {
						ui.Text(c, locales.HistoryDeletedShort(int(sum.Deletes))).SingleLine().FontFeatures("tnum")
					}
					ui.Text(c, transferred(sum)).SingleLine().FontFeatures("tnum")
				})
				// As wide as a range over two days, so the columns line up.
				ui.Text(c, groupTime(c, g)).TextColor(t.TextMuted).SingleLine().Ellipsis("…").FontFeatures("tnum").
					Width(260).TextAlign(ui.End)
			})
			if row.Clicked() {
				if len(g) > 1 {
					a.openHistory(r.TaskID, "", "")
				} else {
					a.openHistory("", "", r.ID)
				}
			}
		}
	})
}

// groupTime is when a row's runs started: the time of one run, or from the
// oldest to the newest, the newest's date left out when it is the same day.
func groupTime(c *ui.Context, g runGroup) string {
	newest, oldest := g[0].StartedAt, g[len(g)-1].StartedAt
	if len(g) == 1 {
		return formatTime(c, newest)
	}
	to := formatTime(c, newest)
	if y, m, d := newest.Date(); oldest.Year() == y && oldest.Month() == m && oldest.Day() == d {
		to = i18n.FormatDate(c.Region().FormatLocale(c.Locale()), newest, i18n.DateOptions{Time: i18n.ShortStyle})
	}
	return locales.OverviewTimeRange(formatTime(c, oldest), to)
}

// quietStatus shows a status as status does, but a success muted: on the
// overview healthy runs stay calm, so the color goes only to what needs
// attention. The icon and the word still tell the statuses apart.
func quietStatus(c *ui.Context, s domain.Status) {
	if s == domain.StatusSuccess {
		statusIn(c, s, c.Theme().TextMuted)
		return
	}
	status(c, s)
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

// OpenRun opens a run selected from the status menu. Call on the main thread.
// The view is kept current by Reload on every change, so this only shows the
// history of that run, without reading it all again.
func (a *App) OpenRun(taskID, runID string) {
	a.openHistory(taskID, "", runID)
}
