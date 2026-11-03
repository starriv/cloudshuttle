package domain

import (
	"errors"
	"net"
	"net/url"
	"runtime"
	"strconv"
	"strings"
)

// Field names a field of a form, for the problem found in it.
type Field string

const (
	FieldName        Field = "name"
	FieldKind        Field = "kind"
	FieldProvider    Field = "provider"
	FieldEndpoint    Field = "endpoint"
	FieldBucket      Field = "bucket"
	FieldAccessKeyID Field = "accessKeyId"
	FieldSecret      Field = "secretAccessKey" // also the SMB password
	FieldHost        Field = "host"
	FieldPort        Field = "port"
	FieldUsername    Field = "username"
	FieldSource      Field = "source"
	FieldDestination Field = "destination"
	FieldMode        Field = "mode"
	FieldSchedule    Field = "schedule"
	FieldSettings    Field = "settings"
	// The password that encrypts the backups, and the same typed again.
	FieldBackupPassword Field = "backupPassword"
	FieldBackupConfirm  Field = "backupConfirm"
)

// Code is what is wrong with a field. The UI says it in the user's language.
type Code string

const (
	CodeRequired       Code = "required"
	CodeTooLong        Code = "tooLong"
	CodeInvalid        Code = "invalid"
	CodeInvalidURL     Code = "invalidUrl"
	CodeInvalidHost    Code = "invalidHost"
	CodeInvalidPort    Code = "invalidPort"
	CodeShareRequired  Code = "shareRequired"
	CodeNotAbsolute    Code = "notAbsolute"
	CodeUnknownStorage Code = "unknownStorage"
	CodeSameLocation   Code = "sameLocation"
	CodeNested         Code = "nested"
	// CodeMismatch is a password typed again differently.
	CodeMismatch Code = "mismatch"
	// CodeNoDays is a weekly schedule with no day chosen.
	CodeNoDays Code = "noDays"
	CodeTaken  Code = "taken"
	// CodeOutOfRange is a number outside the values the app offers.
	CodeOutOfRange Code = "outOfRange"
	// CodeInvalidPath is a path with a ".." step, which would leave the
	// folder it names.
	CodeInvalidPath Code = "invalidPath"
	// CodeForbiddenDestination is a folder a sync must not empty: a volume
	// root, the home folder, or one that holds the app's own data.
	CodeForbiddenDestination Code = "forbiddenDestination"
	// CodeICloudUnavailable is iCloud backup turned on where iCloud Drive
	// is not set up, so there is no folder to keep the copy in.
	CodeICloudUnavailable Code = "icloudUnavailable"
	// CodeSharedDestination is a destination another task also writes to,
	// where each could delete what the other put there.
	CodeSharedDestination Code = "sharedDestination"
)

// Problem is one invalid field.
type Problem struct {
	Field Field
	Code  Code
}

// Problems is the error of a validation that failed.
type Problems []Problem

func (p Problems) Error() string {
	parts := make([]string, len(p))
	for i, problem := range p {
		parts[i] = string(problem.Field) + ": " + string(problem.Code)
	}
	return "invalid: " + strings.Join(parts, ", ")
}

// Is makes a name already used match ErrNameTaken.
func (p Problems) Is(target error) bool {
	return target == ErrNameTaken && p.For(FieldName) == CodeTaken
}

// For returns the code of the first problem with field, or "".
func (p Problems) For(field Field) Code {
	for _, problem := range p {
		if problem.Field == field {
			return problem.Code
		}
	}
	return ""
}

// AsProblems returns the problems err carries, or nil.
func AsProblems(err error) Problems {
	var p Problems
	if errors.As(err, &p) {
		return p
	}
	return nil
}

const maxNameLength = 64

// StorageInput is what the user enters for a storage. Only the fields of its
// Kind count. An empty Secret keeps the storage's current secret when
// editing one.
type StorageInput struct {
	Name string
	Kind Kind

	Provider    Provider
	Endpoint    string
	Region      string
	Bucket      string
	AccessKeyID string

	Host     string
	Port     string // empty for DefaultSMBPort
	Username string

	Secret string // the secret access key, or the SMB password
}

// Normalize trims the input's text. For SMB it also takes the host and the
// port out of an address pasted whole, such as smb://nas.local:445/photos
// or \\nas\photos; the share is the tasks' to choose.
func (in StorageInput) Normalize() StorageInput {
	in.Name = strings.TrimSpace(in.Name)
	if in.Kind == "" {
		in.Kind = KindS3
	}
	in.Endpoint = strings.TrimSpace(in.Endpoint)
	in.Region = strings.TrimSpace(in.Region)
	in.Bucket = strings.Trim(strings.TrimSpace(in.Bucket), "/")
	in.AccessKeyID = strings.TrimSpace(in.AccessKeyID)
	in.Port = strings.TrimSpace(in.Port)
	in.Username = strings.TrimSpace(in.Username)
	in = in.splitAddress()
	// Passwords may begin or end with spaces; keys may not.
	if in.Kind != KindSMB {
		in.Secret = strings.TrimSpace(in.Secret)
	}
	return in
}

func (in StorageInput) splitAddress() StorageInput {
	h := strings.ReplaceAll(strings.TrimSpace(in.Host), `\`, "/")
	if scheme, rest, ok := strings.Cut(h, "://"); ok && (strings.EqualFold(scheme, "smb") || strings.EqualFold(scheme, "cifs")) {
		h = rest
	}
	h = strings.TrimLeft(h, "/")
	host, _, _ := strings.Cut(h, "/")
	if name, port, err := net.SplitHostPort(host); err == nil {
		host = name
		if in.Port == "" {
			in.Port = port
		}
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
		host = "[" + host + "]"
	}
	in.Host = host
	return in
}

// PortNumber is the input's port, DefaultSMBPort when it is empty, or 0
// when it is not a port.
func (in StorageInput) PortNumber() int {
	if in.Port == "" {
		return DefaultSMBPort
	}
	n, err := strconv.Atoi(in.Port)
	if err != nil || n < 1 || n > 65535 {
		return 0
	}
	return n
}

// GuestUser is the user name that signs in to an SMB share open to guests,
// with no password.
const GuestUser = "guest"

// Guest reports whether the input signs in to an SMB share as a guest.
func (in StorageInput) Guest() bool { return strings.EqualFold(in.Username, GuestUser) }

// Validate checks a normalized input. creating says whether the secret is
// required, as it is for a new storage; editing, an empty secret keeps the
// current one. A guest of an SMB share needs no password.
func (in StorageInput) Validate(creating bool) error {
	var p Problems
	p = checkName(p, in.Name)
	switch in.Kind {
	case KindS3:
		p = in.checkS3(p, creating)
	case KindSMB:
		p = in.checkSMB(p, creating)
	default:
		p = append(p, Problem{FieldKind, CodeInvalid})
	}
	if len(p) > 0 {
		return p
	}
	return nil
}

func (in StorageInput) checkS3(p Problems, creating bool) Problems {
	if !in.Provider.Valid() {
		p = append(p, Problem{FieldProvider, CodeInvalid})
	}
	if in.Endpoint == "" {
		if in.Provider != ProviderAWS {
			p = append(p, Problem{FieldEndpoint, CodeRequired})
		}
	} else if u, err := url.Parse(in.Endpoint); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		p = append(p, Problem{FieldEndpoint, CodeInvalidURL})
	}
	if in.Bucket == "" {
		p = append(p, Problem{FieldBucket, CodeRequired})
	}
	if in.AccessKeyID == "" {
		p = append(p, Problem{FieldAccessKeyID, CodeRequired})
	}
	if creating && in.Secret == "" {
		p = append(p, Problem{FieldSecret, CodeRequired})
	}
	return p
}

func (in StorageInput) checkSMB(p Problems, creating bool) Problems {
	if creating && in.Secret == "" && in.Username != "" && !in.Guest() {
		p = append(p, Problem{FieldSecret, CodeRequired})
	}
	switch {
	case in.Host == "":
		p = append(p, Problem{FieldHost, CodeRequired})
	case !validHost(in.Host):
		p = append(p, Problem{FieldHost, CodeInvalidHost})
	}
	if in.PortNumber() == 0 {
		p = append(p, Problem{FieldPort, CodeInvalidPort})
	}
	if in.Username == "" {
		p = append(p, Problem{FieldUsername, CodeRequired})
	}
	return p
}

// validHost accepts a host name, an IPv4 address or a bracketed IPv6 one.
func validHost(h string) bool {
	if strings.HasPrefix(h, "[") {
		ip := net.ParseIP(strings.Trim(h, "[]"))
		return strings.HasSuffix(h, "]") && ip != nil
	}
	if len(h) > 253 {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r == '-' || r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
				return false
			}
		}
	}
	return true
}

// TaskInput is what the user enters for a task.
type TaskInput struct {
	Name        string
	Source      Location
	Destination Location
	Mode        Mode
	Schedule    Schedule
}

// Normalize trims the input's text and cleans its paths.
func (in TaskInput) Normalize() TaskInput {
	in.Name = strings.TrimSpace(in.Name)
	in.Source = normalizeLocation(in.Source)
	in.Destination = normalizeLocation(in.Destination)
	in.Schedule = in.Schedule.Normalize()
	return in
}

func normalizeLocation(l Location) Location {
	l.Path = strings.TrimSpace(l.Path)
	if l.Local() {
		if len(l.Path) > 1 {
			trimmed := strings.TrimRight(l.Path, `/\`)
			switch {
			case len(trimmed) == 2 && trimmed[1] == ':':
				// A drive's root keeps its separator: "C:" alone is not
				// an absolute path.
				l.Path = trimmed + `\`
			default:
				l.Path = trimmed
			}
		}
	} else {
		l.Path = strings.Trim(l.Path, "/")
	}
	return l
}

// Validate checks a normalized input against the storages that exist.
func (in TaskInput) Validate(storages []Storage) error {
	var p Problems
	p = checkName(p, in.Name)
	p = checkLocation(p, FieldSource, in.Source, storages)
	p = checkLocation(p, FieldDestination, in.Destination, storages)
	if root := rootOf(in.Source, storages); p.For(FieldSource) == "" && p.For(FieldDestination) == "" &&
		root == rootOf(in.Destination, storages) {
		src, dst := in.Source.Path, in.Destination.Path
		if foldNames(in.Source, storages) {
			// SMB servers match names regardless of case.
			src, dst = strings.ToLower(src), strings.ToLower(dst)
		}
		switch {
		case src == dst:
			p = append(p, Problem{FieldDestination, CodeSameLocation})
		case within(src, dst) || within(dst, src):
			p = append(p, Problem{FieldDestination, CodeNested})
		}
	}
	if !in.Mode.Valid() {
		p = append(p, Problem{FieldMode, CodeInvalid})
	}
	if in.Mode == ModeSync {
		p = checkSyncDestination(p, in.Destination)
	}
	if code := in.Schedule.Validate(); code != "" {
		p = append(p, Problem{FieldSchedule, code})
	}
	if len(p) > 0 {
		return p
	}
	return nil
}

// checkSyncDestination refuses a folder a sync would empty of everything but
// the source: a volume root, or the top of the computer's folders.
func checkSyncDestination(p Problems, l Location) Problems {
	if p.For(FieldDestination) != "" || !l.Local() {
		return p
	}
	if isRootPath(l.Path) {
		return append(p, Problem{FieldDestination, CodeForbiddenDestination})
	}
	return p
}

// isRootPath reports whether path is a volume's or a share's root, which
// holds everything else.
func isRootPath(path string) bool {
	p := strings.ReplaceAll(path, `\`, "/")
	if strings.HasPrefix(p, "//") {
		// \\server\share, or the same with forward slashes.
		parts := strings.FieldsFunc(p, func(r rune) bool { return r == '/' })
		return len(parts) <= 2
	}
	p = strings.TrimRight(p, "/")
	if p == "" {
		return true // "/"
	}
	return len(p) == 2 && p[1] == ':' // "C:"
}

func checkName(p Problems, name string) Problems {
	switch {
	case name == "":
		return append(p, Problem{FieldName, CodeRequired})
	case len([]rune(name)) > maxNameLength:
		return append(p, Problem{FieldName, CodeTooLong})
	}
	return p
}

func checkLocation(p Problems, field Field, l Location, storages []Storage) Problems {
	if hasParent(l.Path) {
		return append(p, Problem{field, CodeInvalidPath})
	}
	if l.Local() {
		switch {
		case l.Path == "":
			return append(p, Problem{field, CodeRequired})
		case !isAbsolute(l.Path):
			return append(p, Problem{field, CodeNotAbsolute})
		}
		return p
	}
	for _, s := range storages {
		if s.ID == l.StorageID {
			if s.Kind == KindSMB && l.Path == "" {
				// The top of an SMB server holds shares, not files.
				return append(p, Problem{field, CodeShareRequired})
			}
			return p
		}
	}
	return append(p, Problem{field, CodeUnknownStorage})
}

// hasParent reports whether path has a ".." step, which names a folder other
// than the one it appears to.
func hasParent(path string) bool {
	for _, step := range strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' }) {
		if step == ".." {
			return true
		}
	}
	return false
}

// Overlap reports whether a and b name the same place, or one is inside the
// other, so that a sync of both could delete the other's files.
func Overlap(a, b Location, storages []Storage) bool {
	if rootOf(a, storages) != rootOf(b, storages) {
		return false
	}
	as, bs := a.Path, b.Path
	if foldNames(a, storages) || foldNames(b, storages) {
		as, bs = strings.ToLower(as), strings.ToLower(bs)
	}
	return as == bs || within(as, bs) || within(bs, as)
}

// foldNames reports whether the place ignores case in the names it keeps:
// SMB servers, and the filesystems of Macs and Windows.
func foldNames(l Location, storages []Storage) bool {
	if l.Local() {
		return runtime.GOOS == "darwin" || runtime.GOOS == "windows"
	}
	for _, s := range storages {
		if s.ID == l.StorageID {
			return s.Kind == KindSMB
		}
	}
	return false
}

// rootOf names the place a location's path starts from, so two storages of
// the same bucket or SMB server compare equal.
func rootOf(l Location, storages []Storage) string {
	if l.Local() {
		return "local"
	}
	for _, s := range storages {
		if s.ID != l.StorageID {
			continue
		}
		if s.Kind == KindSMB {
			return "smb\x00" + strings.ToLower(s.Host) + "\x00" + strconv.Itoa(s.Port)
		}
		return "s3\x00" + strings.TrimRight(strings.ToLower(s.Endpoint), "/") + "\x00" + s.Bucket
	}
	return "storage\x00" + l.StorageID
}

// isAbsolute accepts absolute paths of every platform, so a task's paths
// validate the same way wherever the tests run.
func isAbsolute(path string) bool {
	if strings.HasPrefix(path, "/") || strings.HasPrefix(path, `\\`) {
		return true
	}
	return len(path) >= 3 && path[1] == ':' && (path[2] == '\\' || path[2] == '/') &&
		(path[0] >= 'A' && path[0] <= 'Z' || path[0] >= 'a' && path[0] <= 'z')
}

// within reports whether path is inside parent, or is parent.
func within(path, parent string) bool {
	path, parent = strings.ReplaceAll(path, `\`, "/"), strings.ReplaceAll(parent, `\`, "/")
	if parent == "" || parent == "/" {
		return true
	}
	return path == parent || strings.HasPrefix(path, strings.TrimRight(parent, "/")+"/")
}
