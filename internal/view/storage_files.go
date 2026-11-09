package view

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"

	"github.com/egoist/mygo/ui"
	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/internal/icons"
	"github.com/starriv/cloudshuttle/internal/service"
	"github.com/starriv/cloudshuttle/locales"
)

type storageFiles struct {
	storage                        domain.Storage
	dir, filter                    string
	entries                        []domain.StorageEntry
	list                           ui.ListState
	selected, crumb                int
	loadCancel, operationCancel    context.CancelFunc
	loadID                         int
	limited, loading, busy         bool
	queued                         bool // the busy change waits for a run to finish
	loadError, operationError      string
	editKind, editValue, editError string
	editOpen                       bool
	moveSource, moveFrom, moveName string
	editEntry                      domain.StorageEntry
}

func (a *App) selectPage(page string) {
	if a.storageFiles != nil {
		a.closeStorageFiles()
	}
	a.page = page
}

func (a *App) closeStorageFiles() {
	b := a.storageFiles
	if b == nil {
		return
	}
	if b.loadCancel != nil {
		b.loadCancel()
	}
	if b.operationCancel != nil {
		b.operationCancel()
	}
	a.storageFiles = nil
}

func (a *App) loadStorageFiles(dir string) {
	b := a.storageFiles
	if b.loadCancel != nil {
		b.loadCancel()
	}
	if b.dir != dir {
		b.filter, b.operationError = "", ""
	}
	b.dir, b.editOpen = dir, false
	b.entries, b.selected, b.loading, b.limited, b.loadError = nil, -1, true, false, ""
	b.list = ui.ListState{}
	b.loadID++
	id := b.loadID
	ctx, cancel := context.WithTimeout(context.Background(), connectionTimeout)
	b.loadCancel = cancel
	go func() {
		defer cancel()
		entries, limited, err := a.svc.StorageEntries(ctx, b.storage, dir)
		slices.SortFunc(entries, func(x, y domain.StorageEntry) int {
			if x.Directory != y.Directory {
				if x.Directory {
					return -1
				}
				return 1
			}
			if order := strings.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name)); order != 0 {
				return order
			}
			return strings.Compare(x.Name, y.Name)
		})
		a.host.Update(func() {
			if a.storageFiles != b || b.loadID != id {
				return
			}
			b.loading, b.loadCancel = false, nil
			if err != nil {
				b.loadError = storageFilesError(err)
				return
			}
			b.entries, b.limited = entries, limited
		})
	}()
}

func storageFilesError(err error) string {
	switch {
	case errors.Is(err, domain.ErrStorageDenied), errors.Is(err, domain.ErrAuthFailed):
		return locales.FilesDenied()
	case errors.Is(err, domain.ErrStorageMissing), errors.Is(err, domain.ErrShareNotFound):
		return locales.FilesMissing()
	case errors.Is(err, domain.ErrStoragePath):
		return locales.FilesInvalid()
	case errors.Is(err, domain.ErrStorageExists):
		return locales.FilesExists()
	case errors.Is(err, domain.ErrStorageProtected):
		return locales.FilesProtected()
	case errors.Is(err, domain.ErrStorageNotEmpty):
		return locales.FilesNotEmpty()
	case errors.Is(err, domain.ErrStorageMoveUnsupported):
		return locales.FilesMoveUnsupported()
	case errors.Is(err, domain.ErrStorageMoveTooLarge):
		return locales.FilesMoveTooLarge()
	case errors.Is(err, domain.ErrStorageIncompleteWrite):
		return locales.FilesIncompleteWrite()
	case errors.Is(err, domain.ErrStorageEntryChanged):
		return locales.FilesEntryChanged()
	case errors.Is(err, domain.ErrStorageChanged):
		return locales.FilesChanged()
	case errors.Is(err, context.DeadlineExceeded):
		return locales.FilesTimeout()
	case runtime.GOOS == "darwin" && errors.Is(err, syscall.EHOSTUNREACH):
		return locales.FilesLocalRoute()
	case errors.Is(err, domain.ErrUnreachable):
		return locales.ErrorUnreachable()
	case errors.Is(err, domain.ErrMissingSecret):
		return locales.FilesMissingSecret()
	case errors.Is(err, domain.ErrNotFound):
		return locales.FilesChanged()
	default:
		return locales.FilesFailed()
	}
}

func (a *App) storageFilesPage(c *ui.Context, b *storageFiles) {
	p := paletteFor(c.Theme())
	root := b.storage.Bucket
	if b.storage.Kind == domain.KindSMB {
		root = b.storage.Host
	}
	pageHeader(c, b.storage.Name, "", func() {
		if accentButton(c, "files-back", icons.Storages, locales.FilesBack(), buttonSoft).Clicked() {
			a.closeStorageFiles()
		}
	})
	if a.storageFiles != b {
		return
	}

	card(c).Grow(1).MinHeight(0).Margin(0, 0, pageBottom, 0).Clip().Children(func() {
		a.storageFilesNavigation(c, b, root)
		if a.storageFiles == b {
			a.storageFilesList(c, b, p)
		}
	})
}

func (a *App) storageFilesNavigation(c *ui.Context, b *storageFiles, root string) {
	parts := strings.Split(b.dir, "/")
	if b.dir == "" {
		parts = nil
	}
	crumbs := append([]string{root}, parts...)
	p := paletteFor(c.Theme())
	ui.Row(c).Height(48).PaddingX(12).Gap(6).AlignItems(ui.Center).Children(func() {
		floor := 0
		if b.moveSource != "" && b.storage.Kind == domain.KindSMB {
			floor = 1
		}
		if len(parts) == 0 && b.moveSource == "" {
			if iconAction(c, "files-root-back", icons.ArrowUp, locales.FilesParent()).Tooltip(locales.FilesBack()).Clicked() {
				a.closeStorageFiles()
				return
			}
		} else if iconAction(c, "files-up", icons.ArrowUp, locales.FilesParent()).Tooltip(locales.FilesParent()).
			Disabled(b.busy || len(parts) <= floor).Clicked() {
			a.loadStorageFiles(strings.Join(parts[:len(parts)-1], "/"))
		}
		ui.Column(c).Grow(1).MinWidth(0).Clip().Children(func() {
			b.crumb = len(parts) - floor
			if ui.Breadcrumbs(c, crumbs[floor:], &b.crumb).Label(locales.BrowsePath()).Changed() &&
				!b.busy && b.crumb+floor < len(parts) {
				a.loadStorageFiles(strings.Join(parts[:b.crumb+floor], "/"))
			}
		})
		if accentButton(c, "files-go", nil, locales.FilesGo(), buttonGhost).
			Disabled(b.busy || b.moveSource != "").Clicked() {
			b.editOpen, b.editKind, b.editValue, b.editError = true, "go", b.dir, ""
		}
		if iconAction(c, "files-refresh", icons.Refresh, locales.FilesRefresh()).Tooltip(locales.FilesRefresh()).
			Disabled(b.busy).Clicked() {
			a.loadStorageFiles(b.dir)
		}
	})
	if a.storageFiles != b {
		return
	}
	ui.Row(c).Height(48).PaddingX(14).Gap(8).AlignItems(ui.Center).Children(func() {
		if b.moveSource != "" {
			ui.Text(c, locales.FilesMoving(b.moveName)).Grow(1).MinWidth(0).SingleLine().Ellipsis("…").TextColor(p.muted)
			if accentButton(c, "files-move-cancel", nil, locales.ButtonCancel(), buttonSoft).Clicked() {
				dir := b.moveFrom
				b.moveSource = ""
				a.loadStorageFiles(dir)
			}
			if accentButton(c, "files-move-here", nil, locales.FilesMoveHere(), buttonPrimary).
				Disabled(b.loading || b.loadError != "" || b.dir == b.moveFrom).Clicked() {
				entry := b.editEntry
				change := service.StorageChange{Source: b.moveSource, Target: storageChild(b.dir, b.moveName), ExpectedSource: &entry}
				b.moveSource = ""
				a.changeStorageFiles(b, change)
			}
			return
		}
		if ui.TextInput(c, &b.filter).Label(locales.FilesFilter()).Placeholder(locales.FilesFilter()).
			Grow(1).MinWidth(0).MaxWidth(320).Changed() {
			b.selected = -1
			b.list.ScrollTo(0, ui.Start)
		}
		ui.Spacer(c)
		disabled := b.busy || b.loading || b.loadError != "" || b.atShares()
		if accentButton(c, "files-mkdir", icons.Add, locales.FilesNewFolder(), buttonSoft).Disabled(disabled).Clicked() {
			b.editOpen, b.editKind, b.editValue, b.editError = true, "mkdir", "", ""
		}
		if accentButton(c, "files-upload", icons.Upload, locales.FilesUpload(), buttonPrimary).Disabled(disabled).Clicked() {
			a.chooseStorageFile(b, nil)
		}
	})
}

func (b *storageFiles) atShares() bool {
	return b.storage.Kind == domain.KindSMB && b.dir == ""
}

func (a *App) storageFileEditDialog(c *ui.Context) {
	b := a.storageFiles
	if b == nil || !b.editOpen {
		return
	}
	title, label, submit := locales.FilesRenameTitle(), locales.FilesNewName(), locales.FilesRename()
	switch b.editKind {
	case "go":
		title, label, submit = locales.FilesGo(), locales.FilesPath(), locales.BrowseOpen()
	case "mkdir":
		title, submit = locales.FilesNewFolder(), locales.FilesCreate()
	}
	submitted := false
	sheet(c, &b.editOpen, sheetOptions{width: 440, title: title}, func() {
		ui.Field(c, label, func() {
			submitted = ui.TextInput(c, &b.editValue).Label(label).FillWidth().AutoFocus().Submitted()
		})
		sheetError(c, b.editError)
	}, func() {
		sheetActions(c, func() {
			if accentButton(c, "files-edit-cancel", nil, locales.ButtonCancel(), buttonSoft).Clicked() {
				b.editOpen = false
			}
			if accentButton(c, "files-edit-submit", nil, submit, buttonPrimary).Clicked() || submitted {
				a.submitStorageEdit(b)
			}
		})
	})
}

func (a *App) storageFilesList(c *ui.Context, b *storageFiles, p palette) {
	query := strings.ToLower(strings.TrimSpace(b.filter))
	entries := make([]domain.StorageEntry, 0, len(b.entries))
	for _, entry := range b.entries {
		if (b.moveSource == "" || entry.Directory) && strings.Contains(strings.ToLower(entry.Name), query) {
			entries = append(entries, entry)
		}
	}
	ui.Row(c).Height(32).PaddingX(22).Gap(12).AlignItems(ui.Center).Background(p.cardAlt).Children(func() {
		ui.Text(c, locales.FilesName()).Grow(1).MinWidth(0).FontSize(sizeMicro).TextColor(p.muted)
		ui.Text(c, locales.FilesSize()).Width(88).TextAlign(ui.End).FontSize(sizeMicro).TextColor(p.muted)
		ui.Text(c, locales.FilesModified()).Width(140).FontSize(sizeMicro).TextColor(p.muted)
	})
	switch {
	case b.loading:
		storageFilesEmpty(c, p, icons.Folder, locales.BrowseLoading(), "", true)
	case b.loadError != "":
		storageFilesEmpty(c, p, icons.Warning, b.loadError, "", false)
	case len(entries) == 0 && query != "":
		storageFilesEmpty(c, p, icons.Search, locales.FilesEmpty(), locales.FilesFilterHint(), false)
	case len(entries) == 0:
		storageFilesEmpty(c, p, icons.FolderOpen, locales.FilesFolderEmpty(), "", false)
	default:
		a.storageFilesRows(c, b, p, entries)
	}
	ui.Row(c).Height(42).PaddingX(14).Gap(8).AlignItems(ui.Center).Background(p.cardAlt).Children(func() {
		message := ""
		if !b.loading && b.loadError == "" {
			message = locales.FilesCount(formatCount(len(entries)))
		}
		if b.limited {
			message = locales.FilesLimited()
		}
		if b.operationError != "" {
			message = b.operationError
			ui.Icon(c, icons.Warning).Size(14, 14).TextColor(p.warning)
		}
		if b.busy {
			message = locales.FilesWorking()
			if b.queued {
				message = locales.FilesQueued()
			}
			ui.Spinner(c).Label(message)
		}
		ui.Text(c, message).Grow(1).MinWidth(0).FontSize(sizeSmall).TextColor(p.muted).
			SingleLine().Ellipsis("…").Tooltip(message)
		if b.busy {
			if accentButton(c, "files-stop", nil, locales.ButtonCancel(), buttonSoft).Clicked() {
				b.operationCancel()
			}
		} else if b.loadError != "" {
			if accentButton(c, "files-retry", icons.Refresh, locales.FilesRetry(), buttonSoft).Clicked() {
				a.loadStorageFiles(b.dir)
			}
		} else if entry, ok := b.selectedEntry(entries); ok {
			if entry.Directory {
				if accentButton(c, "files-open-entry", nil, locales.FilesOpenFolder(), buttonSoft).Clicked() {
					a.loadStorageFiles(storageChild(b.dir, entry.Name))
				}
			} else if accentButton(c, "files-download", icons.Download, locales.FilesDownload(), buttonSoft).Clicked() {
				a.chooseStorageFile(b, &entry)
			}
			iconAction(c, "files-more", icons.More, locales.FilesActions()).Tooltip(locales.FilesActions()).Menu(func(m *ui.Menu) {
				a.storageEntryMenu(m, b, entry)
			})
		}
	})
}

func storageFilesEmpty(c *ui.Context, p palette, icon *ui.SVG, title, hint string, loading bool) {
	ui.Column(c).Grow(1).MinHeight(0).AlignItems(ui.Center).Justify(ui.Center).Gap(8).Children(func() {
		if loading {
			ui.Spinner(c).Label(title)
		} else {
			iconTile(c, icon, 42, p.muted, p.cardAlt)
		}
		ui.Text(c, title).FontSize(sizeBody).FontWeight(600).TextColor(p.text)
		if hint != "" {
			ui.Text(c, hint).FontSize(sizeSmall).TextColor(p.muted)
		}
	})
}

func (b *storageFiles) selectedEntry(entries []domain.StorageEntry) (domain.StorageEntry, bool) {
	if b.selected < 0 || b.selected >= len(entries) {
		return domain.StorageEntry{}, false
	}
	return entries[b.selected], true
}

func (a *App) storageFilesRows(c *ui.Context, b *storageFiles, p palette, entries []domain.StorageEntry) {
	b.list.Selected = &b.selected
	b.list.RoundRows = true
	b.list.Label = func(i int) string { return entries[i].Name }
	b.list.Key = func(i int) any {
		return struct {
			name string
			dir  bool
		}{entries[i].Name, entries[i].Directory}
	}
	list := ui.List(c, &b.list, len(entries), func(i int) {
		entry := entries[i]
		fg, soft := p.text, p.muted
		if b.selected == i {
			fg, soft = p.accentText, p.accentText.Alpha(0.78)
		}
		ui.Row(c).Height(36).PaddingX(14).Gap(12).AlignItems(ui.Center).Children(func() {
			ui.Row(c).Gap(9).AlignItems(ui.Center).Grow(1).MinWidth(0).Children(func() {
				icon := icons.File
				if entry.Directory {
					icon = icons.Folder
				}
				ui.Icon(c, icon).Size(16, 16).TextColor(soft)
				ui.Text(c, entry.Name).FontSize(sizeSmall).TextColor(fg).SingleLine().
					Ellipsis("…").Tooltip(entry.Name).Grow(1).MinWidth(0)
			})
			bytes, modified := "", ""
			if !entry.Directory {
				bytes = size(entry.Size)
				if !entry.Modified.IsZero() {
					modified = formatStarted(c, entry.Modified)
				}
			}
			ui.Text(c, bytes).Width(88).TextAlign(ui.End).FontSize(sizeSmall).TextColor(soft)
			ui.Text(c, modified).Width(140).SingleLine().FontSize(sizeSmall).TextColor(soft)
		})
	}).Label(locales.FilesBrowse()).Grow(1).MinHeight(0).Padding(4, 8).ScrollbarInsets(4, 4, 4)
	if b.busy {
		return
	}
	list.ContextMenu(func(m *ui.Menu) {
		if entry, ok := b.selectedEntry(entries); ok {
			a.storageEntryMenu(m, b, entry)
		}
	})
	if entry, ok := b.selectedEntry(entries); ok {
		switch {
		case list.Submitted() && entry.Directory:
			a.loadStorageFiles(storageChild(b.dir, entry.Name))
		case list.Submitted():
			a.chooseStorageFile(b, &entry)
		case list.Shortcut(ui.Super, ui.KeyBackspace) || list.Shortcut(0, ui.KeyDelete):
			if !b.atShares() && b.moveSource == "" {
				a.confirmStorageDelete(b, entry)
			}
		case list.Shortcut(0, ui.KeyF2) && !entry.Directory && b.moveSource == "":
			a.editStorageName(b, entry)
		case list.Shortcut(0, ui.KeyEscape):
			b.selected = -1
		}
	}
}

func storageChild(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

func (a *App) storageEntryMenu(m *ui.Menu, b *storageFiles, entry domain.StorageEntry) {
	full := storageChild(b.dir, entry.Name)
	if entry.Directory {
		if m.Item(locales.FilesOpenFolder()).Chosen() {
			a.loadStorageFiles(full)
		}
	} else if m.Item(locales.FilesDownload()).Chosen() {
		a.chooseStorageFile(b, &entry)
	}
	if b.atShares() || b.moveSource != "" {
		return
	}
	if !entry.Directory {
		if m.Item(locales.FilesRename()).Chosen() {
			a.editStorageName(b, entry)
		}
		if m.Item(locales.FilesMove()).Chosen() {
			b.moveSource, b.moveFrom, b.moveName = full, b.dir, entry.Name
			b.editEntry = entry
			b.filter, b.selected = "", -1
		}
	}
	m.Separator()
	if m.Item(locales.ButtonDelete()).Chosen() {
		a.confirmStorageDelete(b, entry)
	}
}

func (a *App) editStorageName(b *storageFiles, entry domain.StorageEntry) {
	b.editOpen, b.editKind, b.editValue, b.editError = true, "rename", entry.Name, ""
	b.editEntry = entry
}

func (a *App) confirmStorageDelete(b *storageFiles, entry domain.StorageEntry) {
	dir := b.dir
	full := storageChild(dir, entry.Name)
	confirm := func(entry domain.StorageEntry) {
		a.confirm = confirmation{
			open: true, title: locales.FilesDeleteTitle(), message: locales.FilesDeleteConfirm(full),
			action: func() {
				a.changeStorageFiles(b, service.StorageChange{
					Source: full, Delete: true, Directory: entry.Directory, ExpectedSource: &entry,
				})
			},
		}
	}
	if entry.Directory || b.storage.Kind != domain.KindS3 {
		confirm(entry)
		return
	}
	ctx := a.startStorageOperation(b)
	go func() {
		request, cancel := context.WithTimeout(ctx, connectionTimeout)
		defer cancel()
		prepared, err := a.svc.PrepareStorageDelete(request, b.storage, dir, entry)
		a.host.Update(func() {
			canceled := ctx.Err() != nil
			if !a.finishStorageOperation(b) || canceled {
				return
			}
			if err != nil {
				b.operationError = storageFilesError(err)
				a.loadStorageFiles(dir)
				return
			}
			confirm(prepared)
		})
	}()
}

func (a *App) submitStorageEdit(b *storageFiles) {
	name := b.editValue
	if !domain.ValidStoragePath(name) || b.editKind != "go" && (name == "" || strings.Contains(name, "/")) {
		b.editError = locales.FilesInvalid()
		return
	}
	b.editOpen = false
	if b.editKind == "go" {
		a.loadStorageFiles(name)
		return
	}
	if b.editKind == "rename" && name == b.editEntry.Name {
		return
	}
	change := service.StorageChange{Target: storageChild(b.dir, name), Directory: b.editKind == "mkdir"}
	if b.editKind == "rename" {
		change.Source = storageChild(b.dir, b.editEntry.Name)
		change.ExpectedSource = &b.editEntry
	}
	a.changeStorageFiles(b, change)
}

func (a *App) chooseStorageFile(b *storageFiles, entry *domain.StorageEntry) {
	ctx := a.startStorageOperation(b)
	dir := b.dir
	go func() {
		var selected string
		var err error
		if entry == nil {
			selected, err = a.host.ChooseFile("")
		} else {
			selected, err = a.host.ChooseFolder()
		}
		if err == nil && selected != "" && entry != nil {
			err = a.svc.DownloadStorageFile(ctx, b.storage, dir, entry.Name, selected)
		}
		a.host.Update(func() {
			canceled := ctx.Err() != nil
			if !a.finishStorageOperation(b) || canceled {
				return
			}
			switch {
			case err != nil:
				b.operationError = storageFilesError(err)
			case selected == "":
			case entry != nil:
				a.toast(locales.FilesDownloaded())
			default:
				a.changeStorageFiles(b, service.StorageChange{
					Target: storageChild(dir, filepath.Base(selected)), LocalFile: selected,
				})
			}
		})
	}()
}

func (a *App) startStorageOperation(b *storageFiles) context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	b.busy, b.operationCancel, b.operationError, b.editKind = true, cancel, "", ""
	return ctx
}

func (a *App) finishStorageOperation(b *storageFiles) bool {
	b.operationCancel()
	b.operationCancel, b.busy, b.queued = nil, false, false
	return a.storageFiles == b
}

func (a *App) changeStorageFiles(b *storageFiles, change service.StorageChange) {
	if a.storageFiles != b || b.busy {
		return
	}
	ctx := a.startStorageOperation(b)
	change.Queued = func(queued bool) {
		a.host.Update(func() {
			// finishStorageOperation cancels ctx, so a late call of a
			// finished change leaves the next one alone.
			if a.storageFiles == b && b.busy && ctx.Err() == nil {
				b.queued = queued
			}
		})
	}
	go func() {
		err := a.svc.ChangeStorage(ctx, b.storage, change)
		a.host.Update(func() {
			canceled := ctx.Err() != nil
			if !a.finishStorageOperation(b) {
				return
			}
			var conflict *service.StorageTaskConflict
			var replacement *domain.StorageReplacement
			switch {
			case err == nil:
				a.loadStorageFiles(b.dir)
			case errors.Is(err, domain.ErrStoragePartialMove):
				b.operationError = locales.FilesPartialMove(change.Source, change.Target)
				a.loadStorageFiles(b.dir)
			case errors.Is(err, domain.ErrStorageIncompleteWrite):
				b.operationError = locales.FilesIncompleteWrite()
				a.loadStorageFiles(b.dir)
			case canceled:
				a.loadStorageFiles(b.dir)
			case errors.As(err, &conflict):
				change.ConfirmedTasks = conflict.Tasks()
				a.confirm = confirmation{open: true, title: locales.FilesTaskTitle(), ok: locales.FilesApply(),
					message: storageTaskConflictMessage(conflict),
					action:  func() { a.changeStorageFiles(b, change) }}
			case errors.As(err, &replacement) && change.LocalFile != "":
				change.Replace = replacement
				a.confirm = confirmation{open: true, title: locales.FilesReplaceTitle(), ok: locales.FilesReplace(),
					message: locales.FilesReplaceConfirm(change.Target),
					action:  func() { a.changeStorageFiles(b, change) }}
			default:
				b.operationError = storageFilesError(err)
				a.loadStorageFiles(b.dir)
			}
		})
	}()
}

// storageTaskConflictMessage says what a change does to each task it
// touches: a destination may be overwritten by a later sync, and a source
// is carried to the destination by the next run.
func storageTaskConflictMessage(conflict *service.StorageTaskConflict) string {
	names := func(tasks []domain.Task) string {
		out := make([]string, 0, len(tasks))
		for _, task := range tasks {
			out = append(out, task.Name)
		}
		return strings.Join(out, ", ")
	}
	var parts []string
	if len(conflict.Destinations) != 0 {
		parts = append(parts, locales.FilesTaskConfirm(names(conflict.Destinations)))
	}
	if len(conflict.Sources) != 0 {
		parts = append(parts, locales.FilesTaskSourceConfirm(names(conflict.Sources)))
	}
	return strings.Join(parts, " ")
}
