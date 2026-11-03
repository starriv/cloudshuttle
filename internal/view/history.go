package view

import (
	"context"

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

func (a *App) historyPage(c *ui.Context) {
	t := c.Theme()
	chosen := a.chosenRuns()
	header(c, locales.NavHistory(), func() {
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
		empty(c, icons.History, locales.HistoryEmpty(), nil)
		return
	}

	ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
		changed := ui.Select(c, &a.historyTask, taskOptions).Label(locales.HistoryFilterTask()).Width(220).Changed()
		if ui.Select(c, &a.historyStatus, statusOptions).Label(locales.HistoryFilterStatus()).Width(180).Changed() || changed {
			a.reloadHistory()
		}
		ui.Spacer(c)
		if len(a.history) > 0 {
			a.chooseAllRuns(c, len(chosen))
		}
	})

	if len(a.history) == 0 {
		empty(c, icons.History, locales.HistoryNoMatch(), nil)
		return
	}

	cols := []ui.TableColumn{
		{ID: "chosen", Width: 40, Fixed: true},
		{Title: locales.HistoryColumnStarted(), ID: "started", Width: 160},
		{Title: locales.HistoryColumnTask(), ID: "task", MinWidth: 100},
		{Title: locales.HistoryColumnStatus(), ID: "status", Width: 130},
		{Title: locales.HistoryColumnDuration(), ID: "duration", Width: 90, Align: ui.End},
		{Title: locales.HistoryColumnTransferred(), ID: "transferred", Width: 150, Align: ui.End},
	}
	a.historyList.Label = func(i int) string { return a.history[i].TaskName }
	// A right-click on a run not chosen chooses it first: the menu acts on
	// what is chosen.
	a.historyList.CellMenu = func(m *ui.Menu, _, _ int) {
		if runs := a.chosenRuns(); len(runs) > 0 && m.Item(locales.HistoryDeleteItem(len(runs))).Chosen() {
			a.confirmDeleteRuns(runs)
		}
	}
	table := ui.Table(c, &a.historyList, cols, len(a.history), func(row, col int) {
		r := a.history[row]
		switch col {
		case 0:
			// A check box a row, to choose several without a modifier key.
			chosen := a.historyChosen.Has(r.ID)
			if ui.Checkbox(c, &chosen, "").Label(locales.HistoryChoose(r.TaskName, formatTime(c, r.StartedAt))).Changed() {
				if chosen {
					a.historyChosen.Add(r.ID)
				} else {
					a.historyChosen.Remove(r.ID)
				}
			}
		case 1:
			ui.Text(c, formatTime(c, r.StartedAt)).SingleLine()
		case 2:
			ui.Row(c).Gap(6).AlignItems(ui.Center).MinWidth(0).Children(func() {
				ui.Text(c, r.TaskName).SingleLine().Ellipsis("…").MinWidth(0)
				if r.DryRun {
					ui.Badge(c, locales.DryRunBadge())
				}
			})
		case 3:
			if a.historyChosen.Has(r.ID) {
				// The chosen row is filled with the accent: its text color
				// reads on it, the status's own would not.
				statusIn(c, r.Status, c.Theme().AccentText)
			} else {
				status(c, r.Status)
			}
		case 4:
			ui.Text(c, format.Duration(r.Duration(a.now()))).FontFeatures("tnum").SingleLine()
		case 5:
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, transferred(r.Stats)).FontFeatures("tnum").SingleLine()
				if r.Stats.Deletes > 0 {
					ui.Text(c, locales.HistoryDeletedShort(int(r.Stats.Deletes))).
						TextColor(t.TextMuted).SingleLine()
				}
			})
		}
	}).Grow(1).MinHeight(160)
	if len(a.history) >= store.DefaultRunLimit {
		ui.Text(c, locales.HistoryLimit(store.DefaultRunLimit)).TextColor(t.TextMuted).FontSize(11)
	}
	// Delete, Backspace and Cmd-Backspace, as Finder's, delete the runs chosen.
	if (table.Shortcut(0, ui.KeyBackspace) || table.Shortcut(0, ui.KeyDelete) ||
		table.Shortcut(ui.Super, ui.KeyBackspace)) && len(chosen) > 0 {
		a.confirmDeleteRuns(chosen)
	}
	// Escape chooses none, closing the details.
	if table.Shortcut(0, ui.KeyEscape) {
		a.clearHistoryChoice()
	}

	ui.Column(c).Gap(8).PaddingY(t.Space(3)).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
		switch len(chosen) {
		case 0:
			ui.Text(c, locales.HistoryDetailNone()).TextColor(t.TextMuted)
		case 1:
			a.runDetail(c, chosen[0])
		default:
			ui.Text(c, locales.HistoryChosen(len(chosen))).TextColor(t.TextMuted)
		}
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

func (a *App) runDetail(c *ui.Context, r domain.Run) {
	t := c.Theme()
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, locales.HistoryDetailTitle()).FontSize(16).FontWeight(500).Grow(1)
		if iconButton(c, icons.Stop, locales.HistoryDetailClose(), false).Clicked() {
			a.clearHistoryChoice()
		}
	})
	ui.Form(c, func() {
		ui.Field(c, locales.HistoryTrigger(), func() {
			ui.Text(c, triggerLabel(r.Trigger)+", "+formatTime(c, r.StartedAt)).Selectable()
		})
		ui.Field(c, locales.HistoryCounts(), func() {
			ui.Column(c).Gap(2).Children(func() {
				ui.Text(c, transferred(r.Stats)).FontFeatures("tnum")
				ui.Text(c, locales.HistoryChecked(int(r.Stats.Checks))).TextColor(t.TextMuted)
				if r.Stats.Deletes > 0 {
					ui.Text(c, locales.HistoryDeleted(int(r.Stats.Deletes))).TextColor(t.TextMuted)
				}
				if r.Stats.Errors > 0 {
					ui.Text(c, locales.HistoryErrors(int(r.Stats.Errors))).TextColor(t.Danger)
				}
			})
		})
		if text := runProblemText(r.Problem); text != "" {
			ui.Field(c, locales.HistoryProblem(), func() {
				ui.Text(c, text).TextColor(t.TextMuted)
			})
		}
		if r.Message != "" {
			ui.Field(c, locales.HistoryMessage(), func() {
				ui.Text(c, r.Message).Font("monospace").Selectable().MaxLines(6)
			})
		}
	})
	a.runLogSection(c, r)
}
