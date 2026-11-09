package view

import (
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/internal/format"
	"github.com/starriv/cloudshuttle/internal/icons"
	"github.com/starriv/cloudshuttle/locales"
)

// excludeField is what a task leaves out, as the task form edits it: the
// presets ticked, the other patterns one a line, and the size limit.
type excludeField struct {
	open       bool // the editor shows in place of the form
	unreadable bool // the saved patterns could not be read
	excludeDraft
	before excludeDraft // as the editor opened, for Cancel to go back to
	// checked is the custom text last checked, and invalid its first line
	// that is not a glob: checking compiles every line, so it runs when
	// the text changes rather than on every frame.
	checked string
	invalid int
}

// excludeDraft is what the editor changes.
type excludeDraft struct {
	presets []bool // as domain.ExcludePresets
	custom  string
	maxSize int64 // in bytes, 0 for none; maybe none of maxSizeValues
}

func (d excludeDraft) clone() excludeDraft {
	d.presets = slices.Clone(d.presets)
	return d
}

// maxSizeValues are the size limits the editor offers, in bytes: 0 for
// none.
var maxSizeValues = []int64{0, 100_000_000, 1_000_000_000, 4_000_000_000, 10_000_000_000}

func newExcludeField(e domain.Exclude) excludeField {
	f := excludeField{presets: make([]bool, len(domain.ExcludePresets)), unreadable: e.Unreadable}
	var custom []string
	for _, p := range e.Patterns {
		if i := slices.Index(domain.ExcludePresets, p); i >= 0 {
			f.presets[i] = true
		} else {
			custom = append(custom, p)
		}
	}
	f.custom = strings.Join(custom, "\n")
	f.maxSize = e.MaxSize
	return f
}

// exclude is what the field says, before the service normalizes it.
func (f excludeField) exclude() domain.Exclude {
	var e domain.Exclude
	for i, on := range f.presets {
		if on {
			e.Patterns = append(e.Patterns, domain.ExcludePresets[i])
		}
	}
	e.Patterns = append(e.Patterns, strings.Split(f.custom, "\n")...)
	e.MaxSize = f.maxSize
	return e.Normalize()
}

// size writes a size limit, which are whole units: 100 MB, not 100.0 MB.
func size(n int64) string { return strings.Replace(format.Bytes(n), ".0 ", " ", 1) }

// maxSizeOptions are the size limits the select offers and their labels:
// maxSizeValues, and current too when it is none of them, so a limit set
// otherwise survives an edit.
func maxSizeOptions(current int64) ([]int64, []string) {
	values := maxSizeValues
	if !slices.Contains(values, current) {
		values = append(slices.Clone(values), current)
	}
	labels := make([]string, len(values))
	for i, n := range values {
		labels[i] = locales.TaskExcludeLargeOff()
		if n > 0 {
			labels[i] = locales.TaskExcludeLargeOver(size(n))
		}
	}
	return values, labels
}

// excludeSummary says in a line what the task leaves out.
func excludeSummary(e domain.Exclude) string {
	n := len(e.Patterns)
	switch {
	case n > 0 && e.MaxSize > 0:
		return locales.TaskExcludeRulesAndLarger(n, size(e.MaxSize))
	case n > 0:
		return locales.TaskExcludeRules(n)
	case e.MaxSize > 0:
		return locales.TaskExcludeLarger(size(e.MaxSize))
	}
	return locales.TaskExcludeNone()
}

// excludeInput is the task form's line for what the task leaves out: a
// summary, and a button that opens the editor.
func (a *App) excludeInput(c *ui.Context, f *taskForm) {
	p := paletteFor(c.Theme())
	e := f.exclude.exclude()
	ui.Row(c).Gap(8).FillWidth().AlignItems(ui.Center).Children(func() {
		ui.Text(c, excludeSummary(e)).TextColor(p.muted).FontSize(sizeSmall).Grow(1).MinWidth(0).
			SingleLine().Ellipsis("…")
		if accentButton(c, "exclude-edit", nil, locales.TaskExcludeEdit(), buttonSoft).Clicked() {
			f.exclude.open, f.exclude.before = true, f.exclude.excludeDraft.clone()
		}
	})
}

// excludeDescription says the global rules apply as well, so the form does
// not understate what a run leaves out.
func (a *App) excludeDescription(f *taskForm) string {
	if n := len(a.settings.GlobalExcludes); a.settings.ExcludeEnabled && n > 0 {
		return locales.TaskExcludeGlobalHint(n)
	}
	return ""
}

// excludeError says why the field cannot be saved.
func (a *App) excludeError(f *taskForm) string {
	e := f.exclude.exclude()
	switch {
	case f.problems.For(domain.FieldExclude) != "" || a.invalidExcludeLine(&f.exclude) > 0:
		return locales.TaskExcludeFailed()
	case e.Validate() != "":
		return problemText(e.Validate())
	case f.exclude.unreadable:
		return locales.TaskExcludeUnreadable()
	}
	return ""
}

// invalidExcludeLine is the first line of the field's other rules that is
// not a glob, counted from 1: 0 when all are.
func (a *App) invalidExcludeLine(f *excludeField) int {
	if f.custom == f.checked {
		return f.invalid
	}
	f.checked, f.invalid = f.custom, 0
	for i, line := range strings.Split(f.custom, "\n") {
		if p := strings.TrimSpace(line); p != "" && !a.svc.ValidExclude(p) {
			f.invalid = i + 1
			break
		}
	}
	return f.invalid
}

// excludeEditorBody edits what the task leaves out: the common presets as
// chips, other rules one a line, and a limit on the files' size.
func (a *App) excludeEditorBody(c *ui.Context, f *taskForm) {
	p := paletteFor(c.Theme())
	x := &f.exclude
	if x.unreadable {
		ui.Row(c).Gap(7).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, icons.Warning).Size(13, 13).TextColor(p.danger)
			ui.Text(c, locales.TaskExcludeUnreadable()).FontSize(sizeSmall).TextColor(p.danger).
				Grow(1).MinWidth(0).LineHeight(1.45)
		})
	}
	ui.Column(c).Gap(20).Children(func() {
		fieldGroup(c, locales.TaskExcludeCommon(), func() {
			ui.Row(c).Gap(8).Wrap().Children(func() {
				for i, pattern := range domain.ExcludePresets {
					a.excludeChip(c, &x.presets[i], pattern, i)
				}
			})
		})

		lineErr := ""
		if line := a.invalidExcludeLine(x); line > 0 {
			lineErr = locales.TaskExcludeInvalidLine(line)
		} else if code := x.exclude().Validate(); code != "" {
			lineErr = problemText(code)
		}
		ui.Field(c, locales.TaskExcludePatterns(), func() {
			ui.TextArea(c, &x.custom).Label(locales.TaskExcludePatterns()).Height(120).FillWidth()
		}).Description(locales.TaskExcludePatternsHint()).Error(lineErr)

		ui.Field(c, locales.TaskExcludeLarge(), func() {
			values, labels := maxSizeOptions(x.maxSize)
			chosen := labels[slices.Index(values, x.maxSize)]
			ui.Row(c).Children(func() {
				ui.Select(c, &chosen, labels).Label(locales.TaskExcludeLarge()).Width(260)
			})
			if i := slices.Index(labels, chosen); i >= 0 {
				x.maxSize = values[i]
			}
		})
	})
}

// excludeChip is one of the common patterns, a chip that toggles it.
func (a *App) excludeChip(c *ui.Context, on *bool, pattern string, key int) ui.Element {
	p := paletteFor(c.Theme())
	chip := ui.CheckboxBase(c.Key(key), on).Height(28).PaddingX(10).Gap(6).Radius(14).
		AlignItems(ui.Center).Label(pattern)
	chip.Transition(quickTransition)
	fg, bg, border := p.muted, p.cardAlt, p.hairline
	if *on {
		fg, bg, border = p.accent, p.accentSoft, p.accentEdge
	}
	chip.TextColor(fg)
	if chip.Hovered() {
		bg = p.hover.Over(p.card)
	}
	chip.Background(bg).Border(1, border)
	chip.Children(func() {
		if *on {
			ui.Icon(c, icons.Check).Size(12, 12)
		}
		ui.Text(c, pattern).FontSize(sizeSmall).FontWeight(540).SingleLine()
	})
	return chip
}

// excludeEditorFoot goes back to the form, or keeps the rules the editor
// shows when they are valid.
func (a *App) excludeEditorFoot(c *ui.Context, f *taskForm) {
	x := &f.exclude
	sheetActions(c, func() {
		if accentButton(c, "exclude-cancel", nil, locales.ButtonCancel(), buttonSoft).Clicked() {
			x.open, x.excludeDraft = false, x.before
		}
		if accentButton(c, "exclude-done", nil, locales.TaskExcludeDone(), buttonPrimary).Clicked() {
			if a.invalidExcludeLine(x) == 0 && x.exclude().Validate() == "" {
				x.open, x.unreadable = false, false
			}
		}
	})
}
