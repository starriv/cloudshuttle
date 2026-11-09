# CloudShuttle UI redesign

The window takes its look from [mygo-music](https://github.com/egoist/mygo-music): a quiet desktop app, not a form. Deep background, cards a step above it, one brand blue for what acts, and color only where something needs attention — always with an icon and a word beside it.

## The shape

- **Sidebar (232 DIPs)** — the app's mark and name, then Overview, Tasks, Storages, History, and Settings alone at the foot of the list. The desktop's material shows through it; the content is opaque. What the app is doing now is what the Overview says, not the sidebar.
- **Page** — a title bar (28 pt semibold title, one muted line saying what the page is), the page's main action on the right, then content that scrolls under it. Pages are 30 DIPs from the window's sides, and their cards fill the width the window leaves. The window opens at 1200×780 and works down to 940×660: the overview stacks its cards and the tasks table gives its fixed columns less room when the window is narrow.
- **Cards** — a white (or `#1b1d22`) surface, 14 DIP radius, a hairline, and the least shadow in light mode. A card holds one subject. Rows inside are separated by their own room, never by a rule; a hairline is for the card's edge, not for cutting the card in two. Cards never share an edge: separate cards sit one gutter apart.
- **Room** — one scale, everywhere: the page leaves 30 DIPs at its sides and 28 at its foot; cards and other blocks are 20 apart; a card pads 16 by 18 inside; a row pads 12 by 16; a small gap is 8 and a medium one 12. The page's title bar is 74. A page's content starts 12 below its title.
- **Scroll bars** float in the card's own gutter, never on its edge: 8 DIPs in from the top and the bottom, 4 from the right, with the card's padding leaving them room.
- **Type** — system font. 28/650 display, 19/620 card titles, 13/540–570 body, 12/560 small, 11 micro. Numeric values use `tnum`.
- **Dialogs** — a `sheet` (see `sheet` in `internal/view/design.go`): 16 DIP radius card over a dimmed window, a head that names it, a body that scrolls, a foot with the buttons. The body groups its fields into `formGroup` cards, a subject each, in the same room and with the same card face as the settings page; never `ui.Modal` directly.
- **Motion** — a 110 ms color transition on what the pointer touches; a slight lift (-1 DIP) on cards; the page arrives from 8 DIPs below. All through MyGo's transitions so Reduce Motion is respected. No timers.

## The pieces

`internal/view/design.go` is the one place the look lives. Use it rather than restyling by hand:

| Piece | Use |
| --- | --- |
| `appTheme(c)` | at the top of `App.View`; returns the `palette`, sets the theme |
| `palette` | `bg`, `card`, `cardAlt`, `hairline`, `line`, `text`, `muted`, `faint`, `hover`, `press`, `accent`, `accentText`, `accentSoft`, `success`, `danger`, `warning` and their soft tints, `shadow` |
| `pageHeader(c, title, subtitle, actions)` | every page's title bar |
| `pageScroll(c)` | the scrolling content of a page |
| `card` / `cardPad` | a card's face / a card with padding |
| `cardHeader(c, title, subtitle, action)` | a card's head |
| `sectionTitle` / `groupLabel` | a group's name |
| `settingsGroup(c, icon, title, rows)` | settings cards, from `[]settingsRow`; the rows are set apart by room, not rules |
| `accentButton(c, key, icon, text, kind)` | buttons: `buttonPrimary`, `buttonSoft`, `buttonGhost`, `buttonDanger` |
| `iconAction` | a small icon-only button |
| `statusPill(c, domain.Status)` | a run's status in a soft pill |
| `pill` | any small rounded label |
| `iconTile` | an icon in a soft rounded square |
| `hoverRow` / `lift` | pointer feedback for a row / a card |
| `emptyPage` | a page or card with nothing yet |
| `formGroup` | a subject of a dialog: its name above, then a card of its fields, one card after another with the page's gutter |
| `fieldGroup` | a group of fields in a sheet |
| `sheet(c, open, opts, body, footer)` | the only dialog shape |
| `sheetActions` / `sheetError` | the foot's buttons / an error line |
| `confirmDialog` | the App's `confirmation`, drawn as a sheet |

## Rules that still hold

- Text comes from `locales`; never write user-facing English in `internal/view`. Update both catalogs and regenerate the accessors when adding a message.
- Colors come from the palette or the theme; never a literal color. Icons are Tabler outline SVGs.
- Keep `ui.Element` out of `App` state; `go tool mygo vet` checks it.
- Never block the main thread. Slow work goes to a goroutine and comes back through `Host.Update`.
- The status of a run is an icon and a word, never color alone.
- Keep keyboard navigation and selection semantics when styling controls.

## Checking the look

`internal/view/screenshots_test.go` renders the pages and some dialogs headlessly. Run it with a folder to write the PNGs, then look at them:

```sh
CLOUDSHUTTLE_SHOTS=/tmp/shots go test ./internal/view -run TestScreenshots -count=1
CLOUDSHUTTLE_SHOTS=/tmp/shots-dark CLOUDSHUTTLE_SHOTS_DARK=1 go test ./internal/view -run TestScreenshots -count=1
CLOUDSHUTTLE_SHOTS=/tmp/shots-zh CLOUDSHUTTLE_SHOTS_ZH=1 go test ./internal/view -run TestScreenshots -count=1
```

Extend `screenshots_test.go` when a dialog or state is missing from it; it is a tool, not a test of the design.
