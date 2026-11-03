package view

import (
	"errors"

	"github.com/egoist/mygo/i18n"
	"slices"
	"testing"
	"time"

	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/internal/service"
	"github.com/starriv/cloudshuttle/locales"
)

// refusingRegistrar is a system that does not let the app run in the
// background.
type refusingRegistrar struct{}

func (refusingRegistrar) Register(wakes []domain.Wake) error {
	if len(wakes) == 0 {
		return nil
	}
	return errors.New("Bootstrap failed: 5: Input/output error")
}

// newTask opens the task form with two folders filled in.
func (h *harness) newTask(name string) {
	h.t.Helper()
	h.click(locales.NavTasks())
	h.click(locales.TasksAdd())
	h.fill(locales.TaskFieldName(), name)
	h.app.taskForm.source.path = h.t.TempDir()
	h.app.taskForm.dest.path = h.t.TempDir()
}

func TestAWeeklySchedule(t *testing.T) {
	h := newHarness(t)
	h.tt.SetRegion(i18n.Region{Locale: "en", FirstWeekday: time.Sunday, HourCycle: 24})
	h.newTask("Evenings")
	h.click(locales.RepeatWeekly())
	h.expect(locales.ScheduleInApp()) // the harness has no system scheduler

	// No day chosen: the form says so.
	h.app.taskForm.schedule.days = [7]bool{}
	h.settle()
	h.click(locales.TaskSave())
	h.expect(locales.ErrorNoDays())

	h.click("Mon")
	h.click("Thu")
	h.app.taskForm.schedule.at = time.Date(2000, 1, 1, 21, 30, 0, 0, time.Local)
	h.settle()
	h.click(locales.TaskSave())
	h.settle()
	if h.app.taskForm.open {
		t.Fatalf("the form stayed open: %v", h.app.taskForm.problems)
	}
	task := h.app.tasks[0]
	want := domain.Schedule{Repeat: domain.RepeatWeekly, Days: domain.WeekdaysOf(time.Monday, time.Thursday), Hour: 21, Minute: 30}
	if task.Schedule != want {
		t.Fatalf("schedule = %+v, want %+v", task.Schedule, want)
	}
	h.expect(scheduleTextIn("en-u-hc-h23", time.Sunday, want))

	// Editing it shows what was chosen.
	h.app.openTaskForm(&task)
	if got := h.app.taskForm.schedule.schedule(); got != want {
		t.Fatalf("the form opened with %+v, want %+v", got, want)
	}
}

func TestScheduleTexts(t *testing.T) {
	nine := func(s domain.Schedule) domain.Schedule { s.Hour = 9; return s }
	cases := []struct {
		s    domain.Schedule
		want string
	}{
		{domain.Schedule{}, locales.TasksManual()},
		{domain.Schedule{Repeat: domain.RepeatHourly, Every: 1}, "Every hour"},
		{domain.Schedule{Repeat: domain.RepeatHourly, Every: 6}, "Every 6 hours"},
		{nine(domain.Schedule{Repeat: domain.RepeatWeekly, Days: domain.WorkDays}), "Weekdays at"},
		{nine(domain.Schedule{Repeat: domain.RepeatWeekly, Days: domain.WeekdaysOf(time.Monday, time.Friday)}), "Every Mon, Fri at"},
		{nine(domain.Schedule{Repeat: domain.RepeatMonthly, Day: 2}), "Monthly on the 2nd at"},
		{nine(domain.Schedule{Repeat: domain.RepeatMonthly, Day: 31}), "Monthly on the 31st at"},
	}
	for _, tc := range cases {
		if got := scheduleTextIn("en", time.Monday, tc.s); len(got) < len(tc.want) || got[:len(tc.want)] != tc.want {
			t.Errorf("scheduleText(%+v) = %q, want it to start with %q", tc.s, got, tc.want)
		}
	}
}

func TestTheFormSaysWhenTheSystemRefusesTheScheduler(t *testing.T) {
	h := newHarnessWith(t, service.Options{Registrar: refusingRegistrar{}})
	h.newTask("Nightly")
	h.click(locales.RepeatDaily())
	h.expect(locales.ScheduleAskOnSave())
	h.click(locales.TaskSave())
	h.settle()
	h.expect(locales.BackgroundAskTitle())
	h.click(locales.BackgroundAllow())
	h.settle()
	if h.app.taskForm.open {
		t.Fatal("the form stayed open after the answer")
	}
	h.waitFor("the system to refuse", func() bool {
		b, _ := h.svc.Background()
		return b == service.BackgroundBlocked
	})
	h.settle()
	h.click(locales.NavTasks())
	h.settle()
	h.expect(locales.ScheduleBlocked())
	h.click(locales.ScheduleOpenSettings())
	if h.host.settings != 1 {
		t.Fatalf("the settings were opened %d times, want 1", h.host.settings)
	}
	if !slices.ContainsFunc(h.app.tasks, func(t domain.Task) bool { return t.Schedule.Repeat == domain.RepeatDaily }) {
		t.Fatal("the task was not saved with its schedule")
	}
}

// acceptingRegistrar is a system that runs the schedules.
type acceptingRegistrar struct{}

func (acceptingRegistrar) Register([]domain.Wake) error { return nil }

func TestTheFirstScheduleAsksBeforeRunningInTheBackground(t *testing.T) {
	h := newHarnessWith(t, service.Options{Registrar: acceptingRegistrar{}})
	h.newTask("Nightly")
	h.click(locales.RepeatDaily())
	h.click(locales.TaskSave())
	h.settle()
	h.expect(locales.BackgroundAskMessage())
	h.click(locales.BackgroundDecline())
	h.settle()
	if got := h.svc.Settings().Background; got != domain.ConsentRefused {
		t.Fatalf("consent = %v, want refused", got)
	}
	h.waitFor("the app to run the schedules", func() bool {
		b, _ := h.svc.Background()
		return b == service.BackgroundOff
	})

	// The next schedule does not ask again, and says how it runs.
	// (Typing the name would click the list's Name column behind the
	// dialog, now that there is a task.)
	h.click(locales.TasksAdd())
	h.settle()
	h.app.taskForm.name = "Weekly"
	h.app.taskForm.source.path = t.TempDir()
	h.app.taskForm.dest.path = t.TempDir()
	h.click(locales.RepeatDaily())
	h.expect(locales.ScheduleInAppChosen())
	h.click(locales.TaskSave())
	h.settle()
	if h.app.taskForm.open {
		t.Fatal("asked again")
	}

	// Settings change it.
	h.click(locales.NavSettings())
	h.settle()
	h.expect(locales.SettingsBackground())
	h.app.settingsForm.background = true
	h.app.saveSettings() // the button is below the test window
	h.waitFor("the settings saved", func() bool { return !h.app.settingsForm.saving })
	h.waitFor("the system to run the schedules", func() bool {
		b, _ := h.svc.Background()
		return b == service.BackgroundSystem
	})
}
