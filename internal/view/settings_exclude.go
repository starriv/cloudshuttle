package view

import (
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/internal/icons"
	"github.com/starriv/cloudshuttle/locales"
)

// settingsExcludeForm is the global exclusion list as the editor edits it:
// the rules one a line, the text it opened with for Cancel to go back to,
// and the result of checking the text for an invalid line.
type settingsExcludeForm struct {
	open   bool
	text   string
	before string // as the editor opened
	// checked is the text last checked, and invalid its first line that is
	// not a glob: checking compiles every line, so it runs when the text
	// changes rather than on every frame.
	checked string
	invalid int
}

// withoutField drops the problems with field: editing its value answers
// them.
func withoutField(problems domain.Problems, field domain.Field) domain.Problems {
	kept := make(domain.Problems, 0, len(problems))
	for _, p := range problems {
		if p.Field != field {
			kept = append(kept, p)
		}
	}
	return kept
}

// openGlobalExcludeEditor opens the dialog on the page's rules, with the
// text as it stands.
func (a *App) openGlobalExcludeEditor() {
	text := a.settingsForm.globalExclude
	a.exclude = settingsExcludeForm{open: true, text: text, before: text}
}

// excludeRuleCount is the number of rules in text: one a line, the empty
// ones left out.
func excludeRuleCount(text string) int {
	n := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

// invalidGlobalExcludeLine is the first line of the editor's rules that is
// not a glob, counted from 1: 0 when all are.
func (a *App) invalidGlobalExcludeLine(f *settingsExcludeForm) int {
	if f.text == f.checked {
		return f.invalid
	}
	f.checked, f.invalid = f.text, 0
	for i, line := range strings.Split(f.text, "\n") {
		if p := strings.TrimSpace(line); p != "" && !a.svc.ValidExclude(p) {
			f.invalid = i + 1
			break
		}
	}
	return f.invalid
}

// globalExcludeProblem is why the editor cannot be done: a line that is
// not a glob, or a list that breaks the task rules' limits.
func (a *App) globalExcludeProblem(f *settingsExcludeForm) string {
	if line := a.invalidGlobalExcludeLine(f); line > 0 {
		return locales.SettingsExcludeInvalidLine(line)
	}
	lines := domain.Exclude{Patterns: strings.Split(f.text, "\n")}.Normalize().Patterns
	if code := (domain.Exclude{Patterns: lines}).Validate(); code != "" {
		return problemText(code)
	}
	return ""
}

// globalExcludeDialog edits the rules every run leaves out: the built-in
// list at first, and whatever the person makes of it. The rules take the
// room of the sheet's body, and its foot keeps the way back and the way to
// keep them at hand however long the list is.
func (a *App) globalExcludeDialog(c *ui.Context) {
	f := &a.exclude
	if !f.open {
		return
	}
	p := paletteFor(c.Theme())
	sheet(c, &f.open, sheetOptions{
		width: 640,
		title: locales.SettingsExcludeDialogTitle(),
	}, func() {
		ui.Column(c).Gap(14).Padding(2, 0, 2, 0).Children(func() {
			ui.Text(c, locales.SettingsExcludeDialogHint()).FontSize(sizeSmall).TextColor(p.muted).
				LineHeight(1.55)
			ui.Field(c, locales.SettingsExcludeRules(), func() {
				ui.TextArea(c, &f.text).Height(300).FillWidth()
			}).Error(a.globalExcludeProblem(f))
		})
	}, func() {
		if accentButton(c, "exclude-defaults", icons.Refresh, locales.SettingsExcludeRestoreDefaults(), buttonSoft).
			Clicked() {
			f.text = strings.Join(domain.DevDependencyExcludes, "\n")
			f.checked = ""
		}
		sheetActions(c, func() {
			if accentButton(c, "exclude-cancel", nil, locales.ButtonCancel(), buttonSoft).Clicked() {
				f.text, f.open = f.before, false
			}
			if accentButton(c, "exclude-done", nil, locales.SettingsExcludeDone(), buttonPrimary).Clicked() {
				// An invalid rule keeps the editor open, with the line
				// marked: saving would only bring the page's error back.
				if a.globalExcludeProblem(f) == "" {
					lines := domain.Exclude{Patterns: strings.Split(f.text, "\n")}.Normalize().Patterns
					a.settingsForm.globalExclude = strings.Join(lines, "\n")
					a.settingsForm.problems = withoutField(a.settingsForm.problems, domain.FieldGlobalExclude)
					f.open = false
				}
			}
		})
	})
}
