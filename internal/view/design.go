package view

import (
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/internal/icons"
	"github.com/starriv/cloudshuttle/locales"
)

// The design of the window: a quiet, deep background with cards that float
// on it, one brand blue for what acts, and color only for what needs
// attention. Every page is built from the pieces here, so they agree on
// type, room, corners and motion.
//
// The colors follow the appearance: a copy of the theme, with the app's
// palette set, is what c.Theme returns inside the window. Nothing in the
// view paints a literal color: it asks the palette, or the theme.
type palette struct {
	dark bool

	// Backgrounds, from the back to the front.
	bg       ui.Color // the window's content
	sidebar  ui.Color // under the sidebar where no material shows
	card     ui.Color // a card, a panel, a row of a list
	cardAlt  ui.Color // a card within a card, a field, a well
	hover    ui.Color // a pointer on something that leads somewhere
	press    ui.Color // pressing it
	hairline ui.Color // the line around cards and between rows
	line     ui.Color // the line around fields and controls
	shadow   ui.Color

	text  ui.Color
	muted ui.Color
	faint ui.Color

	accent      ui.Color
	accentText  ui.Color
	accentHover ui.Color
	accentSoft  ui.Color
	accentEdge  ui.Color

	success     ui.Color
	successSoft ui.Color
	warning     ui.Color
	warningSoft ui.Color
	danger      ui.Color
	dangerSoft  ui.Color
	neutralSoft ui.Color
	onTint      ui.Color // text on a soft tint of a status color
}

func paletteFor(t *ui.Theme) palette {
	if t.Dark {
		return palette{
			dark:        true,
			bg:          ui.Hex("#121316"),
			sidebar:     ui.Hex("#17181c"),
			card:        ui.Hex("#1b1d22"),
			cardAlt:     ui.Hex("#23252b"),
			hover:       ui.RGBA(255, 255, 255, 0.055),
			press:       ui.RGBA(255, 255, 255, 0.09),
			hairline:    ui.RGBA(255, 255, 255, 0.07),
			line:        ui.RGBA(255, 255, 255, 0.12),
			shadow:      ui.RGBA(0, 0, 0, 0.5),
			text:        ui.Hex("#f2f4f7"),
			muted:       ui.RGBA(235, 238, 245, 0.58),
			faint:       ui.Hex("#a4a9b4"),
			accent:      ui.Hex("#4c9dff"),
			accentText:  ui.Hex("#04182e"),
			accentHover: ui.Hex("#63aaff"),
			accentSoft:  ui.RGBA(76, 157, 255, 0.16),
			accentEdge:  ui.RGBA(76, 157, 255, 0.38),
			success:     ui.Hex("#5ecb8d"),
			successSoft: ui.RGBA(94, 203, 141, 0.14),
			warning:     ui.Hex("#f0b429"),
			warningSoft: ui.RGBA(240, 180, 41, 0.14),
			danger:      ui.Hex("#ff6f6f"),
			dangerSoft:  ui.RGBA(255, 111, 111, 0.14),
			neutralSoft: ui.RGBA(255, 255, 255, 0.06),
			onTint:      ui.Hex("#f2f4f7"),
		}
	}
	return palette{
		bg:          ui.Hex("#f4f5f7"),
		sidebar:     ui.Hex("#ebedf0"),
		card:        ui.Hex("#ffffff"),
		cardAlt:     ui.Hex("#f5f6f8"),
		hover:       ui.RGBA(20, 28, 40, 0.045),
		press:       ui.RGBA(20, 28, 40, 0.08),
		hairline:    ui.RGBA(20, 28, 40, 0.075),
		line:        ui.RGBA(20, 28, 40, 0.13),
		shadow:      ui.RGBA(24, 34, 52, 0.08),
		text:        ui.Hex("#1a1c20"),
		muted:       ui.Hex("#555e6d"),
		faint:       ui.Hex("#626a78"),
		accent:      ui.Hex("#075fc0"),
		accentText:  ui.Hex("#ffffff"),
		accentHover: ui.Hex("#0968cc"),
		accentSoft:  ui.RGBA(10, 124, 242, 0.1),
		accentEdge:  ui.RGBA(10, 124, 242, 0.32),
		success:     ui.Hex("#126d45"),
		successSoft: ui.RGBA(26, 156, 99, 0.11),
		warning:     ui.Hex("#825400"),
		warningSoft: ui.RGBA(184, 116, 0, 0.12),
		danger:      ui.Hex("#a52c2c"),
		dangerSoft:  ui.RGBA(217, 59, 59, 0.1),
		neutralSoft: ui.RGBA(20, 28, 40, 0.05),
		onTint:      ui.Hex("#1a1c20"),
	}
}

// appTheme gives c the app's theme for the appearance it shows, and the
// palette that goes with it. Call it once, at the top of a window.
func appTheme(c *ui.Context) palette {
	t := *c.Theme()
	p := paletteFor(&t)
	// An accent of the app's own, with its hover and press, which MyGo's
	// own widgets (a switch, a progress bar, the row chosen) take too.
	t.Accent, t.AccentText = p.accent, p.accentText
	t.AccentHover, t.AccentPressed = p.accentHover, p.accent.Mix(p.text, 0.14)
	t.InactiveAccent = p.accent.Mix(p.bg, 0.45)
	t.Background = p.bg
	t.Surface = p.cardAlt
	t.SurfaceHover = p.hover.Over(p.cardAlt)
	t.SurfacePressed = p.press.Over(p.cardAlt)
	t.Border = p.line
	t.Text, t.TextMuted = p.text, p.muted
	t.Selection = p.accent.Alpha(0.24)
	t.Focus = p.accent
	t.Danger, t.Warning, t.Success = p.danger, p.warning, p.success
	t.Radius = 8
	t.Spacing = 4
	if t.FontSize <= 0 || t.FontSize > 14 {
		t.FontSize = 13
	}
	c.SetTheme(&t)
	return p
}

// Type sizes, in DIPs, shared by the pages.
const (
	sizeDisplay = 28 // a page's title
	sizeTitle   = 19 // a card's title, a dialog's
	sizeSection = 13 // the name of a group of rows
	sizeBody    = 13
	sizeSmall   = 12
	sizeMicro   = 11
)

// Room, in DIPs, shared by the whole window. A page leaves pagePad at its
// sides and pageBottom at its foot; cards and other blocks sit gutter apart;
// a card pads cardPadX by cardPadY inside, a row rowPadX by rowPadY; small
// things are gapSmall or gapMedium apart. The same numbers everywhere are
// what makes the pages feel like one app.
const (
	pagePad     = 30
	pageBottom  = 28
	gutter      = 20
	gapSmall    = 8
	gapMedium   = 12
	cardPadX    = 18
	cardPadY    = 16
	rowPadX     = 16
	rowPadY     = 12
	innerRadius = 10
	cardRadius  = 14
	rowRadius   = 10
)

// Motion: quick answers to the pointer, and the one spring the window's
// own arrivals use. Both respect Reduce Motion through MyGo.
var (
	quickTransition = ui.ElementTransition{
		Colors:   true,
		Duration: 110 * time.Millisecond,
	}
	liftTransition = ui.Animation{
		Spring: ui.SpringOptions{Duration: 260 * time.Millisecond, Bounce: 0.18},
	}
)

// pageHeader is the title bar of a page: its title, a line that says what
// is below, and the page's main actions on the right. The empty room
// drags the window.
func pageHeader(c *ui.Context, title, subtitle string, actions func()) {
	p := paletteFor(c.Theme())
	bar := c.TitleBar()
	ui.Row(c).Height(max(bar.Height, 74)).AlignItems(ui.Center).Gap(16).PaddingX(0).DragWindow().
		Children(func() {
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, title).FontSize(sizeDisplay).FontWeight(650).LetterSpacing(-0.6).
					SingleLine().Ellipsis("…").TextColor(p.text)
				if subtitle != "" {
					ui.Text(c, subtitle).FontSize(sizeSmall).TextColor(p.muted).SingleLine().Ellipsis("…")
				}
			})
			if actions != nil {
				actions()
			}
		})
}

// toolbar is a row of controls above what they narrow or change.
func toolbar(c *ui.Context, fn func()) {
	ui.Row(c).Gap(10).AlignItems(ui.Center).Height(36).Children(fn)
}

// sectionTitle heads a group of content, with an optional action beside it.
func sectionTitle(c *ui.Context, title string, action func()) {
	p := paletteFor(c.Theme())
	ui.Row(c).Height(28).AlignItems(ui.Center).Gap(12).Children(func() {
		ui.Text(c, title).FontSize(sizeTitle).FontWeight(620).LetterSpacing(-0.2).
			TextColor(p.text).Grow(1).MinWidth(0).SingleLine().Ellipsis("…")
		if action != nil {
			action()
		}
	})
}

// groupLabel names a group of rows, above its card.
func groupLabel(c *ui.Context, text string) {
	p := paletteFor(c.Theme())
	ui.Text(c, text).FontSize(sizeSmall).FontWeight(600).LetterSpacing(0.2).
		TextColor(p.muted).Padding(2, 4, 0, 4)
}

// card is the face of a group of content: a surface a step above the
// page, with a hairline and the least shadow.
func card(c *ui.Context) ui.Element {
	p := paletteFor(c.Theme())
	e := ui.Column(c).Radius(cardRadius).Background(p.card).Border(1, p.hairline)
	if !p.dark {
		e.Shadow(0, 1, 2, 0, p.shadow)
	}
	return e
}

// cardPad is card with the room a card's content takes.
func cardPad(c *ui.Context) ui.Element {
	return card(c).Padding(cardPadY, cardPadX).Gap(gapMedium)
}

// cardHeader heads a card: its title, a line under it, and what sits on
// the right.
func cardHeader(c *ui.Context, title, subtitle string, action func()) {
	p := paletteFor(c.Theme())
	ui.Row(c).AlignItems(ui.Center).Gap(12).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, title).FontSize(sizeTitle).FontWeight(600).LetterSpacing(-0.2).
				TextColor(p.text).SingleLine().Ellipsis("…")
			if subtitle != "" {
				ui.Text(c, subtitle).FontSize(sizeSmall).TextColor(p.muted).SingleLine().Ellipsis("…")
			}
		})
		if action != nil {
			action()
		}
	})
}

// hoverRow fills a row that leads somewhere as the pointer is on it, and
// more as it is pressed.
func hoverRow(c *ui.Context, row ui.Element) {
	p := paletteFor(c.Theme())
	row.Transition(quickTransition)
	switch {
	case row.Pressed():
		row.Background(p.press)
	case row.Hovered():
		row.Background(p.hover)
	}
}

// lift raises a card as the pointer is on it, as a handle would.
func lift(card ui.Element) {
	card.Transition(quickTransition)
	pose := ui.Pose{}
	if card.Hovered() {
		pose.Y = -1
	}
	card.AnimatePose(pose, liftTransition)
}

// Button kinds: what a button does in the page.
type buttonKind int

const (
	buttonPrimary buttonKind = iota // the one thing to do here
	buttonSoft                      // what one does now and then
	buttonGhost                     // an icon or a word beside something
	buttonDanger                    // what cannot be taken back
)

// accentButton is a button in the page's own look: rounded, quiet, and
// with a little give as it is pressed. label names it for assistive
// technology; text is what it shows when it is not only an icon.
func accentButton(c *ui.Context, key any, icon *ui.SVG, text string, kind buttonKind) ui.Element {
	p := paletteFor(c.Theme())
	var fg, bg, border ui.Color
	padX := float32(14)
	switch kind {
	case buttonPrimary:
		fg, bg = p.accentText, p.accent
	case buttonDanger:
		fg, bg = p.danger, p.dangerSoft
		border = p.danger.Alpha(0.25)
	case buttonSoft:
		fg, bg = p.text, p.card
		border = p.line
	case buttonGhost:
		fg, bg = p.muted, ui.Transparent
		padX = 8
	}
	if icon != nil && text == "" {
		padX = 8
	}
	b := ui.ButtonBase(c.Key(key)).Height(30).PaddingX(padX).Gap(7).Radius(8).
		AlignItems(ui.Center).Justify(ui.Center).TextColor(fg)
	if text != "" {
		b.Label(text)
	}
	// What the face is: the plain color, and under the pointer or a press
	// the fill it takes, gliding between them.
	fill := bg
	hover := bg
	if kind == buttonPrimary {
		hover = p.accentHover
	} else if kind == buttonGhost {
		hover = p.hover
	} else if kind == buttonDanger {
		hover = p.dangerSoft.Mix(p.danger, 0.1)
	} else {
		hover = p.hover.Over(p.card)
	}
	switch {
	case b.Pressed() && kind != buttonGhost:
		fill = hover.Mix(p.text, 0.06)
	case b.Hovered():
		fill = hover
	}
	if fill.A > 0 || b.Hovered() {
		b.Background(b.AnimateColor("bg", fill, ui.Animation{Duration: 110 * time.Millisecond}))
	}
	if border.A > 0 {
		b.Border(1, border)
	}
	pose := ui.Pose{}
	if b.Pressed() {
		pose.Scale = 0.96
	}
	b.AnimatePose(pose, ui.Snappy)
	b.Children(func() {
		if icon != nil {
			ui.Icon(c, icon).Size(15, 15)
		}
		if text != "" {
			ui.Text(c, text).FontSize(sizeBody).FontWeight(560).SingleLine()
		}
	})
	return b
}

// iconAction is a small, quiet button showing only an icon.
func iconAction(c *ui.Context, key any, icon *ui.SVG, label string) ui.Element {
	return accentButton(c, key, icon, "", buttonGhost).Label(label)
}

// statusTint is the soft color of a status and the color its word takes.
func statusTint(p palette, status domain.Status) (soft, fg ui.Color) {
	switch status {
	case domain.StatusSuccess:
		return p.successSoft, p.success
	case domain.StatusFailed:
		return p.dangerSoft, p.danger
	case domain.StatusRunning:
		return p.accentSoft, p.accent
	}
	return p.neutralSoft, p.muted
}

// pill is a small, rounded label, of a status or a number.
func pill(c *ui.Context, key any, icon *ui.SVG, text string, fg, bg ui.Color) ui.Element {
	e := ui.Row(c.Key(key)).Height(21).PaddingX(8).Gap(5).Radius(11).AlignItems(ui.Center).
		Background(bg).TextColor(fg)
	e.Children(func() {
		if icon != nil {
			ui.Icon(c, icon).Size(11, 11)
		}
		ui.Text(c, text).FontSize(sizeMicro).FontWeight(560).SingleLine()
	})
	return e
}

// statusPill shows a run's status as colored words, with its icon: the
// color never carries the meaning alone.
func statusPill(c *ui.Context, s domain.Status) ui.Element {
	p := paletteFor(c.Theme())
	soft, fg := statusTint(p, s)
	e := ui.Row(c.Key(s)).Height(21).PaddingX(8).Gap(5).Radius(11).AlignItems(ui.Center).
		Background(soft).TextColor(fg)
	e.Children(func() {
		icon := ui.Icon(c, StatusIcon(s)).Size(11, 11)
		if s == domain.StatusRunning {
			icon.Rotate(icon.Loop("spin", time.Second, ui.Linear) * 360)
		}
		ui.Text(c, StatusLabel(s)).FontSize(sizeMicro).FontWeight(560).SingleLine()
	})
	return e
}

// iconTile is an icon in a rounded square of a soft color, as a page or a
// row shows what it is about.
func iconTile(c *ui.Context, icon *ui.SVG, size float32, fg, bg ui.Color) ui.Element {
	e := ui.Box(c).Size(size, size).Radius(size * 0.3).Center().Background(bg).TextColor(fg)
	e.Children(func() { ui.Icon(c, icon).Size(size*0.46, size*0.46) })
	return e
}

// emptyPage is a page with nothing to show yet: what to do, and the button
// that does it.
func emptyPage(c *ui.Context, icon *ui.SVG, title, message string, action func()) {
	p := paletteFor(c.Theme())
	ui.Column(c).Grow(1).Center().Gap(10).Padding(24).Children(func() {
		iconTile(c, icon, 56, p.accent, p.accentSoft)
		ui.Text(c, title).FontSize(17).FontWeight(600).LetterSpacing(-0.2).TextColor(p.text).
			TextAlign(ui.Center).Margin(6, 0, 0, 0)
		if message != "" {
			ui.Text(c, message).FontSize(sizeBody).TextColor(p.muted).TextAlign(ui.Center).
				MaxWidth(420).LineHeight(1.5)
		}
		if action != nil {
			ui.Box(c).Margin(8, 0, 0, 0).Children(action)
		}
	})
}

// formGroup is a dialog's group of fields, under a name: a card of its own,
// a step in from the sheet, set apart from the next by the page's gutter.
func formGroup(c *ui.Context, icon *ui.SVG, title string, body func()) {
	p := paletteFor(c.Theme())
	ui.Column(c).Gap(7).Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Padding(0, 4).Children(func() {
			if icon != nil {
				ui.Icon(c, icon).Size(13, 13).TextColor(p.muted)
			}
			ui.Text(c, title).FontSize(sizeSmall).FontWeight(620).LetterSpacing(0.3).
				TextColor(p.muted)
		})
		ui.Column(c).Padding(cardPadY, cardPadX).Gap(gapMedium).Radius(cardRadius).
			Background(p.cardAlt).Border(1, p.hairline).Children(body)
	})
}

// fieldGroup is a dialog's group of fields, under a name.
func fieldGroup(c *ui.Context, label string, body func()) ui.Element {
	p := paletteFor(c.Theme())
	return ui.Column(c).Gap(10).Children(func() {
		if label != "" {
			ui.Text(c, label).FontSize(sizeSmall).FontWeight(600).LetterSpacing(0.2).TextColor(p.muted)
		}
		body()
	})
}

// sheetOptions say how a sheet looks: how wide, and how tall its scrolling
// body may be.
type sheetOptions struct {
	width    float32
	bodyMax  float32
	title    string
	subtitle string
	// noClose hides the close button, for a sheet that can only be
	// answered by its buttons.
	noClose bool
}

// sheet lays out a panel over the window, its content scrolling between a
// head and a foot that stay: the shape every dialog of the app takes.
func sheet(c *ui.Context, open *bool, opts sheetOptions, body, footer func()) {
	if !*open {
		return
	}
	p := paletteFor(c.Theme())
	width := opts.width
	if width == 0 {
		width = 560
	}
	maxBody := opts.bodyMax
	if maxBody == 0 {
		_, h := c.Size()
		maxBody = h - 250
	}
	ui.DialogBase(c, open, func(back, panel ui.Element) {
		back.Background(ui.RGBA(0, 0, 0, 0.35))
		panel.Width(width).Radius(16).Background(p.card).Border(1, p.hairline).
			Shadow(0, 24, 60, 4, p.shadow).Clip()
		panel.Children(func() {
			// The head names the sheet, and closes it.
			ui.Row(c.Key("head")).Gap(14).Padding(20, 22, 14, 22).AlignItems(ui.Start).DragWindow().
				Children(func() {
					ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
						ui.Text(c, opts.title).FontSize(sizeTitle).FontWeight(640).LetterSpacing(-0.3).
							TextColor(p.text).SingleLine().Ellipsis("…")
						if opts.subtitle != "" {
							ui.Text(c, opts.subtitle).FontSize(sizeSmall).TextColor(p.muted).LineHeight(1.4)
						}
					})
					if !opts.noClose {
						if iconAction(c, "close", icons.Close, locales.HistoryDetailClose()).Clicked() {
							*open = false
						}
					}
				})
			ui.Scroll(c.Key("body")).Grow(1).Shrink(1).MinHeight(0).MaxHeight(maxBody).
				Padding(0, 22, 18, 22).ScrollbarInsets(0, 4, gapSmall).Children(body)
			ui.Row(c.Key("foot")).Padding(12, 22, 18, 22).Gap(10).
				AlignItems(ui.Center).Children(footer)
		})
	})
}

// sheetActions puts the buttons of a sheet's foot on the right.
func sheetActions(c *ui.Context, fn func()) {
	ui.Row(c).Grow(1).Gap(10).Justify(ui.End).AlignItems(ui.Center).Children(fn)
}

// sheetError is the line of a save that failed, above the buttons.
func sheetError(c *ui.Context, text string) {
	if text == "" {
		return
	}
	p := paletteFor(c.Theme())
	ui.Row(c).Grow(1).MinWidth(0).Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Icon(c, icons.Warning).Size(14, 14).TextColor(p.danger)
		ui.Text(c, text).FontSize(sizeSmall).TextColor(p.danger).Grow(1).MinWidth(0).LineHeight(1.4)
	})
}

func (a *App) confirmDialog(c *ui.Context) {
	p := paletteFor(c.Theme())
	c0 := &a.confirm
	if !c0.open {
		return
	}
	ok := c0.ok
	if ok == "" {
		ok = locales.ButtonDelete()
	}
	ui.DialogBase(c, &c0.open, func(back, panel ui.Element) {
		back.Background(ui.RGBA(0, 0, 0, 0.35))
		panel.Width(420).Radius(16).Background(p.card).Border(1, p.hairline).
			Shadow(0, 24, 60, 4, p.shadow).Children(func() {
			ui.Column(c).Padding(24, 24, 20, 24).Gap(12).Children(func() {
				iconTile(c, icons.Warning, 40, p.danger, p.dangerSoft)
				ui.Text(c, c0.title).FontSize(sizeTitle).FontWeight(640).LetterSpacing(-0.3).
					TextColor(p.text).LineHeight(1.3)
				ui.Text(c, c0.message).FontSize(sizeBody).TextColor(p.muted).LineHeight(1.5)
				ui.Row(c).Gap(10).Justify(ui.End).Margin(8, 0, 0, 0).Children(func() {
					if accentButton(c, "cancel", nil, locales.ButtonCancel(), buttonSoft).Clicked() {
						c0.open = false
					}
					if accentButton(c, "ok", nil, ok, buttonDanger).Clicked() {
						c0.open = false
						if c0.action != nil {
							c0.action()
						}
					}
				})
			})
		})
	})
}
