package view

import (
	"context"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/internal/format"
	"github.com/starriv/cloudshuttle/internal/icons"
	"github.com/starriv/cloudshuttle/internal/store"
	"github.com/starriv/cloudshuttle/locales"
)

// historyStatuses are the statuses the history filters by, in the order of
// the filter's options after "All statuses".
var historyStatuses = []domain.Status{domain.StatusSuccess, domain.StatusFailed, domain.StatusCanceled, domain.StatusRunning}

func (a *App) historyTaskOptions() []string {
	labels := []string{locales.HistoryAllTasks()}
	for _, t := range a.tasks {
		labels = append(labels, t.Name)
	}
	return uniqueLabels(labels)
}

func (a *App) historyStatusOptions() []string {
	labels := []string{locales.HistoryAllStatuses()}
	for _, s := range historyStatuses {
		labels = append(labels, StatusLabel(s))
	}
	return labels
}

// historyPage shows the history as a master and its details: every run on
// the left, newest first, and the run chosen on the right, with what it
// did, why it stopped and the log it wrote.
func (a *App) historyPage(c *ui.Context) {
	chosen := a.chosenRuns()
	pageHeader(c, locales.NavHistory(), locales.HistorySubtitle(), func() {
		if len(a.history) == 0 {
			return
		}
		label := locales.HistoryDeleteNone()
		if len(chosen) > 0 {
			label = locales.HistoryDelete(len(chosen))
		}
		if iconButton(c, icons.Delete, label, false).Disabled(len(chosen) == 0).Clicked() {
			a.confirmDeleteRuns(chosen)
		}
	})

	taskOptions, statusOptions := a.historyTaskOptions(), a.historyStatusOptions()
	if indexOf(taskOptions, a.historyTask) < 0 {
		a.historyTask = taskOptions[0]
	}
	if indexOf(statusOptions, a.historyStatus) < 0 {
		a.historyStatus = statusOptions[0]
	}
	filtered := a.historyTask != taskOptions[0] || a.historyStatus != statusOptions[0]
	if len(a.history) == 0 && !filtered {
		emptyPage(c, icons.History, locales.HistoryEmptyTitle(), locales.HistoryEmpty(), nil)
		return
	}

	a.historyFilterBar(c, taskOptions, statusOptions, len(chosen))

	if len(a.history) == 0 {
		card(c).Grow(1).Clip().Margin(0, 0, pageBottom, 0).Children(func() {
			emptyPage(c, icons.History, locales.HistoryNoMatchTitle(), locales.HistoryNoMatch(), nil)
		})
		return
	}

	// Two cards with the page's gutter between them, never one card split by
	// a line. A run chosen opens the details card; with none chosen the runs
	// have the whole page.
	panel := float32(0)
	if len(chosen) > 0 {
		a.historyPanel = clampHistoryPanel(c, a.historyPanel)
		panel = a.historyPanel
	}
	ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Gap(gutter).
		Margin(0, 0, pageBottom, 0).Children(func() {
		a.historyRuns(c)
		if panel > 0 {
			ui.Box(c).Width(panel).Shrink(0).FillHeight().
				Children(func() { a.historyDetails(c, chosen) })
		}
	})
}

// clampHistoryPanel keeps the details card wide enough to read, and narrow
// enough that the runs still show beside it as it changes.
func clampHistoryPanel(c *ui.Context, panel float32) float32 {
	total := historyPageWidth(c)
	if total <= 0 {
		return panel
	}
	least := float32(300)
	most := min(float32(420), total*0.45)
	if most < least {
		most = least
	}
	return min(max(panel, least), most)
}

// historyPageWidth is the room both panes of the history share.
func historyPageWidth(c *ui.Context) float32 {
	w, _ := c.Size()
	return w - sidebarWidth - 2*pagePad
}

// historyRunsRoom is the width the runs card gets beside the details card,
// less the gutter, when the details show.
func (a *App) historyRunsRoom(c *ui.Context) float32 {
	room := historyPageWidth(c)
	if len(a.chosenRuns()) > 0 {
		room -= a.historyPanel + gutter
	}
	return room
}

// historyFilterBar narrows what the runs list shows, in a quiet bar above
// both panes: the task, the status, and the choice of every run shown.
func (a *App) historyFilterBar(c *ui.Context, tasks, statuses []string, chosen int) {
	p := paletteFor(c.Theme())
	bar := ui.Row(c).Height(46).PaddingX(14).Gap(gapMedium).Radius(cardRadius).AlignItems(ui.Center).
		Background(p.card).Border(1, p.hairline)
	if !p.dark {
		bar.Shadow(0, 1, 2, 0, p.shadow)
	}
	bar.Children(func() {
		changed := ui.Select(c, &a.historyTask, tasks).Label(locales.HistoryFilterTask()).Width(200).Changed()
		if ui.Select(c, &a.historyStatus, statuses).Label(locales.HistoryFilterStatus()).Width(180).Changed() || changed {
			a.reloadHistory()
		}
		ui.Spacer(c)
		if len(a.history) > 0 {
			a.chooseAllRuns(c, chosen)
		}
	})
}

// historyRuns is the runs list: a card of rows, the chosen ones filled as
// the list chooses them, with the note that says when older runs are not
// loaded.
func (a *App) historyRuns(c *ui.Context) {
	p := paletteFor(c.Theme())
	a.historyList.Label = func(i int) string { return a.history[i].TaskName }
	a.historyList.RoundRows = true
	runs := card(c).Grow(1).MinHeight(0).Clip()
	runs.Children(func() {
		list := ui.List(c, &a.historyList, len(a.history), func(i int) {
			a.historyRunRow(c, i)
		}).Grow(1).MinHeight(0).Padding(10).ScrollbarInsets(10, 4, 10)
		// The menu goes on the list, not the rows: a row chosen with the
		// right button may open the details card and lay the list out
		// again, which would take a menu on the row with it.
		list.ContextMenu(func(m *ui.Menu) {
			if runs := a.chosenRuns(); len(runs) > 0 {
				if m.Item(locales.HistoryDeleteItem(len(runs))).Chosen() {
					a.confirmDeleteRuns(runs)
				}
			}
		})
		if len(a.history) >= store.DefaultRunLimit {
			ui.Row(c).Padding(8, rowPadX).Children(func() {
				ui.Text(c, locales.HistoryLimit(store.DefaultRunLimit)).FontSize(sizeMicro).
					TextColor(p.muted).SingleLine().Ellipsis("…")
			})
		}
		// Delete, Backspace and Cmd-Backspace, as Finder's, delete the runs
		// chosen.
		chosen := a.chosenRuns()
		if (list.Shortcut(0, ui.KeyBackspace) || list.Shortcut(0, ui.KeyDelete) ||
			list.Shortcut(ui.Super, ui.KeyBackspace)) && len(chosen) > 0 {
			a.confirmDeleteRuns(chosen)
		}
		// Escape chooses none, closing the details.
		if list.Shortcut(0, ui.KeyEscape) {
			a.clearHistoryChoice()
		}
	})
}

// historyRunRow is one run of the list: what ran and when, how it ended,
// and what it moved. A click chooses it, as the list's keys do; the box on
// its left chooses it without a modifier key.
func (a *App) historyRunRow(c *ui.Context, i int) {
	p := paletteFor(c.Theme())
	r := a.history[i]
	chosen := a.historyChosen.Has(r.ID)
	text, muted := p.text, p.muted
	if chosen {
		// On the accent the list fills a chosen row with, the words read
		// in the color that goes on it.
		text, muted = c.Theme().AccentText, c.Theme().AccentText.Alpha(0.72)
	}
	dryColor := p.warning
	if chosen {
		dryColor = muted
	}
	// A narrow window gives the runs less room: the status moves onto the
	// task's line, and what the run moved waits in the details, a click
	// away.
	compact := a.historyRunsRoom(c) < 420
	row := ui.Row(c.Key(r.ID)).FillWidth().Padding(10, 14).Gap(12).AlignItems(ui.Center)
	row.Children(func() {
		box := chosen
		if ui.Checkbox(c, &box, "").Label(locales.HistoryChoose(r.TaskName, formatTime(c, r.StartedAt))).Changed() {
			if box {
				a.historyChosen.Add(r.ID)
			} else {
				a.historyChosen.Remove(r.ID)
			}
		}
		if compact {
			ui.Row(c).Grow(1).MinWidth(0).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
					ui.Text(c, r.TaskName).FontSize(sizeBody).FontWeight(570).TextColor(text).
						SingleLine().Ellipsis("…")
					a.historyRunWhen(c, r, muted, dryColor)
				})
				ui.Box(c).Width(96).Shrink(0).AlignItems(ui.Start).Children(func() {
					a.historyRunStatus(c, r, chosen, text)
				})
			})
			return
		}
		ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
			ui.Text(c, r.TaskName).FontSize(sizeBody).FontWeight(570).TextColor(text).
				SingleLine().Ellipsis("…")
			a.historyRunWhen(c, r, muted, dryColor)
		})
		// The status and the size are columns of their own on the right, so
		// their left edges line up from row to row instead of the two
		// hanging off the right edge at different widths.
		ui.Row(c).Gap(14).Shrink(0).AlignItems(ui.Center).Children(func() {
			ui.Box(c).Width(96).AlignItems(ui.Start).Children(func() { a.historyRunStatus(c, r, chosen, text) })
			ui.Box(c).Width(140).Children(func() {
				ui.Text(c, transferred(r.Stats)).FontSize(sizeMicro).TextColor(muted).
					FontFeatures("tnum").SingleLine().Ellipsis("…")
			})
		})
	})
}

// historyRunWhen says when the run started, how long it took, the files it
// deleted, and that it only looked, on one line.
func (a *App) historyRunWhen(c *ui.Context, r domain.Run, muted, dryColor ui.Color) {
	ui.Row(c).Gap(5).AlignItems(ui.Center).MinWidth(0).TextColor(muted).Children(func() {
		ui.Text(c, formatStarted(c, r.StartedAt)).FontSize(sizeMicro).SingleLine().Ellipsis("…")
		ui.Text(c, "·").FontSize(sizeMicro)
		ui.Text(c, format.Duration(r.Duration(a.now()))).FontSize(sizeMicro).
			FontFeatures("tnum").SingleLine()
		if r.Stats.Deletes > 0 {
			ui.Text(c, "·").FontSize(sizeMicro)
			ui.Text(c, locales.HistoryDeletedShort(int(r.Stats.Deletes))).FontSize(sizeMicro).
				SingleLine().Ellipsis("…")
		}
		if r.DryRun {
			ui.Text(c, "·").FontSize(sizeMicro)
			ui.Text(c, locales.DryRunBadge()).FontSize(sizeMicro).TextColor(dryColor).
				SingleLine()
		}
	})
}

// historyRunStatus shows a run's status in its row: on the accent a chosen
// row is filled with, the words take the color that goes on it.
func (a *App) historyRunStatus(c *ui.Context, r domain.Run, chosen bool, onAccent ui.Color) {
	if chosen {
		pill(c, r.ID+"-status", StatusIcon(r.Status), StatusLabel(r.Status), onAccent, ui.Transparent)
		return
	}
	statusPill(c, r.Status)
}

// formatStarted writes when a run started, short, for a row of the list:
// the date and the time, without the year the details show.
func formatStarted(c *ui.Context, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return i18n.FormatDate(c.Region().FormatLocale(c.Locale()), t, i18n.DateOptions{Skeleton: "MMMdjm"})
}

// historyDetails is the right pane: the run chosen and everything about
// it; how many are chosen when several are; or how to begin.
func (a *App) historyDetails(c *ui.Context, runs []domain.Run) {
	p := paletteFor(c.Theme())
	panel := card(c).Fill().Clip().Margin(0, 0, 0, 0).
		Shadow(0, 8, 28, 2, p.shadow)
	panel.Children(func() {
		switch len(runs) {
		case 0:
			emptyPage(c, icons.History, locales.HistoryDetailNoneTitle(), locales.HistoryDetailNone(), nil)
		case 1:
			a.runDetail(c, runs[0])
		default:
			emptyPage(c, icons.History, locales.HistoryChosen(len(runs)), "", func() {
				if accentButton(c, "delete-chosen-runs", icons.Delete, locales.HistoryDelete(len(runs)), buttonDanger).Clicked() {
					a.confirmDeleteRuns(runs)
				}
			})
		}
	})
}

// detailLabel names a group of the details panel.
func detailLabel(c *ui.Context, text string) {
	p := paletteFor(c.Theme())
	ui.Text(c, text).FontSize(sizeSmall).FontWeight(620).LetterSpacing(0.2).TextColor(p.muted)
}

// detailWell is the face of a block of text in the details: a step into
// the card, quiet, so monospace lines have a place of their own.
func detailWell(c *ui.Context) ui.Element {
	p := paletteFor(c.Theme())
	return ui.Column(c).FillWidth().Padding(9, 11).Radius(10).
		Background(p.cardAlt).Border(1, p.hairline)
}

// runDetail is the story of one run: its result, what it moved, where the
// trash it filled is, why it stopped, and its log.
func (a *App) runDetail(c *ui.Context, r domain.Run) {
	p := paletteFor(c.Theme())
	ui.Row(c).Padding(14, 16, 10, 16).Gap(12).AlignItems(ui.Start).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(6).Children(func() {
			ui.Text(c, locales.HistoryDetailTitle()).FontSize(sizeTitle).FontWeight(620).
				LetterSpacing(-0.2).TextColor(p.text).SingleLine().Ellipsis("…")
			ui.Row(c).Gap(8).AlignItems(ui.Center).MinWidth(0).Children(func() {
				statusPill(c, r.Status)
				ui.Text(c, r.TaskName).FontSize(sizeSmall).FontWeight(560).TextColor(p.muted).
					SingleLine().Ellipsis("…").MinWidth(0)
			})
		})
		if iconAction(c, "detail-close", icons.Close, locales.HistoryDetailClose()).Clicked() {
			a.clearHistoryChoice()
		}
	})
	ui.Scroll(c).Grow(1).MinHeight(0).Padding(0, rowPadX, rowPadY, rowPadX).
		ScrollbarInsets(gapSmall, 4, gapSmall).Children(func() {
		ui.Column(c).Gap(14).Children(func() {
			a.runStats(c, r)
			if r.Trash != "" {
				a.runTrash(c, r)
			}
			if text := runProblemText(r.Problem); text != "" {
				runProblemBanner(c, text)
			}
			if r.Problem != "" {
				a.wouldDelete(c, r)
			}
			if r.Message != "" {
				a.runMessage(c, r)
			}
			a.runLogSection(c, r)
		})
	})
}

// historyStat is one number of a run: what it counts and its value.
type historyStat struct {
	label, value string
}

// runStats shows when and how the run went, in a compact grid, then what
// it checked, deleted and failed on.
func (a *App) runStats(c *ui.Context, r domain.Run) {
	p := paletteFor(c.Theme())
	started, files := formatTime(c, r.StartedAt), transferred(r.Stats)
	if a.historyPanel < 340 {
		// A narrow panel shows the time without its year, and the bytes
		// the run moved, so each number reads whole.
		started, files = formatStarted(c, r.StartedAt), format.Bytes(r.Stats.Bytes)
	}
	stats := []historyStat{
		{locales.HistoryColumnStarted(), started},
		{locales.HistoryColumnDuration(), format.Duration(r.Duration(a.now()))},
		{locales.HistoryCounts(), files},
		{locales.HistoryTrigger(), triggerLabel(r.Trigger)},
	}
	ui.Column(c).Gap(8).Children(func() {
		ui.Grid(c).ColumnTracks(ui.Fr(1), ui.Fr(1)).GapX(10).GapY(10).Children(func() {
			for i, s := range stats {
				ui.Column(c.Key(i)).Gap(3).Padding(9, 11).Radius(10).MinWidth(0).
					Background(p.cardAlt).Border(1, p.hairline).Children(func() {
					ui.Text(c, s.label).FontSize(sizeMicro).FontWeight(560).TextColor(p.muted).SingleLine()
					ui.Text(c, s.value).FontSize(sizeSmall).FontWeight(560).TextColor(p.text).
						FontFeatures("tnum").SingleLine().Ellipsis("…")
				})
			}
		})
		ui.Column(c).Gap(3).Children(func() {
			ui.Text(c, locales.HistoryChecked(int(r.Stats.Checks))).FontSize(sizeMicro).TextColor(p.muted)
			if r.Stats.Deletes > 0 {
				ui.Text(c, locales.HistoryDeleted(int(r.Stats.Deletes))).FontSize(sizeMicro).TextColor(p.muted)
			}
			if r.Stats.Errors > 0 {
				ui.Row(c).Gap(5).AlignItems(ui.Center).TextColor(p.danger).Children(func() {
					ui.Icon(c, icons.Failed).Size(12, 12)
					ui.Text(c, locales.HistoryErrors(int(r.Stats.Errors))).FontSize(sizeMicro).
						SingleLine().Ellipsis("…")
				})
			}
		})
	})
}

// runTrash names the folder the run moved what it deleted into.
func (a *App) runTrash(c *ui.Context, r domain.Run) {
	p := paletteFor(c.Theme())
	ui.Column(c).Gap(6).Children(func() {
		detailLabel(c, locales.HistoryTrash())
		detailWell(c).Children(func() {
			ui.Text(c, r.Trash).Font("monospace").FontSize(sizeSmall).TextColor(p.text).
				Selectable().MaxLines(3).LineHeight(1.4)
			ui.Text(c, locales.HistoryTrashed(int(r.Stats.Trashed))).FontSize(sizeMicro).TextColor(p.muted)
		})
	})
}

// runProblemBanner says why a run stopped before it touched anything, as a
// warning the person notices above its log.
func runProblemBanner(c *ui.Context, text string) {
	p := paletteFor(c.Theme())
	ui.Row(c).Radius(12).Padding(11, 13).Gap(10).AlignItems(ui.Start).
		Background(p.warningSoft).Border(1, p.warning.Alpha(0.22)).Children(func() {
		iconTile(c, icons.Warning, 26, p.warning, p.card.Alpha(0.7))
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, locales.HistoryProblem()).FontSize(sizeSmall).FontWeight(620).TextColor(p.warning)
			ui.Text(c, text).FontSize(sizeSmall).TextColor(p.text).LineHeight(1.4)
		})
	})
}

// runMessage shows what the run's tool said, as it said it.
func (a *App) runMessage(c *ui.Context, r domain.Run) {
	p := paletteFor(c.Theme())
	ui.Column(c).Gap(6).Children(func() {
		detailLabel(c, locales.HistoryMessage())
		detailWell(c).Children(func() {
			ui.Text(c, r.Message).Font("monospace").FontSize(sizeSmall).TextColor(p.text).
				Selectable().MaxLines(6).LineHeight(1.4)
		})
	})
}

// chooseAllRuns is the check box that chooses every run the history shows,
// or none once all are: some chosen, it is unchecked, for a click to choose
// them all.
func (a *App) chooseAllRuns(c *ui.Context, chosen int) {
	all := chosen == len(a.history)
	box := ui.Checkbox(c, &all, locales.HistoryChooseAll())
	if chosen > 0 && !all {
		box.Mixed()
	}
	if box.Changed() {
		a.historyChosen.Clear()
		if all {
			for _, r := range a.history {
				a.historyChosen.Add(r.ID)
			}
		}
	}
}

// clearHistoryChoice chooses no run, which closes the details.
func (a *App) clearHistoryChoice() {
	a.historyChosen.Clear()
	a.historyRow = -1
}

// chosenRuns are the runs of the history chosen, in its order.
func (a *App) chosenRuns() []domain.Run {
	var out []domain.Run
	for _, r := range a.history {
		if a.historyChosen.Has(r.ID) {
			out = append(out, r)
		}
	}
	return out
}

// confirmDeleteRuns asks before deleting runs from the history: those in
// progress stay.
func (a *App) confirmDeleteRuns(runs []domain.Run) {
	var ids []string
	running := 0
	for _, r := range runs {
		if r.Status == domain.StatusRunning {
			running++
			continue
		}
		ids = append(ids, r.ID)
	}
	if len(ids) == 0 {
		a.toasts = append(a.toasts, ui.Toast{Title: locales.HistoryDeleteRunning(), Type: "error"})
		return
	}
	message := locales.HistoryDeleteMessage()
	if running > 0 {
		message += " " + locales.HistoryDeleteKeepsRunning(running)
	}
	a.confirm = confirmation{
		open:    true,
		title:   locales.HistoryDeleteTitle(len(ids)),
		message: message,
		action: func() {
			n, err := a.svc.DeleteRuns(context.Background(), ids)
			if err != nil {
				a.toastError(err)
				return
			}
			for _, id := range ids {
				a.historyChosen.Remove(id)
			}
			a.toast(locales.HistoryDeleted(n))
		},
	}
}
