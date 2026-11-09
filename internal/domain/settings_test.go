package domain

import (
	"slices"
	"strings"
	"testing"
)

// The app starts with its icon in the menu bar alone, as every version
// before the setting did; a value the app does not offer is refused.
func TestTheAppIconIsInTheMenuBarUnlessChosen(t *testing.T) {
	if got := DefaultSettings().AppIcon; got != AppIconMenuBar {
		t.Errorf("the default app icon is %v, want the menu bar alone", got)
	}
	for _, icon := range []AppIcon{AppIconDockAndMenuBar, AppIconDock, AppIconMenuBar} {
		s := DefaultSettings()
		s.AppIcon = icon
		if err := s.Validate(); err != nil {
			t.Errorf("icon %v: %v", icon, err)
		}
	}
	s := DefaultSettings()
	s.AppIcon = AppIconMenuBar + 1
	if code := AsProblems(s.Validate()).For(FieldSettings); code != CodeOutOfRange {
		t.Errorf("an icon the app does not offer: %q, want %q", code, CodeOutOfRange)
	}
}

// A new database starts with the system files and the built-in folders,
// which the person can then edit; the default must hold its own copy of the
// list.
func TestTheDefaultsLeaveOutTheDevelopmentDependencies(t *testing.T) {
	s := DefaultSettings()
	if !s.ExcludeEnabled {
		t.Fatal("the global exclusion is off in a new database")
	}
	if !slices.Equal(s.GlobalExcludes, DefaultGlobalExcludes()) {
		t.Fatalf("global excludes = %v, want the built-in list", s.GlobalExcludes)
	}
	if !slices.Contains(s.GlobalExcludes, ".DS_Store") {
		t.Errorf("global excludes = %v, want .DS_Store", s.GlobalExcludes)
	}
	for i := range s.GlobalExcludes {
		s.GlobalExcludes[i] = "changed"
	}
	if SystemFileExcludes[0] == "changed" || DevDependencyExcludes[0] == "changed" {
		t.Fatal("the defaults share their slice with the built-in lists")
	}
}

func TestExcludeForAddsTheGlobalPatterns(t *testing.T) {
	s := DefaultSettings()
	task := Exclude{Patterns: []string{".DS_Store", "node_modules/"}, MaxSize: 10}
	got := s.ExcludeFor(task)
	if got.MaxSize != 10 {
		t.Errorf("max size = %d, want 10", got.MaxSize)
	}
	if !slices.Contains(got.Patterns, ".DS_Store") {
		t.Error("the task's own patterns went")
	}
	// node_modules is both the task's and the global list's, once.
	if n := count(got.Patterns, "node_modules/"); n != 1 {
		t.Errorf("node_modules appears %d times, want once", n)
	}
	// .DS_Store is both the task's and the global list's too.
	if len(got.Patterns) != len(DefaultGlobalExcludes()) {
		t.Errorf("patterns = %d, want %d: %v", len(got.Patterns), len(DefaultGlobalExcludes()), got.Patterns)
	}
	if !slices.Equal(task.Patterns, []string{".DS_Store", "node_modules/"}) {
		t.Errorf("the task's exclusion changed: %v", task.Patterns)
	}

	// The person edits the list: the task gets exactly those patterns.
	s.GlobalExcludes = []string{"*.log", "node_modules/"}
	got = s.ExcludeFor(task)
	for _, want := range []string{".DS_Store", "node_modules/", "*.log"} {
		if !slices.Contains(got.Patterns, want) {
			t.Errorf("patterns = %v, want %q", got.Patterns, want)
		}
	}
	if n := count(got.Patterns, "node_modules/"); n != 1 {
		t.Errorf("node_modules appears %d times, want once", n)
	}
	if len(got.Patterns) != 3 {
		t.Errorf("patterns = %v, want three", got.Patterns)
	}

	off := s
	off.ExcludeEnabled = false
	if got := off.ExcludeFor(task); !slices.Equal(got.Patterns, task.Patterns) {
		t.Errorf("off: patterns = %v, want the task's", got.Patterns)
	}
}

func TestSettingsNormalizeTheGlobalPatterns(t *testing.T) {
	s := Settings{GlobalExcludes: []string{" *.log ", "", "*.log", "node_modules/"}}
	if got, want := s.Normalize().GlobalExcludes, []string{"*.log", "node_modules/"}; !slices.Equal(got, want) {
		t.Errorf("normalized = %v, want %v", got, want)
	}
}

func TestSettingsValidateTheGlobalPatterns(t *testing.T) {
	s := DefaultSettings()
	s.GlobalExcludes = make([]string, maxExcludePatterns+1)
	if code := AsProblems(s.Validate()).For(FieldGlobalExclude); code != CodeOutOfRange {
		t.Errorf("too many patterns: %q, want %q", code, CodeOutOfRange)
	}
	s.GlobalExcludes = []string{strings.Repeat("a", maxExcludeLength+1) + "/"}
	if code := AsProblems(s.Validate()).For(FieldGlobalExclude); code != CodeTooLong {
		t.Errorf("a long pattern: %q, want %q", code, CodeTooLong)
	}
	s.GlobalExcludes = []string{"ok/", "bad\npattern/"}
	if code := AsProblems(s.Validate()).For(FieldGlobalExclude); code != CodeInvalid {
		t.Errorf("a pattern with a newline: %q, want %q", code, CodeInvalid)
	}
	if code := AsProblems(DefaultSettings().Validate()).For(FieldGlobalExclude); code != "" {
		t.Errorf("the defaults are invalid: %q", code)
	}
}

// The built-in list is meant to name folders, and a pattern that is not a
// glob would make every run fail in the engine.
func TestDevDependencyExcludesAreFolders(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range DevDependencyExcludes {
		if !strings.HasSuffix(p, "/") {
			t.Errorf("%q does not name a folder", p)
		}
		if seen[p] {
			t.Errorf("%q is listed twice", p)
		}
		seen[p] = true
	}
}

func count(patterns []string, want string) int {
	n := 0
	for _, p := range patterns {
		if p == want {
			n++
		}
	}
	return n
}
