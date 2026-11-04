package domain

// Settings are the app's own, not a task's: how hard a run works, what the
// app tells the person, and how long it keeps its history. They are kept in
// the app's database, so a task carries none of them.
type Settings struct {
	Transfers     int // files copied at once, 1 to 16
	Checkers      int // files compared at once, 1 to 32
	Retries       int // retries of a failed operation, 1 to 10
	BandwidthMBps int // the cap for a run in MB/s, 0 for none
	NotifyFailed  bool
	NotifySkipped bool
	RetentionDays int // days of run history to keep, 0 for good
	// BackupICloud keeps a copy of the configuration in iCloud Drive.
	BackupICloud bool
	// Background is whether the person lets the system start the app to
	// run the schedules while it is closed. It belongs to this computer:
	// restoring a backup keeps it.
	Background Consent
	// Appearance is light, dark, or the system's.
	Appearance Appearance
	// CheckUpdates lets the app check for a newer version on its own;
	// InstallUpdates lets it then download it and restart into it, when
	// no task runs and its window is closed.
	CheckUpdates   bool
	InstallUpdates bool
}

// Appearance is the look the window takes: the system's, light or dark.
type Appearance int

const (
	AppearanceSystem Appearance = iota
	AppearanceLight
	AppearanceDark
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
// transfer, notifications on, and three months of history.
func DefaultSettings() Settings {
	return Settings{
		Transfers:     4,
		Checkers:      8,
		Retries:       3,
		BandwidthMBps: 0,
		NotifyFailed:  true,
		NotifySkipped: true,
		RetentionDays: 90,
		BackupICloud:  false,
		CheckUpdates:  true,
	}
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
	add(s.Appearance < AppearanceSystem || s.Appearance > AppearanceDark)
	if len(p) > 0 {
		return p
	}
	return nil
}
