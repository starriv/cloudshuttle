package view

import (
	"context"

	"github.com/egoist/mygo/ui"
	"github.com/starriv/cloudshuttle/internal/updates"
	"github.com/starriv/cloudshuttle/locales"
)

// SetUpdater gives the window the app's updater, whose state the settings
// page shows. Without one, as in tests, the page has no Updates card.
func (a *App) SetUpdater(u *updates.Updater) { a.updater = u }

// OpenUpdates shows the settings page, where the Updates card is.
func (a *App) OpenUpdates() { a.page = pageSettings }

// updatesCard says which version runs and how its update goes, offers the
// next step, and keeps the person's choice of automatic updates. The
// choices are kept at once, apart from the page's Save, as the appearance
// is: no run works differently for them.
func (a *App) updatesCard(c *ui.Context) {
	if a.updater == nil {
		return
	}
	s := a.updater.State()
	status := settingsRow{label: locales.UpdatesVersion(s.Current), hint: updateStatusText(c, s)}
	if s.Phase == updates.PhaseCheckFailed || s.Phase == updates.PhaseInstallFailed {
		status.hint, status.problem = "", updateStatusText(c, s)
	}
	status.control = func(string) { a.updateActions(c, s) }
	rows := []settingsRow{status}
	if s.Phase == updates.PhaseDownloading {
		rows[0].control = func(string) {
			ui.Column(c).Width(selectWidth).Children(func() {
				progressBar(c, locales.UpdatesDownloading(s.Version), s.Downloaded, s.Total)
			})
		}
	}
	if s.Phase != updates.PhaseUnavailable {
		check, install := a.settings.CheckUpdates, a.settings.InstallUpdates
		rows = append(rows,
			settingsRow{label: locales.SettingsCheckUpdates(), hint: locales.SettingsCheckUpdatesHint(), control: func(label string) {
				if ui.Checkbox(c, &check, "").Label(label).Changed() {
					a.chooseUpdates(check, install)
				}
			}},
			settingsRow{label: locales.SettingsInstallUpdates(), hint: locales.SettingsInstallUpdatesHint(), control: func(label string) {
				if ui.Checkbox(c, &install, "").Label(label).Disabled(!check).Changed() {
					a.chooseUpdates(check, install)
				}
			}},
		)
	}
	settingsCard(c, locales.SettingsUpdatesTitle(), rows)
}

// updateActions are the buttons of the step the update is at.
func (a *App) updateActions(c *ui.Context, s updates.State) {
	ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
		switch s.Phase {
		case updates.PhaseUnavailable:
			ui.Link(c, locales.UpdatesDownloadPage(), updates.ReleasesURL)
		case updates.PhaseAvailable, updates.PhaseInstallFailed:
			ui.Link(c, locales.UpdatesReleaseNotes(), updates.ReleaseURL(s.Version))
			if ui.PrimaryButton(c, locales.UpdatesInstall()).Clicked() {
				a.updater.Install()
			}
		case updates.PhaseReady:
			ui.Link(c, locales.UpdatesReleaseNotes(), updates.ReleaseURL(s.Version))
			if s.Waiting {
				if ui.Button(c, locales.UpdatesCancelRestart()).Clicked() {
					a.updater.CancelRestart()
				}
			} else if ui.PrimaryButton(c, locales.UpdatesRestart()).Clicked() {
				a.updater.Restart()
			}
		default:
			if ui.Button(c, locales.UpdatesCheck()).Disabled(s.Phase == updates.PhaseChecking).Clicked() {
				a.updater.Check()
			}
		}
	})
}

// updateStatusText says where the update is, in a sentence.
func updateStatusText(c *ui.Context, s updates.State) string {
	switch s.Phase {
	case updates.PhaseUnavailable:
		return locales.UpdatesUnavailable()
	case updates.PhaseChecking:
		return locales.UpdatesChecking()
	case updates.PhaseUpToDate:
		return locales.UpdatesUpToDate(formatTime(c, s.LastCheck))
	case updates.PhaseAvailable:
		return locales.UpdatesAvailable(s.Version)
	case updates.PhaseDownloading:
		return locales.UpdatesDownloading(s.Version)
	case updates.PhaseReady:
		if s.Waiting && s.Runs > 0 {
			return locales.UpdatesWaiting(s.Runs)
		}
		return locales.UpdatesReady(s.Version)
	case updates.PhaseCheckFailed:
		return locales.UpdatesCheckFailed()
	case updates.PhaseInstallFailed:
		return locales.UpdatesInstallFailed()
	}
	return locales.UpdatesIdle()
}

// chooseUpdates keeps whether the app checks for and installs updates by
// itself, and tells the updater at once. It installs by itself only what
// it checks for by itself.
func (a *App) chooseUpdates(check, install bool) {
	install = install && check
	previous := a.settings
	a.settings.CheckUpdates, a.settings.InstallUpdates = check, install
	a.updater.SetAutomatic(check, install)
	go func() {
		err := a.svc.SetUpdates(context.Background(), check, install)
		if err == nil {
			return
		}
		a.host.Update(func() {
			if a.settings.CheckUpdates == check && a.settings.InstallUpdates == install {
				a.settings.CheckUpdates, a.settings.InstallUpdates = previous.CheckUpdates, previous.InstallUpdates
				a.updater.SetAutomatic(previous.CheckUpdates, previous.InstallUpdates)
			}
			a.toastError(err)
		})
	}()
}
