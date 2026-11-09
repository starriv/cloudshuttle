package view

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/starriv/cloudshuttle/internal/discovery"
	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/internal/format"
	"github.com/starriv/cloudshuttle/internal/service"
	"github.com/starriv/cloudshuttle/internal/store"
	"github.com/starriv/cloudshuttle/locales"
)

type memSecrets struct {
	mu sync.Mutex
	m  map[string]string
}

func (s *memSecrets) Get(key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.m[key]; ok {
		return v, nil
	}
	return "", domain.ErrMissingSecret
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

// testHost queues updates from other goroutines for the test to apply on
// its own, as the window applies them on the main thread.
type testHost struct {
	updates  chan func()
	folder   string
	settings int // how many times the background settings were opened
	shown    []domain.Appearance
	icons    []domain.AppIcon
}

func (h *testHost) Update(fn func())                  { h.updates <- fn }
func (h *testHost) ChooseFolder() (string, error)     { return h.folder, nil }
func (h *testHost) OpenBackgroundSettings()           { h.settings++ }
func (h *testHost) ChooseFile(string) (string, error) { return h.folder, nil }
func (h *testHost) SetAppearance(a domain.Appearance) { h.shown = append(h.shown, a) }
func (h *testHost) SetAppIcon(icon domain.AppIcon)    { h.icons = append(h.icons, icon) }

type harness struct {
	t    *testing.T
	app  *App
	host *testHost
	tt   *ui.Tester
	svc  *service.Service
	db   *store.Store
	// found is what searching the network finds.
	found []discovery.Server
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessWith(t, service.Options{})
}

// newHarnessWith starts the service, with opts, as the window does.
func newHarnessWith(t *testing.T, opts service.Options) *harness {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, host: &testHost{updates: make(chan func(), 64)}, db: db}
	opts.Store = db
	if opts.Secrets == nil {
		opts.Secrets = &memSecrets{m: map[string]string{}}
	}
	opts.OnChange = func() { h.host.Update(func() { h.app.Reload() }) }
	opts.OnProgress = func() { h.host.Update(func() { h.app.ReloadProgress() }) }
	opts.Browse = func(context.Context) ([]discovery.Server, error) { return h.found, nil }
	h.svc = service.New(opts)
	if opts.Registrar != nil {
		if err := h.svc.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	h.app = New(h.svc, h.host)
	h.tt = ui.NewTester(h.app.View, 1080, 700)
	h.tt.SetLocale("en")
	t.Cleanup(func() {
		h.svc.Close()
		db.Close()
	})
	return h
}

// settle applies the updates queued so far, and draws a frame.
func (h *harness) settle() {
	for {
		select {
		case fn := <-h.host.updates:
			fn()
		default:
			h.tt.Frame()
			return
		}
	}
}

// waitFor applies updates until cond holds.
func (h *harness) waitFor(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.After(10 * time.Second)
	for !cond() {
		select {
		case fn := <-h.host.updates:
			fn()
		case <-deadline:
			h.t.Fatalf("timed out waiting for %s; texts %q", what, h.tt.Texts())
		}
	}
	h.tt.Frame()
}

func (h *harness) click(label string) {
	h.t.Helper()
	// A form scrolls: a control farther down than the sheet shows is
	// brought into view the way a person brings it, by scrolling.
	h.reveal(label)
	err := h.tt.Click(label)
	for i := 0; i < 8 && err != nil && strings.Contains(err.Error(), "outside the window"); i++ {
		h.tt.Scroll(650, 420, 0, 220)
		h.tt.Frame()
		err = h.tt.Click(label)
	}
	if err != nil {
		h.t.Fatalf("click %q: %v; texts %q", label, err, h.tt.Texts())
	}
	if label == locales.TaskSave() {
		h.waitFor("task save", func() bool { return !h.app.taskForm.saving })
	}
}

// reveal scrolls the sheet until the element showing label is laid out in
// view: an element below or above the scrolling body has no size to click.
func (h *harness) reveal(label string) {
	for i := 0; i < 24; i++ {
		r, ok := h.tt.Find(label)
		if !ok || (r.W > 0 && r.H > 0) {
			return
		}
		if r.Y+r.H/2 < 300 {
			h.tt.Scroll(650, 420, 0, -220)
		} else {
			h.tt.Scroll(650, 420, 0, 220)
		}
		h.tt.Frame()
	}
}

func (h *harness) fill(label, text string) {
	h.t.Helper()
	h.click(label)
	h.tt.Key(ui.Cmd, ui.KeyA) // replace what is there, such as a default
	h.tt.Type(text)
}

func (h *harness) expect(text string) {
	h.t.Helper()
	if !h.tt.HasText(text) {
		h.t.Fatalf("no %q in %q", text, h.tt.Texts())
	}
}

func TestEmptyStatesSayWhatToDo(t *testing.T) {
	h := newHarness(t)
	h.expect(locales.WelcomeTitle())
	h.click(locales.NavTasks())
	h.expect(locales.TasksEmpty())
	h.click(locales.NavStorages())
	h.expect(locales.StoragesEmpty())
	h.click(locales.NavHistory())
	h.expect(locales.HistoryEmpty())
}

func TestAddStorageShowsProblemsThenSaves(t *testing.T) {
	h := newHarness(t)
	h.click(locales.NavStorages())
	h.click(locales.StoragesAdd())
	h.click(locales.StorageSave())
	h.expect(locales.ErrorRequired())

	h.fill(locales.StorageFieldName(), "Archive")
	h.fill(locales.StorageFieldBucket(), "archive")
	h.fill(locales.StorageFieldAccessKeyId(), "AKIA")
	h.fill(locales.StorageFieldSecret(), "secret")
	h.click(locales.StorageSave())
	h.waitFor("storage save", func() bool { return !h.app.storageForm.saving })
	h.settle()

	h.expect("Archive")
	h.expect(locales.ProviderAWS())
	if h.app.storageForm.open {
		t.Fatal("the dialog stayed open after saving")
	}
}

func TestAddAndRunLocalTask(t *testing.T) {
	h := newHarness(t)
	src, dst := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}

	h.click(locales.NavTasks())
	h.click(locales.TasksAdd())
	h.fill(locales.TaskFieldName(), "Mirror")
	h.host.folder = src
	h.click(locales.TaskChooseFolder())
	h.waitFor("the chosen folder", func() bool { return h.app.taskForm.source.path == src })
	h.app.taskForm.dest.path = dst // the second folder field, typed
	h.click(locales.TaskSave())
	h.settle()
	h.expect("Mirror")
	h.expect(locales.TasksNever())

	h.click(locales.TaskRun())
	h.waitFor("the run to finish", func() bool {
		return len(h.app.active) == 0 && len(h.app.lastRuns) == 1 && h.app.recent[0].Status != domain.StatusRunning
	})
	if _, err := os.Stat(filepath.Join(h.app.tasks[0].Destination.Path, "a.txt")); err != nil {
		t.Fatal("the run did not copy the file")
	}
	h.expect(locales.StatusSuccess())

	h.click(locales.NavHistory())
	h.expect("Mirror")
	h.expect(locales.Transferred(1, format.Bytes(5)))
}

func TestTaskFormRejectsNestedFolders(t *testing.T) {
	h := newHarness(t)
	h.click(locales.NavTasks())
	h.click(locales.TasksAdd())
	h.fill(locales.TaskFieldName(), "Loop")
	h.app.taskForm.allowRoot = true
	h.app.taskForm.source.path = "/Users/me/Pictures"
	h.app.taskForm.dest.path = "/Users/me/Pictures/backup"
	h.click(locales.TaskSave())
	h.expect(locales.ErrorNested())
}

func TestFormat(t *testing.T) {
	if got := uniqueLabels([]string{"A", "B", "A"}); got[2] != "A (2)" {
		t.Errorf("uniqueLabels = %q", got)
	}
}

func TestAddSMBServer(t *testing.T) {
	h := newHarness(t)
	h.click(locales.NavStorages())
	h.click(locales.StoragesAdd())
	h.click(locales.KindSMB())
	h.fill(locales.StorageFieldName(), "NAS")
	h.app.storageForm.in.Host = "smb://nas.local:4455/photos" // the first of the row's two fields, typed
	h.click(locales.StorageSave())
	h.expect(locales.ErrorRequired()) // the user name

	h.fill(locales.StorageFieldUsername(), "me")
	h.fill(locales.StorageFieldPassword(), "pw")
	h.click(locales.StorageSave())
	h.waitFor("storage save", func() bool { return !h.app.storageForm.saving })
	h.settle()
	if h.app.storageForm.open {
		t.Fatalf("the dialog stayed open; problems %v", h.app.storageForm.problems)
	}
	h.expect("NAS")
	h.expect(locales.KindSMB())
	h.expect("nas.local:4455") // the share pasted along is the tasks' to choose

	// Editing opens on the server's fields, with its port.
	h.click(locales.StorageActions("NAS"))
	if err := h.tt.ChooseMenuItem(locales.StorageEdit()); err != nil {
		t.Fatalf("menu %q: %v", h.tt.Menu(), err)
	}
	h.settle()
	if f := h.app.storageForm; domain.Kinds[f.kind] != domain.KindSMB || f.in.Port != "4455" || f.in.Host != "nas.local" {
		t.Fatalf("edit form = %+v", f)
	}
}

func TestTestConnectionExplainsUnreachable(t *testing.T) {
	h := newHarness(t)
	h.click(locales.NavStorages())
	h.click(locales.StoragesAdd())
	h.click(locales.KindSMB())
	h.fill(locales.StorageFieldName(), "NAS")
	h.app.storageForm.in.Host, h.app.storageForm.in.Port = "127.0.0.1", "1" // nothing listens there
	h.fill(locales.StorageFieldUsername(), "me")
	h.fill(locales.StorageFieldPassword(), "pw")
	h.click(locales.StorageTest())
	h.waitFor("the test", func() bool { return !h.app.storageForm.testing })
	h.expect(locales.ErrorUnreachable())
}

func TestFindServersFillsTheForm(t *testing.T) {
	h := newHarness(t)
	h.found = []discovery.Server{{
		Name: "DiskStation", HostName: "ds.local", Host: "ds.local",
		Addr: netip.MustParseAddr("192.168.1.20"), Port: 4455,
	}}
	h.click(locales.NavStorages())
	h.click(locales.StoragesAdd())
	h.click(locales.KindSMB())
	if h.app.storageForm.searching || h.app.storageForm.searched {
		t.Fatal("switching storage kind started discovery")
	}
	h.click(locales.StorageFindServers())
	h.waitFor("the search", func() bool { return h.app.storageForm.searched })
	h.expect(locales.StorageServersUnverified())
	h.click("DiskStation")
	if f := h.app.storageForm; f.in.Host != "ds.local" || f.in.Port != "4455" || f.in.Name != "DiskStation" {
		t.Fatalf("form = %+v", f.in)
	}

	h.found = nil
	h.click(locales.StorageFindAgain())
	h.waitFor("the second search", func() bool { return !h.app.storageForm.searching })
	h.expect(locales.StorageServersNone())
}

func TestAPlaintextEndpointIsWarned(t *testing.T) {
	h := newHarness(t)
	h.click(locales.NavStorages())
	h.click(locales.StoragesAdd())
	h.fill(locales.StorageFieldEndpoint(), "http://192.168.1.5:9000")
	h.settle()
	h.expect(locales.StoragePlaintextWarning())
}

func TestTestConnectionAsksForThePassword(t *testing.T) {
	h := newHarness(t)
	h.click(locales.NavStorages())
	h.click(locales.StoragesAdd())
	h.click(locales.KindSMB())
	h.fill(locales.StorageFieldName(), "NAS")
	h.app.storageForm.in.Host = "nas.local"
	h.fill(locales.StorageFieldUsername(), "me")
	h.click(locales.StorageTest())
	if h.app.storageForm.testing || h.app.storageForm.problems.For(domain.FieldSecret) != domain.CodeRequired {
		t.Fatalf("tested the connection without a password; problems %v", h.app.storageForm.problems)
	}
	h.settle()
	h.expect(locales.ErrorRequired())
}

// TestTaskAtTheTopOfASMBServer saves a task whose side is an SMB server
// with no share chosen: the top of a server holds shares, not files.
func TestTaskAtTheTopOfASMBServer(t *testing.T) {
	h := newHarness(t)
	if _, err := h.svc.CreateStorage(context.Background(), domain.StorageInput{
		Name: "NAS", Kind: domain.KindSMB, Host: "nas.local", Username: "guest",
	}); err != nil {
		t.Fatal(err)
	}
	h.settle()
	h.click(locales.NavTasks())
	h.click(locales.TasksAdd()) // its destination is the server
	h.fill(locales.TaskFieldName(), "Photos")
	h.app.taskForm.allowRoot = true
	h.app.taskForm.source.path = "/Users/me/Pictures"
	h.click(locales.TaskSave())
	h.expect(locales.ErrorShareRequired())
	h.expect(locales.TaskFieldSharePathHint())

	h.app.taskForm.dest.path = "Photos/2024"
	h.click(locales.TaskSave())
	h.settle()
	if h.app.taskForm.open {
		t.Fatalf("not saved; problems %v", h.app.taskForm.problems)
	}
}

func TestRunLogInHistory(t *testing.T) {
	h := newHarness(t)
	src, dst := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("alpha"), 0o644); err != nil {
		t.Fatal(err)
	}
	task, err := h.svc.CreateTask(context.Background(), domain.TaskInput{AllowRoot: true, Name: "Mirror",
		Source: domain.Location{Path: src}, Destination: domain.Location{Path: dst}, Mode: domain.ModeCopy})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Run(context.Background(), task.ID, service.RunOptions{Trigger: domain.TriggerManual, DryRun: false}); err != nil {
		t.Fatal(err)
	}
	h.waitFor("the run to finish", func() bool { return len(h.svc.Active()) == 0 && len(h.app.history) == 1 })

	h.click(locales.NavHistory())
	h.click("Mirror")
	h.settle()
	h.expect(locales.HistoryLog())
	var copied string
	for _, line := range h.app.runLog.lines {
		if strings.Contains(line, "a.txt: Copied (") {
			copied = line
		}
	}
	if copied == "" {
		t.Fatalf("no copy in the log: %q", h.app.runLog.lines)
	}
	h.expect(copied)

	h.click(locales.HistoryLogCopy())
	if got := h.tt.Clipboard(); !strings.Contains(got, copied) || !strings.Contains(got, "finished: success") {
		t.Errorf("clipboard = %q", got)
	}
}

func TestStorageStatus(t *testing.T) {
	h := newHarness(t)
	st, err := h.svc.CreateStorage(context.Background(), domain.StorageInput{
		Name: "NAS", Kind: domain.KindSMB, Host: "127.0.0.1", Port: "1", Username: "guest", // nothing listens there
	})
	if err != nil {
		t.Fatal(err)
	}
	h.settle()
	h.click(locales.NavStorages())
	h.expect(locales.CheckNone())

	h.click(locales.StorageTestShort())
	h.waitFor("the test", func() bool {
		return !h.app.checking[st.ID] && len(h.app.storages) == 1 && h.app.storages[0].Check.Checked()
	})
	h.expect(locales.CheckFailed())
	if got := h.app.storages[0].Check; got.OK || got.Problem != domain.ProblemUnreachable {
		t.Errorf("check = %+v", got)
	}
	if h.tt.HasText(locales.CheckNone()) {
		t.Error("still shown as not tested")
	}
}

func TestDeleteRunsFromHistory(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for _, name := range []string{"Alpha", "Bravo", "Charlie"} {
		task, err := h.svc.CreateTask(ctx, domain.TaskInput{AllowRoot: true, Name: name,
			Source: domain.Location{Path: t.TempDir()}, Destination: domain.Location{Path: t.TempDir()}, Mode: domain.ModeCopy})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.svc.Run(ctx, task.ID, service.RunOptions{Trigger: domain.TriggerManual, DryRun: false}); err != nil {
			t.Fatal(err)
		}
		h.waitFor("the run of "+name, func() bool { return len(h.svc.Active()) == 0 })
	}
	h.waitFor("three runs", func() bool { return len(h.app.history) == 3 })
	h.click(locales.NavHistory())
	h.expect(locales.HistoryDeleteNone())

	// Several, chosen with Cmd.
	h.click("Alpha")
	if err := h.tt.ClickWith(ui.Super, "Charlie"); err != nil {
		t.Fatal(err)
	}
	h.settle()
	h.expect(locales.HistoryChosen(2))
	h.click(locales.HistoryDelete(2))
	h.expect(locales.HistoryDeleteTitle(2))
	h.click(locales.ButtonDelete())
	h.settle()
	if len(h.app.history) != 1 || h.app.history[0].TaskName != "Bravo" {
		t.Fatalf("history = %+v", h.app.history)
	}
	if h.app.historyChosen.Len() != 0 {
		t.Errorf("%d deleted runs still chosen", h.app.historyChosen.Len())
	}

	// One, with the Delete key.
	h.click("Bravo")
	h.tt.Key(0, ui.KeyBackspace)
	h.settle()
	h.expect(locales.HistoryDeleteTitle(1))
	h.click(locales.ButtonDelete())
	h.settle()
	if len(h.app.history) != 0 {
		t.Fatalf("history = %+v", h.app.history)
	}
	h.expect(locales.HistoryEmpty())
}

func TestDeleteRunFromItsMenu(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	task, err := h.svc.CreateTask(ctx, domain.TaskInput{AllowRoot: true, Name: "Alpha",
		Source: domain.Location{Path: t.TempDir()}, Destination: domain.Location{Path: t.TempDir()}, Mode: domain.ModeCopy})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Run(ctx, task.ID, service.RunOptions{Trigger: domain.TriggerManual, DryRun: false}); err != nil {
		t.Fatal(err)
	}
	h.waitFor("the run", func() bool { return len(h.svc.Active()) == 0 && len(h.app.history) == 1 })
	h.click(locales.NavHistory())

	if err := h.tt.RightClick("Alpha"); err != nil { // not chosen: the click chooses it
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem(locales.HistoryDeleteItem(1)); err != nil {
		t.Fatalf("menu %q: %v", h.tt.Menu(), err)
	}
	h.settle()
	h.expect(locales.HistoryDeleteTitle(1))
}

func TestChooseRunsWithCheckBoxes(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for _, name := range []string{"Alpha", "Bravo", "Charlie"} {
		task, err := h.svc.CreateTask(ctx, domain.TaskInput{AllowRoot: true, Name: name,
			Source: domain.Location{Path: t.TempDir()}, Destination: domain.Location{Path: t.TempDir()}, Mode: domain.ModeCopy})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.svc.Run(ctx, task.ID, service.RunOptions{Trigger: domain.TriggerManual, DryRun: false}); err != nil {
			t.Fatal(err)
		}
		h.waitFor("the run of "+name, func() bool { return len(h.svc.Active()) == 0 })
	}
	h.waitFor("three runs", func() bool { return len(h.app.history) == 3 })
	h.click(locales.NavHistory())
	// The check box of a run is labeled with its task and its time.
	box := func(name string) string {
		prefix := locales.HistoryChoose(name, "")
		for _, text := range h.tt.Texts() {
			if strings.HasPrefix(text, prefix) {
				return text
			}
		}
		t.Fatalf("no check box for %s", name)
		return ""
	}

	h.click("Alpha")
	h.click(box("Charlie")) // adds to what is chosen, as no plain click does
	h.settle()
	if got := h.app.chosenRuns(); len(got) != 2 {
		t.Fatalf("chosen %d runs, want Alpha and Charlie", len(got))
	}
	h.expect(locales.HistoryDelete(2))
	h.click(box("Alpha"))
	h.settle()
	if got := h.app.chosenRuns(); len(got) != 1 || got[0].TaskName != "Charlie" {
		t.Fatalf("chosen %+v, want Charlie alone", got)
	}

	h.click(locales.HistoryChooseAll())
	h.settle()
	if h.app.historyChosen.Len() != 3 {
		t.Fatalf("chose %d of 3", h.app.historyChosen.Len())
	}
	h.click(locales.HistoryChooseAll())
	h.settle()
	if h.app.historyChosen.Len() != 0 {
		t.Fatalf("%d still chosen", h.app.historyChosen.Len())
	}
}

func TestOverviewLeadsToPages(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.svc.CreateStorage(ctx, domain.StorageInput{Name: "NAS", Kind: domain.KindSMB, Host: "nas.local", Username: "guest"}); err != nil {
		t.Fatal(err)
	}
	var tasks []domain.Task
	for _, name := range []string{"Alpha", "Bravo"} {
		task, err := h.svc.CreateTask(ctx, domain.TaskInput{AllowRoot: true, Name: name,
			Source: domain.Location{Path: t.TempDir()}, Destination: domain.Location{Path: t.TempDir()}, Mode: domain.ModeCopy})
		if err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, task)
		if _, err := h.svc.Run(ctx, task.ID, service.RunOptions{Trigger: domain.TriggerManual, DryRun: false}); err != nil {
			t.Fatal(err)
		}
		h.waitFor("the run of "+name, func() bool { return len(h.svc.Active()) == 0 })
	}
	h.waitFor("two runs", func() bool { return len(h.app.recent) == 2 })
	h.settle()

	// The count of tasks leads to the tasks; none failing, its card is not a button.
	h.click(locales.OverviewTasks() + " 2")
	if h.app.page != pageTasks {
		t.Fatalf("page = %s", h.app.page)
	}
	h.click(locales.NavOverview())
	if err := h.tt.Click(locales.OverviewFailing() + " 0"); err == nil {
		t.Error("a count of nothing leads somewhere")
	}

	// A recent run leads to its details in the history.
	h.click(locales.OverviewOpenRun("Alpha"))
	h.settle()
	if h.app.page != pageHistory || len(h.app.chosenRuns()) != 1 || h.app.chosenRuns()[0].TaskName != "Alpha" {
		t.Fatalf("page %s, chosen %+v", h.app.page, h.app.chosenRuns())
	}
	h.expect(locales.HistoryDetailTitle())

	// A task leads to its runs alone.
	h.click(locales.NavOverview())
	h.click(locales.OverviewOpenTask("Bravo"))
	h.settle()
	if len(h.app.history) != 1 || h.app.history[0].TaskID != tasks[1].ID {
		t.Fatalf("history = %+v", h.app.history)
	}

	// Storages lead to theirs.
	h.click(locales.NavOverview())
	h.click(locales.OverviewStorages() + " 1")
	if h.app.page != pageStorages {
		t.Fatalf("page = %s", h.app.page)
	}
}

func TestRunAnywaySkipsTheDeleteCheck(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	src, dst := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(dst, "precious.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.CreateTask(ctx, domain.TaskInput{AllowRoot: true, Name: "Alpha", Mode: domain.ModeSync,
		Source: domain.Location{Path: src}, Destination: domain.Location{Path: dst}}); err != nil {
		t.Fatal(err)
	}
	h.settle()
	h.click(locales.NavTasks())
	h.click(locales.TaskActions("Alpha"))
	if err := h.tt.ChooseMenuItem(locales.TaskRunAnyway()); err != nil {
		t.Fatalf("menu %q: %v", h.tt.Menu(), err)
	}
	h.settle()
	h.expect(locales.TaskRunAnywayTitle("Alpha"))
	h.click(locales.TaskRunAnywayButton())
	h.waitFor("the forced run", func() bool { return len(h.svc.Active()) == 0 && len(h.app.history) == 1 })
	if run := h.app.history[0]; run.Status != domain.StatusSuccess {
		t.Fatalf("run = %+v, want success", run)
	}
	if _, err := os.Stat(filepath.Join(dst, "precious.txt")); !os.IsNotExist(err) {
		t.Fatal("the forced run kept the destination's file")
	}
}

func TestAStoppedSyncListsItsFilesThenRunsAnyway(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	src, dst := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(dst, "precious.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	task, err := h.svc.CreateTask(ctx, domain.TaskInput{AllowRoot: true, Name: "Alpha", Mode: domain.ModeSync,
		Source: domain.Location{Path: src}, Destination: domain.Location{Path: dst}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Run(ctx, task.ID, service.RunOptions{Trigger: domain.TriggerManual}); err != nil {
		t.Fatal(err)
	}
	h.waitFor("the stopped run", func() bool { return len(h.svc.Active()) == 0 && len(h.app.history) == 1 })

	h.click(locales.NavHistory())
	h.click("Alpha")
	h.settle()
	h.expect(locales.HistoryWouldDelete())
	h.expect("precious.txt")

	// From the files, a person runs it anyway; the file goes to the trash.
	h.click(locales.TaskRunAnyway())
	h.settle()
	h.expect(locales.TaskRunAnywayTrash(30))
	h.click(locales.TaskRunAnywayButton())
	h.waitFor("the forced run", func() bool { return len(h.svc.Active()) == 0 && len(h.app.history) == 2 })
	forced := h.app.history[0]
	if forced.Status != domain.StatusSuccess || forced.Trash == "" {
		t.Fatalf("run = %+v, want success into the trash", forced)
	}
	h.app.clearHistoryChoice()
	h.app.historyChosen.Add(forced.ID)
	h.settle()
	h.expect(locales.HistoryTrash())
	h.expect(forced.Trash)
	h.expect(locales.HistoryTrashed(1))
}

func TestSettingsPageSaves(t *testing.T) {
	h := newHarness(t)
	h.click(locales.NavSettings())
	h.expect(locales.SettingsTransferTitle())
	h.expect(locales.SettingsNoLimit())

	// What a person would choose in the selects.
	h.app.settingsForm.transfers = "2"
	h.app.settingsForm.bandwidth = locales.SettingsSpeed(10)
	h.app.settingsForm.retention = locales.SettingsKeepDays(30)
	h.expect(locales.SettingsTrash())
	h.app.settingsForm.trash = locales.SettingsTrashOff()
	h.app.saveSettings()
	h.waitFor("the save", func() bool { return !h.app.settingsForm.saving })

	got := h.svc.Settings()
	if got.Transfers != 2 || got.BandwidthMBps != 10 || got.RetentionDays != 30 || got.TrashDays != 0 {
		t.Fatalf("settings = %+v, want the chosen values", got)
	}
	h.expect(locales.SettingsSaved())
}

func TestTheSettingsKeepTheGlobalExclude(t *testing.T) {
	h := newHarness(t)
	if !domain.DefaultSettings().ExcludeEnabled {
		t.Fatal("the defaults leave the global exclusion off")
	}
	h.click(locales.NavSettings())
	// A new install leaves out the folders that development tools fill, so
	// the box shows checked and the rules are the built-in ones.
	h.expect(locales.SettingsExcludeEnabled())
	h.expect(locales.SettingsExcludeRulesHint(len(domain.DefaultGlobalExcludes())))
	if !h.app.settingsForm.excludeEnabled {
		t.Fatal("the checkbox is off at start, want it on")
	}
	// Turn the box off, as a click does, and save the page.
	h.app.settingsForm.excludeEnabled = false
	h.app.saveSettings()
	h.waitFor("the save", func() bool { return !h.svc.Settings().ExcludeEnabled })
	// A reload reads the settings again, and opening the page fills the
	// form in with them: the choice was kept, not just held by the page.
	h.app.settingsForm.loaded = false
	h.app.Reload()
	h.click(locales.NavSettings())
	if h.app.settingsForm.excludeEnabled {
		t.Fatal("the checkbox is on again after a reload")
	}
	// The rules open in the editor as the built-in list.
	h.expect(locales.SettingsExcludeRules())
	h.app.openGlobalExcludeEditor()
	h.settle()
	h.expect(locales.SettingsExcludeDialogTitle())
	if !h.app.exclude.open {
		t.Fatal("the editor is not open")
	}
	if !strings.Contains(h.app.exclude.text, "node_modules/") {
		t.Fatalf("the editor shows %q, want the built-in rules", h.app.exclude.text)
	}
	// Replace them with rules of the person's own, then keep them.
	h.app.exclude.text = "*.log\ncustom/" // as typing does
	h.click(locales.SettingsExcludeDone())
	h.settle()
	if h.app.exclude.open {
		t.Fatal("the editor stayed open after Done")
	}
	h.app.saveSettings()
	h.waitFor("the rules to save", func() bool {
		return reflect.DeepEqual(h.svc.Settings().GlobalExcludes, []string{"*.log", "custom/"})
	})
	// A reload and a reopened editor show the rules as they were kept.
	h.app.settingsForm.loaded = false
	h.app.Reload()
	h.click(locales.NavSettings())
	h.app.openGlobalExcludeEditor()
	h.settle()
	if got := h.app.exclude.text; got != "*.log\ncustom/" {
		t.Fatalf("the editor shows %q after a reload, want the saved rules", got)
	}
	// A line that is not a rule keeps the editor open, marked, rather than
	// closing on text the page would then fail to save.
	h.app.exclude.text = "*.log\n[bad"
	h.click(locales.SettingsExcludeDone())
	h.settle()
	if !h.app.exclude.open {
		t.Fatal("the editor closed with an invalid rule")
	}
	if h.tt.HasText(locales.SettingsExcludeInvalidLine(2)) == false {
		t.Fatal("the invalid line is not marked")
	}
	h.click(locales.ButtonCancel())
	h.settle()
	// Restore Defaults brings the built-in list back, and Save keeps it.
	h.app.openGlobalExcludeEditor()
	h.settle()
	h.click(locales.SettingsExcludeRestoreDefaults())
	h.click(locales.SettingsExcludeDone())
	h.settle()
	h.app.saveSettings()
	h.waitFor("the defaults to save", func() bool {
		return reflect.DeepEqual(h.svc.Settings().GlobalExcludes, domain.DefaultGlobalExcludes())
	})
}

// The window's minimum is chosen so the task dialog's save row and the
// global exclusion editor's buttons are laid out and reachable.
func TestTheDialogsFitTheSmallestWindow(t *testing.T) {
	h := newHarness(t)
	h.tt.SetSize(900, 640)
	h.tt.Advance(500 * time.Millisecond)
	h.click(locales.NavSettings())
	h.app.openGlobalExcludeEditor()
	h.settle()
	h.click(locales.SettingsExcludeDone())
	if h.app.exclude.open {
		t.Fatal("the global exclusion editor did not close at the smallest window")
	}
	h.app.openTaskForm(nil)
	h.settle()
	h.fill(locales.TaskFieldName(), "Smallest")
	h.app.taskForm.source.path, h.app.taskForm.dest.path = t.TempDir(), t.TempDir()
	h.click(locales.TaskSave())
	h.settle()
	if h.app.taskForm.open {
		t.Fatalf("the task dialog did not save at the smallest window: %v", h.app.taskForm.problems)
	}
}

func TestATaskWithUnreadableExclusionsSaysSo(t *testing.T) {
	h := newHarness(t)
	h.app.openTaskForm(&domain.Task{ID: "t", Name: "T", Mode: domain.ModeCopy,
		Source: domain.Location{Path: t.TempDir()}, Destination: domain.Location{Path: t.TempDir()},
		Exclude: domain.Exclude{Unreadable: true}})
	h.settle()
	h.expect(locales.TaskExcludeUnreadable())
	h.app.taskForm.exclude.open = true // the editor says it too
	h.settle()
	h.expect(locales.TaskExcludeUnreadable())
}

// The editors refuse a list that breaks the exclusion limits, not only a
// pattern that is not a glob.
func TestTheExcludeEditorsRefuseTooManyRules(t *testing.T) {
	var many strings.Builder
	for i := range 201 {
		fmt.Fprintf(&many, "x%d/\n", i)
	}
	h := newHarness(t)
	h.click(locales.NavTasks())
	h.click(locales.TasksAdd())
	h.click(locales.TaskExcludeEdit())
	h.fill(locales.TaskExcludePatterns(), many.String())
	h.settle()
	h.click(locales.TaskExcludeDone())
	if !h.app.taskForm.exclude.open {
		t.Fatal("the task editor closed with too many rules")
	}
	h.expect(locales.ErrorOutOfRange())
	h.click(locales.ButtonCancel())

	h.click(locales.NavSettings())
	h.app.openGlobalExcludeEditor()
	h.settle()
	h.fill(locales.SettingsExcludeRules(), many.String())
	h.settle()
	h.click(locales.SettingsExcludeDone())
	if !h.app.exclude.open {
		t.Fatal("the global editor closed with too many rules")
	}
	h.expect(locales.ErrorOutOfRange())
	h.click(locales.ButtonCancel())
}

func TestTheSettingsKeepTheStaleNotify(t *testing.T) {
	h := newHarness(t)
	h.click(locales.NavSettings())
	if !h.app.settingsForm.notifyStale || h.app.settingsForm.staleDays != locales.SettingsStaleDaysValue(7) {
		t.Fatalf("stale notify at start = %v %q", h.app.settingsForm.notifyStale, h.app.settingsForm.staleDays)
	}
	h.app.settingsForm.notifyStale = false
	h.app.settingsForm.staleDays = locales.SettingsStaleDaysValue(14)
	h.app.saveSettings()
	h.waitFor("the stale settings to save", func() bool {
		st := h.svc.Settings()
		return !st.NotifyStale && st.StaleDays == 14
	})
	h.app.settingsForm.loaded = false
	h.app.Reload()
	h.click(locales.NavSettings())
	if h.app.settingsForm.notifyStale || h.app.settingsForm.staleDays != locales.SettingsStaleDaysValue(14) {
		t.Fatalf("stale notify after reload = %v %q", h.app.settingsForm.notifyStale, h.app.settingsForm.staleDays)
	}
}

func TestDefaultStorageName(t *testing.T) {
	a := &App{storages: []domain.Storage{
		{ID: "1", Name: "MinIO"},
		{ID: "2", Name: "DiskStation"},
	}}
	cases := []struct {
		in   domain.StorageInput
		want string
	}{
		{domain.StorageInput{Kind: domain.KindS3, Provider: domain.ProviderCloudflare}, providerLabel(domain.ProviderCloudflare)},
		{domain.StorageInput{Kind: domain.KindS3, Provider: domain.ProviderMinio}, "MinIO (2)"},
		{domain.StorageInput{Kind: domain.KindSMB, Host: "nas.local"}, "nas.local"},
		{domain.StorageInput{Kind: domain.KindSMB, Host: "DiskStation"}, "DiskStation (2)"},
		{domain.StorageInput{Kind: domain.KindSMB}, kindLabel(domain.KindSMB)},
	}
	for _, c := range cases {
		if got := a.defaultStorageName(c.in); got != c.want {
			t.Errorf("defaultStorageName(%+v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestAnUnnamedStorageTakesTheProviderName(t *testing.T) {
	h := newHarness(t)
	h.click(locales.NavStorages())
	h.click(locales.StoragesAdd())
	f := &h.app.storageForm
	f.provider = locales.ProviderMinio()
	f.in.Name = ""
	f.in.Endpoint = "http://127.0.0.1:1"
	f.in.Bucket = "archive"
	f.in.AccessKeyID = "AKIA"
	f.in.Secret = "secret"
	h.app.saveStorage()
	h.waitFor("storage save", func() bool { return !h.app.storageForm.saving })
	h.settle()
	if f.open {
		t.Fatalf("the dialog stayed open; problems %v", f.problems)
	}
	if len(h.app.storages) != 1 || h.app.storages[0].Name != locales.ProviderMinio() {
		t.Fatalf("storages = %+v, want one named %q", h.app.storages, locales.ProviderMinio())
	}
}

func TestChoosingAnAppearanceShowsAndKeepsIt(t *testing.T) {
	h := newHarness(t)
	if len(h.host.shown) != 1 || h.host.shown[0] != domain.AppearanceSystem {
		t.Fatalf("shown at start = %v, want the system's", h.host.shown)
	}
	h.click(locales.NavSettings())
	// An unsaved change of the page stays unsaved.
	h.app.settingsForm.transfers = "2"
	h.click(locales.AppearanceDark())
	if got := h.host.shown[len(h.host.shown)-1]; got != domain.AppearanceDark {
		t.Fatalf("shown = %v, want dark", got)
	}
	h.waitFor("the save", func() bool { return h.svc.Settings().Appearance == domain.AppearanceDark })
	if got := h.svc.Settings().Transfers; got != domain.DefaultSettings().Transfers {
		t.Fatalf("transfers = %d, saved with the appearance", got)
	}
	// A reload, as after any change, shows it no more than once.
	n := len(h.host.shown)
	h.app.Reload()
	if len(h.host.shown) != n {
		t.Fatalf("shown again on reload: %v", h.host.shown)
	}
}

func TestChoosingAnAppIconShowsAndKeepsIt(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("only macOS has a Dock to put the icon in")
	}
	h := newHarness(t)
	if len(h.host.icons) != 1 || h.host.icons[0] != domain.AppIconMenuBar {
		t.Fatalf("shown at start = %v, want the menu bar alone", h.host.icons)
	}
	h.click(locales.NavSettings())
	h.expect(locales.SettingsAppIcon())
	// An unsaved change of the page stays unsaved.
	h.app.settingsForm.transfers = "2"
	// The select shows the mode in force; opening it offers the others.
	h.click(locales.SettingsAppIconMenuBar())
	h.settle()
	h.click(locales.SettingsAppIconDock())
	h.settle()
	if got := h.host.icons[len(h.host.icons)-1]; got != domain.AppIconDock {
		t.Fatalf("shown = %v, want the Dock", got)
	}
	h.waitFor("the save", func() bool { return h.svc.Settings().AppIcon == domain.AppIconDock })
	h.expect(locales.SettingsAppIconDockHint())
	if got := h.svc.Settings().Transfers; got != domain.DefaultSettings().Transfers {
		t.Fatalf("transfers = %d, saved with the icon", got)
	}
	// A reload, as after any change, shows it no more than once.
	n := len(h.host.icons)
	h.app.Reload()
	if len(h.host.icons) != n {
		t.Fatalf("shown again on reload: %v", h.host.icons)
	}
}

func TestATaskChoosesItsTrash(t *testing.T) {
	h := newHarness(t)
	src, dst := t.TempDir(), t.TempDir()
	h.click(locales.NavTasks())
	h.click(locales.TasksAdd())
	h.expect(locales.TaskTrashFollow(30))
	h.expect(locales.TaskTrashHint())
	h.fill(locales.TaskFieldName(), "Mirror")
	h.app.taskForm.source.path, h.app.taskForm.dest.path = src, dst
	// What a person would choose in the select.
	h.app.taskForm.trash = domain.TrashOff
	h.settle()
	h.expect(locales.TaskTrashOffHint())
	h.click(locales.TaskSave())
	h.settle()
	tasks, err := h.svc.Tasks(context.Background())
	if err != nil || len(tasks) != 1 || tasks[0].TrashDays != domain.TrashOff {
		t.Fatalf("tasks = %+v, %v", tasks, err)
	}
	h.app.openTaskForm(&tasks[0])
	h.settle()
	h.expect(locales.SettingsTrashOff())
}

func TestATaskExcludesFiles(t *testing.T) {
	h := newHarness(t)
	src, dst := t.TempDir(), t.TempDir()
	h.click(locales.NavTasks())
	h.click(locales.TasksAdd())
	// A new task leaves out what the systems make, on top of the global rules.
	h.expect(locales.TaskExcludeRules(2))
	h.expect(locales.TaskExcludeGlobalHint(len(domain.DefaultGlobalExcludes())))
	h.fill(locales.TaskFieldName(), "Projects")
	h.app.taskForm.source.path, h.app.taskForm.dest.path = src, dst
	h.click(locales.TaskExcludeEdit())
	h.expect(locales.TaskExcludeTitle())
	h.click("node_modules/")
	h.fill(locales.TaskExcludePatterns(), "*.log\n[build")
	h.settle()
	h.expect(locales.TaskExcludeInvalidLine(2))
	h.fill(locales.TaskExcludePatterns(), "*.log\nbuild/")
	h.app.taskForm.exclude.maxSize = maxSizeValues[2] // as the select chooses
	h.settle()
	if h.tt.HasText(locales.TaskExcludeInvalidLine(2)) {
		t.Fatal("the fixed rule is still invalid")
	}
	h.click(locales.TaskExcludeDone())
	h.settle()
	h.expect(locales.TaskExcludeRulesAndLarger(5, "1 GB"))
	h.click(locales.TaskSave())
	h.settle()
	tasks, err := h.svc.Tasks(context.Background())
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks = %+v, %v", tasks, err)
	}
	want := domain.Exclude{Patterns: []string{".DS_Store", "Thumbs.db", "node_modules/", "*.log", "build/"}, MaxSize: 1_000_000_000}
	if !reflect.DeepEqual(tasks[0].Exclude, want) {
		t.Fatalf("exclude = %+v, want %+v", tasks[0].Exclude, want)
	}
	h.app.openTaskForm(&tasks[0])
	h.settle()
	h.expect(locales.TaskExcludeRulesAndLarger(5, "1 GB"))
}

func TestClickingATaskOpensItsEditor(t *testing.T) {
	h := newHarness(t)
	h.newTask("Editable")
	h.click(locales.TaskSave())
	h.settle()

	// The task opens its editor, while the button beside it still runs it.
	h.click("Editable")
	if !h.app.taskForm.open || h.app.taskForm.id == "" {
		t.Fatalf("the task did not open its editor: open=%v id=%q", h.app.taskForm.open, h.app.taskForm.id)
	}
	h.app.taskForm.open = false
	h.settle()
	h.click(locales.TaskRun())
	h.waitFor("the run", func() bool { return len(h.svc.Active()) == 0 })
	if h.app.taskForm.open {
		t.Fatal("the run button opened the editor")
	}
}

func TestSidebarArrowKeysMoveSelectionAndFocus(t *testing.T) {
	h := newHarness(t)
	h.click(locales.NavTasks())
	h.tt.Key(0, ui.KeyDown)
	if h.app.page != pageStorages || !h.tt.Focused(locales.NavStorages()) {
		t.Fatalf("down did not choose and focus Storages: %s", h.app.page)
	}
	h.tt.Key(0, ui.KeyEnd)
	if h.app.page != pageSettings || !h.tt.Focused(locales.NavSettings()) {
		t.Fatalf("end did not choose and focus Settings: %s", h.app.page)
	}
	h.tt.Key(0, ui.KeyUp)
	if h.app.page != pageHistory || !h.tt.Focused(locales.NavHistory()) {
		t.Fatalf("up did not choose and focus History: %s", h.app.page)
	}
}
