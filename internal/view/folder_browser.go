package view

import (
	"context"
	"errors"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/internal/icons"
	"github.com/starriv/cloudshuttle/locales"
)

// folderBrowser chooses a folder of a storage for one side of a task. It
// takes the place of the task form in the task dialog while it is open.
type folderBrowser struct {
	target  *locationField
	storage domain.Storage
	path    []string // the folder shown, as its names from the top

	loading bool
	folders []string // the folders in it: at the top of an SMB server, its shares
	failed  string   // why reading them failed, if it did
	notice  string   // why it shows another folder than the field held
	typed   string   // a share's name, typed at the top of an SMB server
	list    ui.ListState
	choice  int // the folder chosen in the list, or -1
	crumb   int // the breadcrumb clicked
}

// browseHeight is how high the list of folders is: it scrolls beyond.
const browseHeight = 8 * serverRowHeight

// browseFolder opens a browser on f's storage, at the folder f holds.
func (a *App) browseFolder(f *locationField, s domain.Storage) {
	var path []string
	for _, name := range strings.Split(f.path, "/") {
		if name != "" {
			path = append(path, name)
		}
	}
	a.taskForm.browser = &folderBrowser{target: f, storage: s, path: path, choice: -1}
	a.loadFolders(true)
}

// loadFolders reads the folders of the browser's path off the main thread.
// Opening, a folder that does not exist yet gives way to its parent, and a
// share that does not to the top of its server.
func (a *App) loadFolders(opening bool) {
	b := a.taskForm.browser
	b.loading, b.failed, b.notice, b.folders, b.choice = true, "", "", nil, -1
	a.folderLoads++
	seq, id, path := a.folderLoads, b.storage.ID, b.path
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), connectionTimeout)
		defer cancel()
		folders, err := a.svc.Folders(ctx, id, strings.Join(path, "/"))
		notice := ""
		if opening && errors.Is(err, domain.ErrShareNotFound) && len(path) > 0 {
			// As when the share was renamed on the server since.
			notice = locales.BrowseShareGone(path[0])
			path = nil
			folders, err = a.svc.Folders(ctx, id, "")
		}
		for opening && errors.Is(err, domain.ErrFolderNotFound) && len(path) > 0 {
			path = path[:len(path)-1]
			folders, err = a.svc.Folders(ctx, id, strings.Join(path, "/"))
		}
		a.host.Update(func() {
			if a.folderLoads != seq || a.taskForm.browser != b {
				return // the browser moved on, or closed
			}
			b.loading, b.path, b.folders, b.notice = false, path, folders, notice
			switch {
			case errors.Is(err, domain.ErrShareNotFound):
				b.failed = locales.BrowseShareGone(path[0])
			case err != nil:
				b.failed = locales.BrowseFailed(connectionText(err))
			}
		})
	}()
}

// open shows the folder name inside the one shown.
func (a *App) openFolder(name string) {
	b := a.taskForm.browser
	b.path = append(b.path[:len(b.path):len(b.path)], name)
	a.loadFolders(false)
}

// chooseBrowsed puts path into the browser's field and closes it.
func (a *App) chooseBrowsed(path []string) {
	b := a.taskForm.browser
	b.target.path = strings.Join(path, "/")
	a.taskForm.browser = nil
}

func (a *App) folderBrowserView(c *ui.Context) {
	b := a.taskForm.browser
	t := c.Theme()
	ui.Text(c, locales.BrowseTitle(b.storage.Name)).FontSize(18).FontWeight(500)

	// The top: the bucket, or the SMB server, whose folders are shares.
	top, shares := b.storage.Name, b.storage.Kind == domain.KindSMB && len(b.path) == 0
	if b.storage.Kind != domain.KindSMB && b.storage.Bucket != "" {
		top = b.storage.Bucket
	}
	// At the top, the path would only repeat the title.
	if len(b.path) > 0 {
		crumbs := append([]string{top}, b.path...)
		b.crumb = len(crumbs) - 1
		if ui.Breadcrumbs(c, crumbs, &b.crumb).Label(locales.BrowsePath()).Changed() && b.crumb < len(b.path) {
			b.path = b.path[:b.crumb]
			a.loadFolders(false)
		}
	}

	if b.notice != "" {
		ui.Row(c).Gap(8).AlignItems(ui.Start).TextColor(t.TextMuted).Children(func() {
			ui.Icon(c, icons.Stopped)
			ui.Text(c, b.notice).Grow(1).MinWidth(0)
		})
	}

	ui.Column(c).Gap(6).Children(func() {
		box := func(fn func()) {
			ui.Column(c).Height(browseHeight).Justify(ui.Center).AlignItems(ui.Center).
				Padding(t.Space(4)).Border(1, t.Border).Radius(t.Radius).TextColor(t.TextMuted).Children(fn)
		}
		switch {
		case b.loading:
			box(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Spinner(c).Label(locales.BrowseLoading())
					ui.Text(c, locales.BrowseLoading())
				})
			})
		case b.failed != "":
			box(func() { result(c, b.failed, true) })
		case len(b.folders) == 0:
			empty := locales.BrowseEmpty()
			if shares {
				empty = locales.StorageSharesNone()
			}
			box(func() { ui.Text(c, empty).TextAlign(ui.Center) })
		default:
			b.list.Selected = &b.choice
			b.list.Key = func(i int) any { return b.folders[i] }
			icon, label := icons.Folder, locales.BrowseFolders()
			if shares {
				icon, label = icons.Shares, locales.BrowseShares()
			}
			list := ui.List(c, &b.list, len(b.folders), func(i int) {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Height(serverRowHeight).Padding(0, t.Space(2.5)).Children(func() {
					ui.Icon(c, icon)
					ui.Text(c, b.folders[i]).SingleLine().Ellipsis("…").MinWidth(0).Grow(1)
				})
			}).Label(label).FillWidth().Height(browseHeight).Border(1, t.Border).Radius(t.Radius)
			if list.Submitted() && b.choice >= 0 && b.choice < len(b.folders) {
				a.openFolder(b.folders[b.choice])
				return
			}
			ui.Text(c, locales.BrowseHint()).TextColor(t.TextMuted)
		}
		if shares && !b.loading {
			a.typeShare(c, b)
		}
	})

	ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
		if ui.Button(c, locales.ButtonCancel()).Clicked() {
			a.taskForm.browser = nil
			return
		}
		label, path := locales.BrowseChooseCurrent(), b.path
		chosen := b.choice >= 0 && b.choice < len(b.folders) && !b.loading
		if chosen {
			label = locales.BrowseChoose(b.folders[b.choice])
			path = append(b.path[:len(b.path):len(b.path)], b.folders[b.choice])
		}
		// The top of an SMB server holds shares: one of them is the least
		// a task can name.
		if ui.PrimaryButton(c, label).Disabled(b.loading || shares && !chosen).Clicked() {
			a.chooseBrowsed(path)
		}
	})
}

// typeShare opens a share by the name typed, for one the server does not
// list: a NAS may go on listing a renamed share by its old name, and not
// by its new one, for a while.
func (a *App) typeShare(c *ui.Context, b *folderBrowser) {
	t := c.Theme()
	ui.Row(c).Gap(6).FillWidth().Children(func() {
		in := ui.TextInput(c, &b.typed).Placeholder(locales.BrowseShareName()).
			Label(locales.BrowseShareName()).Grow(1).MinWidth(0)
		name := strings.Trim(strings.TrimSpace(b.typed), "/")
		open := ui.Button(c, locales.BrowseOpen()).Disabled(name == "")
		if (open.Clicked() || in.Submitted()) && name != "" {
			b.path, b.typed = []string{name}, ""
			a.loadFolders(false)
		}
	})
	ui.Text(c, locales.BrowseShareHint()).TextColor(t.TextMuted)
}
