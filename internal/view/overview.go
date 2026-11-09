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
// A number for each thing that matters, then what ran lately, then the
// tasks themselves. Healthy data stays quiet; only what needs attention
// takes color, and always with a word beside it.

func (a *App) overviewPage(c *ui.Context) {
	days := a.lastDays()
	failing, scheduled, succeeded := a.taskHealth()
	allSucceeded := len(a.tasks) > 0 && succeeded == len(a.tasks) && a.loadErr == nil
	running := len(a.running)
	// Two columns of cards need the room; a window as narrow as the
	// smallest one stacks them instead of squeezing them.
	w, _ := c.Size()
	narrow := w-sidebarWidth-2*pagePad < 720

	// A first run has nothing to count yet: what to do next instead.
	if len(a.storages) == 0 && len(a.tasks) == 0 {
		pageHeader(c, locales.NavOverview(), locales.OverviewSubtitleWelcome(), nil)
		emptyPage(c, icons.Mark, locales.WelcomeTitle(), locales.WelcomeBody(), func() {
			if accentButton(c, "welcome", icons.Add, locales.WelcomeAction(), buttonPrimary).Clicked() {
				a.openStorageForm(nil)
			}
		})
		return
	}

	subtitle := locales.OverviewSubtitleNeutral()
	switch {
	case running > 0:
		subtitle = locales.OverviewSubtitleRunning()
	case failing > 0:
		subtitle = locales.OverviewSubtitleFailing(failing)
	case allSucceeded:
		subtitle = locales.OverviewSubtitleAll()
	}
	pageHeader(c, locales.NavOverview(), subtitle, func() {
		if accentButton(c, "overview-add", icons.Add, locales.TasksAdd(), buttonSoft).Clicked() {
			a.openTaskForm(nil)
		}
	})

	pageScroll(c).Children(func() {
		ui.Column(c).Gap(gutter).Padding(0, 0, pageBottom, 0).Children(func() {
			if failing > 0 {
				a.attentionBanner(c, failing)
			}
			a.overviewStats(c, days, failing, scheduled, narrow, allSucceeded)
			if len(a.tasks) == 0 {
				emptyPage(c, icons.Tasks, locales.OverviewReadyTitle(), locales.OverviewReadyBody(), func() {
					if accentButton(c, "ready", icons.Add, locales.OverviewReadyAction(), buttonPrimary).Clicked() {
						a.openTaskForm(nil)
					}
				})
				return
			}
			a.activityCard(c, days)
			if narrow {
				a.overviewTasks(c)
				if running > 0 {
					a.runningCard(c)
				}
				a.recentCard(c)
			} else {
				ui.Grid(c).ColumnTracks(ui.Fr(2), ui.Fr(1)).GapX(gutter).GapY(gutter).Children(func() {
					a.overviewTasks(c)
					ui.Column(c).Gap(gutter).Children(func() {
						if running > 0 {
							a.runningCard(c)
						}
						a.recentCard(c)
					})
				})
			}
		})
	})
}

// taskHealth counts the latest real outcomes and configured schedules.
func (a *App) taskHealth() (failing, scheduled, succeeded int) {
	for _, task := range a.tasks {
		if task.Schedule.On() {
			scheduled++
		}
		switch a.lastRealRuns[task.ID].Status {
		case domain.StatusFailed:
			failing++
		case domain.StatusSuccess:
			succeeded++
		}
	}
	return failing, scheduled, succeeded
}

// attentionBanner says how many tasks need a look, and leads to them.
func (a *App) attentionBanner(c *ui.Context, failing int) {
	p := paletteFor(c.Theme())
	ui.Row(c).Radius(cardRadius).Padding(14, 16).Gap(14).AlignItems(ui.Center).
		Background(p.dangerSoft).Border(1, p.danger.Alpha(0.18)).Children(func() {
		iconTile(c, icons.Warning, 34, p.danger, p.card.Alpha(0.6))
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, locales.OverviewAttentionTitle(failing)).FontSize(sizeBody).FontWeight(600).
				TextColor(p.text).SingleLine().Ellipsis("…")
			ui.Text(c, locales.OverviewAttentionBody()).FontSize(sizeSmall).TextColor(p.muted).
				SingleLine().Ellipsis("…")
		})
		if accentButton(c, "attention", icons.History, locales.OverviewAttentionAction(), buttonSoft).Clicked() {
			a.openHistory("", domain.StatusFailed, "")
		}
	})
}

// stat is one of the numbers at the top of the overview.
type stat struct {
	icon     *ui.SVG
	label    string
	value    int
	note     string
	noteIcon *ui.SVG
	noteTone ui.Color // the note's color; the palette's muted when zero
	open     func()
}

// overviewStats are the four counts at the top, each a way to its page.
func (a *App) overviewStats(c *ui.Context, days []day, failing, scheduled int, narrow, allSucceeded bool) {
	p := paletteFor(c.Theme())
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

	firstFailing := ""
	for _, task := range a.tasks {
		if a.lastRealRuns[task.ID].Status == domain.StatusFailed {
			firstFailing = task.Name
			break
		}
	}
	stats := []stat{
		{icon: icons.Tasks, label: locales.OverviewTasks(), value: len(a.tasks),
			note: locales.OverviewScheduledOf(scheduled), open: func() { a.page = pageTasks }},
		{icon: icons.Storages, label: locales.OverviewStorages(), value: len(a.storages),
			note: locales.OverviewWorking(working), open: func() { a.page = pageStorages }},
		{icon: icons.Activity, label: locales.OverviewWeek(), value: weekRuns,
			note: locales.OverviewNoneFailed(), open: func() { a.openHistory("", "", "") }},
		{icon: icons.Shield, label: locales.OverviewFailing(), value: failing,
			note: locales.OverviewNoBackupFailures(), open: func() { a.openHistory("", domain.StatusFailed, "") }},
	}
	if allSucceeded {
		stats[3].note = locales.OverviewAllSucceeded()
	}
	if broken > 0 {
		stats[1].note = locales.OverviewBroken(broken)
		stats[1].noteIcon, stats[1].noteTone = icons.Failed, p.danger
	}
	if weekFailed > 0 {
		stats[2].note = locales.OverviewSomeFailed(weekFailed)
		stats[2].noteIcon, stats[2].noteTone = icons.Warning, p.warning
		stats[2].open = func() { a.openHistory("", domain.StatusFailed, "") }
	}
	if failing > 0 {
		stats[3].note, stats[3].noteTone, stats[3].noteIcon = firstFailing, p.danger, icons.Failed
	}
	var grid ui.Element
	if narrow {
		grid = ui.Grid(c).ColumnTracks(ui.Fr(1), ui.Fr(1)).GapX(14).GapY(14)
	} else {
		grid = ui.Grid(c).ColumnTracks(ui.Fr(1), ui.Fr(1), ui.Fr(1), ui.Fr(1)).GapX(14)
	}
	grid.Children(func() {
		for i, s := range stats {
			a.statCard(c, i, s)
		}
	})
}

// statCard is a number on a card of its own, lifted under the pointer.
func (a *App) statCard(c *ui.Context, key int, s stat) {
	p := paletteFor(c.Theme())
	noteTone := s.noteTone
	if noteTone == (ui.Color{}) {
		noteTone = p.muted
	}
	// A count of nothing leads nowhere: it is not a button, and does not
	// answer the pointer.
	var card ui.Element
	clickable := s.value > 0 && s.open != nil
	if clickable {
		card = ui.ButtonBase(c.Key(key)).Justify(ui.Start).AlignItems(ui.Stretch).
			Padding(14, 16).Radius(cardRadius).Background(p.card).Border(1, p.hairline).
			Label(s.label + " " + strconv.Itoa(s.value))
		if card.Hovered() {
			card.BorderColor(p.accentEdge)
		}
		lift(card)
	} else {
		card = ui.Column(c.Key(key)).Justify(ui.Start).AlignItems(ui.Stretch).
			Padding(14, 16).Radius(cardRadius).Background(p.card).Border(1, p.hairline)
	}
	if !p.dark {
		card.Shadow(0, 1, 2, 0, p.shadow)
	}
	card.Transition(quickTransition)
	card.Children(func() {
		shown := card.AnimateTo("n", float32(s.value), ui.Smooth)
		ui.Column(c).Gap(4).Children(func() {
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				iconTile(c, s.icon, 30, p.accent, p.accentSoft)
				ui.Column(c).Grow(1).MinWidth(0).Gap(0).Children(func() {
					ui.Text(c, strconv.Itoa(int(math.Round(float64(shown))))).
						FontSize(26).FontWeight(650).LetterSpacing(-0.5).TextColor(p.text).
						FontFeatures("tnum").SingleLine()
					ui.Text(c, s.label).FontSize(sizeSmall).TextColor(p.muted).
						SingleLine().Ellipsis("…")
				})
			})
			ui.Row(c).Gap(4).AlignItems(ui.Center).MinWidth(0).TextColor(noteTone).Children(func() {
				if s.noteIcon != nil {
					ui.Icon(c, s.noteIcon).Size(11, 11)
				}
				ui.Text(c, s.note).FontSize(sizeMicro).SingleLine().Ellipsis("…")
			})
		})
	})
	if clickable && card.Clicked() {
		s.open()
	}
}

// activityCard shows the runs of the last seven days, a bar a day, the
// failed part of a day's runs on top of it.
func (a *App) activityCard(c *ui.Context, days []day) {
	p := paletteFor(c.Theme())
	weekRuns, weekFailed := 0, 0
	for _, d := range days {
		weekRuns += d.runs
		weekFailed += d.failed
	}
	subtitle := locales.ActivityRuns(weekRuns)
	if weekFailed > 0 {
		subtitle += " · " + locales.ActivityFailed(weekFailed)
	}
	cardPad(c).Children(func() {
		cardHeader(c, locales.OverviewLast7Days(), subtitle, func() {
			ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
				ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
					ui.Box(c).Size(7, 7).Radius(3.5).Background(p.accent)
					ui.Text(c, locales.StatusSuccess()).FontSize(sizeMicro).TextColor(p.muted)
				})
				if weekFailed > 0 {
					ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
						ui.Box(c).Size(7, 7).Radius(3.5).Background(p.danger)
						ui.Text(c, locales.StatusFailed()).FontSize(sizeMicro).TextColor(p.muted)
					})
				}
			})
		})
		a.weekChart(c, days)
	})
}

// weekChartHeight is the height of the tallest bar of the week.
const weekChartHeight = 116

// weekChart draws the runs of each of the last seven days as a bar, its
// failed part on top in the danger color, the day's count inside the bar
// and the whole of it in its tooltip.
func (a *App) weekChart(c *ui.Context, days []day) {
	p := paletteFor(c.Theme())
	locale := c.Region().FormatLocale(c.Locale())
	most := 1
	for _, d := range days {
		most = max(most, d.runs)
	}
	ui.Row(c).Height(weekChartHeight + 52).Gap(6).AlignItems(ui.Stretch).Children(func() {
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
			col := ui.Column(c.Key(i)).Grow(1).Basis(0).MinWidth(0).Gap(7).AlignItems(ui.Center).
				Justify(ui.End).Padding(6, 2).Radius(8).Tooltip(tip)
			if col.Hovered() {
				col.Background(p.hover)
			}
			col.Children(func() {
				v := col.AnimateTo("bar", float32(d.runs)/float32(most), ui.Smooth)
				ui.Column(c).Height(weekChartHeight + 18).FillWidth().Justify(ui.End).
					AlignItems(ui.Center).Gap(3).Children(func() {
					// The count lives in the chart, over its bar, so the
					// row of days below stays one line.
					count := ui.Text(c, strconv.Itoa(d.runs)).FontSize(sizeMicro).FontWeight(600).
						FontFeatures("tnum").SingleLine()
					if d.runs == 0 {
						count.TextColor(p.faint)
						ui.Box(c).Height(3).Width(20).Radius(2).Background(p.hairline)
						return
					}
					count.TextColor(p.text)
					h := max(float32(6), weekChartHeight*v)
					failedH := float32(0)
					if d.failed > 0 {
						failedH = max(float32(4), h*float32(d.failed)/float32(d.runs))
					}
					bar := ui.Column(c).Height(h).FillWidth().MaxWidth(46).Radius(7).Clip().
						Justify(ui.End).AlignItems(ui.End)
					bar.Children(func() {
						if failedH > 0 {
							ui.Box(c).Height(failedH).FillWidth().Background(p.danger)
						}
						ui.Box(c).Grow(1).FillWidth().
							Gradient(p.accent.Alpha(0.95), p.accent.Alpha(0.38), 90)
					})
				})
				ui.Text(c, weekday).FontSize(sizeSmall).FontWeight(540).
					TextColor(p.muted).SingleLine().Ellipsis("…")
			})
		}
	})
}

// overviewTasks lists the tasks, a row each: where it goes, how it last
// ran and when it runs next. Clicked, a row shows the task's runs.
func (a *App) overviewTasks(c *ui.Context) {
	p := paletteFor(c.Theme())
	card(c).Children(func() {
		ui.Row(c).Padding(16, 18, 6, 18).AlignItems(ui.Center).Gap(12).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, locales.NavTasks()).FontSize(sizeTitle).FontWeight(620).
					LetterSpacing(-0.2).TextColor(p.text)
				ui.Text(c, locales.TasksSubtitle()).FontSize(sizeSmall).TextColor(p.muted).
					SingleLine().Ellipsis("…")
			})
			if accentButton(c, "all-tasks", nil, locales.OverviewViewAll(), buttonGhost).Clicked() {
				a.page = pageTasks
			}
		})
		for i, task := range a.tasks {
			if i >= 4 {
				break
			}
			a.overviewTaskRow(c, task)
		}
		if len(a.tasks) > 4 {
			ui.Row(c).Padding(rowPadY, rowPadX).Children(func() {
				ui.Button(c, "").Label(locales.OverviewViewAll()).Children(func() {
					ui.Text(c, locales.OverviewViewAll()).FontSize(sizeSmall).TextColor(p.accent)
				}).Clicked()
			})
		}
	})
}

func (a *App) overviewTaskRow(c *ui.Context, task domain.Task) {
	p := paletteFor(c.Theme())
	_, running := a.active[task.ID]
	row := ui.Row(c.Key(task.ID)).Transition(listTransition).Padding(0, 8, 0, 8)
	row.Children(func() {
		body := ui.ButtonBase(c.Key(task.ID+"-open")).Justify(ui.Start).AlignItems(ui.Center).Gap(12).
			Padding(10, 10).Radius(rowRadius).Label(locales.OverviewOpenTask(task.Name))
		hoverRow(c, body)
		body.Children(func() {
			icon := icons.Copy
			if task.Mode == domain.ModeSync {
				icon = icons.Transfer
			}
			iconTile(c, icon, 30, p.accent, p.accentSoft)
			ui.Column(c).Gap(3).Grow(1).Basis(0).Shrink(1).MinWidth(0).Children(func() {
				ui.Text(c, task.Name).FontSize(sizeBody).FontWeight(570).TextColor(p.text).
					SingleLine().Ellipsis("…")
				route := locales.TasksRouteCopy(a.locationText(task.Destination))
				if task.Mode == domain.ModeSync {
					route = locales.TasksRouteSync(a.locationText(task.Destination))
				}
				ui.Row(c).Gap(7).AlignItems(ui.Center).MinWidth(0).Children(func() {
					ui.Text(c, route).FontSize(sizeMicro).TextColor(p.muted).
						SingleLine().Ellipsis("…").MinWidth(0)
					ui.Text(c, "·").FontSize(sizeMicro).TextColor(p.faint)
					ui.Text(c, a.scheduleLine(c, task)).FontSize(sizeMicro).TextColor(p.muted).
						SingleLine().Ellipsis("…")
				})
			})
			ui.Box(c).Width(100).Shrink(0).AlignItems(ui.End).Children(func() {
				switch last, ok := a.lastRuns[task.ID]; {
				case running:
					statusPill(c, domain.StatusRunning)
				case ok:
					statusPill(c, last.Status)
				default:
					pill(c, "never", nil, locales.TasksNever(), p.muted, p.neutralSoft)
				}
			})
		})
		if body.Clicked() {
			a.openHistory(task.ID, "", "")
		}
		if running {
			if iconAction(c, task.ID+"-stop", icons.Stop, locales.TaskStop()).Clicked() {
				a.stop(task.ID)
			}
		} else if iconAction(c, task.ID+"-run", icons.Run, locales.TaskRun()).Clicked() {
			a.run(task, false, false)
		}
	})
}

// scheduleLine says when a task runs, in a few words.
func (a *App) scheduleLine(c *ui.Context, task domain.Task) string {
	if !task.Schedule.On() {
		return locales.TasksManual()
	}
	return scheduleText(c, task.Schedule)
}

// runningCard shows the transfer in progress, and stops it.
func (a *App) runningCard(c *ui.Context) {
	p := paletteFor(c.Theme())
	card(c).Children(func() {
		ui.Row(c).Padding(16, 18, 8, 18).Gap(12).AlignItems(ui.Center).Children(func() {
			ui.Text(c, locales.OverviewRunningTitle()).FontSize(sizeTitle).FontWeight(620).
				LetterSpacing(-0.2).Grow(1)
			ui.Box(c).Size(8, 8).Radius(4).Background(p.accent)
		})
		for _, run := range a.running {
			r := run
			ui.Column(c).Key(r.Run.ID).Padding(0, 18).Gap(8).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, r.Run.TaskName).FontSize(sizeBody).FontWeight(570).Grow(1).
						MinWidth(0).SingleLine().Ellipsis("…")
					if r.Run.DryRun {
						ui.Badge(c, locales.DryRunBadge())
					}
				})
				if r.Queued {
					ui.Text(c, locales.TaskWaiting()).FontSize(sizeSmall).TextColor(p.muted)
				} else {
					progressBar(c, r.Run.TaskName, r.Progress.Bytes, r.Progress.TotalBytes)
					ui.Row(c).Gap(10).TextColor(p.muted).Children(func() {
						ui.Text(c, locales.Progress(format.Bytes(r.Progress.Bytes), format.Bytes(r.Progress.TotalBytes))).
							FontSize(sizeMicro).FontFeatures("tnum")
						if r.Progress.Speed > 0 {
							ui.Text(c, locales.Speed(format.Bytes(int64(r.Progress.Speed)))).FontSize(sizeMicro).
								FontFeatures("tnum")
						}
					})
				}
				ui.Row(c).Gap(8).PaddingY(4).Children(func() {
					if accentButton(c, "open-run", nil, locales.HistoryDetailTitle(), buttonGhost).Clicked() {
						a.openHistory("", "", r.Run.ID)
					}
					ui.Spacer(c)
					if accentButton(c, "stop-run", icons.Stop, locales.TaskStop(), buttonSoft).Clicked() {
						a.stop(r.Run.TaskID)
					}
				})
			})
			ui.Box(c).Height(gapMedium)
		}
		ui.Box(c).Height(10)
	})
}

// recentCard lists the latest runs, each leading to its details and log.
func (a *App) recentCard(c *ui.Context) {
	p := paletteFor(c.Theme())
	groups := a.recentGroups()
	card(c).Children(func() {
		ui.Row(c).Padding(16, 18, 6, 18).Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, locales.OverviewRecent()).FontSize(sizeTitle).FontWeight(620).LetterSpacing(-0.2)
				ui.Text(c, locales.OverviewRecentSubtitle()).FontSize(sizeSmall).TextColor(p.muted).
					SingleLine().Ellipsis("…")
			})
		})
		if len(groups) == 0 {
			ui.Text(c, locales.OverviewNoRuns()).FontSize(sizeSmall).TextColor(p.muted).
				Padding(2, 18, 16, 18).LineHeight(1.5)
			return
		}
		for i, g := range groups {
			if i >= 6 {
				break
			}
			r := g[0]
			label := locales.OverviewOpenRun(r.TaskName)
			if len(g) > 1 {
				label = locales.OverviewOpenTask(r.TaskName)
			}
			row := ui.ButtonBase(c.Key(g[len(g)-1].ID)).Justify(ui.Start).Gap(9).
				Padding(9, 18).Radius(0).Label(label)
			hoverRow(c, row)
			row.Children(func() {
				ui.Box(c).Width(16).Children(func() {
					it := ui.Icon(c, StatusIcon(r.Status)).Size(15, 15).TextColor(statusColorFor(p, r.Status))
					if r.Status == domain.StatusRunning {
						it.Rotate(it.Loop("spin", time.Second, ui.Linear) * 360)
					}
				})
				ui.Column(c).Grow(1).MinWidth(0).Gap(1).Children(func() {
					ui.Text(c, r.TaskName).FontSize(sizeSmall).FontWeight(540).TextColor(p.text).
						SingleLine().Ellipsis("…")
					ui.Text(c, groupTime(c, g)).FontSize(sizeMicro).TextColor(p.muted).
						SingleLine().Ellipsis("…").FontFeatures("tnum")
				})
				ui.Text(c, format.Bytes(r.Stats.Bytes)).FontSize(sizeMicro).TextColor(p.faint).
					SingleLine().FontFeatures("tnum")
			})
			if row.Clicked() {
				if len(g) > 1 {
					a.openHistory(r.TaskID, "", "")
				} else {
					a.openHistory("", "", r.ID)
				}
			}
		}
		ui.Box(c).Height(8)
	})
}

// recentTime is when a run started, in a few words: the time alone for
// today, the date and time otherwise.
func recentTime(c *ui.Context, g runGroup) string {
	t := g[0].StartedAt
	now := c.Now()
	if y, m, d := now.Date(); t.Year() == y && t.Month() == m && t.Day() == d {
		return i18n.FormatDate(c.Region().FormatLocale(c.Locale()), t,
			i18n.DateOptions{Time: i18n.ShortStyle})
	}
	return groupTime(c, g)
}

// statusColorFor is a status's color in the palette.
func statusColorFor(p palette, s domain.Status) ui.Color {
	switch s {
	case domain.StatusRunning:
		return p.accent
	case domain.StatusSuccess:
		return p.success
	case domain.StatusFailed:
		return p.danger
	}
	return p.muted
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

// activeCard shows a run in progress in the overview's older shape, kept
// for the tests and the pages that still use it.
func (a *App) activeCard(c *ui.Context, run service.Active) {
	// Kept for compatibility; the running card above is the one the page
	// shows.
	_ = run
	_ = c
}
