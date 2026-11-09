package domain

import (
	"strconv"
	"strings"
	"testing"
)

func TestStorageInputValidate(t *testing.T) {
	valid := StorageInput{
		Name: "Backups", Kind: KindS3, Provider: ProviderCloudflare, Endpoint: "https://acct.r2.cloudflarestorage.com",
		Bucket: "photos", AccessKeyID: "key", Secret: "secret",
	}
	if err := valid.Validate(true); err != nil {
		t.Fatalf("valid input: %v", err)
	}

	aws := valid
	aws.Provider, aws.Endpoint = ProviderAWS, ""
	if err := aws.Validate(true); err != nil {
		t.Errorf("AWS needs no endpoint: %v", err)
	}

	cases := []struct {
		name  string
		edit  func(*StorageInput)
		field Field
		code  Code
	}{
		{"empty name", func(in *StorageInput) { in.Name = "" }, FieldName, CodeRequired},
		{"unknown provider", func(in *StorageInput) { in.Provider = "Nope" }, FieldProvider, CodeInvalid},
		{"missing endpoint", func(in *StorageInput) { in.Endpoint = "" }, FieldEndpoint, CodeRequired},
		{"bad endpoint", func(in *StorageInput) { in.Endpoint = "r2.example" }, FieldEndpoint, CodeInvalidURL},
		{"missing bucket", func(in *StorageInput) { in.Bucket = "" }, FieldBucket, CodeRequired},
		{"missing secret", func(in *StorageInput) { in.Secret = "" }, FieldSecret, CodeRequired},
	}
	for _, tc := range cases {
		in := valid
		tc.edit(&in)
		if got := AsProblems(in.Validate(true)).For(tc.field); got != tc.code {
			t.Errorf("%s: %s = %q, want %q", tc.name, tc.field, got, tc.code)
		}
	}

	editing := valid
	editing.Secret = ""
	if err := editing.Validate(false); err != nil {
		t.Errorf("editing keeps the secret: %v", err)
	}
}

// The form an edit starts from names the storage, with the fields of its
// kind and no secret.
func TestStorageInputForEditing(t *testing.T) {
	cases := []struct {
		storage Storage
		want    StorageInput
	}{
		{
			Storage{ID: "s1", Name: "R2", Kind: KindS3, Provider: ProviderCloudflare,
				Endpoint: "https://acct.r2.cloudflarestorage.com", Region: "auto", Bucket: "photos", AccessKeyID: "key"},
			StorageInput{Name: "R2", Kind: KindS3, Provider: ProviderCloudflare,
				Endpoint: "https://acct.r2.cloudflarestorage.com", Region: "auto", Bucket: "photos", AccessKeyID: "key"},
		},
		{
			Storage{ID: "s2", Name: "NAS", Kind: KindSMB, Host: "nas.local", Port: DefaultSMBPort, Username: "me"},
			StorageInput{Name: "NAS", Kind: KindSMB, Host: "nas.local", Username: "me"},
		},
		{
			Storage{ID: "s3", Name: "NAS", Kind: KindSMB, Host: "nas.local", Port: 4455, Username: "me"},
			StorageInput{Name: "NAS", Kind: KindSMB, Host: "nas.local", Port: "4455", Username: "me"},
		},
		{
			Storage{ID: "s4", Name: "Tank", Kind: KindNFS, Host: "nas.local", Export: "/volume1/backup", UID: 501, GID: 20},
			StorageInput{Name: "Tank", Kind: KindNFS, Host: "nas.local", Export: "/volume1/backup", UID: "501", GID: "20"},
		},
		{
			Storage{ID: "s5", Name: "Tank", Kind: KindNFS, Host: "nas.local", Port: 2049, Export: "/volume1/backup", UID: 0, GID: 0},
			StorageInput{Name: "Tank", Kind: KindNFS, Host: "nas.local", Port: "2049", Export: "/volume1/backup", UID: "0", GID: "0"},
		},
	}
	for _, c := range cases {
		if got := c.storage.Input(); got != c.want {
			t.Errorf("Input(%+v) = %+v, want %+v", c.storage, got, c.want)
		}
		if err := c.storage.Input().Normalize().Validate(false); err != nil {
			t.Errorf("the input of %+v is not a valid edit: %v", c.storage, err)
		}
	}
}

// NFS keeps no secret, so it has no credential store key; an unknown kind
// keeps the S3 one, as it did before it had a kind of its own.
func TestStorageSecretKey(t *testing.T) {
	for _, c := range []struct {
		storage Storage
		wantKey string
		wantHas bool
	}{
		{Storage{ID: "n1", Kind: KindNFS}, "", false},
		{Storage{ID: "s1", Kind: KindSMB}, "storage/s1/password", true},
		{Storage{ID: "b1", Kind: KindS3}, "storage/b1/secret-access-key", true},
		{Storage{ID: "x1", Kind: "ftp"}, "storage/x1/secret-access-key", true},
	} {
		if got := c.storage.SecretKey(); got != c.wantKey {
			t.Errorf("SecretKey of %q = %q, want %q", c.storage.Kind, got, c.wantKey)
		}
		if got := c.storage.HasSecret(); got != c.wantHas {
			t.Errorf("HasSecret of %q = %v, want %v", c.storage.Kind, got, c.wantHas)
		}
	}
}

func TestSMBStorageInput(t *testing.T) {
	valid := StorageInput{Name: "NAS", Kind: KindSMB, Host: "nas.local", Username: "me", Secret: "pw"}
	if err := valid.Normalize().Validate(true); err != nil {
		t.Fatalf("valid input: %v", err)
	}
	guest := valid
	guest.Username, guest.Secret = "Guest", ""
	if err := guest.Normalize().Validate(true); err != nil {
		t.Errorf("a guest needs no password: %v", err)
	}
	editing := valid
	editing.Secret = ""
	if err := editing.Normalize().Validate(false); err != nil {
		t.Errorf("editing keeps the password: %v", err)
	}
	if got := valid.Normalize().PortNumber(); got != DefaultSMBPort {
		t.Errorf("default port = %d", got)
	}

	cases := []struct {
		name  string
		edit  func(*StorageInput)
		field Field
		code  Code
	}{
		{"missing host", func(in *StorageInput) { in.Host = "" }, FieldHost, CodeRequired},
		{"bad host", func(in *StorageInput) { in.Host = "my nas" }, FieldHost, CodeInvalidHost},
		{"bad port", func(in *StorageInput) { in.Port = "70000" }, FieldPort, CodeInvalidPort},
		{"text port", func(in *StorageInput) { in.Port = "smb" }, FieldPort, CodeInvalidPort},
		{"missing user", func(in *StorageInput) { in.Username = "" }, FieldUsername, CodeRequired},
		{"missing password", func(in *StorageInput) { in.Secret = "" }, FieldSecret, CodeRequired},
		{"unknown kind", func(in *StorageInput) { in.Kind = "ftp" }, FieldKind, CodeInvalid},
	}
	for _, tc := range cases {
		in := valid
		tc.edit(&in)
		if got := AsProblems(in.Normalize().Validate(true)).For(tc.field); got != tc.code {
			t.Errorf("%s: %s = %q, want %q", tc.name, tc.field, got, tc.code)
		}
	}

	// The share of an address pasted whole is the tasks' to choose.
	pasted := []struct{ host, wantHost, wantPort string }{
		{"smb://nas.local/photos/2024", "nas.local", ""},
		{`\\192.168.1.20\media`, "192.168.1.20", ""},
		{"SMB://nas:4455/backup", "nas", "4455"},
		{"fe80::1", "[fe80::1]", ""},
		{"[fe80::1]:445", "[fe80::1]", "445"},
	}
	for _, tc := range pasted {
		in := StorageInput{Kind: KindSMB, Host: tc.host}.Normalize()
		if in.Host != tc.wantHost || in.Port != tc.wantPort {
			t.Errorf("%q: host %q, port %q", tc.host, in.Host, in.Port)
		}
		if !validHost(in.Host) {
			t.Errorf("%q: host %q is not valid", tc.host, in.Host)
		}
	}

	spaced := StorageInput{Kind: KindSMB, Secret: " pass "}.Normalize()
	if spaced.Secret != " pass " {
		t.Errorf("password trimmed to %q", spaced.Secret)
	}
}

func TestTaskInputOnSMBServers(t *testing.T) {
	storages := []Storage{
		{ID: "a", Kind: KindSMB, Host: "NAS.local", Port: 445},
		{ID: "b", Kind: KindSMB, Host: "nas.local", Port: 445},
		{ID: "c", Kind: KindSMB, Host: "nas2.local", Port: 445},
	}
	in := TaskInput{
		Name: "NAS to NAS", Mode: ModeSync,
		Source:      Location{StorageID: "a", Path: "Photos/2024"},
		Destination: Location{StorageID: "b", Path: "photos/2024/copy"},
	}
	if got := AsProblems(in.Normalize().Validate(storages)).For(FieldDestination); got != CodeNested {
		t.Errorf("two storages of one server, names in other cases: %q, want nested", got)
	}
	in.Destination.StorageID = "c"
	if err := in.Normalize().Validate(storages); err != nil {
		t.Errorf("two NAS: %v", err)
	}
	in.Destination.Path = ""
	if got := AsProblems(in.Normalize().Validate(storages)).For(FieldDestination); got != CodeShareRequired {
		t.Errorf("the top of a server: %q, want a share required", got)
	}
}

func TestTaskInputValidate(t *testing.T) {
	storages := []Storage{{ID: "s1", Name: "R2"}}
	valid := TaskInput{
		Name:        "Photos",
		Source:      Location{Path: "/Users/me/Pictures"},
		Destination: Location{StorageID: "s1", Path: "pictures"},
		Mode:        ModeCopy,
		Schedule:    Schedule{Repeat: RepeatDaily, Hour: 3},
	}
	if err := valid.Validate(storages); err != nil {
		t.Fatalf("valid input: %v", err)
	}

	cases := []struct {
		name  string
		edit  func(*TaskInput)
		field Field
		code  Code
	}{
		{"relative local path", func(in *TaskInput) { in.Source.Path = "Pictures" }, FieldSource, CodeNotAbsolute},
		{"empty local path", func(in *TaskInput) { in.Source.Path = "" }, FieldSource, CodeRequired},
		{"unknown storage", func(in *TaskInput) { in.Destination.StorageID = "gone" }, FieldDestination, CodeUnknownStorage},
		{"same location", func(in *TaskInput) { in.Destination = in.Source }, FieldDestination, CodeSameLocation},
		{"nested folders", func(in *TaskInput) { in.Destination = Location{Path: "/Users/me/Pictures/backup"} }, FieldDestination, CodeNested},
		{"bad mode", func(in *TaskInput) { in.Mode = "move" }, FieldMode, CodeInvalid},
		{"unknown repeat", func(in *TaskInput) { in.Schedule.Repeat = "yearly" }, FieldSchedule, CodeInvalid},
		{"hour past the day", func(in *TaskInput) { in.Schedule.Hour = 24 }, FieldSchedule, CodeInvalid},
		{"weekly with no day", func(in *TaskInput) { in.Schedule.Repeat = RepeatWeekly }, FieldSchedule, CodeNoDays},
		{"hours that do not divide a day", func(in *TaskInput) {
			in.Schedule = Schedule{Repeat: RepeatHourly, Every: 5}
		}, FieldSchedule, CodeInvalid},
		{"day past a month", func(in *TaskInput) { in.Schedule = Schedule{Repeat: RepeatMonthly, Day: 32} }, FieldSchedule, CodeInvalid},
	}
	for _, tc := range cases {
		in := valid
		tc.edit(&in)
		if got := AsProblems(in.Normalize().Validate(storages)).For(tc.field); got != tc.code {
			t.Errorf("%s: %s = %q, want %q", tc.name, tc.field, got, tc.code)
		}
	}

	windows := valid
	windows.Source.Path = `C:\Users\me\Pictures`
	if err := windows.Normalize().Validate(storages); err != nil {
		t.Errorf("Windows path: %v", err)
	}

	sibling := valid
	sibling.Destination = Location{Path: "/Users/me/Pictures2"}
	if err := sibling.Normalize().Validate(storages); err != nil {
		t.Errorf("sibling folder with a common prefix is not nested: %v", err)
	}
}

func TestTaskInputNormalize(t *testing.T) {
	in := TaskInput{
		Name:        "  Photos ",
		Source:      Location{Path: " /Users/me/Pictures/ "},
		Destination: Location{StorageID: "s1", Path: "/pictures/"},
		Schedule:    Schedule{Repeat: RepeatDaily, Hour: 3, Days: WorkDays, Day: 9, Every: 2},
	}.Normalize()
	if in.Name != "Photos" || in.Source.Path != "/Users/me/Pictures" ||
		in.Destination.Path != "pictures" || in.Schedule != (Schedule{Repeat: RepeatDaily, Hour: 3}) {
		t.Errorf("normalized = %+v", in)
	}
}

func TestSyncDestinationRules(t *testing.T) {
	storages := []Storage{
		{ID: "s1", Name: "R2"},
		{ID: "n1", Name: "NAS", Kind: KindSMB, Host: "nas.local", Port: 445, Username: "me"},
	}
	// A source on a storage, so that a local destination's own folder is
	// the only check that can refuse it.
	in := TaskInput{
		Name:        "Backup",
		Source:      Location{StorageID: "s1", Path: "pictures"},
		Destination: Location{Path: "/Users/me/Backup"},
		Mode:        ModeSync,
	}
	if err := in.Normalize().Validate(storages); err != nil {
		t.Fatalf("valid sync: %v", err)
	}

	// A copy may write anywhere; a sync would empty the folders everything
	// else lives in.
	for _, path := range []string{"/", `C:\`, `\\server\share`} {
		for _, mode := range []Mode{ModeSync, ModeCopy} {
			bad := in
			bad.Mode, bad.Destination = mode, Location{Path: path}
			got := AsProblems(bad.Normalize().Validate(storages)).For(FieldDestination)
			want := Code("")
			if mode == ModeSync {
				want = CodeForbiddenDestination
			}
			if got != want {
				t.Errorf("%s destination %q with %s = %q, want %q", mode, path, mode, got, want)
			}
		}
	}

	// A ".." step names a folder other than the one it appears to.
	for _, l := range []Location{
		{Path: "/Users/me/Pictures/../Secret"},
		{StorageID: "n1", Path: "Documents/../../Photos"},
		{StorageID: "s1", Path: "photos/../secrets"},
	} {
		bad := in
		bad.Source = l
		if got := AsProblems(bad.Normalize().Validate(storages)).For(FieldSource); got != CodeInvalidPath {
			t.Errorf("source %+v = %q, want a path without steps", l, got)
		}
	}
}

func TestOverlap(t *testing.T) {
	storages := []Storage{
		{ID: "a", Kind: KindS3, Endpoint: "https://r2.example", Bucket: "b"},
		{ID: "b", Kind: KindS3, Endpoint: "https://R2.example", Bucket: "b"},
		{ID: "other", Kind: KindS3, Endpoint: "https://r2.example", Bucket: "other"},
		{ID: "nas", Kind: KindSMB, Host: "nas.local", Port: 445},
		{ID: "nas2", Kind: KindSMB, Host: "NAS.local", Port: 445},
		{ID: "smb5", Kind: KindSMB, Host: "nas.local", Port: 4455},
		{ID: "nfs", Kind: KindNFS, Host: "nas", Export: "/backup"},
		{ID: "nfs2049", Kind: KindNFS, Host: "nas", Export: "/backup", Port: 2049},
		{ID: "nfs111", Kind: KindNFS, Host: "nas", Export: "/backup", Port: 111},
	}
	cases := []struct {
		a, b Location
		want bool
	}{
		{Location{StorageID: "a", Path: "photos"}, Location{StorageID: "b", Path: "photos/2024"}, true},
		{Location{StorageID: "a", Path: "photos"}, Location{StorageID: "b", Path: "photo"}, false},
		{Location{StorageID: "a", Path: "photos"}, Location{StorageID: "other", Path: "photos"}, false},
		{Location{Path: "/Users/me/a"}, Location{Path: "/Users/me/a/b"}, true},
		{Location{Path: "/Users/me/a"}, Location{Path: "/Users/me/b"}, false},
		{Location{StorageID: "nas", Path: "Photos"}, Location{StorageID: "nas2", Path: "photos/2024"}, true},
		// S3 keys keep their case; two spellings are two prefixes.
		{Location{StorageID: "a", Path: "Photos"}, Location{StorageID: "a", Path: "photos"}, false},
		// Two ports of one server are two servers; a port nobody chose
		// matches the one it listens on.
		{Location{StorageID: "smb5", Path: "photos"}, Location{StorageID: "nas", Path: "photos"}, false},
		{Location{StorageID: "nfs", Path: "x"}, Location{StorageID: "nfs2049", Path: "x/y"}, true},
		{Location{StorageID: "nfs2049", Path: "x"}, Location{StorageID: "nfs111", Path: "x"}, false},
	}
	for _, tc := range cases {
		if got := Overlap(tc.a, tc.b, storages); got != tc.want {
			t.Errorf("Overlap(%+v, %+v) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestEndpointRejectsEmbeddedCredentials(t *testing.T) {
	for _, endpoint := range []string{"https://user:password@host", "https://host/?token=secret", "https://host/?", "https://host/#secret"} {
		in := StorageInput{Name: "S3", Kind: KindS3, Provider: ProviderAWS, Bucket: "b", AccessKeyID: "k", Endpoint: endpoint}
		if p := AsProblems(in.Validate(false)); p.For(FieldEndpoint) != CodeInvalidURL {
			t.Fatalf("endpoint %q accepted", endpoint)
		}
	}
}

func TestTaskTrash(t *testing.T) {
	settings := Settings{TrashDays: 30}
	for _, c := range []struct{ task, settings, want int }{
		{TrashFollow, 30, 30},
		{TrashFollow, 0, 0},
		{TrashOff, 30, 0},
		{7, 0, 7},
		{90, 30, 90},
	} {
		settings.TrashDays = c.settings
		if got := (Task{TrashDays: c.task}).Trash(settings); got != c.want {
			t.Errorf("task %d, settings %d: %d days, want %d", c.task, c.settings, got, c.want)
		}
	}
	in := TaskInput{Name: "T", Source: Location{Path: "/a"}, Destination: Location{Path: "/b"}, Mode: ModeCopy}
	for days, ok := range map[int]bool{TrashOff: true, TrashFollow: true, 7: true, -2: false, 4000: false} {
		in.TrashDays = days
		if got := AsProblems(in.Validate(nil)).For(FieldTrash); (got == "") != ok {
			t.Errorf("trash days %d: problem %q", days, got)
		}
	}
}

func TestExclude(t *testing.T) {
	e := Exclude{Patterns: []string{" *.tmp", "", "*.tmp", "node_modules/ "}}.Normalize()
	if len(e.Patterns) != 2 || e.Patterns[0] != "*.tmp" || e.Patterns[1] != "node_modules/" {
		t.Fatalf("normalized = %q", e.Patterns)
	}
	for _, c := range []struct {
		e    Exclude
		want Code
	}{
		{NewExclude(), ""},
		{Exclude{MaxSize: -1}, CodeOutOfRange},
		{Exclude{Patterns: []string{strings.Repeat("a", maxExcludeLength+1)}}, CodeTooLong},
		{Exclude{Patterns: make([]string, maxExcludePatterns+1)}, CodeOutOfRange},
	} {
		if got := c.e.Validate(); got != c.want {
			t.Errorf("Validate(%+v) = %q, want %q", c.e, got, c.want)
		}
	}
}

func TestAnNFSAddressIsPastedWhole(t *testing.T) {
	for _, c := range []struct {
		host, export         string
		wantHost, wantExport string
		wantPort             string
	}{
		{"nas.local", "/volume1/backup", "nas.local", "/volume1/backup", ""},
		{"nfs://nas.local/volume1/backup/", "", "nas.local", "/volume1/backup", ""},
		{"nfs://nas.local:2049/export", "", "nas.local", "/export", "2049"},
		{"nas.local:/volume1/backup", "", "nas.local", "/volume1/backup", ""},
		{"192.168.1.20:/mnt/pool", "", "192.168.1.20", "/mnt/pool", ""},
		{"nas.local", "volume1/backup", "nas.local", "/volume1/backup", ""},
		{"nas.local:/a", "/kept", "nas.local", "/kept", ""}, // a typed export wins
	} {
		in := StorageInput{Kind: KindNFS, Name: "N", Host: c.host, Export: c.export, UID: "501", GID: "20"}.Normalize()
		if in.Host != c.wantHost || in.Export != c.wantExport || in.Port != c.wantPort {
			t.Errorf("%q %q: host %q export %q port %q; want %q %q %q", c.host, c.export,
				in.Host, in.Export, in.Port, c.wantHost, c.wantExport, c.wantPort)
		}
		if err := in.Validate(true); err != nil {
			t.Errorf("%q: %v", c.host, err)
		}
	}
}

func TestAnNFSStorageNeedsAnExportAndIDs(t *testing.T) {
	base := StorageInput{Kind: KindNFS, Name: "N", Host: "nas.local", Export: "/backup", UID: "501", GID: "20"}
	if err := base.Normalize().Validate(true); err != nil {
		t.Fatalf("a whole input: %v", err)
	}
	if in := base.Normalize(); in.PortNumber() != 0 {
		t.Errorf("an empty NFS port = %d, want 0 for the portmapper", in.PortNumber())
	}
	for _, c := range []struct {
		edit  func(*StorageInput)
		field Field
		code  Code
	}{
		{func(in *StorageInput) { in.Export = "" }, FieldExport, CodeRequired},
		{func(in *StorageInput) { in.Export = "/a/../b" }, FieldExport, CodeInvalidPath},
		{func(in *StorageInput) { in.UID = "" }, FieldUID, CodeRequired},
		{func(in *StorageInput) { in.GID = "-1" }, FieldGID, CodeInvalidID},
		{func(in *StorageInput) { in.UID = "4294967296" }, FieldUID, CodeInvalidID},
		{func(in *StorageInput) { in.GID = "me" }, FieldGID, CodeInvalidID},
		{func(in *StorageInput) { in.Port = "70000" }, FieldPort, CodeInvalidPort},
		{func(in *StorageInput) { in.Host = "" }, FieldHost, CodeRequired},
	} {
		in := base
		c.edit(&in)
		if got := AsProblems(in.Normalize().Validate(true)).For(c.field); got != c.code {
			t.Errorf("%+v: %s = %q, want %q", in, c.field, got, c.code)
		}
	}
	// No password: whatever is typed is dropped.
	in := base
	in.Secret = "x"
	if in.Normalize().Secret != "" {
		t.Error("an NFS input kept a secret")
	}
	// The largest id is accepted where an int holds it; a 32-bit build
	// refuses it rather than truncating it.
	uid, gid, ok := (StorageInput{UID: "4294967295", GID: "0"}).IDs()
	if strconv.IntSize == 64 {
		if !ok || int64(uid) != MaxID || gid != 0 {
			t.Errorf("the largest uid = %d, %d, %v", uid, gid, ok)
		}
	} else if ok {
		t.Error("an id that does not fit in an int was accepted")
	}
}

// An NFS export keeps case, and two storages of one export overlap.
func TestNFSLocationsOverlapByExport(t *testing.T) {
	storages := []Storage{
		{ID: "a", Kind: KindNFS, Host: "nas", Export: "/backup"},
		{ID: "b", Kind: KindNFS, Host: "NAS", Export: "/backup"},
		{ID: "c", Kind: KindNFS, Host: "nas", Export: "/other"},
		{ID: "nested", Kind: KindNFS, Host: "NAS.", Export: "/backup//x"},
	}
	if !Overlap(Location{StorageID: "a", Path: "x"}, Location{StorageID: "b", Path: "x/y"}, storages) {
		t.Error("one export through two storages does not overlap")
	}
	if Overlap(Location{StorageID: "a", Path: "x"}, Location{StorageID: "c", Path: "x"}, storages) {
		t.Error("two exports overlap")
	}
	if Overlap(Location{StorageID: "a", Path: "Photos"}, Location{StorageID: "a", Path: "photos"}, storages) {
		t.Error("NFS paths compared without case")
	}
	source := Location{StorageID: "a", Path: "x"}
	destination := Location{StorageID: "nested"}
	if !Overlap(source, destination, storages) || !Overlap(destination, source, storages) {
		t.Fatal("the parent export and nested export name the same folder")
	}
	in := TaskInput{Name: "backup", Source: source, Destination: destination, Mode: ModeSync}
	if got := AsProblems(in.Validate(storages)).For(FieldDestination); got != CodeSameLocation {
		t.Fatalf("same folder via two exports = %v", got)
	}
	if p := checkLocation(nil, FieldDestination, Location{StorageID: "a"}, storages); len(p) != 0 {
		t.Errorf("the top of an export: %v", p)
	}
}

// A whitespace-only Export must not shadow the export pasted into Host, and
// an Export is cleaned before it is compared or stored.
func TestAnNFSExportIsTrimmedBeforeTheAddressIsSplit(t *testing.T) {
	for _, c := range []struct{ host, export string }{
		{"nas.local:/volume1/backup", "  "},
		{"nfs://nas.local/volume1/backup", "\t"},
		{"nas.local", "  volume1/backup  "},
	} {
		in := StorageInput{Kind: KindNFS, Name: "N", Host: c.host, Export: c.export, UID: "501", GID: "20"}.Normalize()
		if in.Host != "nas.local" || in.Export != "/volume1/backup" {
			t.Errorf("host %q, export %q: host %q, export %q", c.host, c.export, in.Host, in.Export)
		}
		if err := in.Validate(true); err != nil {
			t.Errorf("host %q, export %q: %v", c.host, c.export, err)
		}
	}
}

// An export is a path of the server: a control character in it would split
// the line of a log or a message that carries it.
func TestAnNFSExportRejectsControlCharacters(t *testing.T) {
	base := StorageInput{Kind: KindNFS, Name: "N", Host: "nas.local", UID: "501", GID: "20"}
	for _, export := range []string{"/a\x00b", "/a\rb", "/a\nb"} {
		in := base
		in.Export = export
		if got := AsProblems(in.Normalize().Validate(true)).For(FieldExport); got != CodeInvalidPath {
			t.Errorf("export %q = %q, want %q", export, got, CodeInvalidPath)
		}
	}
}

// Two spellings of one NFS place name the same place when their canonical
// hosts match and their ports agree, a port of 0 matching any.
func TestNFSPlacesMatchByCanonicalHostAndPort(t *testing.T) {
	storages := []Storage{
		{ID: "a", Kind: KindNFS, Host: "NAS.local.", Export: "/backup"},
		{ID: "a2049", Kind: KindNFS, Host: "nas.local", Export: "/backup", Port: 2049},
		{ID: "a111", Kind: KindNFS, Host: "nas.local", Export: "/backup", Port: 111},
		{ID: "v6", Kind: KindNFS, Host: "[fe80::1]", Export: "/backup"},
		{ID: "v6full", Kind: KindNFS, Host: "[fe80:0:0:0:0:0:0:1]", Export: "/backup"},
		{ID: "child", Kind: KindNFS, Host: "nas.local.", Export: "/backup/photos"},
	}
	for _, c := range []struct {
		name string
		a, b Location
		want bool
	}{
		{"the portmapper and the port", Location{StorageID: "a", Path: "x"}, Location{StorageID: "a2049", Path: "x/y"}, true},
		{"two ports", Location{StorageID: "a2049", Path: "x"}, Location{StorageID: "a111", Path: "x"}, false},
		{"two spellings of an address", Location{StorageID: "v6", Path: "x"}, Location{StorageID: "v6full", Path: "x/y"}, true},
		{"an export and one below it", Location{StorageID: "a", Path: "photos"}, Location{StorageID: "child"}, true},
	} {
		if got := Overlap(c.a, c.b, storages); got != c.want {
			t.Errorf("%s: Overlap = %v, want %v", c.name, got, c.want)
		}
		if got := Overlap(c.b, c.a, storages); got != c.want {
			t.Errorf("%s, the other way: Overlap = %v, want %v", c.name, got, c.want)
		}
	}

	// The same export through the portmapper and through its port is one
	// place: a sync between them would delete what it copies.
	in := TaskInput{Name: "backup", Mode: ModeSync,
		Source:      Location{StorageID: "a", Path: "photos"},
		Destination: Location{StorageID: "a2049", Path: "photos"},
	}
	if got := AsProblems(in.Validate(storages)).For(FieldDestination); got != CodeSameLocation {
		t.Errorf("one place through the portmapper and its port = %q, want same location", got)
	}
	in.Destination = Location{StorageID: "a111", Path: "photos"}
	in.Source.StorageID = "a2049" // both ports chosen, and different
	if err := in.Validate(storages); err != nil {
		t.Errorf("another port is another server: %v", err)
	}
}
