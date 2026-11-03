package domain

import (
	"slices"
	"testing"
	"time"
)

func TestScheduleNext(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no time zone database:", err)
	}
	at := func(y int, m time.Month, d, h, min int) time.Time { return time.Date(y, m, d, h, min, 0, 0, loc) }
	// Tuesday, 3 November 2026, 10:00.
	now := at(2026, 11, 3, 10, 0)
	cases := []struct {
		name string
		s    Schedule
		from time.Time
		want time.Time
	}{
		{"off", Schedule{}, now, time.Time{}},
		{"daily, later today", Schedule{Repeat: RepeatDaily, Hour: 21}, now, at(2026, 11, 3, 21, 0)},
		{"daily, tomorrow", Schedule{Repeat: RepeatDaily, Hour: 3, Minute: 30}, now, at(2026, 11, 4, 3, 30)},
		{"daily, not the time itself", Schedule{Repeat: RepeatDaily, Hour: 10}, now, at(2026, 11, 4, 10, 0)},
		{"every hour", Schedule{Repeat: RepeatHourly, Every: 1, Minute: 15}, now, at(2026, 11, 3, 10, 15)},
		{"every 6 hours", Schedule{Repeat: RepeatHourly, Every: 6}, now, at(2026, 11, 3, 12, 0)},
		{"every 12 hours, past the last", Schedule{Repeat: RepeatHourly, Every: 12}, at(2026, 11, 3, 13, 0), at(2026, 11, 4, 0, 0)},
		{"weekly, later this week", Schedule{Repeat: RepeatWeekly, Days: WeekdaysOf(time.Friday), Hour: 9}, now, at(2026, 11, 6, 9, 0)},
		{"weekly, next week", Schedule{Repeat: RepeatWeekly, Days: WeekdaysOf(time.Tuesday), Hour: 9}, now, at(2026, 11, 10, 9, 0)},
		{"weekly, today", Schedule{Repeat: RepeatWeekly, Days: WorkDays, Hour: 18}, now, at(2026, 11, 3, 18, 0)},
		{"monthly", Schedule{Repeat: RepeatMonthly, Day: 1, Hour: 2}, now, at(2026, 12, 1, 2, 0)},
		{"monthly, the 31st of a short month", Schedule{Repeat: RepeatMonthly, Day: 31, Hour: 2}, now, at(2026, 11, 30, 2, 0)},
		{"monthly, the 30th in February", Schedule{Repeat: RepeatMonthly, Day: 30}, at(2027, 2, 1, 0, 0), at(2027, 2, 28, 0, 0)},
		// 8 March 2026: the clocks go from 2:00 to 3:00, and the run waits for them.
		{"a time the clocks skip", Schedule{Repeat: RepeatDaily, Hour: 2, Minute: 30}, at(2026, 3, 8, 1, 0), at(2026, 3, 8, 3, 0)},
		{"invalid", Schedule{Repeat: RepeatWeekly, Hour: 9}, now, time.Time{}},
	}
	for _, c := range cases {
		if got := c.s.Next(c.from); !got.Equal(c.want) {
			t.Errorf("%s: Next(%v) = %v, want %v", c.name, c.from, got, c.want)
		}
	}
}

func TestScheduleWakes(t *testing.T) {
	cases := []struct {
		name string
		s    Schedule
		want []Wake
	}{
		{"off", Schedule{}, nil},
		{"every hour", Schedule{Repeat: RepeatHourly, Every: 1, Minute: 5}, []Wake{{-1, -1, -1, 5}}},
		{"every 8 hours", Schedule{Repeat: RepeatHourly, Every: 8}, []Wake{{-1, -1, 0, 0}, {-1, -1, 8, 0}, {-1, -1, 16, 0}}},
		{"daily", Schedule{Repeat: RepeatDaily, Hour: 21, Minute: 30}, []Wake{{-1, -1, 21, 30}}},
		{"weekly", Schedule{Repeat: RepeatWeekly, Days: WeekdaysOf(time.Monday, time.Sunday), Hour: 9},
			[]Wake{{-1, 0, 9, 0}, {-1, 1, 9, 0}}},
		{"monthly", Schedule{Repeat: RepeatMonthly, Day: 15, Hour: 1}, []Wake{{15, -1, 1, 0}}},
		{"monthly, past the 28th", Schedule{Repeat: RepeatMonthly, Day: 30, Hour: 1},
			[]Wake{{28, -1, 1, 0}, {29, -1, 1, 0}, {30, -1, 1, 0}}},
	}
	for _, c := range cases {
		if got := c.s.Wakes(); !slices.Equal(got, c.want) {
			t.Errorf("%s: Wakes() = %v, want %v", c.name, got, c.want)
		}
	}
}

// Every time a schedule runs is a time the system wakes the scheduler.
func TestWakesCoverNext(t *testing.T) {
	schedules := []Schedule{
		{Repeat: RepeatHourly, Every: 3, Minute: 10},
		{Repeat: RepeatDaily, Hour: 7, Minute: 45},
		{Repeat: RepeatWeekly, Days: Weekends, Hour: 22},
		{Repeat: RepeatMonthly, Day: 31, Hour: 4},
	}
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, s := range schedules {
		next := from
		for range 40 {
			next = s.Next(next)
			if !slices.ContainsFunc(s.Wakes(), func(w Wake) bool { return w.matches(next) }) {
				t.Fatalf("%+v runs at %v, when the system does not wake", s, next)
			}
		}
	}
}

func (w Wake) matches(t time.Time) bool {
	ok := func(want, got int) bool { return want == -1 || want == got }
	return ok(w.Day, t.Day()) && ok(w.Weekday, int(t.Weekday())) && ok(w.Hour, t.Hour()) && ok(w.Minute, t.Minute())
}

func TestDefaultSchedule(t *testing.T) {
	tuesday := time.Date(2026, 11, 3, 10, 0, 0, 0, time.Local)
	for _, r := range Repeats {
		if code := DefaultSchedule(r, tuesday).Validate(); code != "" {
			t.Errorf("the default %q schedule: %s", r, code)
		}
	}
	if got := DefaultSchedule(RepeatWeekly, tuesday).Days; got != WeekdaysOf(time.Tuesday) {
		t.Errorf("a new weekly schedule runs on %v, want today's day", got.Days())
	}
}
