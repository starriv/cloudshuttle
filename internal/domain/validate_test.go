package domain

import "testing"

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
