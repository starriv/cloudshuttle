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
	staleValues     = []int{3, 7, 14, 30}
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
	notifyStale   bool
	staleDays     string
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

func staleLabels() []string {
	out := make([]string, len(staleValues))
	for i, days := range staleValues {
		out[i] = locales.SettingsStaleDaysValue(days)
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
	f.notifyStale = s.NotifyStale
	f.staleDays = labelAt(staleValues, staleLabels(), s.StaleDays)
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
		NotifyStale:   f.notifyStale,
		StaleDays:     valueAt(staleValues, staleLabels(), f.staleDays),
		RetentionDays: valueAt(retentionValues, retentionLabels(), f.retention),
		TrashDays:     valueAt(trashValues, trashLabels(), f.trash),
		BackupICloud:  f.backupICloud,
		// The rules every run leaves out, and whether it does.
		ExcludeEnabled: f.excludeEnabled,
		GlobalExcludes: strings.Split(f.globalExclude, "\n"),
		Background:     a.settings.Background,
		Appearance:     a.settings.Appearance,
		AppIcon:        a.settings.AppIcon,
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
	p := paletteFor(c.Theme())
	pageHeader(c, locales.NavSettings(), locales.SettingsSubtitle(), nil)
	f := &a.settingsForm
	if !f.loaded {
		a.loadSettingsForm()
		f.loaded = true
	}
	ui.Column(c).Grow(1).MinHeight(0).Children(func() {
		pageScroll(c).Children(func() {
			ui.Column(c).Gap(gutter).Padding(0, 0, pageBottom, 0).Children(func() {
				if text := problemText(f.problems.For(domain.FieldSettings)); text != "" {
					sheetError(c, text)
				}
				ui.Column(c).Gap(22).Children(func() {
					a.appearanceCard(c)
					a.updatesCard(c)
					a.settingsTransfers(c)
					a.settingsTrash(c)
					a.settingsExclude(c)
					a.settingsNotify(c)
					a.settingsHistory(c)
					if a.svc.SystemScheduler() {
						a.scheduleCard(c)
					}
					if runtime.GOOS == "darwin" {
						// iCloud Drive exists on macOS alone, so the item would
						// only offer a backup that can never be written elsewhere.
						a.backupCard(c)
					}
				})
			})
		})
		// The page's one save, a card of its own at the foot so it is always
		// at hand.
		ui.Row(c).Height(56).Margin(10, 0, pageBottom, 0).PaddingX(rowPadX).Radius(cardRadius).
			Background(p.card).Border(1, p.hairline).AlignItems(ui.Center).Gap(12).Children(func() {
			ui.Text(c, locales.SettingsApplied()).FontSize(sizeMicro).TextColor(p.muted).
				Grow(1).MinWidth(0).SingleLine().Ellipsis("…")
			if accentButton(c, "save-settings", nil, locales.SettingsSave(), buttonPrimary).
				Disabled(f.saving).Clicked() {
				a.saveSettings()
			}
		})
	})
}

// settingsTransfers groups what a run's transfer takes from the settings.
func (a *App) settingsTransfers(c *ui.Context) {
	f := &a.settingsForm
	settingsGroup(c, icons.Transfer, locales.SettingsTransferTitle(), []settingsRow{
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
}

// settingsTrash groups how long what a run deletes or replaces is kept.
func (a *App) settingsTrash(c *ui.Context) {
	f := &a.settingsForm
	settingsGroup(c, icons.Archive, locales.SettingsTrashTitle(), []settingsRow{
		{label: locales.SettingsTrash(), hint: locales.SettingsTrashHint(), control: func(label string) {
			ui.Select(c, &f.trash, trashLabels()).Label(label).Width(selectWidth)
		}},
	})
}

// settingsExclude groups the rules every run leaves out.
func (a *App) settingsExclude(c *ui.Context) {
	f := &a.settingsForm
	count := excludeRuleCount(f.globalExclude)
	hint := locales.SettingsExcludeRulesHint(count)
	if !f.excludeEnabled {
		hint = locales.SettingsExcludeRulesOff(count)
	}
	rules := settingsRow{
		label: locales.SettingsExcludeRules(),
		hint:  hint,
		control: func(string) {
			if accentButton(c, "edit-excludes", nil, locales.SettingsExcludeEdit(), buttonSoft).Clicked() {
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
	settingsGroup(c, icons.Filter, locales.SettingsExcludeTitle(), []settingsRow{
		{label: locales.SettingsExcludeEnabled(), hint: locales.SettingsExcludeEnabledHint(), control: func(label string) {
			ui.Checkbox(c, &f.excludeEnabled, "").Label(label)
		}},
		rules,
	})
}

// settingsNotify groups what the app tells the person about.
func (a *App) settingsNotify(c *ui.Context) {
	f := &a.settingsForm
	settingsGroup(c, icons.Bell, locales.SettingsNotifyTitle(), []settingsRow{
		{label: locales.SettingsNotifyFailed(), control: func(label string) {
			ui.Checkbox(c, &f.notifyFailed, "").Label(label)
		}},
		{label: locales.SettingsNotifySkipped(), control: func(label string) {
			ui.Checkbox(c, &f.notifySkipped, "").Label(label)
		}},
		{label: locales.SettingsNotifyStale(), hint: locales.SettingsNotifyStaleHint(), control: func(label string) {
			ui.Checkbox(c, &f.notifyStale, "").Label(label)
		}},
		{label: locales.SettingsStaleDays(), control: func(label string) {
			ui.Select(c, &f.staleDays, staleLabels()).Label(label).Width(selectWidth)
		}},
	})
}

// settingsHistory groups how much of the history the app keeps.
func (a *App) settingsHistory(c *ui.Context) {
	f := &a.settingsForm
	settingsGroup(c, icons.History, locales.SettingsHistoryTitle(), []settingsRow{
		{label: locales.SettingsRetention(), hint: locales.SettingsRetentionHint(), control: func(label string) {
			ui.Select(c, &f.retention, retentionLabels()).Label(label).Width(selectWidth)
		}},
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
	// field is a control that takes the focus, as a text field, in place
	// of control: a click on the row's label focuses it, as a label does
	// the field it names.
	field   func(label string) ui.Element
	problem string
	warning string
	// extra adds to the words on the left, under the hint, such as how a
	// check the row starts went.
	extra func()
	// below takes the card's width under the row, for what is too wide to
	// sit beside the words, such as a list to choose from.
	below func()
}

// settingsGroup groups the settings of one subject: its name above, then
// a card of rows, each with its words on the left and its control on the
// right, parted by hairlines.
func settingsGroup(c *ui.Context, icon *ui.SVG, title string, rows []settingsRow) {
	p := paletteFor(c.Theme())
	ui.Column(c).Gap(7).Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Padding(0, 4).Children(func() {
			if icon != nil {
				ui.Icon(c, icon).Size(13, 13).TextColor(p.muted)
			}
			ui.Text(c, title).FontSize(sizeSmall).FontWeight(620).LetterSpacing(0.3).
				TextColor(p.muted)
		})
		card(c).Children(func() {
			for _, r := range rows {
				row := ui.Row(c.Key(r.label)).Gap(18).AlignItems(ui.Center).Padding(rowPadY, rowPadX)
				row.Children(func() {
					var labelClicked bool
					ui.Column(c).Gap(3).Grow(1).MinWidth(0).Children(func() {
						name := ui.Text(c.Key("label"), r.label).FontSize(sizeBody).FontWeight(540).TextColor(p.text)
						if r.field != nil {
							labelClicked = name.Clicked()
						}
						switch {
						case r.problem != "":
							ui.Text(c, r.problem).FontSize(sizeSmall).TextColor(p.danger).
								LineHeight(1.4)
						case r.hint != "":
							ui.Text(c, r.hint).FontSize(sizeSmall).TextColor(p.muted).LineHeight(1.45)
						}
						if r.warning != "" {
							ui.Row(c).Gap(6).AlignItems(ui.Center).TextColor(p.warning).Children(func() {
								ui.Icon(c, icons.Warning).Size(13, 13)
								ui.Text(c, r.warning).FontSize(sizeSmall).Grow(1).MinWidth(0).LineHeight(1.4)
							})
						}
						if r.extra != nil {
							r.extra()
						}
					})
					switch {
					case r.field != nil:
						if field := r.field(r.label); labelClicked {
							field.Focus()
						}
					case r.control != nil:
						r.control(r.label)
					}
				})
				if r.below != nil {
					ui.Column(c.Key(r.label+"-below")).Gap(8).Padding(0, rowPadX, rowPadY, rowPadX).
						Children(r.below)
				}
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
	settingsGroup(c, icons.Calendar, locales.SettingsScheduleTitle(), []settingsRow{row})
}

// appearanceCard lets the person choose the window's look, and on macOS
// where the app shows its icon. The choices show and are kept at once,
// apart from the page's Save: nothing runs differently for them.
func (a *App) appearanceCard(c *ui.Context) {
	rows := []settingsRow{{
		label: locales.SettingsAppearance(), hint: locales.SettingsAppearanceHint(),
		control: func(label string) {
			chosen := int(a.settings.Appearance)
			if ui.Segmented(c, &chosen, locales.AppearanceSystem(), locales.AppearanceLight(),
				locales.AppearanceDark()).Label(label).Changed() {
				a.chooseAppearance(domain.Appearance(chosen))
			}
		},
	}}
	if runtime.GOOS == "darwin" {
		// The Dock and the menu bar are the macOS edges: elsewhere the
		// app keeps its taskbar entry and its notification-area icon.
		rows = append(rows, settingsRow{
			label: locales.SettingsAppIcon(), hint: appIconHint(a.settings.AppIcon),
			control: func(label string) {
				labels := appIconLabels()
				chosen := appIconLabel(a.settings.AppIcon)
				if ui.Select(c, &chosen, labels).Label(label).Width(selectWidth).Changed() {
					a.chooseAppIcon(appIconFor(chosen))
				}
			},
		})
	}
	settingsGroup(c, icons.Sun, locales.SettingsAppearanceTitle(), rows)
}

// appIconLabels names the placements in the order of domain.AppIcon.
func appIconLabels() []string {
	return []string{
		locales.SettingsAppIconDockAndMenuBar(),
		locales.SettingsAppIconDock(),
		locales.SettingsAppIconMenuBar(),
	}
}

// appIconLabel is the label of a placement, the first one's when it is not
// one the app offers.
func appIconLabel(icon domain.AppIcon) string {
	labels := appIconLabels()
	if i := int(icon); i >= 0 && i < len(labels) {
		return labels[i]
	}
	return labels[0]
}

// appIconFor is the placement a label names; the first when it names none.
func appIconFor(label string) domain.AppIcon {
	for i, l := range appIconLabels() {
		if l == label {
			return domain.AppIcon(i)
		}
	}
	return domain.AppIconDockAndMenuBar
}

// appIconHint says what closing the window does where the icon shows.
func appIconHint(icon domain.AppIcon) string {
	switch icon {
	case domain.AppIconDock:
		return locales.SettingsAppIconDockHint()
	case domain.AppIconMenuBar:
		return locales.SettingsAppIconMenuBarHint()
	}
	return locales.SettingsAppIconDockAndMenuBarHint()
}

// chooseAppIcon shows and keeps where the app's icon goes. A failure to
// keep it puts the icon back where it was.
func (a *App) chooseAppIcon(choice domain.AppIcon) {
	previous := a.settings.AppIcon
	a.settings.AppIcon = choice
	a.showIcon()
	go func() {
		err := a.svc.SetAppIcon(context.Background(), choice)
		if err == nil {
			return
		}
		a.host.Update(func() {
			if a.settings.AppIcon == choice {
				a.settings.AppIcon = previous
				a.showIcon()
			}
			a.toastError(err)
		})
	}()
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
