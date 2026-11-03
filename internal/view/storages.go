package view

import (
	"context"
	"net/url"
	"strconv"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/internal/icons"
	"github.com/starriv/cloudshuttle/locales"
)

// connectionTimeout bounds a connection test.
const connectionTimeout = 20 * time.Second

func (a *App) storagesPage(c *ui.Context) {
	header(c, locales.NavStorages(), func() {
		if iconButton(c, icons.Add, locales.StoragesAdd(), true).Clicked() {
			a.openStorageForm(nil)
		}
	})
	if len(a.storages) == 0 {
		empty(c, icons.Storages, locales.StoragesEmpty(), nil)
		return
	}

	t := c.Theme()
	cols := []ui.TableColumn{
		{Title: locales.StoragesColumnName(), ID: "name", MinWidth: 100},
		{Title: locales.StoragesColumnKind(), ID: "kind", Width: 170},
		{Title: locales.StoragesColumnAddress(), ID: "address", MinWidth: 100},
		{Title: locales.StoragesColumnStatus(), ID: "status", Width: 120},
		{Title: "", ID: "actions", Width: 140, Fixed: true, Align: ui.End},
	}
	a.storageList.Label = func(i int) string { return a.storages[i].Name }
	ui.Table(c, &a.storageList, cols, len(a.storages), func(row, col int) {
		s := a.storages[row]
		switch col {
		case 0:
			ui.Text(c, s.Name).SingleLine()
		case 1:
			svg, label := icons.Bucket, providerLabel(s.Provider)
			if s.Kind == domain.KindSMB {
				svg, label = icons.Server, kindLabel(s.Kind)
			}
			ui.Row(c).Gap(6).AlignItems(ui.Center).MinWidth(0).Children(func() {
				ui.Icon(c, svg).TextColor(t.TextMuted)
				ui.Text(c, label).SingleLine().Ellipsis("…")
			})
		case 2:
			ui.Row(c).Gap(6).AlignItems(ui.Center).MinWidth(0).Children(func() {
				ui.Text(c, storageAddress(s)).SingleLine().Ellipsis("…").TextColor(t.TextMuted).MinWidth(0)
				if plaintextEndpoint(s.Endpoint) {
					ui.Icon(c, icons.Warning).TextColor(t.Warning).Tooltip(locales.StoragePlaintextWarning())
				}
			})
		case 3:
			a.storageStatus(c, s)
		case 4:
			a.storageActions(c, s)
		}
	}).Grow(1)
}

func (a *App) storageActions(c *ui.Context, s domain.Storage) {
	ui.Row(c).Gap(6).Justify(ui.End).Children(func() {
		if iconButton(c, icons.Test, locales.StorageTestShort(), false).Tooltip(locales.StorageTest()).Clicked() {
			a.testStorage(s)
		}
		ui.Button(c, "").Label(locales.StorageActions(s.Name)).Children(func() {
			ui.Icon(c, icons.More)
		}).Menu(func(m *ui.Menu) {
			if m.Item(locales.StorageEdit()).Chosen() {
				a.openStorageForm(&s)
			}
			if m.Item(locales.StorageDelete()).Chosen() {
				a.confirmDeleteStorage(s)
			}
		})
	})
}

// storageAddress is where a storage is: the bucket at its endpoint, as a
// path-style URL names it without the scheme (the bucket alone for Amazon
// S3, which needs no endpoint), or the SMB host, with its port when it is
// not the usual one.
func storageAddress(s domain.Storage) string {
	if s.Kind != domain.KindSMB {
		host := s.Endpoint
		if u, err := url.Parse(s.Endpoint); err == nil && u.Host != "" {
			host = u.Host
		}
		if host == "" {
			return s.Bucket
		}
		return host + "/" + s.Bucket
	}
	if s.Port != 0 && s.Port != domain.DefaultSMBPort {
		return s.Host + ":" + strconv.Itoa(s.Port)
	}
	return s.Host
}

func storageInput(s domain.Storage) domain.StorageInput {
	in := domain.StorageInput{Name: s.Name, Kind: s.Kind}
	if s.Kind == domain.KindSMB {
		in.Host, in.Username = s.Host, s.Username
		if s.Port != 0 && s.Port != domain.DefaultSMBPort {
			in.Port = strconv.Itoa(s.Port)
		}
		return in
	}
	in.Kind = domain.KindS3
	in.Provider, in.Endpoint, in.Region = s.Provider, s.Endpoint, s.Region
	in.Bucket, in.AccessKeyID = s.Bucket, s.AccessKeyID
	return in
}

// testStorage checks a saved storage off the main thread and toasts the
// outcome.
func (a *App) testStorage(s domain.Storage) {
	a.checkStorage(s, true)
}

// checkStorage tests a saved storage off the main thread; the service
// records how it went, which the status column shows. told toasts it too.
func (a *App) checkStorage(s domain.Storage, told bool) {
	if a.checking[s.ID] {
		return
	}
	a.checking[s.ID] = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), connectionTimeout)
		defer cancel()
		check, err := a.svc.CheckStorage(ctx, s.ID)
		a.host.Update(func() {
			delete(a.checking, s.ID)
			switch {
			case err != nil:
				a.toastError(err)
			case !told:
			case check.OK:
				a.toast(locales.StorageTestPassed(s.Name))
			default:
				a.toasts = append(a.toasts, ui.Toast{Title: checkProblem(check), Type: "error"})
			}
		})
	}()
}

// storageStatus shows how a storage last tested, with an icon and a word.
func (a *App) storageStatus(c *ui.Context, s domain.Storage) {
	t := c.Theme()
	svg, label, color, tip := icons.Untested, locales.CheckNone(), t.TextMuted, locales.CheckNoneHint()
	switch {
	case a.checking[s.ID]:
		svg, label, color, tip = icons.Running, locales.StorageTesting(), t.Accent, ""
	case !s.Check.Checked():
	case s.Check.OK:
		svg, label, color = icons.Success, locales.CheckOK(), t.Success
		tip = locales.CheckOKAt(formatTime(c, s.Check.At))
	default:
		svg, label, color = icons.Failed, locales.CheckFailed(), t.Danger
		tip = locales.CheckFailedAt(formatTime(c, s.Check.At), checkProblem(s.Check))
	}
	row := ui.Row(c).Gap(6).AlignItems(ui.Center).MinWidth(0).TextColor(color).Children(func() {
		icon := ui.Icon(c, svg)
		if a.checking[s.ID] {
			icon.Rotate(icon.Loop("spin", time.Second, ui.Linear) * 360)
		}
		ui.Text(c, label).SingleLine().Ellipsis("…")
	})
	if tip != "" {
		row.Tooltip(tip)
	}
}

// checkProblem says why a test failed.
func checkProblem(c domain.Check) string {
	switch c.Problem {
	case domain.ProblemUnreachable:
		return locales.ErrorUnreachable()
	case domain.ProblemAuthFailed:
		return locales.ErrorAuthFailed()
	case domain.ProblemShareNotFound:
		return locales.ErrorShareNotFound()
	}
	return locales.StorageTestFailed(c.Message)
}

func (a *App) confirmDeleteStorage(s domain.Storage) {
	a.confirm = confirmation{
		open:    true,
		title:   locales.StorageDeleteTitle(s.Name),
		message: deleteStorageMessage(s),
		action: func() {
			go func() {
				err := a.svc.DeleteStorage(context.Background(), s.ID)
				a.host.Update(func() {
					if err != nil {
						a.toastError(err)
						return
					}
					a.toast(locales.StorageDeleted())
				})
			}()
		},
	}
}

func deleteStorageMessage(s domain.Storage) string {
	if s.Kind == domain.KindSMB {
		return locales.StorageDeleteMessageShare()
	}
	return locales.StorageDeleteMessage()
}
