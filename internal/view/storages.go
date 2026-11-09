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

// storageCardGap is the room between the storage cards of the page.
const storageCardGap = gutter

// storageMinCard is how narrow a storage card may be before the grid drops a
// column: two at the window's usual width.
const storageMinCard = 320

func (a *App) storagesPage(c *ui.Context) {
	if a.storageFiles != nil {
		a.storageFilesPage(c, a.storageFiles)
		return
	}
	pageHeader(c, locales.NavStorages(), locales.StoragesSubtitle(), func() {
		if accentButton(c, "storages-add", icons.Add, locales.StoragesAdd(), buttonPrimary).Clicked() {
			a.openStorageForm(nil)
		}
	})
	if len(a.storages) == 0 {
		emptyPage(c, icons.Storages, locales.StoragesEmptyTitle(), locales.StoragesEmpty(), func() {
			if accentButton(c, "storages-empty-add", icons.Add, locales.StoragesAdd(), buttonPrimary).Clicked() {
				a.openStorageForm(nil)
			}
		})
		return
	}
	pageScroll(c).Children(func() {
		ui.Column(c).Gap(storageCardGap).Padding(0, 0, pageBottom, 0).Children(func() {
			a.storagesGrid(c)
		})
	})
}

// storagesGrid puts the storages in columns of cards, as many as the window
// has room for: two at its usual width, one at its narrowest.
func (a *App) storagesGrid(c *ui.Context) {
	cols := storageColumns(c)
	tracks := make([]ui.Track, cols)
	for i := range tracks {
		tracks[i] = ui.Fr(1)
	}
	ui.Grid(c).ColumnTracks(tracks...).GapX(storageCardGap).GapY(storageCardGap).Children(func() {
		for _, s := range a.storages {
			a.storageCard(c, s)
		}
	})
}

// storageColumns is how many cards fit in a row at the window's width.
func storageColumns(c *ui.Context) int {
	w, _ := c.Size()
	room := w - sidebarWidth - 2*pagePad
	cols := int((room + storageCardGap) / (storageMinCard + storageCardGap))
	return min(4, max(1, cols))
}

// storageCard is one storage as a card: what it is and where it points, how
// its last connection test went, and what can be done with it. The card
// lifts as the pointer is on it.
func (a *App) storageCard(c *ui.Context, s domain.Storage) {
	p := paletteFor(c.Theme())
	svg, kind := icons.Bucket, providerLabel(s.Provider)
	if s.Kind == domain.KindSMB {
		svg, kind = icons.Server, kindLabel(s.Kind)
	}
	face := card(c).Padding(cardPadY, cardPadX).Gap(gapMedium)
	lift(face)
	face.Children(func() {
		ui.Row(c).Gap(12).AlignItems(ui.Center).MinWidth(0).Children(func() {
			iconTile(c, svg, 38, p.accent, p.accentSoft)
			ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
				ui.Text(c, s.Name).FontSize(15).FontWeight(600).LetterSpacing(-0.1).TextColor(p.text).
					SingleLine().Ellipsis("…")
				ui.Row(c).Gap(5).AlignItems(ui.Center).MinWidth(0).Children(func() {
					ui.Text(c, storageAddress(s)).FontSize(sizeSmall).TextColor(p.muted).
						FontFeatures("tnum").SingleLine().Ellipsis("…").MinWidth(0).
						Tooltip(storageAddress(s))
					if plaintextEndpoint(s.Endpoint) {
						ui.Icon(c, icons.Warning).Size(13, 13).TextColor(p.warning).
							Tooltip(locales.StoragePlaintextWarning())
					}
				})
			})
			a.storageCheckPill(c, s)
		})
		ui.Row(c).Gap(8).AlignItems(ui.Center).MinWidth(0).Children(func() {
			ui.Text(c, kind).FontSize(sizeMicro).FontWeight(600).LetterSpacing(0.3).
				TextColor(p.muted).SingleLine().Ellipsis("…").MinWidth(0).Grow(1)
			if accentButton(c, "storage-browse-"+s.ID, icons.FolderOpen, locales.FilesBrowse(), buttonSoft).Clicked() {
				a.storageFiles = &storageFiles{storage: s}
				a.loadStorageFiles("")
			}
			if iconAction(c, "storage-test-"+s.ID, icons.Test, locales.StorageTestShort()).
				Tooltip(locales.StorageTestShort()).
				Disabled(a.checking[s.ID]).Clicked() {
				a.testStorage(s)
			}
			iconAction(c, "storage-more-"+s.ID, icons.More, locales.StorageActions(s.Name)).
				Menu(func(m *ui.Menu) {
					if m.Item(locales.StorageEdit()).Chosen() {
						a.openStorageForm(&s)
					}
					if m.Item(locales.StorageDelete()).Chosen() {
						a.confirmDeleteStorage(s)
					}
				})
		})
	})
}

// storageCheckPill shows how a storage last tested as a pill of the page's
// look, its icon turning while a test runs, with what a test found in its
// tooltip.
func (a *App) storageCheckPill(c *ui.Context, s domain.Storage) ui.Element {
	p := paletteFor(c.Theme())
	checking := a.checking[s.ID]
	svg, label, tip := icons.Untested, locales.CheckNone(), locales.CheckNoneHint()
	fg, bg := p.muted, p.neutralSoft
	switch {
	case checking:
		svg, label, tip = icons.Running, locales.StorageTesting(), ""
		fg, bg = p.accent, p.accentSoft
	case !s.Check.Checked():
	case s.Check.OK:
		svg, label, tip = icons.Success, locales.CheckOK(), locales.CheckOKAt(formatTime(c, s.Check.At))
		fg, bg = p.success, p.successSoft
	default:
		svg, label = icons.Failed, locales.CheckFailed()
		tip = locales.CheckFailedAt(formatTime(c, s.Check.At), checkProblem(s.Check))
		fg, bg = p.danger, p.dangerSoft
	}
	e := ui.Row(c.Key("check-" + s.ID)).Height(21).PaddingX(8).Gap(5).Radius(11).
		AlignItems(ui.Center).Background(bg).TextColor(fg).Shrink(0)
	e.Children(func() {
		icon := ui.Icon(c, svg).Size(12, 12)
		if checking {
			icon.Rotate(icon.Loop("spin", time.Second, ui.Linear) * 360)
		}
		ui.Text(c, label).FontSize(sizeMicro).FontWeight(560).SingleLine()
	})
	if tip != "" {
		e.Tooltip(tip)
	}
	return e
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
// records how it went, which the card shows. told toasts it too.
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
