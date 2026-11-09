package domain

import "slices"

// Settings are the app's own, not a task's: how hard a run works, what the
// app tells the person, how long it keeps its history, and what every run
// leaves out. They are kept in the app's database, so a task carries none
// of them.
type Settings struct {
	Transfers     int // files copied at once, 1 to 16
	Checkers      int // files compared at once, 1 to 32
	Retries       int // retries of a failed operation, 1 to 10
	BandwidthMBps int // the cap for a run in MB/s, 0 for none
	NotifyFailed  bool
	NotifySkipped bool
	// NotifyStale tells the person when a scheduled task has not
	// succeeded for StaleDays; it never tells about a task that runs only
	// by hand.
	NotifyStale bool
	// StaleDays is how many days a scheduled task may go without a
	// success before that notice, 1 to 365.
	StaleDays     int
	RetentionDays int // days of run history to keep, 0 for good
	// TrashDays is how long a run keeps the files it deletes or replaces,
	// in a trash folder of its destination, for the tasks that follow the
	// settings; 0 deletes them at once.
	TrashDays int
	// BackupICloud keeps a copy of the configuration in iCloud Drive.
	BackupICloud bool
	// Background is whether the person lets the system start the app to
	// run the schedules while it is closed. It belongs to this computer:
	// restoring a backup keeps it.
	Background Consent
	// Appearance is light, dark, or the system's.
	Appearance Appearance
	// AppIcon is where the app shows its icon besides its window: the
	// Dock, the menu bar, or both. The app's bundle carries no Dock icon
	// (LSUIElement), so the window process applies it at startup; other
	// systems keep their taskbar entry and notification-area icon.
	AppIcon AppIcon
	// CheckUpdates lets the app check for a newer version on its own;
	// InstallUpdates lets it then download it and restart into it, when
	// no task runs and its window is closed.
	CheckUpdates   bool
	InstallUpdates bool
	// ExcludeEnabled applies GlobalExcludes to every run. When off, a
	// run leaves out only what the task itself does.
	ExcludeEnabled bool
	// GlobalExcludes are globs every run leaves out, on both sides, on top
	// of the task's own patterns. A new database starts with
	// DefaultGlobalExcludes, which the person can then edit. The same
	// syntax as a task's patterns applies (see Exclude).
	GlobalExcludes []string
}

// Appearance is the look the window takes: the system's, light or dark.
type Appearance int

const (
	AppearanceSystem Appearance = iota
	AppearanceLight
	AppearanceDark
)

// AppIcon is where the app shows itself besides its window: in the Dock,
// in the menu bar (its status menu), or both. Either way closing the window
// keeps the app running, and the icon it leaves behind opens the window
// again.
type AppIcon int

const (
	// AppIconDockAndMenuBar shows the app in both places.
	AppIconDockAndMenuBar AppIcon = iota
	// AppIconDock shows the app in the Dock alone.
	AppIconDock
	// AppIconMenuBar shows the app in the menu bar alone, without a Dock
	// icon: how every version before the setting started.
	AppIconMenuBar
)

// Consent is the person's answer to a question the app must ask before it
// does something on its own.
type Consent int

const (
	// ConsentUnasked is no answer yet: the app does not do it, and asks
	// when it would.
	ConsentUnasked Consent = iota
	ConsentGiven
	ConsentRefused
)

// DefaultSettings is how the app starts: rclone's own defaults for the
// transfer, notifications on, three months of history, a month of what
// syncs delete or replace, and the system files and the folders of
// development dependencies left out of every run.
func DefaultSettings() Settings {
	return Settings{
		Transfers:      4,
		Checkers:       8,
		Retries:        3,
		BandwidthMBps:  0,
		NotifyFailed:   true,
		NotifySkipped:  true,
		NotifyStale:    true,
		StaleDays:      7,
		RetentionDays:  90,
		TrashDays:      30,
		BackupICloud:   false,
		AppIcon:        AppIconMenuBar,
		CheckUpdates:   true,
		ExcludeEnabled: true,
		GlobalExcludes: DefaultGlobalExcludes(),
	}
}

// Normalize trims the global patterns and drops the empty ones and the
// repeated, as a task's exclusion does.
func (s Settings) Normalize() Settings {
	s.GlobalExcludes = Exclude{Patterns: s.GlobalExcludes}.Normalize().Patterns
	return s
}

// ExcludeFor is the exclusion a run uses: the task's own, plus the global
// ones while ExcludeEnabled is on. A pattern both have is not repeated.
func (s Settings) ExcludeFor(e Exclude) Exclude {
	if !s.ExcludeEnabled {
		return e
	}
	patterns := slices.Clone(e.Patterns)
	for _, p := range s.GlobalExcludes {
		if !slices.Contains(patterns, p) {
			patterns = append(patterns, p)
		}
	}
	e.Patterns = patterns
	return e
}

// Validate checks that every setting is one the app offers.
func (s Settings) Validate() error {
	var p Problems
	add := func(bad bool) {
		if bad {
			p = append(p, Problem{FieldSettings, CodeOutOfRange})
		}
	}
	add(s.Transfers < 1 || s.Transfers > 16)
	add(s.Checkers < 1 || s.Checkers > 32)
	add(s.Retries < 1 || s.Retries > 10)
	add(s.BandwidthMBps < 0 || s.BandwidthMBps > 1000)
	add(s.RetentionDays < 0 || s.RetentionDays > 3650)
	add(s.TrashDays < 0 || s.TrashDays > 3650)
	add(s.StaleDays < 1 || s.StaleDays > 365)
	add(s.Appearance < AppearanceSystem || s.Appearance > AppearanceDark)
	add(s.AppIcon < AppIconDockAndMenuBar || s.AppIcon > AppIconMenuBar)
	if code := (Exclude{Patterns: s.GlobalExcludes}).Validate(); code != "" {
		p = append(p, Problem{FieldGlobalExclude, code})
	}
	if len(p) > 0 {
		return p
	}
	return nil
}
