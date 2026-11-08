package service

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/internal/engine"
	"github.com/starriv/cloudshuttle/internal/store"
)

type memSecrets struct {
	mu sync.Mutex
	m  map[string]string
}

func (s *memSecrets) Get(key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[key]
	if !ok {
		return "", domain.ErrMissingSecret
	}
	return v, nil
}

func (s *memSecrets) Set(key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = value
	return nil
}

func (s *memSecrets) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, key)
	return nil
}

// restarted is a service on the same database, as the app has once it
// starts again.
func (s *Service) restarted(t *testing.T, secrets Secrets, done chan domain.Run) *Service {
	t.Helper()
	again := New(Options{Store: s.store, Secrets: secrets, OnRunFinished: func(r domain.Run) { done <- r }})
	if err := again.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(again.Close)
	return again
}

func newService(t *testing.T) (*Service, *memSecrets, chan domain.Run) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	secrets := &memSecrets{m: map[string]string{}}
	done := make(chan domain.Run, 4)
	s := New(Options{Store: st, Secrets: secrets, OnRunFinished: func(r domain.Run) { done <- r }})
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.Close()
		st.Close()
	})
	return s, secrets, done
}

func wait(t *testing.T, done chan domain.Run) domain.Run {
	t.Helper()
	select {
	case r := <-done:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("run did not finish")
		return domain.Run{}
	}
}

func TestStorageSecretsLiveInTheCredentialStore(t *testing.T) {
	ctx := context.Background()
	s, secrets, _ := newService(t)
	in := domain.StorageInput{Name: "R2", Provider: domain.ProviderCloudflare,
		Endpoint: "https://acct.r2.cloudflarestorage.com", Bucket: "b", AccessKeyID: "k", Secret: "s3cret"}

	st, err := s.CreateStorage(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := secrets.Get(st.SecretKey()); got != "s3cret" {
		t.Fatalf("secret = %q", got)
	}

	in.Secret, in.Bucket = "", "other"
	if _, err := s.UpdateStorage(ctx, st.ID, in); err != nil {
		t.Fatal(err)
	}
	if got, _ := secrets.Get(st.SecretKey()); got != "s3cret" {
		t.Fatalf("an empty secret replaced the kept one: %q", got)
	}

	if _, err := s.CreateStorage(ctx, domain.StorageInput{Name: "r2", Provider: domain.ProviderAWS,
		Bucket: "b", AccessKeyID: "k", Secret: "x"}); !errors.Is(err, domain.ErrNameTaken) {
		t.Fatalf("duplicate name: %v", err)
	}

	if err := s.DeleteStorage(ctx, st.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := secrets.Get(st.SecretKey()); !errors.Is(err, domain.ErrMissingSecret) {
		t.Fatal("deleting the storage kept its secret")
	}
}

func TestRunRecordsHistory(t *testing.T) {
	ctx := context.Background()
	s, _, done := newService(t)
	src, dst := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}

	task, err := s.CreateTask(ctx, domain.TaskInput{AllowRoot: true, Name: "Local", Source: domain.Location{Path: src},
		Destination: domain.Location{Path: dst}, Mode: domain.ModeCopy})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Run(ctx, task.ID, RunOptions{Trigger: domain.TriggerManual}); err != nil {
		t.Fatal(err)
	}
	run := wait(t, done)
	if run.Status != domain.StatusSuccess || run.Stats.Transfers != 1 {
		t.Fatalf("run = %+v", run)
	}
	if _, err := os.Stat(filepath.Join(dst, "a.txt")); err != nil {
		t.Fatal("file not copied")
	}
	if len(s.Active()) != 0 {
		t.Fatal("finished run still active")
	}

	runs, err := s.Runs(ctx, store.RunFilter{TaskID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Status != domain.StatusSuccess || runs[0].Stats.Bytes != 5 {
		t.Fatalf("history = %+v", runs)
	}
}

func TestRunFailsWithoutSecret(t *testing.T) {
	ctx := context.Background()
	s, secrets, done := newService(t)
	st, err := s.CreateStorage(ctx, domain.StorageInput{Name: "R2", Provider: domain.ProviderCloudflare,
		Endpoint: "https://acct.r2.cloudflarestorage.com", Bucket: "b", AccessKeyID: "k", Secret: "x"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.CreateTask(ctx, domain.TaskInput{AllowRoot: true, Name: "Up", Source: domain.Location{Path: t.TempDir()},
		Destination: domain.Location{StorageID: st.ID, Path: "x"}, Mode: domain.ModeCopy})
	if err != nil {
		t.Fatal(err)
	}
	// The secret goes from the credential store, and the app starts again:
	// the service of before kept it in memory.
	_ = secrets.Delete(st.SecretKey())
	s = s.restarted(t, secrets, done)
	if err := s.DeleteStorage(ctx, st.ID); !errors.Is(err, domain.ErrStorageInUse) {
		t.Fatalf("deleting a storage in use: %v", err)
	}
	if _, err := s.Run(ctx, task.ID, RunOptions{Trigger: domain.TriggerManual}); err != nil {
		t.Fatal(err)
	}
	run := wait(t, done)
	if run.Status != domain.StatusFailed || !strings.Contains(run.Message, domain.ErrMissingSecret.Error()) {
		t.Fatalf("run = %+v, want failed for the missing secret", run)
	}
	log, _, err := s.RunLog(ctx, run.ID)
	if err != nil || !strings.Contains(log, "ERROR  app     the destination: R2: "+domain.ErrMissingSecret.Error()) ||
		!strings.Contains(log, "finished: failed") {
		t.Errorf("log = %q, %v", log, err)
	}
}

func TestRunLog(t *testing.T) {
	ctx := context.Background()
	s, _, done := newService(t)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}
	task, err := s.CreateTask(ctx, domain.TaskInput{AllowRoot: true, Name: "Local", Source: domain.Location{Path: src},
		Destination: domain.Location{Path: t.TempDir()}, Mode: domain.ModeSync})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Run(ctx, task.ID, RunOptions{Trigger: domain.TriggerManual}); err != nil {
		t.Fatal(err)
	}
	run := wait(t, done)
	log, _, err := s.RunLog(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`task "Local": sync, manual`, "INFO   rclone  a.txt: Copied (", "finished: success"} {
		if !strings.Contains(log, want) {
			t.Errorf("no %q in the log:\n%s", want, log)
		}
	}
	if _, _, err := s.RunLog(ctx, "before-logs"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("a run with no log: %v", err)
	}
}

func TestSMBStorage(t *testing.T) {
	ctx := context.Background()
	s, secrets, _ := newService(t)
	in := domain.StorageInput{Name: "NAS", Kind: domain.KindSMB, Host: "smb://nas.local/photos", Username: "guest"}

	st, err := s.CreateStorage(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if st.Host != "nas.local" || st.Port != domain.DefaultSMBPort || st.Bucket != "" {
		t.Fatalf("storage = %+v", st)
	}
	if _, err := secrets.Get(st.SecretKey()); !errors.Is(err, domain.ErrMissingSecret) {
		t.Fatal("a share open to guests kept a password")
	}

	in.Username, in.Secret = "me", "pass word "
	if st, err = s.UpdateStorage(ctx, st.ID, in); err != nil {
		t.Fatal(err)
	}
	if got, _ := secrets.Get("storage/" + st.ID + "/password"); got != "pass word " {
		t.Fatalf("password = %q", got)
	}

	// A bucket needs its own secret, and drops the share's password.
	bucket := domain.StorageInput{Name: "NAS", Kind: domain.KindS3, Provider: domain.ProviderAWS, Bucket: "b", AccessKeyID: "k"}
	if _, err := s.UpdateStorage(ctx, st.ID, bucket); domain.AsProblems(err).For(domain.FieldSecret) != domain.CodeRequired {
		t.Fatalf("changing to a bucket without a secret: %v", err)
	}
	bucket.Secret = "s3cret"
	if st, err = s.UpdateStorage(ctx, st.ID, bucket); err != nil {
		t.Fatal(err)
	}
	if st.Host != "" || st.Kind != domain.KindS3 {
		t.Fatalf("storage = %+v", st)
	}
	if _, err := secrets.Get("storage/" + st.ID + "/password"); !errors.Is(err, domain.ErrMissingSecret) {
		t.Fatal("the share's password stayed")
	}
}

func TestTestConnectionUnreachable(t *testing.T) {
	s, _, _ := newService(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
	l.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	in := domain.StorageInput{Name: "NAS", Kind: domain.KindSMB, Host: "127.0.0.1", Port: port, Username: "me"}
	if err := s.TestConnection(ctx, "", in); domain.AsProblems(err).For(domain.FieldSecret) != domain.CodeRequired {
		t.Fatalf("test without a password = %v, want it required", err)
	}
	in.Secret = "pw"
	if err := s.TestConnection(ctx, "", in); !errors.Is(err, domain.ErrUnreachable) {
		t.Fatalf("test = %v, want ErrUnreachable", err)
	}
	st, err := s.CreateStorage(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Folders(ctx, st.ID, ""); !errors.Is(err, domain.ErrUnreachable) {
		t.Fatalf("shares = %v, want ErrUnreachable", err)
	}
}

func TestTaskOnASMBServerNamesAShare(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newService(t)
	st, err := s.CreateStorage(ctx, domain.StorageInput{Name: "NAS", Kind: domain.KindSMB, Host: "nas.local", Username: "guest"})
	if err != nil {
		t.Fatal(err)
	}
	in := domain.TaskInput{AllowRoot: true, Name: "Photos", Mode: domain.ModeCopy,
		Source: domain.Location{Path: "/Users/me/Pictures"}, Destination: domain.Location{StorageID: st.ID}}
	if _, err := s.CreateTask(ctx, in); domain.AsProblems(err).For(domain.FieldDestination) != domain.CodeShareRequired {
		t.Fatalf("a task at the top of the server: %v, want a share required", err)
	}
	in.Destination.Path = "Photos/2024"
	if _, err := s.CreateTask(ctx, in); err != nil {
		t.Fatal(err)
	}
}

// countingSecrets counts how often the store is read.
type countingSecrets struct {
	memSecrets
	gets int
}

func (c *countingSecrets) Get(key string) (string, error) {
	c.mu.Lock()
	c.gets++
	c.mu.Unlock()
	return c.memSecrets.Get(key)
}

func TestSecretsReadOnce(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newService(t)
	st, err := s.CreateStorage(ctx, domain.StorageInput{Name: "NAS", Kind: domain.KindSMB, Host: "nas.local", Username: "me", Secret: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTask(ctx, domain.TaskInput{AllowRoot: true, Name: "Nightly", Mode: domain.ModeCopy, Schedule: domain.Schedule{Repeat: domain.RepeatDaily, Hour: 3},
		Source: domain.Location{Path: "/Users/me/Pictures"}, Destination: domain.Location{StorageID: st.ID, Path: "Photos"}}); err != nil {
		t.Fatal(err)
	}

	// Starting again, the app reads the secret of the scheduled task's
	// storage at once, and then never again.
	store := &countingSecrets{memSecrets: memSecrets{m: map[string]string{st.SecretKey(): "pw"}}}
	again := s.restarted(t, store, make(chan domain.Run, 1))
	deadline := time.Now().Add(5 * time.Second)
	for {
		store.mu.Lock()
		gets := store.gets
		store.mu.Unlock()
		if gets == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("read %d times as the app started, want 1", gets)
		}
		time.Sleep(10 * time.Millisecond)
	}
	for range 3 {
		if got, err := again.secret(st); err != nil || got != "pw" {
			t.Fatalf("secret = %q, %v", got, err)
		}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.gets != 1 {
		t.Errorf("read the store %d times, want once", store.gets)
	}
}

func TestCheckStorage(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newService(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
	l.Close()
	in := domain.StorageInput{Name: "NAS", Kind: domain.KindSMB, Host: "127.0.0.1", Port: port, Username: "me", Secret: "pw"}
	st, err := s.CreateStorage(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if st.Check.Checked() {
		t.Fatal("a new storage counts as tested")
	}

	check, err := s.CheckStorage(ctx, st.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !check.Checked() || check.OK || check.Problem != domain.ProblemUnreachable || check.Message == "" {
		t.Fatalf("check = %+v", check)
	}
	saved, _ := s.store.Storage(ctx, st.ID)
	if saved.Check.Problem != domain.ProblemUnreachable || !saved.Check.At.Equal(check.At.Truncate(time.Millisecond)) {
		t.Fatalf("recorded = %+v", saved.Check)
	}

	// Renamed, it keeps its check; with another host, it has none.
	in.Name, in.Secret = "Living room NAS", ""
	if st, err = s.UpdateStorage(ctx, st.ID, in); err != nil || !st.Check.Checked() {
		t.Fatalf("renamed: %+v, %v", st.Check, err)
	}
	in.Host = "127.0.0.2"
	if st, err = s.UpdateStorage(ctx, st.ID, in); err != nil || st.Check.Checked() {
		t.Fatalf("moved: %+v, %v", st.Check, err)
	}
	in.Host = "127.0.0.1"
	if _, err := s.UpdateStorage(ctx, st.ID, in); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CheckStorage(ctx, st.ID); err != nil {
		t.Fatal(err)
	}
	in.Secret = "new password"
	if st, err = s.UpdateStorage(ctx, st.ID, in); err != nil || st.Check.Checked() {
		t.Fatalf("a new password: %+v, %v", st.Check, err)
	}
}

func TestSyncDestinationsProtected(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newService(t)
	data := t.TempDir()
	s.dataDir = data
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home folder")
	}
	// A storage as the source: a local source under a temp folder could
	// already be nested in the destination, which hides the check.
	storage, err := s.CreateStorage(ctx, domain.StorageInput{Name: "R2", Provider: domain.ProviderCloudflare,
		Endpoint: "https://r2.example", Bucket: "b", AccessKeyID: "k", Secret: "s"})
	if err != nil {
		t.Fatal(err)
	}
	src := domain.Location{StorageID: storage.ID, Path: "photos"}
	mk := func(dest domain.Location) error {
		_, err := s.CreateTask(ctx, domain.TaskInput{AllowRoot: true, Name: "Backup", Mode: domain.ModeSync,
			Source: src, Destination: dest})
		return err
	}
	for _, dest := range []string{home, data, filepath.Dir(data), filepath.Join(data, "logs")} {
		if got := domain.AsProblems(mk(domain.Location{Path: dest})).For(domain.FieldDestination); got != domain.CodeForbiddenDestination {
			t.Errorf("sync to %q = %q, want it forbidden", dest, got)
		}
	}
	// A copy deletes nothing, so the home folder is a destination it may
	// use; the app's own data is not, either way.
	if _, err := s.CreateTask(ctx, domain.TaskInput{AllowRoot: true, Name: "Copy", Mode: domain.ModeCopy,
		Source: src, Destination: domain.Location{Path: home}}); err != nil {
		t.Errorf("a copy into the home folder: %v", err)
	}
	if _, err := s.CreateTask(ctx, domain.TaskInput{AllowRoot: true, Name: "Copy data", Mode: domain.ModeCopy,
		Source: src, Destination: domain.Location{Path: data}}); domain.AsProblems(err).For(domain.FieldDestination) != domain.CodeForbiddenDestination {
		t.Errorf("a copy into the data folder: %v, want it forbidden", err)
	}
}

func TestOneDestinationTwoTasks(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newService(t)
	src := t.TempDir()
	dst := t.TempDir()
	sub := filepath.Join(dst, "2024")
	if _, err := s.CreateTask(ctx, domain.TaskInput{AllowRoot: true, Name: "A", Mode: domain.ModeSync,
		Source: domain.Location{Path: src}, Destination: domain.Location{Path: dst}}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		mode domain.Mode
		path string
		want domain.Code
	}{
		{"B", domain.ModeSync, dst, domain.CodeSharedDestination},
		{"C", domain.ModeCopy, dst, domain.CodeSharedDestination},
		{"D", domain.ModeSync, sub, domain.CodeSharedDestination},
		{"E", domain.ModeCopy, t.TempDir(), domain.Code("")},
	} {
		got := domain.AsProblems((func() error {
			_, err := s.CreateTask(ctx, domain.TaskInput{AllowRoot: true, Name: c.name, Mode: c.mode,
				Source: domain.Location{Path: src}, Destination: domain.Location{Path: c.path}})
			return err
		})()).For(domain.FieldDestination)
		if got != c.want {
			t.Errorf("task %s to %q = %q, want %q", c.name, c.path, got, c.want)
		}
	}
}

func TestSyncRunStopsOnAnEmptySource(t *testing.T) {
	ctx := context.Background()
	s, _, done := newService(t)
	src, dst := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(dst, "precious.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	task, err := s.CreateTask(ctx, domain.TaskInput{AllowRoot: true, Name: "Nightly", Mode: domain.ModeSync,
		Source: domain.Location{Path: src}, Destination: domain.Location{Path: dst}})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.Run(ctx, task.ID, RunOptions{Trigger: domain.TriggerScheduled}); err != nil {
		t.Fatal(err)
	}
	run := wait(t, done)
	if run.Status != domain.StatusFailed || !strings.Contains(run.Message, "source is empty") {
		t.Fatalf("run = %+v, want a failure for the empty source", run)
	}
	if run.Problem != domain.RunProblemEmptySource {
		t.Errorf("problem = %q, want %q", run.Problem, domain.RunProblemEmptySource)
	}
	if runs, err := s.Runs(ctx, store.RunFilter{}); err != nil || len(runs) != 1 ||
		runs[0].Problem != domain.RunProblemEmptySource {
		t.Errorf("history = %+v, %v; want the problem kept", runs, err)
	}
	if _, err := os.Stat(filepath.Join(dst, "precious.txt")); err != nil {
		t.Fatal("the destination was emptied")
	}
	log, _, err := s.RunLog(ctx, run.ID)
	if err != nil || !strings.Contains(log, "the preview would delete 1") {
		t.Errorf("log = %q, %v", log, err)
	}
	// What it would have deleted is kept, for the person to look over.
	if d, err := s.RunDeletes(ctx, run.ID); err != nil || d.Total != 1 || len(d.Paths) != 1 || d.Paths[0] != "precious.txt" {
		t.Errorf("deletes = %+v, %v", d, err)
	}

	// A person who means it forces the run.
	if _, err := s.Run(ctx, task.ID, RunOptions{Trigger: domain.TriggerManual, Force: true}); err != nil {
		t.Fatal(err)
	}
	forced := wait(t, done)
	if forced.Status != domain.StatusSuccess {
		t.Fatalf("forced run = %+v", forced)
	}
	if _, err := os.Stat(filepath.Join(dst, "precious.txt")); !os.IsNotExist(err) {
		t.Fatal("the forced run kept the destination's file")
	}
	// The trash keeps it, and the history says where.
	if forced.Stats.Trashed != 1 || forced.Trash != engine.TrashFolder(forced.StartedAt) {
		t.Fatalf("forced run trashed %d into %q", forced.Stats.Trashed, forced.Trash)
	}
	if _, err := os.Stat(filepath.Join(dst, filepath.FromSlash(forced.Trash), "precious.txt")); err != nil {
		t.Fatalf("the trash lacks the file: %v", err)
	}
	if _, err := s.RunDeletes(ctx, forced.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("a run that went ahead lists deletes: %v", err)
	}
}

func TestSyncWithoutTheTrashDeletes(t *testing.T) {
	ctx := context.Background()
	s, _, done := newService(t)
	settings := s.Settings()
	settings.TrashDays = 0
	if _, err := s.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	src, dst := t.TempDir(), t.TempDir()
	for _, dir := range []string{src, dst} {
		if err := os.WriteFile(filepath.Join(dir, "kept.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dst, "gone.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	task, err := s.CreateTask(ctx, domain.TaskInput{AllowRoot: true, Name: "No trash", Mode: domain.ModeSync,
		Source: domain.Location{Path: src}, Destination: domain.Location{Path: dst}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Run(ctx, task.ID, RunOptions{Trigger: domain.TriggerManual}); err != nil {
		t.Fatal(err)
	}
	run := wait(t, done)
	if run.Status != domain.StatusSuccess || run.Stats.Deletes != 1 || run.Stats.Trashed != 0 || run.Trash != "" {
		t.Fatalf("run = %+v", run)
	}
	if _, err := os.Stat(filepath.Join(dst, engine.TrashRoot)); !os.IsNotExist(err) {
		t.Fatal("a sync without the trash made one")
	}
}

// failingSecrets fails to keep a secret, as a locked keychain does.
type failingSecrets struct {
	memSecrets
	fail bool
}

func (s *failingSecrets) Set(key, value string) error {
	if s.fail {
		return errors.New("keychain locked")
	}
	return s.memSecrets.Set(key, value)
}

func TestAStorageKeepsItsSecretWhenTheNewOneCannotBeKept(t *testing.T) {
	ctx := context.Background()
	secrets := &failingSecrets{memSecrets: memSecrets{m: map[string]string{}}}
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := New(Options{Store: st, Secrets: secrets})
	t.Cleanup(s.Close)
	if err := s.Start(ctx); err != nil {
		t.Fatal(err)
	}
	in := domain.StorageInput{Name: "NAS", Kind: domain.KindSMB, Host: "nas.local", Username: "me", Secret: "old"}
	storage, err := s.CreateStorage(ctx, in)
	if err != nil {
		t.Fatal(err)
	}

	secrets.fail = true
	in.Secret = "new"
	if _, err := s.UpdateStorage(ctx, storage.ID, in); err == nil {
		t.Fatal("saving a secret that cannot be kept passed")
	}
	if got, _ := secrets.Get(storage.SecretKey()); got != "old" {
		t.Fatalf("secret = %q, want the one kept before", got)
	}
}

func TestOneDestinationTwoRunningTasks(t *testing.T) {
	ctx := context.Background()
	s, _, done := newService(t)
	src, dst := t.TempDir(), t.TempDir()
	a, err := s.CreateTask(ctx, domain.TaskInput{AllowRoot: true, Name: "A", Mode: domain.ModeSync,
		Source: domain.Location{Path: src}, Destination: domain.Location{Path: dst}})
	if err != nil {
		t.Fatal(err)
	}
	// A task saved before the check on shared destinations existed, which
	// the form would refuse today.
	b := domain.Task{ID: domain.NewID(), Name: "B", Mode: domain.ModeCopy,
		Source: domain.Location{Path: t.TempDir()}, Destination: domain.Location{Path: dst},
		CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := s.store.PutTask(ctx, b); err != nil {
		t.Fatal(err)
	}

	// As if A were running.
	s.mu.Lock()
	s.active[a.ID] = &activeRun{
		Active: Active{Run: domain.Run{ID: "a-run", TaskID: a.ID}},
		dest:   a.Destination, mode: a.Mode, log: &runLog{},
	}
	s.mu.Unlock()

	if _, err := s.Run(ctx, b.ID, RunOptions{}); !errors.Is(err, domain.ErrDestinationBusy) {
		t.Fatalf("run = %v, want ErrDestinationBusy", err)
	}
	// A dry run changes nothing, so it may go on beside it.
	if _, err := s.Run(ctx, b.ID, RunOptions{DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if run := wait(t, done); run.Status != domain.StatusSuccess {
		t.Fatalf("dry run = %+v", run)
	}
}

func TestRunsTakeTurns(t *testing.T) {
	ctx := context.Background()
	s, _, done := newService(t)
	release := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once
	old := beforeRun
	beforeRun = func() {
		once.Do(func() { close(started) })
		<-release
	}
	t.Cleanup(func() { beforeRun = old })

	mk := func(name string) domain.Task {
		task, err := s.CreateTask(ctx, domain.TaskInput{AllowRoot: true, Name: name, Mode: domain.ModeCopy,
			Source: domain.Location{Path: t.TempDir()}, Destination: domain.Location{Path: t.TempDir()}})
		if err != nil {
			t.Fatal(err)
		}
		return task
	}
	a, b := mk("A"), mk("B")
	if _, err := s.Run(ctx, a.ID, RunOptions{}); err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := s.Run(ctx, b.ID, RunOptions{}); err != nil {
		t.Fatal(err)
	}

	active := s.Active()
	if len(active) != 2 || active[0].Run.TaskID != a.ID || active[0].Queued ||
		active[1].Run.TaskID != b.ID || !active[1].Queued {
		t.Fatalf("active = %+v, want A transferring and B waiting", active)
	}

	close(release)
	for range 2 {
		if run := wait(t, done); run.Status != domain.StatusSuccess {
			t.Fatalf("run = %+v", run)
		}
	}
	if active := s.Active(); len(active) != 0 {
		t.Fatalf("still active: %+v", active)
	}
}

func TestAPanicEndsTheRun(t *testing.T) {
	ctx := context.Background()
	s, _, done := newService(t)
	old := beforeRun
	beforeRun = func() { panic("boom") }
	t.Cleanup(func() { beforeRun = old })

	task, err := s.CreateTask(ctx, domain.TaskInput{AllowRoot: true, Name: "Crash", Mode: domain.ModeCopy,
		Source: domain.Location{Path: t.TempDir()}, Destination: domain.Location{Path: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Run(ctx, task.ID, RunOptions{}); err != nil {
		t.Fatal(err)
	}
	run := wait(t, done)
	if run.Status != domain.StatusFailed || !strings.Contains(run.Message, "stopped unexpectedly") {
		t.Fatalf("run = %+v, want a failed run", run)
	}
	if active := s.Active(); len(active) != 0 {
		t.Fatalf("still active after a panic: %+v", active)
	}
	log, _, err := s.RunLog(ctx, run.ID)
	if err != nil || !strings.Contains(log, "crashed: boom") {
		t.Errorf("log = %q, %v", log, err)
	}
}

func TestSettingsAreKept(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newService(t)
	if got := s.Settings(); !reflect.DeepEqual(got, domain.DefaultSettings()) {
		t.Fatalf("settings = %+v, want the defaults", got)
	}
	want := domain.DefaultSettings()
	want.Transfers, want.Checkers, want.Retries, want.BandwidthMBps = 2, 4, 5, 10
	want.NotifyFailed, want.NotifySkipped, want.RetentionDays = false, false, 30
	want.ExcludeEnabled = false
	want.GlobalExcludes = []string{"*.log", "node_modules/"}
	if _, err := s.SaveSettings(ctx, want); err != nil {
		t.Fatal(err)
	}
	if got := s.Settings(); !reflect.DeepEqual(got, want) {
		t.Fatalf("settings = %+v, want %+v", got, want)
	}

	// A value the app does not offer changes nothing.
	bad := want
	bad.Transfers = 0
	if _, err := s.SaveSettings(ctx, bad); domain.AsProblems(err).For(domain.FieldSettings) != domain.CodeOutOfRange {
		t.Fatalf("saving %+v = %v, want a problem with the value", bad, err)
	}
	if got := s.Settings(); !reflect.DeepEqual(got, want) {
		t.Fatalf("settings = %+v after a refused save, want %+v", got, want)
	}
}

// Where the app shows its icon is kept at once, by itself, as the
// appearance is: the settings page may hold changes not saved yet.
func TestTheAppIconIsKeptByItself(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newService(t)
	if err := s.SetAppIcon(ctx, domain.AppIconDock); err != nil {
		t.Fatal(err)
	}
	got := s.Settings()
	if got.AppIcon != domain.AppIconDock {
		t.Fatalf("app icon = %v, want the Dock", got.AppIcon)
	}
	if got.Transfers != domain.DefaultSettings().Transfers {
		t.Fatalf("transfers = %d, changed with the icon", got.Transfers)
	}
	// A value the app does not offer changes nothing.
	if err := s.SetAppIcon(ctx, domain.AppIconMenuBar+1); domain.AsProblems(err).For(domain.FieldSettings) != domain.CodeOutOfRange {
		t.Fatalf("an icon the app does not offer = %v, want a problem", err)
	}
	if got := s.Settings().AppIcon; got != domain.AppIconDock {
		t.Fatalf("app icon = %v after a refused save, want the Dock", got)
	}
}

func TestATaskChoosesItsTrash(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name           string
		mode           domain.Mode
		settings, task int
		trashed, kept  bool // the replaced file went to the trash; the run's folder is named
	}{
		{"follows the settings", domain.ModeCopy, 30, domain.TrashFollow, true, true},
		{"off for the task", domain.ModeSync, 30, domain.TrashOff, false, false},
		{"its own days", domain.ModeSync, 0, 7, true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, _, done := newService(t)
			settings := s.Settings()
			settings.TrashDays = c.settings
			if _, err := s.SaveSettings(ctx, settings); err != nil {
				t.Fatal(err)
			}
			src, dst := t.TempDir(), t.TempDir()
			if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("new version"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dst, "a.txt"), []byte("old"), 0o644); err != nil {
				t.Fatal(err)
			}
			task, err := s.CreateTask(ctx, domain.TaskInput{AllowRoot: true, Name: "T", Mode: c.mode, TrashDays: c.task,
				Source: domain.Location{Path: src}, Destination: domain.Location{Path: dst}})
			if err != nil {
				t.Fatal(err)
			}
			if got, _ := s.store.Task(ctx, task.ID); got.TrashDays != c.task {
				t.Fatalf("kept trash days %d, want %d", got.TrashDays, c.task)
			}
			if _, err := s.Run(ctx, task.ID, RunOptions{Trigger: domain.TriggerManual}); err != nil {
				t.Fatal(err)
			}
			run := wait(t, done)
			if run.Status != domain.StatusSuccess || (run.Stats.Trashed == 1) != c.trashed || (run.Trash != "") != c.kept {
				t.Fatalf("run = %+v", run)
			}
			_, err = os.Stat(filepath.Join(dst, engine.TrashRoot))
			if exists := err == nil; exists != c.trashed {
				t.Fatalf("trash exists = %v, want %v", exists, c.trashed)
			}
		})
	}
	s, _, _ := newService(t)
	in := domain.TaskInput{AllowRoot: true, Name: "Bad", Mode: domain.ModeCopy, TrashDays: -5,
		Source: domain.Location{Path: t.TempDir()}, Destination: domain.Location{Path: t.TempDir()}}
	if _, err := s.CreateTask(ctx, in); domain.AsProblems(err).For(domain.FieldTrash) != domain.CodeOutOfRange {
		t.Fatalf("bad trash days: %v", err)
	}
}

func TestATaskLeavesOutWhatItExcludes(t *testing.T) {
	ctx := context.Background()
	s, _, done := newService(t)
	src, dst := t.TempDir(), t.TempDir()
	for name, body := range map[string]string{"a.txt": "a", ".DS_Store": "x", "big.bin": "0123456789"} {
		if err := os.WriteFile(filepath.Join(src, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	in := domain.TaskInput{AllowRoot: true, Name: "T", Mode: domain.ModeCopy,
		Source: domain.Location{Path: src}, Destination: domain.Location{Path: dst},
		Exclude: domain.Exclude{Patterns: []string{" .DS_Store ", "", ".DS_Store"}, MaxSize: 5}}
	task, err := s.CreateTask(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.store.Task(ctx, task.ID); !slices.Equal(got.Exclude.Patterns, []string{".DS_Store"}) || got.Exclude.MaxSize != 5 {
		t.Fatalf("kept %+v", got.Exclude)
	}
	if _, err := s.Run(ctx, task.ID, RunOptions{Trigger: domain.TriggerManual}); err != nil {
		t.Fatal(err)
	}
	if run := wait(t, done); run.Status != domain.StatusSuccess || run.Stats.Transfers != 1 {
		t.Fatalf("run = %+v", run)
	}
	for name, want := range map[string]bool{"a.txt": true, ".DS_Store": false, "big.bin": false} {
		if _, err := os.Stat(filepath.Join(dst, name)); (err == nil) != want {
			t.Errorf("%s copied = %v, want %v", name, err == nil, want)
		}
	}

	in.Name, in.Exclude = "Bad", domain.Exclude{Patterns: []string{"*.log", "[abc"}}
	if _, err := s.CreateTask(ctx, in); domain.AsProblems(err).For(domain.FieldExclude) != domain.CodeInvalid {
		t.Fatalf("a pattern that is no glob: %v", err)
	}
	if s.ValidExclude("[abc") || !s.ValidExclude("*.log") {
		t.Error("ValidExclude")
	}
}

// Every run leaves out the folders development tools fill with dependencies
// while the setting is on, which it is by default; turning it off copies
// them on the next run.
func TestTheSettingsLeaveOutDevelopmentDependencies(t *testing.T) {
	ctx := context.Background()
	s, _, done := newService(t)
	src, dst := t.TempDir(), t.TempDir()
	for name, body := range map[string]string{
		"notes.txt":                     "keep",
		"app/node_modules/lib/index.js": "download",
		"app/target/debug/app":          "build",
		"app/.venv/bin/python":          "environment",
	} {
		path := filepath.Join(src, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	task, err := s.CreateTask(ctx, domain.TaskInput{AllowRoot: true, Name: "Projects", Mode: domain.ModeCopy,
		Source: domain.Location{Path: src}, Destination: domain.Location{Path: dst}})
	if err != nil {
		t.Fatal(err)
	}
	if !s.Settings().ExcludeEnabled {
		t.Fatal("the global exclusion is off in a new database")
	}
	if _, err := s.Run(ctx, task.ID, RunOptions{Trigger: domain.TriggerManual}); err != nil {
		t.Fatal(err)
	}
	if run := wait(t, done); run.Status != domain.StatusSuccess || run.Stats.Transfers != 1 {
		t.Fatalf("run = %+v, want only the note copied", run)
	}
	for name, want := range map[string]bool{
		"notes.txt": true, "app/node_modules/lib/index.js": false,
		"app/target/debug/app": false, "app/.venv/bin/python": false,
	} {
		if _, err := os.Stat(filepath.Join(dst, filepath.FromSlash(name))); (err == nil) != want {
			t.Errorf("%s copied = %v, want %v", name, err == nil, want)
		}
	}

	settings := s.Settings()
	settings.ExcludeEnabled = false
	if _, err := s.SaveSettings(ctx, settings); err != nil {
		t.Fatalf("save the setting: %v", err)
	}
	if _, err := s.Run(ctx, task.ID, RunOptions{Trigger: domain.TriggerManual}); err != nil {
		t.Fatal(err)
	}
	if run := wait(t, done); run.Status != domain.StatusSuccess || run.Stats.Transfers != 3 {
		t.Fatalf("run = %+v, want the dependency files copied", run)
	}
	for _, name := range []string{"app/node_modules/lib/index.js", "app/target/debug/app", "app/.venv/bin/python"} {
		if _, err := os.Stat(filepath.Join(dst, filepath.FromSlash(name))); err != nil {
			t.Errorf("%s was not copied with the setting off: %v", name, err)
		}
	}
}

// The global patterns are the person's: an edited list changes what a run
// copies, and a pattern that is not a glob is refused.
func TestTheSettingsGlobalExcludesAreEditable(t *testing.T) {
	ctx := context.Background()
	s, _, done := newService(t)
	src, dst := t.TempDir(), t.TempDir()
	for name, body := range map[string]string{
		"notes.txt":                     "keep",
		"app/node_modules/lib/index.js": "download",
		"debug.log":                     "log",
	} {
		path := filepath.Join(src, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	settings := s.Settings()
	settings.GlobalExcludes = []string{"*.log"}
	if _, err := s.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	task, err := s.CreateTask(ctx, domain.TaskInput{AllowRoot: true, Name: "Projects", Mode: domain.ModeCopy,
		Source: domain.Location{Path: src}, Destination: domain.Location{Path: dst}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Run(ctx, task.ID, RunOptions{Trigger: domain.TriggerManual}); err != nil {
		t.Fatal(err)
	}
	if run := wait(t, done); run.Status != domain.StatusSuccess || run.Stats.Transfers != 2 {
		t.Fatalf("run = %+v, want the note and node_modules copied", run)
	}
	if _, err := os.Stat(filepath.Join(dst, "app", "node_modules", "lib", "index.js")); err != nil {
		t.Errorf("node_modules was not copied after the list lost it: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "debug.log")); err == nil {
		t.Error("debug.log was copied, but the edited list leaves it out")
	}

	// Spaces and repeats are cleaned, and the result is what runs use.
	settings.GlobalExcludes = []string{" *.log ", "", "*.log", "node_modules/"}
	saved, err := s.SaveSettings(ctx, settings)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"*.log", "node_modules/"}; !slices.Equal(saved.GlobalExcludes, want) {
		t.Fatalf("saved global excludes = %v, want %v", saved.GlobalExcludes, want)
	}

	// A pattern rclone cannot take is refused.
	settings.GlobalExcludes = []string{"*.log", "[bad"}
	if _, err := s.SaveSettings(ctx, settings); domain.AsProblems(err).For(domain.FieldGlobalExclude) != domain.CodeInvalid {
		t.Fatalf("a pattern that is no glob: %v", err)
	}
	if got := s.Settings().GlobalExcludes; !slices.Equal(got, []string{"*.log", "node_modules/"}) {
		t.Fatalf("the refused save changed the list: %v", got)
	}
}

// The engine builds a filter from the built-in patterns: one that is not a
// glob would make every run fail.
func TestDevDependencyExcludesAreGlobs(t *testing.T) {
	for _, p := range domain.DevDependencyExcludes {
		if !engine.ValidExclude(p) {
			t.Errorf("DevDependencyExcludes has %q, which rclone does not take as a glob", p)
		}
	}
}

// The global rules apply to a sync's delete pass too: the folders left out
// of the destination are not deleted, and turning the setting off lets the
// next sync delete them as the source lacks them.
func TestTheGlobalExclusionsApplyToSync(t *testing.T) {
	ctx := context.Background()
	s, _, done := newService(t)
	src, dst := t.TempDir(), t.TempDir()
	for name, body := range map[string]string{
		"keep.txt":                  "keep",
		"node_modules/lib/index.js": "download",
		"target/debug/app":          "build",
		"old.txt":                   "old",
	} {
		path := filepath.Join(dst, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(src, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	task, err := s.CreateTask(ctx, domain.TaskInput{AllowRoot: true, Name: "Projects", Mode: domain.ModeSync,
		Source: domain.Location{Path: src}, Destination: domain.Location{Path: dst}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Run(ctx, task.ID, RunOptions{Trigger: domain.TriggerManual}); err != nil {
		t.Fatal(err)
	}
	if run := wait(t, done); run.Status != domain.StatusSuccess {
		t.Fatalf("run = %+v", run)
	}
	for _, kept := range []string{"node_modules/lib/index.js", "target/debug/app"} {
		if _, err := os.Stat(filepath.Join(dst, filepath.FromSlash(kept))); err != nil {
			t.Errorf("%s was deleted by the sync: %v", kept, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dst, "old.txt")); err == nil {
		t.Error("old.txt was not deleted: it is not excluded")
	}

	settings := s.Settings()
	settings.ExcludeEnabled = false
	if _, err := s.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Run(ctx, task.ID, RunOptions{Trigger: domain.TriggerManual}); err != nil {
		t.Fatal(err)
	}
	if run := wait(t, done); run.Status != domain.StatusSuccess {
		t.Fatalf("run with the setting off = %+v", run)
	}
	for _, gone := range []string{"node_modules", "target"} {
		if _, err := os.Stat(filepath.Join(dst, gone)); err == nil {
			t.Errorf("%s stayed after the setting was turned off", gone)
		}
	}
}

// A scheduled task that has gone too long without a real success is told
// about once a day; manual tasks and tasks that are succeeding are not.
func TestStaleScheduledTasksAreTold(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	var told []struct {
		task domain.Task
		last time.Time
		days int
	}
	s := New(Options{Store: st, Secrets: &memSecrets{m: map[string]string{}},
		Now: func() time.Time { return now },
		OnStale: func(task domain.Task, last time.Time, days int) {
			told = append(told, struct {
				task domain.Task
				last time.Time
				days int
			}{task, last, days})
		}})
	tell := func() map[string]bool {
		told = nil
		s.tellStale(ctx)
		out := map[string]bool{}
		for _, v := range told {
			out[v.task.ID] = true
			if v.days != 7 {
				t.Errorf("%s: days = %d, want 7", v.task.ID, v.days)
			}
		}
		return out
	}
	task := func(id string, schedule domain.Schedule, since time.Time) domain.Task {
		return domain.Task{ID: id, Name: id, Mode: domain.ModeCopy,
			Source: domain.Location{Path: "/src"}, Destination: domain.Location{Path: "/dst"},
			Schedule: schedule, ScheduleSince: since, CreatedAt: since.AddDate(0, 0, -30)}
	}
	daily := domain.Schedule{Repeat: domain.RepeatDaily, Hour: 3}
	stale := task("stale", daily, now.AddDate(0, 0, -10))
	fresh := task("fresh", daily, now.AddDate(0, 0, -10))
	dry := task("dry", daily, now.AddDate(0, 0, -10))
	manual := task("manual", domain.Schedule{}, now.AddDate(0, 0, -30))
	for _, task := range []domain.Task{stale, fresh, dry, manual} {
		if err := st.PutTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.InsertRun(ctx, domain.Run{ID: "fresh-ok", TaskID: "fresh", Mode: domain.ModeCopy,
		Status: domain.StatusSuccess, StartedAt: now.Add(-24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertRun(ctx, domain.Run{ID: "dry-ok", TaskID: "dry", Mode: domain.ModeCopy,
		Status: domain.StatusSuccess, DryRun: true, StartedAt: now.Add(-24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}

	got := tell()
	if !got["stale"] || !got["dry"] || got["fresh"] || got["manual"] {
		t.Fatalf("told = %v, want stale and dry only", got)
	}
	if len(told) != 2 {
		t.Fatalf("told %d tasks, want 2", len(told))
	}
	for _, v := range told {
		if v.task.ID == "stale" && !v.last.IsZero() {
			t.Errorf("stale last success = %v, want zero", v.last)
		}
	}
	// The same evening, nothing is repeated.
	if got := tell(); len(got) != 0 {
		t.Fatalf("told again the same day: %v", got)
	}
	// The next day, the still-stale tasks are told again; the fresh one is
	// still recent enough.
	now = now.Add(25 * time.Hour)
	got = tell()
	if !got["stale"] || !got["dry"] || got["fresh"] || got["manual"] {
		t.Fatalf("next day told = %v, want stale and dry only", got)
	}
}

// A longer threshold or the switch off keeps the notice quiet.
func TestStaleNotifyRespectsItsSettings(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	task := domain.Task{ID: "t", Name: "T", Mode: domain.ModeCopy,
		Source: domain.Location{Path: "/src"}, Destination: domain.Location{Path: "/dst"},
		Schedule:      domain.Schedule{Repeat: domain.RepeatDaily, Hour: 3},
		ScheduleSince: now.AddDate(0, 0, -10), CreatedAt: now.AddDate(0, 0, -30)}
	if err := st.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	told := 0
	makeService := func(notify bool, days int) *Service {
		settings := domain.DefaultSettings()
		settings.NotifyStale, settings.StaleDays = notify, days
		if err := st.PutSettings(ctx, settings); err != nil {
			t.Fatal(err)
		}
		svc := New(Options{Store: st, Secrets: &memSecrets{m: map[string]string{}},
			Now: func() time.Time { return now },
			OnStale: func(domain.Task, time.Time, int) {
				told++
			}})
		svc.tellStale(ctx)
		return svc
	}
	// 10 days without success is inside a 14-day threshold.
	makeService(true, 14)
	if told != 0 {
		t.Fatalf("told with a 14-day threshold: %d", told)
	}
	// Off.
	makeService(false, 7)
	if told != 0 {
		t.Fatalf("told with the switch off: %d", told)
	}
	// And on at the default 7 days, it tells once.
	makeService(true, 7)
	if told != 1 {
		t.Fatalf("told %d times with the switch on, want once", told)
	}
}

// Turning stale notices on checks at once, without waiting for the periodic
// check.
func TestStaleNotifyChecksWhenTurnedOn(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	task := domain.Task{ID: "t", Name: "T", Mode: domain.ModeCopy,
		Source: domain.Location{Path: "/src"}, Destination: domain.Location{Path: "/dst"},
		Schedule:      domain.Schedule{Repeat: domain.RepeatDaily, Hour: 3},
		ScheduleSince: now.AddDate(0, 0, -10), CreatedAt: now.AddDate(0, 0, -30)}
	if err := st.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	settings := domain.DefaultSettings()
	settings.NotifyStale = false
	if err := st.PutSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	told := make(chan domain.Task, 1)
	s := New(Options{Store: st, Secrets: &memSecrets{m: map[string]string{}},
		Now: func() time.Time { return now },
		OnStale: func(task domain.Task, _ time.Time, _ int) {
			told <- task
		}})
	settings.NotifyStale = true
	if _, err := s.SaveSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-told:
		if got.ID != task.ID {
			t.Fatalf("told %q, want %q", got.ID, task.ID)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no notice after turning stale notices on")
	}
}

// A task that is running is not stale; once it ends, it is told.
func TestStaleNotifySkipsRunningTasks(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	task := domain.Task{ID: "t", Name: "T", Mode: domain.ModeCopy,
		Source: domain.Location{Path: "/src"}, Destination: domain.Location{Path: "/dst"},
		Schedule:      domain.Schedule{Repeat: domain.RepeatDaily, Hour: 3},
		ScheduleSince: now.AddDate(0, 0, -10), CreatedAt: now.AddDate(0, 0, -30)}
	if err := st.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	told := make(chan domain.Task, 1)
	s := New(Options{Store: st, Secrets: &memSecrets{m: map[string]string{}},
		Now: func() time.Time { return now },
		OnStale: func(task domain.Task, _ time.Time, _ int) {
			told <- task
		}})
	s.mu.Lock()
	s.active[task.ID] = &activeRun{}
	s.mu.Unlock()
	s.tellStale(ctx)
	select {
	case got := <-told:
		t.Fatalf("told about running task %q", got.ID)
	case <-time.After(100 * time.Millisecond):
	}
	s.mu.Lock()
	delete(s.active, task.ID)
	s.mu.Unlock()
	s.tellStale(ctx)
	select {
	case got := <-told:
		if got.ID != task.ID {
			t.Fatalf("told %q, want %q", got.ID, task.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("no notice after the task stopped running")
	}
}

// The scheduler leaves a stale notice for the window and wakes the app;
// the window shows it, and neither process repeats it within the day.
func TestSchedulerLeavesStaleNoticeForTheWindow(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	task := domain.Task{ID: "t", Name: "T", Mode: domain.ModeCopy,
		Source: domain.Location{Path: "/src"}, Destination: domain.Location{Path: "/dst"},
		Schedule:      domain.Schedule{Repeat: domain.RepeatDaily, Hour: 3},
		ScheduleSince: now.AddDate(0, 0, -10), CreatedAt: now.AddDate(0, 0, -30)}
	if err := st.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	var woken, windowTold int
	scheduler := New(Options{Store: st, Secrets: &memSecrets{m: map[string]string{}},
		Owner: OwnerScheduler, Now: func() time.Time { return now },
		WakeApp: func() { woken++ },
		OnStale: func(domain.Task, time.Time, int) {
			t.Error("the scheduler told stale itself")
		}})
	scheduler.tellStale(ctx)
	if woken != 1 {
		t.Fatalf("woke the app %d times, want once", woken)
	}
	notices, err := st.StaleNotices(ctx)
	if err != nil || !notices["t"].Pending {
		t.Fatalf("notices = %+v, %v; want a pending notice", notices, err)
	}

	window := New(Options{Store: st, Secrets: &memSecrets{m: map[string]string{}},
		Now: func() time.Time { return now },
		OnStale: func(task domain.Task, _ time.Time, _ int) {
			if task.ID != "t" {
				t.Errorf("told %q", task.ID)
			}
			windowTold++
		}})
	window.tellStale(ctx)
	if windowTold != 1 {
		t.Fatalf("the window told %d times, want once", windowTold)
	}
	notices, _ = st.StaleNotices(ctx)
	if notices["t"].Pending {
		t.Error("the notice is still pending after the window told it")
	}
	window.tellStale(ctx)
	scheduler.tellStale(ctx)
	if windowTold != 1 || woken != 1 {
		t.Fatalf("repeated: window %d, wake %d", windowTold, woken)
	}
}

// The notice is kept across a restart, so the same day does not repeat it;
// a success clears it.
func TestStaleNoticeSurvivesARestart(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	task := domain.Task{ID: "t", Name: "T", Mode: domain.ModeCopy,
		Source: domain.Location{Path: "/src"}, Destination: domain.Location{Path: "/dst"},
		Schedule:      domain.Schedule{Repeat: domain.RepeatDaily, Hour: 3},
		ScheduleSince: now.AddDate(0, 0, -10), CreatedAt: now.AddDate(0, 0, -30)}
	if err := st.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	told := 0
	newWindow := func(at time.Time) *Service {
		return New(Options{Store: st, Secrets: &memSecrets{m: map[string]string{}},
			Now: func() time.Time { return at },
			OnStale: func(domain.Task, time.Time, int) {
				told++
			}})
	}
	newWindow(now).tellStale(ctx)
	if told != 1 {
		t.Fatalf("told %d times, want once", told)
	}
	newWindow(now.Add(time.Hour)).tellStale(ctx)
	if told != 1 {
		t.Fatalf("told again after a restart: %d", told)
	}
	newWindow(now.Add(25 * time.Hour)).tellStale(ctx)
	if told != 2 {
		t.Fatalf("did not tell the next day: %d", told)
	}
	// A new success clears the notice and is the new baseline.
	if err := st.MarkTaskSuccess(ctx, "t", now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := st.ClearStaleNotice(ctx, "t"); err != nil {
		t.Fatal(err)
	}
	newWindow(now.Add(25 * time.Hour)).tellStale(ctx)
	if told != 2 {
		t.Fatalf("told after a success: %d", told)
	}
}

// A success still in the kept history wins over an older LastSuccessAt, as
// after restoring an older configuration on a machine that kept its runs.
func TestStaleUsesANewerKeptSuccess(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	task := domain.Task{ID: "t", Name: "T", Mode: domain.ModeCopy,
		Source: domain.Location{Path: "/src"}, Destination: domain.Location{Path: "/dst"},
		Schedule:      domain.Schedule{Repeat: domain.RepeatDaily, Hour: 3},
		ScheduleSince: now.AddDate(0, 0, -30), LastSuccessAt: now.AddDate(0, 0, -10),
		CreatedAt: now.AddDate(0, 0, -40)}
	if err := st.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertRun(ctx, domain.Run{ID: "new", TaskID: "t", Mode: domain.ModeCopy,
		Status: domain.StatusSuccess, StartedAt: now.Add(-24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	told := 0
	s := New(Options{Store: st, Secrets: &memSecrets{m: map[string]string{}},
		Now: func() time.Time { return now },
		OnStale: func(domain.Task, time.Time, int) {
			told++
		}})
	s.tellStale(ctx)
	if told != 0 {
		t.Fatalf("told although a success a day ago is still kept: %d", told)
	}
}

// A real success records its time and clears the stale notice.
func TestASuccessClearsTheStaleNotice(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	task := domain.Task{ID: "t", Name: "T", Mode: domain.ModeCopy,
		Source: domain.Location{Path: "/src"}, Destination: domain.Location{Path: "/dst"},
		CreatedAt: now, UpdatedAt: now}
	if err := st.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := st.PutStaleNotice(ctx, task.ID, now, true); err != nil {
		t.Fatal(err)
	}
	run := domain.Run{ID: "r", TaskID: task.ID, TaskName: task.Name,
		Mode: domain.ModeCopy, Status: domain.StatusSuccess, StartedAt: now}
	if err := st.InsertRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	s := New(Options{Store: st, Secrets: &memSecrets{m: map[string]string{}},
		Now: func() time.Time { return now }})
	s.recordRun(ctx, &activeRun{log: &runLog{}, done: make(chan struct{})}, run, s.Settings())
	got, _ := st.Task(ctx, task.ID)
	if !got.LastSuccessAt.Equal(now) {
		t.Fatalf("last success = %v, want %v", got.LastSuccessAt, now)
	}
	if notices, _ := st.StaleNotices(ctx); len(notices) != 0 {
		t.Fatalf("stale notice stayed: %+v", notices)
	}
}

// RunDue checks for stale tasks after its runs, leaves a pending notice for
// the window, and wakes the app when no window is running.
func TestRunDueLeavesAStaleNotice(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	task := domain.Task{ID: "t", Name: "T", Mode: domain.ModeCopy,
		Source: domain.Location{Path: "/src"}, Destination: domain.Location{Path: "/dst"},
		Schedule:      domain.Schedule{Repeat: domain.RepeatDaily, Hour: 3},
		ScheduleSince: now.AddDate(0, 0, -10), CreatedAt: now.AddDate(0, 0, -30)}
	if err := st.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	// It started a run now, so it is not due; it has not succeeded for ten
	// days, so it is stale once that run is over.
	run := domain.Run{ID: "old", TaskID: task.ID, TaskName: task.Name, Mode: domain.ModeCopy,
		Status: domain.StatusRunning, StartedAt: now}
	if err := st.StartRun(ctx, run, OwnerApp, false, time.Time{}); err != nil {
		t.Fatal(err)
	}
	run.Status, run.FinishedAt = domain.StatusCanceled, now
	if err := st.FinishRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	woken := 0
	s := New(Options{Store: st, Secrets: &memSecrets{m: map[string]string{}},
		Owner: OwnerScheduler, Now: func() time.Time { return now },
		WakeApp: func() { woken++ },
		OnStale: func(domain.Task, time.Time, int) {
			t.Error("the scheduler told stale itself")
		}})
	if err := s.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	if woken != 1 {
		t.Fatalf("woke %d times, want once", woken)
	}
	notices, _ := st.StaleNotices(ctx)
	if !notices[task.ID].Pending {
		t.Fatalf("notices = %+v, want a pending notice", notices)
	}
}
