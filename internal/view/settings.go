package view

import (
	"context"
	"runtime"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/internal/icons"
	"github.com/starriv/cloudshuttle/internal/service"
	"github.com/starriv/cloudshuttle/locales"
)

// Settings the page offers, by their value. Each is a select, so what the
// app saves is always one of the values it works with.
var (
	transferValues  = []int{1, 2, 3, 4, 6, 8, 12, 16}
	checkerValues   = []int{2, 4, 8, 16, 24, 32}
	retryValues     = []int{1, 2, 3, 5, 10}
	bandwidthValues = []int{0, 1, 5, 10, 50}
	retentionValues = []int{0, 30, 90, 180, 365}
	trashValues     = []int{0, 7, 30, 90}
)

// settingsForm is the settings page as it is edited: the labels the selects
// show, and the problems of a save that failed.
type settingsForm struct {
	loaded        bool
	passwordLoad  int
	transfers     string
	checkers      string
	retries       string
	bandwidth     string
	retention     string
	trash         string
	notifyFailed  bool
	notifySkipped bool
	// excludeEnabled leaves out the global rules in every run, and
	// globalExclude holds them as the page edits them: one a line.
	excludeEnabled bool
	globalExclude  string
	backupICloud   bool
	background     bool // the system may run the schedules while the app is closed
	problems       domain.Problems

	// The backups' password: whether one is set, and a new one as typed.
	hasPassword      bool
	changingPassword bool
	password         string
	confirm          string
	saving           bool // a save runs, off the main thread
}

func numberLabels(values []int) []string {
	out := make([]string, len(values))
	for i, n := range values {
		out[i] = strconv.Itoa(n)
	}
	return out
}

func bandwidthLabels() []string {
	out := make([]string, len(bandwidthValues))
	for i, mbps := range bandwidthValues {
		if mbps == 0 {
			out[i] = locales.SettingsNoLimit()
		} else {
			out[i] = locales.SettingsSpeed(mbps)
		}
	}
	return out
}

func retentionLabels() []string {
	out := make([]string, len(retentionValues))
	for i, days := range retentionValues {
		if days == 0 {
			out[i] = locales.SettingsKeepForever()
		} else {
			out[i] = locales.SettingsKeepDays(days)
		}
	}
	return out
}

func trashLabels() []string {
	out := make([]string, len(trashValues))
	for i, days := range trashValues {
		if days == 0 {
			out[i] = locales.SettingsTrashOff()
		} else {
			out[i] = locales.SettingsKeepDays(days)
		}
	}
	return out
}

// labelAt is the label of a value, the first value's when it is unknown.
func labelAt(values []int, labels []string, value int) string {
	for i, n := range values {
		if n == value {
			return labels[i]
		}
	}
	return labels[0]
}

// valueAt is the value a label names, the first value's when it names none.
func valueAt(values []int, labels []string, label string) int {
	for i, l := range labels {
		if l == label {
			return values[i]
		}
	}
	return values[0]
}

// loadSettingsForm fills the page in with the settings as they are.
func (a *App) loadSettingsForm() {
	f := &a.settingsForm
	s := a.settings
	f.transfers = labelAt(transferValues, numberLabels(transferValues), s.Transfers)
	f.checkers = labelAt(checkerValues, numberLabels(checkerValues), s.Checkers)
	f.retries = labelAt(retryValues, numberLabels(retryValues), s.Retries)
	f.bandwidth = labelAt(bandwidthValues, bandwidthLabels(), s.BandwidthMBps)
	f.retention = labelAt(retentionValues, retentionLabels(), s.RetentionDays)
	f.trash = labelAt(trashValues, trashLabels(), s.TrashDays)
	f.notifyFailed, f.notifySkipped = s.NotifyFailed, s.NotifySkipped
	f.excludeEnabled = s.ExcludeEnabled
	f.globalExclude = strings.Join(s.GlobalExcludes, "\n")
	f.backupICloud = s.BackupICloud
	f.background = s.Background == domain.ConsentGiven
	f.problems = nil
	f.passwordLoad++
	seq := f.passwordLoad
	go func() {
		has := a.svc.HasBackupPassword()
		a.host.Update(func() {
			if f.passwordLoad == seq {
				f.hasPassword = has
			}
		})
	}()
	f.changingPassword, f.password, f.confirm = false, "", ""
}

// saveSettings keeps what the page holds and uses it from the next run on.
// A new backup password is set first: making its key takes a moment, so
// the save runs off the main thread.
func (a *App) saveSettings() {
	f := &a.settingsForm
	if f.saving {
		return
	}
	st := domain.Settings{
		Transfers:     valueAt(transferValues, numberLabels(transferValues), f.transfers),
		Checkers:      valueAt(checkerValues, numberLabels(checkerValues), f.checkers),
		Retries:       valueAt(retryValues, numberLabels(retryValues), f.retries),
		BandwidthMBps: valueAt(bandwidthValues, bandwidthLabels(), f.bandwidth),
		NotifyFailed:  f.notifyFailed,
		NotifySkipped: f.notifySkipped,
		RetentionDays: valueAt(retentionValues, retentionLabels(), f.retention),
		TrashDays:     valueAt(trashValues, trashLabels(), f.trash),
		BackupICloud:  f.backupICloud,
		// The rules every run leaves out, and whether it does.
		ExcludeEnabled: f.excludeEnabled,
		GlobalExcludes: strings.Split(f.globalExclude, "\n"),
		Background:     a.settings.Background,
		Appearance:     a.settings.Appearance,
		// Kept at once by their card, as the appearance is.
		CheckUpdates:   a.settings.CheckUpdates,
		InstallUpdates: a.settings.InstallUpdates,
	}
	switch {
	case f.background:
		st.Background = domain.ConsentGiven
	case st.Background == domain.ConsentGiven:
		st.Background = domain.ConsentRefused
	}
	password, confirm := f.password, f.confirm
	newPassword := password != "" || confirm != ""
	f.passwordLoad++
	f.saving = true
	go func() {
		ctx := context.Background()
		var err error
		if newPassword {
			err = a.svc.SetBackupPassword(ctx, password, confirm)
		}
		saved := st
		if err == nil {
			saved, err = a.svc.SaveSettings(ctx, st)
		}
		hasPassword := a.svc.HasBackupPassword()
		a.host.Update(func() {
			f.hasPassword = hasPassword
			f.saving = false
			if problems := domain.AsProblems(err); problems != nil {
				f.problems = problems
				return
			}
			if err != nil {
				a.toastError(err)
				return
			}
			a.settings, f.problems = saved, nil
			// The saved rules, trimmed and without the repeats, are what
			// the page shows from now on.
			f.globalExclude = strings.Join(saved.GlobalExcludes, "\n")
			if newPassword {
				f.hasPassword, f.changingPassword, f.password, f.confirm = true, false, "", ""
			}
			a.toast(locales.SettingsSaved())
		})
	}()
}

func (a *App) settingsPage(c *ui.Context) {
	t := c.Theme()
	header(c, locales.NavSettings(), nil)
	f := &a.settingsForm
	if !f.loaded {
		a.loadSettingsForm()
		f.loaded = true
	}
	pageScroll(c).Children(func() {
		ui.Column(c).Gap(t.Space(5)).Padding(0, 0, t.Space(2), 0).Children(func() {
			if text := problemText(f.problems.For(domain.FieldSettings)); text != "" {
				result(c, text, true)
			}
			a.appearanceCard(c)
			a.updatesCard(c)
			settingsCard(c, locales.SettingsTransferTitle(), []settingsRow{
				{label: locales.SettingsTransfers(), hint: locales.SettingsTransfersHint(), control: func(label string) {
					ui.Select(c, &f.transfers, numberLabels(transferValues)).Label(label).Width(selectWidth)
				}},
				{label: locales.SettingsCheckers(), hint: locales.SettingsCheckersHint(), control: func(label string) {
					ui.Select(c, &f.checkers, numberLabels(checkerValues)).Label(label).Width(selectWidth)
				}},
				{label: locales.SettingsRetries(), hint: locales.SettingsRetriesHint(), control: func(label string) {
					ui.Select(c, &f.retries, numberLabels(retryValues)).Label(label).Width(selectWidth)
				}},
				{label: locales.SettingsBandwidth(), hint: locales.SettingsBandwidthHint(), control: func(label string) {
					ui.Select(c, &f.bandwidth, bandwidthLabels()).Label(label).Width(selectWidth)
				}},
			})
			settingsCard(c, locales.SettingsTrashTitle(), []settingsRow{
				{label: locales.SettingsTrash(), hint: locales.SettingsTrashHint(), control: func(label string) {
					ui.Select(c, &f.trash, trashLabels()).Label(label).Width(selectWidth)
				}},
			})
			count := excludeRuleCount(f.globalExclude)
			hint := locales.SettingsExcludeRulesHint(count)
			if !f.excludeEnabled {
				hint = locales.SettingsExcludeRulesOff(count)
			}
			rules := settingsRow{
				label: locales.SettingsExcludeRules(),
				hint:  hint,
				control: func(string) {
					if ui.Button(c, locales.SettingsExcludeEdit()).Clicked() {
						a.openGlobalExcludeEditor()
					}
				},
			}
			if code := f.problems.For(domain.FieldGlobalExclude); code != "" {
				rules.problem = problemText(code)
				if code == domain.CodeInvalid {
					rules.problem = locales.SettingsExcludeFailed()
				}
			}
			settingsCard(c, locales.SettingsExcludeTitle(), []settingsRow{
				{label: locales.SettingsExcludeEnabled(), hint: locales.SettingsExcludeEnabledHint(), control: func(label string) {
					ui.Checkbox(c, &f.excludeEnabled, "").Label(label)
				}},
				rules,
			})
			settingsCard(c, locales.SettingsNotifyTitle(), []settingsRow{
				{label: locales.SettingsNotifyFailed(), hint: "", control: func(label string) {
					ui.Checkbox(c, &f.notifyFailed, "").Label(label)
				}},
				{label: locales.SettingsNotifySkipped(), hint: "", control: func(label string) {
					ui.Checkbox(c, &f.notifySkipped, "").Label(label)
				}},
			})
			settingsCard(c, locales.SettingsHistoryTitle(), []settingsRow{
				{label: locales.SettingsRetention(), hint: locales.SettingsRetentionHint(), control: func(label string) {
					ui.Select(c, &f.retention, retentionLabels()).Label(label).Width(selectWidth)
				}},
			})
			if a.svc.SystemScheduler() {
				a.scheduleCard(c)
			}
			if runtime.GOOS == "darwin" {
				// iCloud Drive exists on macOS alone, so the item would
				// only offer a backup that can never be written elsewhere.
				a.backupCard(c)
			}
			ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
				ui.Text(c, locales.SettingsApplied()).Grow(1).MinWidth(0).
					TextColor(t.TextMuted).FontSize(12)
				if ui.PrimaryButton(c, locales.SettingsSave()).Disabled(f.saving).Clicked() {
					a.saveSettings()
				}
			})
		})
	})
}

// selectWidth is the width of the settings' selects, the same for all, so
// that their edges line up down the page.
const selectWidth = 160

// settingsRow is one setting of a card: what it is, what it does, and its
// control, which gets the label to be named by. A problem shows in place
// of the hint, and a warning below it.
type settingsRow struct {
	label, hint string
	control     func(label string)
	problem     string
	warning     string
}

// settingsCard groups the settings of one subject: its title above, then a
// card of rows, each with its words on the left and its control on the
// right, parted by hairlines.
func settingsCard(c *ui.Context, title string, rows []settingsRow) {
	t := c.Theme()
	ui.Column(c).Gap(8).Children(func() {
		ui.Text(c, title).FontSize(16).FontWeight(500)
		ui.Column(c).Radius(12).Border(1, t.Border).Background(t.Surface).Children(func() {
			for i, r := range rows {
				row := ui.Row(c.Key(r.label)).Gap(16).AlignItems(ui.Center).Padding(12, 16)
				if i > 0 {
					row.BorderWidth(1, 0, 0, 0).BorderColor(t.Border)
				}
				row.Children(func() {
					ui.Column(c).Gap(2).Grow(1).MinWidth(0).Children(func() {
						ui.Text(c, r.label)
						switch {
						case r.problem != "":
							ui.Text(c, r.problem).FontSize(12).TextColor(t.Danger)
						case r.hint != "":
							ui.Text(c, r.hint).FontSize(12).TextColor(t.TextMuted)
						}
						if r.warning != "" {
							ui.Row(c).Gap(6).AlignItems(ui.Center).TextColor(t.Warning).Children(func() {
								ui.Icon(c, icons.Warning)
								ui.Text(c, r.warning).FontSize(12).Grow(1).MinWidth(0)
							})
						}
					})
					r.control(r.label)
				})
			}
		})
	})
}

// scheduleCard lets the person choose whether the system runs the
// schedules while the app is closed, and says when the system refuses.
func (a *App) scheduleCard(c *ui.Context) {
	f := &a.settingsForm
	row := settingsRow{label: locales.SettingsBackground(), hint: locales.SettingsBackgroundHint()}
	blocked := false
	if b, _ := a.svc.Background(); b == service.BackgroundBlocked && a.settings.Background == domain.ConsentGiven {
		row.warning, blocked = locales.ScheduleBlocked(), true
	}
	row.control = func(label string) {
		ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
			if blocked && ui.Button(c, locales.ScheduleOpenSettings()).Clicked() {
				a.host.OpenBackgroundSettings()
			}
			ui.Checkbox(c, &f.background, "").Label(label)
		})
	}
	settingsCard(c, locales.SettingsScheduleTitle(), []settingsRow{row})
}

// appearanceCard lets the person choose the window's look. The choice
// shows and is kept at once, apart from the page's Save: nothing runs
// differently for it.
func (a *App) appearanceCard(c *ui.Context) {
	settingsCard(c, locales.SettingsAppearanceTitle(), []settingsRow{{
		label: locales.SettingsAppearance(), hint: locales.SettingsAppearanceHint(),
		control: func(label string) {
			chosen := int(a.settings.Appearance)
			if ui.Segmented(c, &chosen, locales.AppearanceSystem(), locales.AppearanceLight(),
				locales.AppearanceDark()).Label(label).Changed() {
				a.chooseAppearance(domain.Appearance(chosen))
			}
		},
	}})
}

func (a *App) chooseAppearance(choice domain.Appearance) {
	previous := a.settings.Appearance
	a.settings.Appearance = choice
	a.showAppearance()
	go func() {
		err := a.svc.SetAppearance(context.Background(), choice)
		if err == nil {
			return
		}
		a.host.Update(func() {
			if a.settings.Appearance == choice {
				a.settings.Appearance = previous
				a.showAppearance()
			}
			a.toastError(err)
		})
	}()
}
