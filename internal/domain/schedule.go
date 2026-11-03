package domain

import (
	"slices"
	"time"
)

// Repeat is how often a task runs on its own.
type Repeat string

const (
	// RepeatOff runs the task by hand only.
	RepeatOff     Repeat = ""
	RepeatHourly  Repeat = "hourly"
	RepeatDaily   Repeat = "daily"
	RepeatWeekly  Repeat = "weekly"
	RepeatMonthly Repeat = "monthly"
)

// Repeats lists the choices the app offers, in the order it shows them.
var Repeats = []Repeat{RepeatOff, RepeatHourly, RepeatDaily, RepeatWeekly, RepeatMonthly}

// HourIntervals are the hours an hourly schedule can wait between runs:
// those that divide a day, so that the runs fall at the same hours each day.
var HourIntervals = []int{1, 2, 3, 4, 6, 8, 12}

// Weekdays is a set of days of the week, one bit per time.Weekday.
type Weekdays uint8

// WeekdaysOf returns the set of days.
func WeekdaysOf(days ...time.Weekday) Weekdays {
	var w Weekdays
	for _, d := range days {
		w = w.With(d, true)
	}
	return w
}

// Has reports whether d is in the set.
func (w Weekdays) Has(d time.Weekday) bool { return w&(1<<d) != 0 }

// With returns the set with d in it, or out of it.
func (w Weekdays) With(d time.Weekday, in bool) Weekdays {
	if in {
		return w | 1<<d
	}
	return w &^ (1 << d)
}

// Days returns the days in the set, Sunday first.
func (w Weekdays) Days() []time.Weekday {
	var out []time.Weekday
	for d := time.Sunday; d <= time.Saturday; d++ {
		if w.Has(d) {
			out = append(out, d)
		}
	}
	return out
}

// Weekdays and weekends, as a person says them.
const (
	EveryDay = Weekdays(1<<7 - 1)
	WorkDays = Weekdays(1<<time.Monday | 1<<time.Tuesday | 1<<time.Wednesday | 1<<time.Thursday | 1<<time.Friday)
	Weekends = Weekdays(1<<time.Saturday | 1<<time.Sunday)
)

// Schedule is when a task runs on its own, in the computer's time zone,
// said the way a person says it: every day at 21:00, every Monday and
// Thursday at 9:30, on the 1st of every month. The zero Schedule runs the
// task by hand only.
type Schedule struct {
	Repeat Repeat
	// Every is the hours between the runs of an hourly schedule, one of
	// HourIntervals; they fall at its multiples from midnight.
	Every int
	// Hour and Minute are the time of day of a daily, weekly or monthly
	// run; an hourly one runs at Minute past the hour.
	Hour, Minute int
	// Days are the days a weekly schedule runs.
	Days Weekdays
	// Day is the day of the month a monthly schedule runs, 1 to 31; a
	// month without that day runs on its last.
	Day int
}

// On reports whether the schedule runs the task on its own.
func (s Schedule) On() bool { return s.Repeat != RepeatOff }

// Normalize keeps only the fields of the schedule's Repeat, so that two
// schedules that run at the same times are equal.
func (s Schedule) Normalize() Schedule {
	switch s.Repeat {
	case RepeatHourly:
		return Schedule{Repeat: s.Repeat, Every: s.Every, Minute: s.Minute}
	case RepeatDaily:
		return Schedule{Repeat: s.Repeat, Hour: s.Hour, Minute: s.Minute}
	case RepeatWeekly:
		return Schedule{Repeat: s.Repeat, Hour: s.Hour, Minute: s.Minute, Days: s.Days & EveryDay}
	case RepeatMonthly:
		return Schedule{Repeat: s.Repeat, Hour: s.Hour, Minute: s.Minute, Day: s.Day}
	}
	return Schedule{Repeat: s.Repeat}
}

// Validate returns what is wrong with the schedule, or "".
func (s Schedule) Validate() Code {
	if !slices.Contains(Repeats, s.Repeat) {
		return CodeInvalid
	}
	if !s.On() {
		return ""
	}
	if s.Minute < 0 || s.Minute > 59 || s.Hour < 0 || s.Hour > 23 {
		return CodeInvalid
	}
	switch s.Repeat {
	case RepeatHourly:
		if !slices.Contains(HourIntervals, s.Every) {
			return CodeInvalid
		}
	case RepeatWeekly:
		if s.Days&EveryDay == 0 {
			return CodeNoDays
		}
	case RepeatMonthly:
		if s.Day < 1 || s.Day > 31 {
			return CodeInvalid
		}
	}
	return ""
}

// Next returns the first time the schedule runs after after, in after's
// time zone, or the zero time for a schedule that does not run.
func (s Schedule) Next(after time.Time) time.Time {
	if !s.On() || s.Validate() != "" {
		return time.Time{}
	}
	loc := after.Location()
	y, m, d := after.Date()
	at := func(day, hour int) time.Time { return wallTime(y, m, d+day, hour, s.Minute, loc) }
	switch s.Repeat {
	case RepeatHourly:
		for day := 0; day < 2; day++ {
			for h := 0; h < 24; h += s.Every {
				if t := at(day, h); t.After(after) {
					return t
				}
			}
		}
	case RepeatDaily, RepeatWeekly:
		for day := 0; day < 8; day++ {
			t := at(day, s.Hour)
			if s.Repeat == RepeatWeekly && !s.Days.Has(time.Date(y, m, d+day, 12, 0, 0, 0, loc).Weekday()) {
				continue
			}
			if t.After(after) {
				return t
			}
		}
	case RepeatMonthly:
		for month := 0; month < 3; month++ {
			last := time.Date(y, m+time.Month(month)+1, 0, 12, 0, 0, 0, loc).Day()
			t := wallTime(y, m+time.Month(month), min(s.Day, last), s.Hour, s.Minute, loc)
			if t.After(after) {
				return t
			}
		}
	}
	return time.Time{}
}

// wallTime is the time the clocks of loc show hour:minute on a day; on a
// day they skip past it, as the clocks go forward, the time they come back
// to the hour after the gap: the run comes late, never early.
func wallTime(y int, m time.Month, d, hour, minute int, loc *time.Location) time.Time {
	t := time.Date(y, m, d, hour, minute, 0, 0, loc)
	want := time.Date(y, m, d, hour, minute, 0, 0, time.UTC)
	for i := 0; i < 8; i++ {
		wy, wm, wd := t.Date()
		if !time.Date(wy, wm, wd, t.Hour(), t.Minute(), 0, 0, time.UTC).Before(want) {
			break
		}
		t = t.Add(15 * time.Minute)
	}
	return t
}

// Wake is a time of the calendar at which the system starts the app's
// scheduler, as launchd's StartCalendarInterval says it: a field of -1
// matches any value. Weekday counts from Sunday, 0.
type Wake struct {
	Day, Weekday, Hour, Minute int
}

// Wakes returns the times at which the system must start the scheduler for
// the schedule to run on time. They may match more times than the schedule
// runs at (the scheduler then finds nothing to do), never fewer.
func (s Schedule) Wakes() []Wake {
	if !s.On() || s.Validate() != "" {
		return nil
	}
	base := Wake{Day: -1, Weekday: -1, Hour: s.Hour, Minute: s.Minute}
	switch s.Repeat {
	case RepeatHourly:
		if s.Every == 1 {
			base.Hour = -1
			return []Wake{base}
		}
		var out []Wake
		for h := 0; h < 24; h += s.Every {
			w := base
			w.Hour = h
			out = append(out, w)
		}
		return out
	case RepeatDaily:
		return []Wake{base}
	case RepeatWeekly:
		var out []Wake
		for _, d := range s.Days.Days() {
			w := base
			w.Weekday = int(d)
			out = append(out, w)
		}
		return out
	case RepeatMonthly:
		// The calendar has no "last day of the month": a day past the
		// 28th wakes on each day from the 28th, and the schedule says
		// which of them is this month's.
		var out []Wake
		for day := min(s.Day, 28); day <= s.Day; day++ {
			w := base
			w.Day = day
			out = append(out, w)
		}
		return out
	}
	return nil
}

// DefaultSchedule is the schedule a task gets when a person turns on one
// of r, before they choose a time: in the evening, when a computer is
// usually on and no one is waiting for it.
func DefaultSchedule(r Repeat, now time.Time) Schedule {
	s := Schedule{Repeat: r, Hour: 21}
	switch r {
	case RepeatHourly:
		s.Every, s.Hour = 1, 0
	case RepeatWeekly:
		s.Days = WeekdaysOf(now.Weekday())
	case RepeatMonthly:
		s.Day = 1
	}
	return s.Normalize()
}
