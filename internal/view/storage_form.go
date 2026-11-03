package view

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/internal/icons"
	"github.com/starriv/cloudshuttle/internal/service"
	"github.com/starriv/cloudshuttle/locales"
)

// storageForm is the dialog that adds or edits a storage.
type storageForm struct {
	open     bool
	id       string // the storage edited, "" for a new one
	kind     int    // the index in domain.Kinds, as the segmented control shows it
	provider string // the provider select's label
	in       domain.StorageInput
	problems domain.Problems

	// nameAuto says the name is one the form gave, not one the person
	// typed: it follows the provider, the kind and the host.
	nameAuto bool
	autoName string

	saving     bool
	testing    bool
	testResult string // the outcome of the last connection test
	testFailed bool

	searching    bool
	searched     bool                  // a search of the network has finished
	servers      []service.FoundServer // what it found
	searchFailed string                // why it failed, if it did
	serverList   ui.ListState
	serverChoice int // the row of the server the form is filled in with, or -1
}

func providerOptions() []string {
	labels := make([]string, len(domain.Providers))
	for i, p := range domain.Providers {
		labels[i] = providerLabel(p)
	}
	return labels
}

func (a *App) openStorageForm(s *domain.Storage) {
	f := storageForm{open: true, in: domain.StorageInput{Kind: domain.KindS3, Provider: domain.ProviderAWS}}
	if s != nil {
		f.id, f.in = s.ID, storageInput(*s)
		if f.in.Provider == "" {
			f.in.Provider = domain.ProviderAWS // for the select, should it become a bucket
		}
	}
	f.kind = max(0, indexOfKind(f.in.Kind))
	f.provider = providerLabel(f.in.Provider)
	if s == nil {
		f.giveName(a.defaultStorageName(f.in))
	}
	a.storageForm = f
	a.storageTests++ // tests still on their way report to no form
	a.serverSearches++
}

// findServers searches the local network for SMB servers off the main
// thread.
func (a *App) findServers() {
	f := &a.storageForm
	f.searching, f.searchFailed = true, ""
	a.serverSearches++
	seq := a.serverSearches
	go func() {
		servers, err := a.svc.FindServers(context.Background())
		a.host.Update(func() {
			if a.serverSearches != seq {
				return
			}
			f.searching, f.searched, f.servers = false, true, servers
			if err != nil {
				f.searchFailed = locales.StorageServersFailed(err.Error())
			}
		})
	}()
}

// giveName gives the form the name it suggests for a new storage, and
// remembers it as the form's own.
func (f *storageForm) giveName(name string) {
	f.in.Name, f.autoName, f.nameAuto = name, name, true
}

// useServer fills the form in with a server found on the network.
func (a *App) useServer(s service.FoundServer) {
	f := &a.storageForm
	f.in.Host, f.in.Port = s.Host, ""
	if s.Port != domain.DefaultSMBPort {
		f.in.Port = strconv.Itoa(s.Port)
	}
	switch {
	case f.id == "" && f.nameAuto && s.Name != "":
		f.giveName(s.Name)
	case f.in.Name == "":
		f.in.Name = s.Name
	}
	f.testResult = ""
}

func indexOfKind(k domain.Kind) int {
	for i, known := range domain.Kinds {
		if k == known {
			return i
		}
	}
	return -1
}

func (a *App) storageFormInput() domain.StorageInput {
	f := &a.storageForm
	in := f.in
	in.Kind = domain.Kinds[f.kind]
	if i := indexOf(providerOptions(), f.provider); i >= 0 {
		in.Provider = domain.Providers[i]
	}
	return in
}

func (a *App) saveStorage() {
	f := &a.storageForm
	in := a.storageFormInput().Normalize()
	if f.id == "" && in.Name == "" {
		// Left unnamed, a storage takes its provider's name, or its
		// server's host for a share.
		in.Name = a.defaultStorageName(in)
	}
	if f.saving {
		return
	}
	if problems := domain.AsProblems(in.Validate(f.id == "")); len(problems) > 0 {
		f.problems = problems
		return
	}
	id, seq := f.id, a.storageTests
	f.saving = true
	go func() {
		ctx := context.Background()
		var st domain.Storage
		var err error
		if id == "" {
			st, err = a.svc.CreateStorage(ctx, in)
		} else {
			st, err = a.svc.UpdateStorage(ctx, id, in)
		}
		a.host.Update(func() {
			if seq != a.storageTests {
				return
			}
			f.saving = false
			if problems := domain.AsProblems(err); problems != nil {
				f.problems = problems
				return
			}
			if err != nil {
				a.toastError(err)
				return
			}
			f.open = false
			f.in.Secret = ""
			a.toast(locales.StorageSaved())
			if !st.Check.Checked() {
				a.checkStorage(st, false)
			}
		})
	}()
}

// testConnection checks what the form holds off the main thread.
func (a *App) testConnection() {
	f := &a.storageForm
	in := a.storageFormInput().Normalize()
	if f.id == "" && in.Name == "" {
		in.Name = a.defaultStorageName(in)
		f.in.Name = in.Name
	}
	if err := in.Validate(f.id == ""); err != nil {
		f.problems = domain.AsProblems(err)
		return
	}
	f.problems, f.testing, f.testResult = nil, true, ""
	a.storageTests++
	seq, id := a.storageTests, f.id
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), connectionTimeout)
		defer cancel()
		err := a.svc.TestConnection(ctx, id, in)
		a.host.Update(func() {
			if a.storageTests != seq {
				return // the form was opened again or tested again since
			}
			f.testing, f.testFailed = false, err != nil
			if p := domain.AsProblems(err); p != nil {
				f.problems, f.testResult = p, ""
				return
			}
			if err != nil {
				f.testResult = connectionText(err)
			} else {
				f.testResult = locales.StorageTestPassed(in.Name)
			}
		})
	}()
}

// connectionText says why connecting to a storage failed.
func connectionText(err error) string {
	switch {
	case errors.Is(err, domain.ErrUnreachable):
		return locales.ErrorUnreachable()
	case errors.Is(err, domain.ErrAuthFailed):
		return locales.ErrorAuthFailed()
	case errors.Is(err, domain.ErrShareNotFound):
		return locales.ErrorShareNotFound()
	}
	return locales.StorageTestFailed(err.Error())
}

func (a *App) storageDialog(c *ui.Context) {
	f := &a.storageForm
	ui.Modal(c, &f.open, func() {
		t := c.Theme()
		ui.Column(c).Width(520).Gap(t.Space(4)).Children(func() {
			title := locales.StorageDialogNew()
			if f.id != "" {
				title = locales.StorageDialogEdit()
			}
			ui.Text(c, title).FontSize(18).FontWeight(500)

			ui.Form(c, func() {
				ui.Field(c, locales.StorageFieldKind(), func() {
					// The field stretches its child; a row lets the control
					// keep the width of its segments.
					ui.Row(c).Children(func() {
						kind := ui.Segmented(c, &f.kind, locales.KindS3(), locales.KindSMB()).
							Label(locales.StorageFieldKind())
						if kind.Changed() {
							if f.id == "" && f.nameAuto {
								f.giveName(a.defaultStorageName(a.storageFormInput().Normalize()))
							}
							// The first time a share is chosen, look for its server.
							if domain.Kinds[f.kind] == domain.KindSMB && !f.searched && !f.searching {
								a.findServers()
							}
						}
						reserveLabels(c, storageLabels()...)
					})
				})
				ui.Field(c, locales.StorageFieldName(), func() {
					// Once the person edits the name, the form stops
					// changing it.
					if f.nameAuto && f.in.Name != f.autoName {
						f.nameAuto = false
					}
					name := ui.TextInput(c, &f.in.Name).AutoFocus()
					if f.id == "" {
						// What the name becomes when it is left empty.
						name.Placeholder(a.defaultStorageName(a.storageFormInput().Normalize()))
					}
				}).Error(problemText(f.problems.For(domain.FieldName)))
				// Keyed by kind, so the fields of one kind keep none of the
				// state of those that were in their place, such as a
				// placeholder.
				kind := domain.Kinds[f.kind]
				ui.Column(c).Key(kind).Gap(t.Space(3)).Children(func() {
					if kind == domain.KindSMB {
						a.smbFields(c, f)
					} else {
						a.s3Fields(c, f)
					}
				})
			})

			if f.testResult != "" {
				result(c, f.testResult, f.testFailed)
			}

			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				label := locales.StorageTest()
				if f.testing {
					label = locales.StorageTesting()
				}
				if iconButton(c, icons.Test, label, false).Disabled(f.testing || f.saving).Clicked() {
					a.testConnection()
				}
				ui.Spacer(c)
				if ui.Button(c, locales.ButtonCancel()).Disabled(f.saving).Clicked() {
					f.open = false
				}
				if ui.PrimaryButton(c, locales.StorageSave()).Disabled(f.saving).Clicked() {
					a.saveStorage()
				}
			})
		})
	})
}

func (a *App) s3Fields(c *ui.Context, f *storageForm) {
	secretHint := locales.StorageSecretHint()
	if f.id != "" {
		secretHint = locales.StorageSecretKeptHint()
	}
	ui.Field(c, locales.StorageFieldProvider(), func() {
		if ui.Select(c, &f.provider, providerOptions()).Changed() && f.id == "" && f.nameAuto {
			f.giveName(a.defaultStorageName(a.storageFormInput().Normalize()))
		}
	}).Error(problemText(f.problems.For(domain.FieldProvider)))
	ui.Field(c, locales.StorageFieldEndpoint(), func() {
		ui.TextInput(c, &f.in.Endpoint).Placeholder("https://")
	}).Description(locales.StorageEndpointHint()).
		Error(problemText(f.problems.For(domain.FieldEndpoint)))
	if plaintextEndpoint(f.in.Endpoint) {
		warning(c, locales.StoragePlaintextWarning())
	}
	ui.Field(c, locales.StorageFieldRegion(), func() {
		ui.TextInput(c, &f.in.Region)
	}).Description(locales.StorageRegionHint())
	ui.Field(c, locales.StorageFieldBucket(), func() {
		ui.TextInput(c, &f.in.Bucket)
	}).Error(problemText(f.problems.For(domain.FieldBucket)))
	ui.Field(c, locales.StorageFieldAccessKeyId(), func() {
		ui.TextInput(c, &f.in.AccessKeyID)
	}).Error(problemText(f.problems.For(domain.FieldAccessKeyID)))
	ui.Field(c, locales.StorageFieldSecret(), func() {
		ui.TextInput(c, &f.in.Secret).Password()
	}).Description(secretHint).Error(problemText(f.problems.For(domain.FieldSecret)))
}

func (a *App) smbFields(c *ui.Context, f *storageForm) {
	passwordHint := locales.StoragePasswordHint()
	if f.id != "" {
		passwordHint = locales.StoragePasswordKeptHint()
	}
	hostError := problemText(f.problems.For(domain.FieldHost))
	if hostError == "" {
		hostError = problemText(f.problems.For(domain.FieldPort))
	}
	ui.Field(c, locales.StorageFieldHost(), func() {
		ui.Row(c).Gap(6).FillWidth().Children(func() {
			host := ui.TextInput(c, &f.in.Host).Placeholder("nas.local").
				Label(locales.StorageFieldHost()).Grow(1).MinWidth(0)
			if host.Changed() && f.id == "" && f.nameAuto {
				f.giveName(a.defaultStorageName(a.storageFormInput().Normalize()))
			}
			ui.TextInput(c, &f.in.Port).Placeholder(strconv.Itoa(domain.DefaultSMBPort)).
				Label(locales.StorageFieldPort()).Width(72)
		})
	}).Description(locales.StorageHostHint()).Error(hostError)
	a.foundServers(c, f)
	ui.Field(c, locales.StorageFieldUsername(), func() {
		ui.TextInput(c, &f.in.Username)
	}).Description(locales.StorageUsernameHint()).
		Error(problemText(f.problems.For(domain.FieldUsername)))
	ui.Field(c, locales.StorageFieldPassword(), func() {
		ui.TextInput(c, &f.in.Secret).Password()
	}).Description(passwordHint).Error(problemText(f.problems.For(domain.FieldSecret)))
}

// defaultStorageName is the name a new storage gets when the person leaves
// it empty: the provider's name for a bucket, the server's host for a share,
// numbered when that name is taken.
func (a *App) defaultStorageName(in domain.StorageInput) string {
	base := providerLabel(in.Provider)
	if in.Kind == domain.KindSMB {
		base = in.Host
		if base == "" {
			base = kindLabel(domain.KindSMB)
		}
	}
	if base == "" {
		base = kindLabel(in.Kind)
	}
	name := base
	for n := 2; a.storageNameTaken(name, ""); n++ {
		name = fmt.Sprintf("%s (%d)", base, n)
	}
	return name
}

// storageNameTaken reports whether another storage already has the name, as
// the database compares them.
func (a *App) storageNameTaken(name, id string) bool {
	for _, s := range a.storages {
		if s.ID != id && strings.EqualFold(s.Name, name) {
			return true
		}
	}
	return false
}

// plaintextEndpoint reports an S3 address whose traffic, and the secret key
// with it, is not encrypted.
func plaintextEndpoint(endpoint string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(endpoint)), "http://")
}

// warning shows something the person should know but that does not stop them.
func warning(c *ui.Context, text string) {
	t := c.Theme()
	ui.Row(c).Gap(8).AlignItems(ui.Start).TextColor(t.Warning).Children(func() {
		ui.Icon(c, icons.Warning)
		ui.Text(c, text).Grow(1).MinWidth(0)
	})
}

// result shows how a check went, with an icon as well as the color.
func result(c *ui.Context, text string, failed bool) {
	t := c.Theme()
	color, svg := t.Success, icons.Success
	if failed {
		color, svg = t.Danger, icons.Failed
	}
	ui.Row(c).Gap(8).AlignItems(ui.Start).TextColor(color).Children(func() {
		ui.Icon(c, svg)
		ui.Text(c, text).Grow(1).MinWidth(0).Selectable()
	})
}

// storageLabels are the labels of the fields of both kinds.
func storageLabels() []string {
	return []string{
		locales.StorageFieldProvider(), locales.StorageFieldEndpoint(), locales.StorageFieldRegion(),
		locales.StorageFieldBucket(), locales.StorageFieldAccessKeyId(), locales.StorageFieldSecret(),
		locales.StorageFieldHost(), locales.StorageFieldUsername(), locales.StorageFieldPassword(),
		locales.StorageFieldFound(),
	}
}

// reserveLabels makes the label column of the Form it is in as wide as
// labels too, though no field shows them, so the controls stay in place as
// the fields change: a Form sizes the column to its widest label. The
// fields are invisible, which keeps them out of drawing, the pointer, the
// focus and assistive technology, in a box that takes no room.
func reserveLabels(c *ui.Context, labels ...string) {
	ui.Box(c).Width(0).Height(0).Invisible().Children(func() {
		for _, label := range labels {
			ui.Field(c, label, func() {})
		}
	})
}

// serverRows is how many servers the list shows before it scrolls: the
// dialog does not scroll, so the list stays short.
const (
	serverRows      = 3
	serverRowHeight = 32
)

// foundServers lists the servers a search of the network found, to fill
// the server in with one, and says what to do when it found none.
func (a *App) foundServers(c *ui.Context, f *storageForm) {
	t := c.Theme()
	ui.Field(c, locales.StorageFieldFound(), func() {
		if len(f.servers) > 0 {
			a.serverList(c, f)
			ui.Text(c, locales.StorageServersUnverified()).TextColor(t.TextMuted).FontSize(11)
		}
		ui.Row(c).Gap(8).AlignItems(ui.Center).FillWidth().Children(func() {
			switch {
			case f.searching:
				ui.Row(c).Gap(8).AlignItems(ui.Center).MinHeight(28).TextColor(t.TextMuted).Children(func() {
					ui.Spinner(c).Label(locales.StorageFindingServers())
					ui.Text(c, locales.StorageFindingServers())
				})
			case !f.searched:
				if iconButton(c, icons.Refresh, locales.StorageFindServers(), false).Clicked() {
					a.findServers()
				}
			default:
				if iconButton(c, icons.Refresh, locales.StorageFindAgain(), false).Clicked() {
					a.findServers()
				}
			}
		})
		if f.searched && !f.searching {
			switch {
			case f.searchFailed != "":
				result(c, f.searchFailed, true)
			case len(f.servers) == 0:
				ui.Text(c, locales.StorageServersNone()).TextColor(t.TextMuted)
			}
		}
	})
}

// serverList shows the servers found, one a row: choosing one fills the
// form in with it. The row of the server the form holds shows as chosen.
func (a *App) serverList(c *ui.Context, f *storageForm) {
	t := c.Theme()
	f.serverChoice = -1
	for i, s := range f.servers {
		if strings.EqualFold(s.Host, f.in.Host) {
			f.serverChoice = i
		}
	}
	f.serverList.Selected = &f.serverChoice
	f.serverList.Key = func(i int) any { return f.servers[i].Host }
	n := len(f.servers)
	list := ui.List(c, &f.serverList, n, func(i int) {
		s := f.servers[i]
		address := s.Host
		if s.Port != domain.DefaultSMBPort {
			address += ":" + strconv.Itoa(s.Port)
		}
		ui.Row(c).Gap(8).AlignItems(ui.Center).Height(serverRowHeight).Padding(0, t.Space(2.5)).Children(func() {
			ui.Icon(c, icons.Server)
			// A long name gives way; the address shows whole.
			ui.Text(c, s.Name).SingleLine().Ellipsis("…").MinWidth(0).Grow(1)
			addr := ui.Text(c, address).SingleLine().Shrink(0)
			if i != f.serverChoice {
				// The chosen row's own color reads on its accent.
				addr.TextColor(t.TextMuted)
			}
		})
	}).Label(locales.StorageFieldFound()).FillWidth().
		Height(float32(min(n, serverRows)*serverRowHeight+2)).
		Border(1, t.Border).Radius(t.Radius)
	if list.Changed() && f.serverChoice >= 0 && f.serverChoice < n {
		a.useServer(f.servers[f.serverChoice])
	}
}
