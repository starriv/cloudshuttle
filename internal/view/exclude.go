package view

import (
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/internal/format"
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
	t := c.Theme()
	e := f.exclude.exclude()
	err := ""
	switch {
	case f.problems.For(domain.FieldExclude) != "" || a.invalidExcludeLine(&f.exclude) > 0:
		err = locales.TaskExcludeFailed()
	case e.Validate() != "":
		err = problemText(e.Validate())
	case f.exclude.unreadable:
		err = locales.TaskExcludeUnreadable()
	}
	// The summary counts the task's own rules; the description says the
	// global ones apply as well, so the form does not understate a run.
	desc := ""
	if n := len(a.settings.GlobalExcludes); a.settings.ExcludeEnabled && n > 0 {
		desc = locales.TaskExcludeGlobalHint(n)
	}
	ui.Field(c, locales.TaskFieldExclude(), func() {
		ui.Row(c).Gap(8).FillWidth().AlignItems(ui.Center).Children(func() {
			ui.Text(c, excludeSummary(e)).TextColor(t.TextMuted).Grow(1).MinWidth(0)
			if ui.Button(c, locales.TaskExcludeEdit()).Clicked() {
				f.exclude.open, f.exclude.before = true, f.exclude.excludeDraft.clone()
			}
		})
	}).Description(desc).Error(err)
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

// excludeEditor edits what the task leaves out, in place of the form.
func (a *App) excludeEditor(c *ui.Context, f *taskForm) {
	t := c.Theme()
	x := &f.exclude
	ui.Text(c, locales.TaskExcludeTitle()).FontSize(18).FontWeight(500)
	ui.Text(c, locales.TaskExcludeHint()).TextColor(t.TextMuted)
	if x.unreadable {
		ui.Text(c, locales.TaskExcludeUnreadable()).FontSize(12).TextColor(t.Danger)
	}
	if n := len(a.settings.GlobalExcludes); a.settings.ExcludeEnabled && n > 0 {
		ui.Text(c, locales.TaskExcludeGlobalHint(n)).FontSize(12).TextColor(t.TextMuted)
	}
	ui.Form(c, func() {
		ui.Field(c, locales.TaskExcludeCommon(), func() {
			ui.Column(c).Gap(6).Children(func() {
				for i, p := range domain.ExcludePresets {
					ui.Checkbox(c, &x.presets[i], p)
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
			ui.Select(c, &chosen, labels).Label(locales.TaskExcludeLarge()).Width(220)
			if i := slices.Index(labels, chosen); i >= 0 {
				x.maxSize = values[i]
			}
		})
	})
	ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
		if ui.Button(c, locales.ButtonCancel()).Clicked() {
			x.open, x.excludeDraft = false, x.before
		}
		if ui.PrimaryButton(c, locales.TaskExcludeDone()).Clicked() {
			if a.invalidExcludeLine(x) == 0 && x.exclude().Validate() == "" {
				x.open, x.unreadable = false, false
			}
		}
	})
}
