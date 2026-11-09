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
	serverChoice int                   // the row of the server the form is filled in with, or -1

	// A port means one thing for each kind, so the kinds do not share one:
	// lastKind is the kind the field's port belongs to, and ports keeps the
	// port each kind was given, to come back to.
	lastKind domain.Kind
	ports    map[domain.Kind]string

	// The exports of an NFS server, as a listing found them.
	listingExports bool
	exportsListed  bool
	exports        []string
	exportsHidden  bool   // the server keeps its list to itself, so the path is typed
	exportsFailed  string // why listing failed, other than that
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
	if f.in.UID == "" && f.in.GID == "" {
		// An NFS storage acts as the person by default.
		if uid, gid, ok := service.LocalIDs(); ok {
			f.in.UID, f.in.GID = strconv.Itoa(uid), strconv.Itoa(gid)
		}
	}
	if s == nil {
		f.giveName(a.defaultStorageName(f.in))
	}
	f.lastKind = domain.Kinds[f.kind]
	f.ports = map[domain.Kind]string{f.lastKind: f.in.Port}
	a.storageForm = f
	a.storageTests++ // tests still on their way report to no form
	a.serverSearches++
	a.exportLists++
}

// findServers searches the local network for servers of the form's kind,
// SMB or NFS, off the main thread.
func (a *App) findServers() {
	f := &a.storageForm
	f.searching, f.searchFailed = true, ""
	a.serverSearches++
	seq, kind := a.serverSearches, domain.Kinds[f.kind]
	go func() {
		servers, err := a.svc.FindServers(context.Background(), kind)
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
	a.clearExports()
	f.in.Host, f.in.Port = s.Host, ""
	switch domain.Kinds[f.kind] {
	case domain.KindNFS:
		// The port it announces is NFS's alone: the portmapper says
		// where MOUNT is.
		if s.Path != "" && f.in.Export == "" {
			f.in.Export = s.Path
		}
	default:
		if s.Port != domain.DefaultSMBPort {
			f.in.Port = strconv.Itoa(s.Port)
		}
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
	seq, id, kind := a.storageTests, f.id, f.kind
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), connectionTimeout)
		defer cancel()
		err := a.svc.TestConnection(ctx, id, in)
		a.host.Update(func() {
			if a.storageTests != seq || f.kind != kind {
				return // the form was opened or tested again, or its kind changed since
			}
			f.testing, f.testFailed = false, err != nil
			if p := domain.AsProblems(err); p != nil {
				f.problems, f.testResult = p, ""
				return
			}
			if err != nil {
				f.testResult = connectionText(in.Kind, err)
			} else {
				f.testResult = locales.StorageTestPassed(in.Name)
			}
		})
	}()
}

// connectionText says why connecting to a storage of kind failed.
func connectionText(kind domain.Kind, err error) string {
	switch {
	case errors.Is(err, domain.ErrUnreachable):
		return problemOfCheck(kind, domain.ProblemUnreachable)
	case errors.Is(err, domain.ErrAuthFailed):
		return problemOfCheck(kind, domain.ProblemAuthFailed)
	case errors.Is(err, domain.ErrShareNotFound):
		return problemOfCheck(kind, domain.ProblemShareNotFound)
	case errors.Is(err, domain.ErrNoNFSv3):
		return problemOfCheck(kind, domain.ProblemNoNFSv3)
	case errors.Is(err, domain.ErrNoMount):
		return problemOfCheck(kind, domain.ProblemNoMount)
	}
	return locales.StorageTestFailed(err.Error())
}

// problemOfCheck says a connection problem of a storage of kind in words,
// or "" for one it has none for.
func problemOfCheck(kind domain.Kind, problem string) string {
	nfs := kind == domain.KindNFS
	switch problem {
	case domain.ProblemUnreachable:
		return locales.ErrorUnreachable()
	case domain.ProblemAuthFailed:
		if nfs {
			return locales.ErrorAuthFailedNFS()
		}
		return locales.ErrorAuthFailed()
	case domain.ProblemShareNotFound:
		if nfs {
			return locales.ErrorExportNotFound()
		}
		return locales.ErrorShareNotFound()
	case domain.ProblemNoNFSv3:
		return locales.ErrorNoNFSv3()
	case domain.ProblemNoMount:
		return locales.ErrorNoMount()
	}
	return ""
}

// storageProblem is what the foot of the sheet says about a save that did
// not go through, when the fields cannot say it alone.
func (f *storageForm) storageProblem() string {
	if len(f.problems) == 0 {
		return ""
	}
	return problemText(f.problems[0].Code)
}

// storageDialog adds or edits a storage, as a sheet: its name and kind, the
// fields of that kind, a connection test and the buttons that keep it.
func (a *App) storageDialog(c *ui.Context) {
	f := &a.storageForm
	if !f.open {
		return
	}
	title := locales.StorageDialogNew()
	if f.id != "" {
		title = locales.StorageDialogEdit()
	}
	sheet(c, &f.open, sheetOptions{
		width:    640,
		title:    title,
		subtitle: locales.StoragesSubtitle(),
	}, func() {
		a.storageFormBody(c, f)
	}, func() {
		sheetError(c, f.storageProblem())
		sheetActions(c, func() {
			if accentButton(c, "storage-cancel", nil, locales.ButtonCancel(), buttonSoft).
				Disabled(f.saving).Clicked() {
				f.open = false
			}
			if accentButton(c, "storage-save", nil, locales.StorageSave(), buttonPrimary).
				Disabled(f.saving).Clicked() {
				a.saveStorage()
			}
		})
	})
}

// formControlWidth is how wide a field of the storage sheet is, on the
// right of its row as the settings page has its controls.
const (
	formControlWidth = 260
	formPortWidth    = 110
)

// storageFormBody is what the storage sheet holds, which scrolls under its
// head: groups of rows as on the settings page, the words of each on the
// left and its field on the right. What the storage is comes first, then
// the groups its kind needs, then its connection test.
func (a *App) storageFormBody(c *ui.Context, f *storageForm) {
	ui.Column(c).Gap(22).Padding(2, 0, 2, 0).Children(func() {
		a.storageBasics(c, f)
		// Keyed by kind, so the fields of one kind keep none of the state of
		// those that were in their place, such as a placeholder.
		kind := domain.Kinds[f.kind]
		ui.Column(c).Key(kind).Gap(22).Children(func() {
			switch kind {
			case domain.KindSMB:
				a.smbGroups(c, f)
			case domain.KindNFS:
				a.nfsGroups(c, f)
			default:
				a.s3Groups(c, f)
			}
		})
		a.storageConnection(c, f)
	})
}

// storageBasics names the storage and says what kind it is.
func (a *App) storageBasics(c *ui.Context, f *storageForm) {
	kind := domain.Kinds[f.kind]
	settingsGroup(c, icons.Storages, locales.StorageGroupStorage(), []settingsRow{
		{
			label: locales.StorageFieldName(), hint: locales.StorageNameHint(),
			problem: problemText(f.problems.For(domain.FieldName)),
			field: func(label string) ui.Element {
				// Once the person edits the name, the form stops changing it.
				if f.nameAuto && f.in.Name != f.autoName {
					f.nameAuto = false
				}
				name := ui.TextInput(c, &f.in.Name).Label(label).AutoFocus().Width(formControlWidth)
				if f.id == "" {
					// What the name becomes when it is left empty.
					name.Placeholder(a.defaultStorageName(a.storageFormInput().Normalize()))
				}
				return name
			},
		},
		{
			label: locales.StorageFieldKind(), hint: kindHint(kind),
			control: func(label string) {
				labels := make([]string, len(domain.Kinds))
				for i, k := range domain.Kinds {
					labels[i] = kindLabel(k)
				}
				if ui.Segmented(c, &f.kind, labels...).Label(label).Changed() {
					a.kindChanged()
				}
			},
		},
	})
}

// kindChanged follows the kind the person chose: the name the form gives,
// and what a test, a search or a listing found for the other kind no longer
// holds. The port goes too: one port means one thing for each kind, and the
// value the other kind held would be read as a port of this one.
func (a *App) kindChanged() {
	f := &a.storageForm
	if f.id == "" && f.nameAuto {
		f.giveName(a.defaultStorageName(a.storageFormInput().Normalize()))
	}
	f.testResult, f.testFailed, f.testing, f.problems = "", false, false, nil
	if kind := domain.Kinds[f.kind]; f.lastKind != kind {
		if f.ports == nil {
			f.ports = map[domain.Kind]string{}
		}
		f.ports[f.lastKind] = f.in.Port
		f.in.Port, f.lastKind = f.ports[kind], kind
	}
	f.searched, f.servers, f.searchFailed = false, nil, ""
	f.searching = false
	a.serverSearches++ // a search on its way reports to no kind
	a.clearExports()
}

func kindHint(k domain.Kind) string {
	switch k {
	case domain.KindSMB:
		return locales.StorageKindHintSMB()
	case domain.KindNFS:
		return locales.StorageKindHintNFS()
	}
	return locales.StorageKindHintS3()
}

// s3Groups are the groups of an S3-compatible bucket: the service, then
// the bucket and the key a run signs in with.
func (a *App) s3Groups(c *ui.Context, f *storageForm) {
	secretHint := locales.StorageSecretHint()
	if f.id != "" {
		secretHint = locales.StorageSecretKeptHint()
	}
	endpoint := settingsRow{
		label: locales.StorageFieldEndpoint(), hint: locales.StorageEndpointHint(),
		problem: problemText(f.problems.For(domain.FieldEndpoint)),
		field: func(label string) ui.Element {
			return ui.TextInput(c, &f.in.Endpoint).Label(label).Placeholder("https://").Width(formControlWidth)
		},
	}
	if plaintextEndpoint(f.in.Endpoint) {
		endpoint.warning = locales.StoragePlaintextWarning()
	}
	settingsGroup(c, icons.Bucket, locales.StorageGroupService(), []settingsRow{
		{
			label: locales.StorageFieldProvider(), problem: problemText(f.problems.For(domain.FieldProvider)),
			control: func(label string) {
				if ui.Select(c, &f.provider, providerOptions()).Label(label).Width(formControlWidth).Changed() &&
					f.id == "" && f.nameAuto {
					f.giveName(a.defaultStorageName(a.storageFormInput().Normalize()))
				}
			},
		},
		endpoint,
		{
			label: locales.StorageFieldRegion(), hint: locales.StorageRegionHint(),
			field: func(label string) ui.Element {
				return ui.TextInput(c, &f.in.Region).Label(label).Width(formControlWidth)
			},
		},
	})
	settingsGroup(c, icons.Shield, locales.StorageGroupBucket(), []settingsRow{
		textRow(c, locales.StorageFieldBucket(), "", f.problems.For(domain.FieldBucket), &f.in.Bucket),
		textRow(c, locales.StorageFieldAccessKeyId(), "", f.problems.For(domain.FieldAccessKeyID), &f.in.AccessKeyID),
		{
			label: locales.StorageFieldSecret(), hint: secretHint,
			problem: problemText(f.problems.For(domain.FieldSecret)),
			field: func(label string) ui.Element {
				return ui.TextInput(c, &f.in.Secret).Label(label).Password().Width(formControlWidth)
			},
		},
	})
}

// textRow is a row of one text field of the storage sheet.
func textRow(c *ui.Context, label, hint string, problem domain.Code, value *string) settingsRow {
	return settingsRow{
		label: label, hint: hint, problem: problemText(problem),
		field: func(label string) ui.Element {
			return ui.TextInput(c, value).Label(label).Width(formControlWidth)
		},
	}
}

// serverRows are the rows of where an SMB or NFS server is: its host, its
// port, and the servers a search of the network found to fill them in.
func (a *App) serverRows(c *ui.Context, f *storageForm, kind domain.Kind) []settingsRow {
	portPlaceholder, portHint := strconv.Itoa(domain.DefaultSMBPort), locales.StoragePortHintSMB()
	if kind == domain.KindNFS {
		portPlaceholder, portHint = locales.StoragePortAuto(), locales.StoragePortHintNFS()
	}
	return []settingsRow{
		{
			label: locales.StorageFieldHost(), hint: locales.StorageHostHint(),
			problem: problemText(f.problems.For(domain.FieldHost)),
			field: func(label string) ui.Element {
				host := ui.TextInput(c, &f.in.Host).Label(label).Placeholder("nas.local").Width(formControlWidth)
				if host.Changed() {
					a.clearExports()
					if f.id == "" && f.nameAuto {
						f.giveName(a.defaultStorageName(a.storageFormInput().Normalize()))
					}
				}
				return host
			},
		},
		{
			label: locales.StorageFieldPort(), hint: portHint,
			problem: problemText(f.problems.For(domain.FieldPort)),
			field: func(label string) ui.Element {
				port := ui.TextInput(c, &f.in.Port).Label(label).Placeholder(portPlaceholder).
					FontFeatures("tnum").Width(formPortWidth)
				if port.Changed() {
					a.clearExports()
				}
				return port
			},
		},
		a.foundServersRow(c, f, kind),
	}
}

// smbGroups are the groups of an SMB server: where it is, then what a run
// signs in with.
func (a *App) smbGroups(c *ui.Context, f *storageForm) {
	passwordHint := locales.StoragePasswordHint()
	if f.id != "" {
		passwordHint = locales.StoragePasswordKeptHint()
	}
	settingsGroup(c, icons.Server, locales.StorageGroupServer(), a.serverRows(c, f, domain.KindSMB))
	settingsGroup(c, icons.Shield, locales.StorageGroupSignIn(), []settingsRow{
		textRow(c, locales.StorageFieldUsername(), locales.StorageUsernameHint(),
			f.problems.For(domain.FieldUsername), &f.in.Username),
		{
			label: locales.StorageFieldPassword(), hint: passwordHint,
			problem: problemText(f.problems.For(domain.FieldSecret)),
			field: func(label string) ui.Element {
				return ui.TextInput(c, &f.in.Secret).Label(label).Password().Width(formControlWidth)
			},
		},
	})
}

// nfsGroups are the groups of an NFS export: the server, the folder it
// exports, and the user and group the app acts as there.
func (a *App) nfsGroups(c *ui.Context, f *storageForm) {
	settingsGroup(c, icons.Server, locales.StorageGroupServer(), a.serverRows(c, f, domain.KindNFS))
	settingsGroup(c, icons.Folder, locales.StorageGroupExport(), []settingsRow{a.exportRow(c, f)})
	uidHint, gidHint := locales.StorageIDHint(), locales.StorageIDHint()
	if uid, gid, ok := service.LocalIDs(); ok {
		uidHint = locales.StorageUIDHint(strconv.Itoa(uid))
		gidHint = locales.StorageGIDHint(strconv.Itoa(gid))
	}
	idRow := func(label, hint string, field domain.Field, value *string) settingsRow {
		return settingsRow{
			label: label, hint: hint, problem: problemText(f.problems.For(field)),
			field: func(label string) ui.Element {
				return ui.TextInput(c, value).Label(label).FontFeatures("tnum").Width(formPortWidth)
			},
		}
	}
	settingsGroup(c, icons.Shield, locales.StorageGroupIdentity(), []settingsRow{
		idRow(locales.StorageFieldUID(), uidHint, domain.FieldUID, &f.in.UID),
		idRow(locales.StorageFieldGID(), gidHint, domain.FieldGID, &f.in.GID),
	})
}

// exportRow is the export's path, with a way to list what the server
// exports and choose one.
func (a *App) exportRow(c *ui.Context, f *storageForm) settingsRow {
	p := paletteFor(c.Theme())
	return settingsRow{
		label: locales.StorageFieldExport(), hint: locales.StorageExportHint(),
		problem: problemText(f.problems.For(domain.FieldExport)),
		field: func(label string) ui.Element {
			return ui.TextInput(c, &f.in.Export).Label(label).Placeholder("/volume1/backup").Width(formControlWidth)
		},
		below: func() {
			ui.Row(c).Gap(10).AlignItems(ui.Center).MinHeight(30).Children(func() {
				if accentButton(c, "storage-exports", icons.Search, locales.StorageListExports(), buttonSoft).
					Disabled(f.listingExports).Clicked() {
					a.listExports()
				}
				if f.listingExports {
					ui.Row(c).Gap(7).AlignItems(ui.Center).TextColor(p.muted).Children(func() {
						ui.Spinner(c).Label(locales.StorageListingExports())
						ui.Text(c, locales.StorageListingExports()).FontSize(sizeSmall).SingleLine()
					})
				}
			})
			switch {
			case f.listingExports || !f.exportsListed:
			case f.exportsHidden:
				ui.Text(c, locales.StorageExportsHidden()).FontSize(sizeSmall).TextColor(p.muted).LineHeight(1.45)
			case f.exportsFailed != "":
				result(c, f.exportsFailed, true)
			case len(f.exports) == 0:
				ui.Text(c, locales.StorageExportsNone()).FontSize(sizeSmall).TextColor(p.muted).LineHeight(1.45)
			default:
				a.exportList(c, f)
			}
		},
	}
}

// clearExports invalidates the old server's results and any pending reply.
func (a *App) clearExports() {
	a.exportLists++
	f := &a.storageForm
	f.listingExports, f.exportsListed, f.exports = false, false, nil
	f.exportsHidden, f.exportsFailed = false, ""
}

// listExports asks the server the form names for its exports, off the main thread.
func (a *App) listExports() {
	f := &a.storageForm
	in := a.storageFormInput()
	f.listingExports, f.exportsHidden, f.exportsFailed = true, false, ""
	a.exportLists++
	seq := a.exportLists
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), connectionTimeout)
		defer cancel()
		exports, err := a.svc.NFSExports(ctx, in)
		a.host.Update(func() {
			if a.exportLists != seq {
				return
			}
			f.listingExports, f.exportsListed, f.exports = false, true, exports
			if p := domain.AsProblems(err); p != nil {
				f.problems, f.exportsListed = p, false
				return
			}
			switch {
			case errors.Is(err, domain.ErrExportsHidden):
				// A server may keep the list to itself: that is its
				// policy, not a failure, and the path is typed instead.
				f.exportsHidden = true
			case err != nil:
				f.exportsFailed = exportsText(err)
			}
		})
	}()
}

// exportsText says why listing the exports of a server failed.
func exportsText(err error) string {
	switch {
	case errors.Is(err, domain.ErrExportsHidden):
		return locales.StorageExportsHidden()
	case errors.Is(err, domain.ErrUnreachable), errors.Is(err, domain.ErrAuthFailed), errors.Is(err, domain.ErrNoNFSv3):
		return connectionText(domain.KindNFS, err)
	}
	return locales.StorageExportsFailed(err.Error())
}

// normalizeExport is an export path as the listing and the form compare it:
// one leading slash and none at the end, so the root is "/".
func normalizeExport(p string) string {
	return "/" + strings.Trim(p, "/")
}

// exportList shows the exports listed, one a row: choosing one fills the
// path in. The row of the export the form holds shows as chosen.
func (a *App) exportList(c *ui.Context, f *storageForm) {
	p := paletteFor(c.Theme())
	n := len(f.exports)
	ui.Scroll(c).Height(float32(min(n, serverRows)*listRowHeight+6)).FillWidth().
		ScrollbarInsets(2, 4, 2).Border(1, p.line).Radius(rowRadius).Clip().Children(func() {
		ui.Column(c).Gap(2).Padding(3, 3).Children(func() {
			for _, export := range f.exports {
				chosen := normalizeExport(f.in.Export) == normalizeExport(export)
				listRow(c, export, locales.StorageUseExport(export), chosen, func() {
					iconTile(c, icons.Folder, 24, p.accent, tileFor(p, chosen))
					ui.Text(c, export).FontSize(sizeSmall).TextColor(p.text).Grow(1).MinWidth(0).
						SingleLine().Ellipsis("…")
				}, func() {
					f.in.Export, f.testResult = export, ""
				})
			}
		})
	})
}

// listRowHeight is the height of a row of a list of the storage sheet.
const listRowHeight = 40

// listRow is a row of a list to choose from: chosen shows as such, and
// clicking it runs choose.
func listRow(c *ui.Context, key, label string, chosen bool, content, choose func()) {
	p := paletteFor(c.Theme())
	row := ui.ButtonBase(c.Key(key)).Height(listRowHeight - 4).PaddingX(8).Gap(10).
		Radius(8).AlignItems(ui.Center).Label(label)
	row.Transition(quickTransition)
	switch {
	case chosen:
		row.Background(p.accentSoft)
	case row.Pressed():
		row.Background(p.press)
	case row.Hovered():
		row.Background(p.hover)
	}
	row.Children(func() {
		content()
		if chosen {
			ui.Icon(c, icons.Check).Size(14, 14).TextColor(p.accent)
		}
	})
	if row.Clicked() {
		choose()
	}
}

func tileFor(p palette, chosen bool) ui.Color {
	if chosen {
		return p.card
	}
	return p.accentSoft
}

// storageConnection is the connection test: how it last went, and the
// button that runs it.
func (a *App) storageConnection(c *ui.Context, f *storageForm) {
	p := paletteFor(c.Theme())
	row := settingsRow{
		label: locales.StorageFieldStatus(),
		control: func(string) {
			if accentButton(c, "storage-test", icons.Test, locales.StorageTest(), buttonSoft).
				Disabled(f.testing || f.saving).Clicked() {
				a.testConnection()
			}
		},
	}
	switch {
	case f.testing:
		row.extra = func() {
			ui.Row(c).Gap(7).AlignItems(ui.Center).TextColor(p.muted).Children(func() {
				ui.Spinner(c).Label(locales.StorageTesting())
				ui.Text(c, locales.StorageTesting()).FontSize(sizeSmall).SingleLine()
			})
		}
	case f.testResult != "":
		row.extra = func() { result(c, f.testResult, f.testFailed) }
	default:
		row.hint = locales.StorageTestHint()
	}
	settingsGroup(c, icons.Test, locales.StorageGroupConnection(), []settingsRow{row})
}

// defaultStorageName is the name a new storage gets when the person leaves
// it empty: the provider's name for a bucket, the server's host for a share
// or an export, numbered when that name is taken.
func (a *App) defaultStorageName(in domain.StorageInput) string {
	base := providerLabel(in.Provider)
	if in.Kind.Server() {
		base = in.Host
		if base == "" {
			base = kindLabel(in.Kind)
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

// warning shows something the person should know but that does not stop
// them, as a soft banner of the page's warning color.
func warning(c *ui.Context, text string) {
	p := paletteFor(c.Theme())
	ui.Row(c).Radius(rowRadius).Padding(10, 12).Gap(9).AlignItems(ui.Start).
		Background(p.warningSoft).Children(func() {
		ui.Icon(c, icons.Warning).Size(14, 14).TextColor(p.warning)
		ui.Text(c, text).FontSize(sizeSmall).TextColor(p.warning).Grow(1).MinWidth(0).LineHeight(1.45)
	})
}

// result shows how a check went, with an icon as well as the color.
func result(c *ui.Context, text string, failed bool) {
	p := paletteFor(c.Theme())
	color, svg := p.success, icons.Success
	if failed {
		color, svg = p.danger, icons.Failed
	}
	ui.Row(c).Gap(8).AlignItems(ui.Start).TextColor(color).Children(func() {
		ui.Icon(c, svg).Size(14, 14)
		ui.Text(c, text).FontSize(sizeSmall).Grow(1).MinWidth(0).LineHeight(1.45).Selectable()
	})
}

// serverRows is how many rows a list of the sheet shows before it
// scrolls: the sheet's body may scroll, but the list stays short.
const (
	serverRows      = 3
	serverRowHeight = 44 // a row of the folder browser
)

// foundServersRow searches the network for servers of kind, lists those it
// found to fill the server in with one, and says what to do when it found
// none.
func (a *App) foundServersRow(c *ui.Context, f *storageForm, kind domain.Kind) settingsRow {
	p := paletteFor(c.Theme())
	return settingsRow{
		label: locales.StorageFieldFound(), hint: locales.StorageFoundHint(),
		control: func(string) {
			switch {
			case f.searching:
				ui.Row(c).Gap(7).AlignItems(ui.Center).TextColor(p.muted).Children(func() {
					ui.Spinner(c).Label(locales.StorageFindingServers())
					ui.Text(c, locales.StorageFindingServers()).FontSize(sizeSmall).SingleLine()
				})
			case !f.searched:
				if accentButton(c, "storage-find", icons.Refresh, locales.StorageFindServers(), buttonSoft).Clicked() {
					a.findServers()
				}
			default:
				if accentButton(c, "storage-find-again", icons.Refresh, locales.StorageFindAgain(), buttonSoft).Clicked() {
					a.findServers()
				}
			}
		},
		below: func() {
			if len(f.servers) > 0 {
				a.serverList(c, f, kind)
				ui.Text(c, locales.StorageServersUnverified()).FontSize(sizeMicro).TextColor(p.faint).
					LineHeight(1.45)
			}
			if f.searched && !f.searching {
				switch {
				case f.searchFailed != "":
					result(c, f.searchFailed, true)
				case len(f.servers) == 0:
					none := locales.StorageServersNone()
					if kind == domain.KindNFS {
						none = locales.StorageServersNoneNFS()
					}
					ui.Text(c, none).FontSize(sizeSmall).TextColor(p.muted).LineHeight(1.45)
				}
			}
		},
	}
}

// serverList shows the servers found, one a row: choosing one fills the
// form in with it. The row of the server the form holds shows as chosen.
func (a *App) serverList(c *ui.Context, f *storageForm, kind domain.Kind) {
	p := paletteFor(c.Theme())
	f.serverChoice = -1
	for i, s := range f.servers {
		if strings.EqualFold(s.Host, f.in.Host) {
			f.serverChoice = i
		}
	}
	n := len(f.servers)
	ui.Scroll(c).Height(float32(min(n, serverRows)*(listRowHeight+4)+2)).FillWidth().
		ScrollbarInsets(2, 4, 2).
		Border(1, p.line).Radius(rowRadius).Clip().Children(func() {
		ui.Column(c).Gap(2).Padding(3, 3).Children(func() {
			for i, s := range f.servers {
				address := s.Host
				if kind == domain.KindSMB && s.Port != domain.DefaultSMBPort {
					address += ":" + strconv.Itoa(s.Port)
				}
				if kind == domain.KindNFS && s.Path != "" {
					address += ":" + s.Path
				}
				chosen := i == f.serverChoice
				listRow(c, s.Host, locales.StorageUseServer(s.Name, s.Host), chosen, func() {
					iconTile(c, icons.Server, 26, p.accent, tileFor(p, chosen))
					ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
						// A long name gives way; the address shows whole.
						ui.Text(c, s.Name).FontSize(sizeSmall).FontWeight(560).TextColor(p.text).
							SingleLine().Ellipsis("…")
						ui.Text(c, address).FontSize(sizeMicro).TextColor(p.muted).
							FontFeatures("tnum").SingleLine().Ellipsis("…")
					})
				}, func() { a.useServer(s) })
			}
		})
	})
}
