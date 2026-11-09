// Package domain holds CloudShuttle's model: storages, sync tasks and the
// runs that execute them, with the rules that keep them valid. It has no
// dependencies on storage, rclone or the UI.
package domain

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"slices"
	"time"
)

// Kind is what a storage connects to.
type Kind string

const (
	// KindS3 is a bucket of an S3-compatible service.
	KindS3 Kind = "s3"
	// KindSMB is a shared folder on a computer or NAS of the local
	// network, reached with SMB.
	KindSMB Kind = "smb"
)

// Kinds lists the kinds the app offers, in the order it shows them.
var Kinds = []Kind{KindS3, KindSMB}

// Valid reports whether k is one of Kinds.
func (k Kind) Valid() bool { return k == KindS3 || k == KindSMB }

// DefaultSMBPort is the port SMB servers listen on.
const DefaultSMBPort = 445

// Provider names an S3-compatible service. The values are rclone's s3
// provider names, so they pass to the engine unchanged.
type Provider string

const (
	ProviderAWS        Provider = "AWS"
	ProviderCloudflare Provider = "Cloudflare"
	ProviderAlibaba    Provider = "Alibaba"
	ProviderTencent    Provider = "TencentCOS"
	ProviderHuawei     Provider = "HuaweiOBS"
	ProviderMinio      Provider = "Minio"
	ProviderOther      Provider = "Other"
)

// Providers lists the providers the app offers, in the order it shows them.
var Providers = []Provider{
	ProviderAWS,
	ProviderCloudflare,
	ProviderAlibaba,
	ProviderTencent,
	ProviderHuawei,
	ProviderMinio,
	ProviderOther,
}

// Valid reports whether p is one of Providers.
func (p Provider) Valid() bool {
	for _, known := range Providers {
		if p == known {
			return true
		}
	}
	return false
}

// Storage is how to reach where a task's files can be: an S3-compatible
// bucket, or an SMB server, whose shares and their folders the tasks
// choose. The fields of the other kind are empty.
// Its secret, the secret access key or the SMB password, is not part of it:
// the app keeps it in the system's credential store, under SecretKey.
type Storage struct {
	ID   string
	Name string
	Kind Kind

	// An S3 bucket.
	Provider    Provider
	Endpoint    string
	Region      string
	Bucket      string
	AccessKeyID string

	// An SMB server.
	Host     string // a host name or an IP address; IPv6 in brackets
	Port     int
	Username string

	// Check is how its connection last tested.
	Check Check

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Check is the outcome of a connection test of a storage. A storage never
// tested, or changed since, has none: its At is zero.
type Check struct {
	At time.Time
	OK bool
	// Problem says what failed, for the view to explain: one of the
	// Problem constants, or "" for an error it can only show as Message.
	Problem string
	Message string // the error, as it came; "" when OK
}

// Problems a check can find.
const (
	ProblemUnreachable   = "unreachable"
	ProblemAuthFailed    = "authFailed"
	ProblemShareNotFound = "shareNotFound"
)

// Checked reports whether the storage's connection was tested as it is.
func (c Check) Checked() bool { return !c.At.IsZero() }

// SameConnection reports whether s and o connect the same way, with the
// same secret kept: a check of one holds for the other.
func (s Storage) SameConnection(o Storage) bool {
	s.Name, o.Name = "", ""
	s.Check, o.Check = Check{}, Check{}
	s.CreatedAt, o.CreatedAt = time.Time{}, time.Time{}
	s.UpdatedAt, o.UpdatedAt = time.Time{}, time.Time{}
	return s == o
}

// SecretKey is the credential store key of the storage's secret.
func (s Storage) SecretKey() string {
	if s.Kind == KindSMB {
		return "storage/" + s.ID + "/password"
	}
	return "storage/" + s.ID + "/secret-access-key"
}

// Location is one side of a task: a folder on this computer when StorageID
// is empty, or a path inside a storage: in its bucket, or on its SMB
// server, where the path begins with the share, as in Documents/Photos.
type Location struct {
	StorageID string
	Path      string
}

// Local reports whether the location is on this computer.
func (l Location) Local() bool { return l.StorageID == "" }

// Mode is how a task transfers files.
type Mode string

const (
	// ModeCopy copies new and changed files, and never deletes.
	ModeCopy Mode = "copy"
	// ModeSync makes the destination identical to the source, deleting
	// files the source does not have.
	ModeSync Mode = "sync"
)

// Valid reports whether m is a known mode.
func (m Mode) Valid() bool { return m == ModeCopy || m == ModeSync }

// Task transfers files from Source to Destination, by hand or on Schedule.
type Task struct {
	ID          string
	Name        string
	Source      Location
	Destination Location
	Mode        Mode
	// Destination is the final transfer path; DestinationFolder records the
	// child chosen below the user-selected parent. Empty means direct transfer.
	AllowRoot         bool
	DestinationFolder string
	// DestinationPending requires a fresh, unoccupied folder on the first real run.
	DestinationPending bool
	Schedule           Schedule
	// ScheduleSince is when the schedule was set as it is: the times it
	// passed before then are not runs the task missed.
	ScheduleSince time.Time
	// TrashDays is how long the task's runs keep the files they delete or
	// replace, in the trash of the destination: TrashFollow takes the
	// settings', TrashOff keeps none, and more is that many days.
	TrashDays int
	// Exclude is what the task's runs leave out, on both sides.
	Exclude Exclude
	// LastSuccessAt is when the task last finished a real run that
	// succeeded, for the long-without-success notice; zero when it has
	// not in the history the app keeps.
	LastSuccessAt time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Exclude is what a task leaves out: a file it matches is neither copied
// from the source nor, by a sync, deleted from the destination.
type Exclude struct {
	// Patterns are globs matched against paths relative to the source and
	// the destination. Only one starting with a slash is anchored to the
	// top; every other one, even one with slashes inside, matches at any
	// depth. One ending with a slash names a folder and all it holds. *
	// stands for any characters but a slash, ** for any, ? for one; [...]
	// and {a,b} as in a shell.
	Patterns []string
	// MaxSize leaves out the files larger than it, in bytes; 0 for none.
	MaxSize int64
	// Unreadable says the stored patterns could not be read: a run would
	// copy or delete what they left out, so the task's runs refuse to
	// start until it is saved again.
	Unreadable bool
}

// ExcludePresets are the patterns the task form offers to tick: files the
// systems make that nobody backs up, and folders a build fills again.
var ExcludePresets = []string{".DS_Store", "Thumbs.db", "node_modules/", "*.tmp"}

// DevDependencyExcludes are the folders that development tools fill with
// version-control data, downloaded dependencies and regenerable builds.
// They are part of what a new database puts in Settings.GlobalExcludes (see
// DefaultGlobalExcludes), which every run leaves out while ExcludeEnabled
// is on, as it is by default: a backup does not need them, and copying or
// keeping them only makes a run slower. The person can then edit that list.
// Each names a folder, and a name without a leading slash matches it in any
// folder (see Exclude).
var DevDependencyExcludes = []string{
	// Git keeps a repository's objects and history here; its remote holds
	// them, and its thousands of small files slow every run.
	".git/",
	// JavaScript and TypeScript: npm, Yarn and Bun install into
	// node_modules; pnpm links it from its store. The rest are the build
	// caches of the frameworks and monorepo tools.
	"node_modules/",
	".pnpm-store/",
	".next/",
	".nuxt/",
	".svelte-kit/",
	".astro/",
	".turbo/",
	".nx/",
	".angular/",
	".parcel-cache/",
	".expo/",
	// Cargo, Maven and sbt build in target.
	"target/",
	// Gradle and Kotlin keep their caches beside the project.
	".gradle/",
	".kotlin/",
	// Python virtual environments, bytecode, and the caches of its tools
	// and of Jupyter.
	".venv/",
	"venv/",
	"__pycache__/",
	".pytest_cache/",
	".mypy_cache/",
	".ruff_cache/",
	".tox/",
	".nox/",
	".hypothesis/",
	"*.egg-info/",
	".ipynb_checkpoints/",
	// CocoaPods and SwiftPM downloads, Carthage builds, Xcode's cache.
	"Pods/",
	".build/",
	"Carthage/Build/",
	"Carthage/Checkouts/",
	"DerivedData/",
	// Dart and Flutter.
	".dart_tool/",
	// Elixir and Erlang downloaded dependencies and builds.
	"deps/",
	"_build/",
	// Haskell.
	"dist-newstyle/",
	".stack-work/",
	// C and C++ package managers and CMake.
	"vcpkg_installed/",
	"CMakeFiles/",
	// Terraform's downloaded providers and modules.
	".terraform/",
	// Godot's import cache.
	".godot/",
}

// SystemFileExcludes are the files a system leaves in the folders it shows,
// which no backup needs. With DevDependencyExcludes they make
// DefaultGlobalExcludes.
var SystemFileExcludes = []string{
	// Finder writes one into every folder it opens on macOS.
	".DS_Store",
}

// DefaultGlobalExcludes is what a new database puts in
// Settings.GlobalExcludes, and what the editor's "Restore defaults" brings
// back: the system files, then the development folders.
func DefaultGlobalExcludes() []string {
	return slices.Concat(SystemFileExcludes, DevDependencyExcludes)
}

// build, dist, out, bin, obj, packages and Library are not in the list
// above on purpose: their names are too common, or the folder can hold the
// output a person means to keep, so a task leaves them out itself. Neither
// is vendor/: a project may commit it on purpose. The list holds no lock
// files either, which a rebuild needs.

// NewExclude is what a new task leaves out.
func NewExclude() Exclude {
	return Exclude{Patterns: []string{".DS_Store", "Thumbs.db"}}
}

// Any reports whether the exclusion leaves anything out.
func (e Exclude) Any() bool { return len(e.Patterns) > 0 || e.MaxSize > 0 }

// What a task's TrashDays may say besides a number of days.
const (
	TrashFollow = 0
	TrashOff    = -1
)

// Trash is how many days the task's runs keep what they delete or replace,
// with settings: 0 for none.
func (t Task) Trash(settings Settings) int {
	switch t.TrashDays {
	case TrashFollow:
		return max(settings.TrashDays, 0)
	case TrashOff:
		return 0
	}
	return t.TrashDays
}

// Trigger says what started a run.
type Trigger string

const (
	TriggerManual    Trigger = "manual"
	TriggerScheduled Trigger = "scheduled"
)

// Status is the outcome of a run.
type Status string

const (
	StatusRunning  Status = "running"
	StatusSuccess  Status = "success"
	StatusFailed   Status = "failed"
	StatusCanceled Status = "canceled"
)

// Problems that stop a run before it deletes anything. Run.Problem holds
// one of them, and the view says what it means.
const (
	RunProblemEmptySource        = "emptySource"
	RunProblemLargeDelete        = "largeDelete"
	RunProblemDestinationChanged = "destinationChanged"
)

// Stats counts what a run transferred.
type Stats struct {
	Bytes          int64
	TotalBytes     int64
	Transfers      int64
	TotalTransfers int64
	Checks         int64
	Deletes        int64
	Errors         int64
	Speed          float64 // bytes per second
	// Trashed is the files a sync moved into the destination's trash: those
	// it deleted, and the earlier versions of those it replaced.
	Trashed int64
}

// Run is one execution of a task. TaskName keeps the task's name at the time,
// so the history still reads after the task is renamed or deleted.
type Run struct {
	ID       string
	TaskID   string
	TaskName string
	Mode     Mode
	Trigger  Trigger
	DryRun   bool
	Status   Status
	Message  string
	// Problem is why the run was stopped before deleting anything, for
	// the view to explain in the person's language; "" otherwise.
	Problem string
	Stats   Stats
	// Trash is the folder, relative to the destination, that the run moved
	// the files it deleted or replaced into; "" when it moved none.
	Trash      string
	StartedAt  time.Time
	FinishedAt time.Time
}

// StoppedDeletes is what a sync the delete check stopped would have
// deleted: how many files, and the paths of the first of them, relative to
// the destination.
type StoppedDeletes struct {
	Total int
	Paths []string
}

// Duration is how long the run took, or has taken so far.
func (r Run) Duration(now time.Time) time.Duration {
	if r.FinishedAt.IsZero() {
		return now.Sub(r.StartedAt)
	}
	return r.FinishedAt.Sub(r.StartedAt)
}

// Errors the service and the store return.
var (
	ErrNotFound      = errors.New("not found")
	ErrNameTaken     = errors.New("name taken")
	ErrStorageInUse  = errors.New("storage in use")
	ErrAlreadyActive = errors.New("task already running")
	ErrNotActive     = errors.New("task not running")
	// ErrDestinationBusy is a task that cannot start because another run
	// is writing to its destination.
	ErrDestinationBusy    = errors.New("another task is running into this destination")
	ErrDestinationChanged = errors.New("destination changed before initialization")
	ErrMissingSecret      = errors.New("secret access key missing")

	// Connecting to a storage failed: the server could not be reached, it
	// refused the credentials, or it has no share of that name.
	ErrUnreachable   = errors.New("server unreachable")
	ErrAuthFailed    = errors.New("authentication failed")
	ErrShareNotFound = errors.New("share not found")
	// ErrFolderNotFound is a folder of a storage that does not exist.
	ErrFolderNotFound = errors.New("folder not found")

	// A backup that does not restore: the password does not open it (or
	// it was changed since), it is no backup of the app, or tasks are
	// running, whose storages and tasks it would replace under them.
	ErrWrongPassword = errors.New("wrong password, or the backup was changed")
	ErrNotBackup     = errors.New("not a backup of the app")
	ErrRunsActive    = errors.New("tasks are running")
)

// NewID returns a random identifier of 16 hex digits.
func NewID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand does not fail on supported platforms
	}
	return hex.EncodeToString(b[:])
}
