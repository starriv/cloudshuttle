package view

import (
	"context"
	"errors"

	"github.com/egoist/mygo/ui"
	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/internal/icons"
	"github.com/starriv/cloudshuttle/internal/store"
	"github.com/starriv/cloudshuttle/locales"
)

// restoreForm is the dialog that restores the configuration from a backup.
type restoreForm struct {
	open     bool
	path     string
	password string
	problem  string
	busy     bool
}

// backupCard is the settings of the iCloud backup: whether to keep one, the
// password it is encrypted with, and the way back from one.
func (a *App) backupCard(c *ui.Context) {
	f := &a.settingsForm
	rows := []settingsRow{
		{label: locales.SettingsBackupICloud(), hint: locales.SettingsBackupICloudHint(), control: func(label string) {
			ui.Checkbox(c, &f.backupICloud, "").Label(label)
		}},
	}
	if f.backupICloud || f.hasPassword {
		row := settingsRow{label: locales.SettingsBackupPassword(), hint: locales.SettingsBackupPasswordHint()}
		if p := f.problems.For(domain.FieldBackupPassword); p != "" {
			row.problem = backupPasswordText(p)
		} else if p := f.problems.For(domain.FieldBackupConfirm); p != "" {
			row.problem = backupPasswordText(p)
		}
		if a.settings.BackupICloud && !f.hasPassword {
			row.warning = locales.SettingsBackupPasswordMissing()
		}
		if f.hasPassword && !f.changingPassword {
			row.hint = locales.SettingsBackupPasswordSet()
			row.control = func(string) {
				if ui.Button(c, locales.SettingsBackupPasswordChange()).Clicked() {
					f.changingPassword = true
				}
			}
		} else {
			row.control = func(label string) {
				ui.Column(c).Gap(6).Width(selectWidth + 40).Children(func() {
					ui.TextInput(c, &f.password).Password().Placeholder(locales.SettingsBackupPasswordNew()).
						Label(label)
					ui.TextInput(c, &f.confirm).Password().Placeholder(locales.SettingsBackupPasswordConfirm()).
						Label(locales.SettingsBackupPasswordConfirm())
				})
			}
		}
		rows = append(rows, row)
	}
	rows = append(rows, settingsRow{label: locales.RestoreTitle(), hint: locales.RestoreHint(), control: func(string) {
		if ui.Button(c, locales.RestoreOpen()).Clicked() {
			a.restoreForm = restoreForm{open: true}
		}
	}})
	settingsGroup(c, icons.Shield, locales.SettingsBackupTitle(), rows)
}

func backupPasswordText(code domain.Code) string {
	switch code {
	case domain.CodeMismatch:
		return locales.ErrorPasswordMismatch()
	case domain.CodeRequired:
		return locales.ErrorPasswordRequired()
	}
	return problemText(code)
}

// restoreDialog asks for the backup and its password, says what restoring
// replaces, and restores off the main thread: opening the backup stretches
// the password on purpose.
func (a *App) restoreDialog(c *ui.Context) {
	f := &a.restoreForm
	if !f.open {
		return
	}
	p := paletteFor(c.Theme())
	sheet(c, &f.open, sheetOptions{
		width: 520,
		title: locales.RestoreTitle(),
	}, func() {
		ui.Column(c).Gap(16).Padding(2, 0, 2, 0).Children(func() {
			ui.Text(c, locales.RestoreMessage()).FontSize(sizeSmall).TextColor(p.muted).LineHeight(1.55)
			ui.Field(c, locales.RestoreFile(), func() {
				ui.Row(c).Gap(8).FillWidth().Children(func() {
					ui.TextInput(c, &f.path).Placeholder(BackupFileHint()).Grow(1).MinWidth(0)
					if accentButton(c, "restore-choose", nil, locales.RestoreChoose(), buttonSoft).
						Disabled(f.busy).Clicked() {
						a.chooseBackup()
					}
				})
			})
			ui.Field(c, locales.RestorePassword(), func() {
				ui.TextInput(c, &f.password).Password().AutoFocus()
			}).Description(locales.RestorePasswordHint())
			if f.problem != "" {
				result(c, f.problem, true)
			}
		})
	}, func() {
		sheetActions(c, func() {
			if accentButton(c, "restore-cancel", nil, locales.ButtonCancel(), buttonSoft).
				Disabled(f.busy).Clicked() {
				f.open = false
			}
			label := locales.RestoreButton()
			if f.busy {
				label = locales.RestoreBusy()
			}
			if accentButton(c, "restore-go", nil, label, buttonPrimary).
				Disabled(f.busy || f.path == "").Clicked() {
				a.restore()
			}
		})
	})
}

// BackupFileHint is the name of the backup file, as the placeholder of its
// path.
func BackupFileHint() string { return "cloudshuttle.backup" }

func (a *App) chooseBackup() {
	f := &a.restoreForm
	go func() {
		dir, _ := a.svc.BackupFolder()
		path, err := a.host.ChooseFile(dir)
		if err != nil || path == "" {
			return
		}
		a.host.Update(func() { f.path, f.problem = path, "" })
	}()
}

func (a *App) restore() {
	f := &a.restoreForm
	f.busy, f.problem = true, ""
	path, password := f.path, f.password
	go func() {
		err := a.svc.RestoreBackup(context.Background(), path, password)
		a.host.Update(func() {
			f.busy = false
			if err != nil {
				f.problem = restoreErrorText(err)
				return
			}
			f.open, f.password = false, ""
			a.settingsForm.loaded = false // read the restored settings
			a.toast(locales.RestoreDone())
		})
	}()
}

func restoreErrorText(err error) string {
	switch {
	case errors.Is(err, domain.ErrWrongPassword):
		return locales.RestoreWrongPassword()
	case errors.Is(err, domain.ErrNotBackup):
		return locales.RestoreNotBackup()
	case errors.Is(err, domain.ErrRunsActive):
		return locales.RestoreRunsActive()
	case errors.Is(err, store.ErrNewerDatabase):
		return locales.RestoreNewer()
	}
	return errorText(err)
}
