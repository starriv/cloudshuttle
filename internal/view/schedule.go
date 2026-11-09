package view

import (
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/internal/icons"
	"github.com/starriv/cloudshuttle/internal/service"
	"github.com/starriv/cloudshuttle/locales"
)

// scheduleField is the schedule of the task form as it is edited. Each
// repeat keeps what was chosen for it, so that trying another and coming
// back loses nothing.
type scheduleField struct {
	repeat int       // index in domain.Repeats, as the segmented control shows them
	at     time.Time // the time of day
	days   [7]bool   // by time.Weekday
	every  string    // the hourly select's label
	day    string    // the monthly select's label
}

func newScheduleField(s domain.Schedule, now time.Time) scheduleField {
	f := scheduleField{repeat: max(slices.Index(domain.Repeats, s.Repeat), 0)}
	// What the other repeats start with: the defaults, today's day.
	def := domain.DefaultSchedule(domain.RepeatWeekly, now)
	hour, minute, days, every, day := def.Hour, def.Minute, def.Days, 1, 1
	if s.On() && s.Repeat != domain.RepeatHourly {
		hour, minute = s.Hour, s.Minute
	}
	if s.Repeat == domain.RepeatWeekly {
		days = s.Days
	}
	if s.Repeat == domain.RepeatHourly {
		every = s.Every
	}
	if s.Repeat == domain.RepeatMonthly {
		day = s.Day
	}
	f.at = time.Date(2000, 1, 1, hour, minute, 0, 0, time.Local)
	for d := time.Sunday; d <= time.Saturday; d++ {
		f.days[d] = days.Has(d)
	}
	f.every = everyLabels()[max(slices.Index(domain.HourIntervals, every), 0)]
	f.day = dayLabels()[min(max(day, 1), 31)-1]
	return f
}

// schedule is the schedule the field says.
func (f scheduleField) schedule() domain.Schedule {
	s := domain.Schedule{Repeat: domain.Repeats[f.repeat], Hour: f.at.Hour(), Minute: f.at.Minute()}
	switch s.Repeat {
	case domain.RepeatHourly:
		s.Every = domain.HourIntervals[max(indexOf(everyLabels(), f.every), 0)]
		s.Hour, s.Minute = 0, 0
	case domain.RepeatWeekly:
		for d, on := range f.days {
			s.Days = s.Days.With(time.Weekday(d), on)
		}
	case domain.RepeatMonthly:
		s.Day = max(indexOf(dayLabels(), f.day), 0) + 1
	}
	return s.Normalize()
}

func repeatLabels() []string {
	return []string{locales.RepeatOff(), locales.RepeatHourly(), locales.RepeatDaily(),
		locales.RepeatWeekly(), locales.RepeatMonthly()}
}

func everyLabels() []string {
	out := make([]string, len(domain.HourIntervals))
	for i, n := range domain.HourIntervals {
		out[i] = locales.ScheduleEveryHours(n)
	}
	return out
}

func dayLabels() []string {
	out := make([]string, 31)
	for i := range out {
		out[i] = locales.ScheduleDayOfMonth(i + 1)
	}
	return out
}

// scheduleField is the task form's schedule: how often, then when, and
// what that means, in the person's words.
func (a *App) scheduleField(c *ui.Context, f *taskForm) {
	p := paletteFor(c.Theme())
	sf := &f.schedule
	s := sf.schedule()
	ui.Column(c).Gap(10).FillWidth().Children(func() {
		// The column stretches its children; a row lets the control keep
		// the width of its segments.
		ui.Row(c).Children(func() {
			ui.Segmented(c, &sf.repeat, repeatLabels()...).Label(locales.TaskFieldSchedule())
		})
		if s.On() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Wrap().Children(func() {
				switch s.Repeat {
				case domain.RepeatHourly:
					ui.Row(c).Children(func() {
						ui.Select(c, &sf.every, everyLabels()).Label(locales.ScheduleEvery())
					})
				case domain.RepeatWeekly:
					a.weekdayToggles(c, sf)
				case domain.RepeatMonthly:
					ui.Row(c).Children(func() {
						ui.Select(c, &sf.day, dayLabels()).Label(locales.ScheduleDay())
					})
				}
				if s.Repeat != domain.RepeatHourly {
					ui.Row(c).Children(func() {
						ui.TimeInput(c, &sf.at).Label(locales.ScheduleTime())
					})
				}
			})
		}
		// What the schedule means, a line at a time, so the dialog keeps
		// its height as the times change.
		for _, line := range strings.Split(a.scheduleDescription(c, s), "\n") {
			if line == "" {
				continue
			}
			ui.Text(c, line).FontSize(sizeMicro).TextColor(p.muted).LineHeight(1.45)
		}
		a.blockedNote(c)
		sheetError(c, problemText(f.problems.For(domain.FieldSchedule)))
	})
}

// weekdayToggles are the days of a weekly schedule, from the first day of
// the person's week.
func (a *App) weekdayToggles(c *ui.Context, sf *scheduleField) {
	names := i18n.Weekdays(c.Region().FormatLocale(c.Locale()), i18n.Abbreviated, true)
	first := c.Region().FirstWeekday
	ui.ToggleGroup(c, func() {
		for i := range 7 {
			d := (first + time.Weekday(i)) % 7
			ui.Toggle(c.Key(int(d)), &sf.days[d], names[d])
		}
	}).Label(locales.ScheduleDays())
}

// scheduleDescription says when the schedule runs next, and what to know
// of the times it chose.
func (a *App) scheduleDescription(c *ui.Context, s domain.Schedule) string {
	if !s.On() {
		return locales.TaskScheduleOff()
	}
	// The line on who runs it stays as the times change, so the dialog
	// keeps its height while a day is chosen.
	var lines []string
	if s.Validate() == "" {
		next := locales.TaskScheduleNext(formatTime(c, s.Next(a.now())))
		if s.Repeat == domain.RepeatMonthly && s.Day > 28 {
			next += " " + locales.TaskScheduleLastDay(s.Day)
		}
		lines = append(lines, next)
	}
	b, _ := a.svc.Background()
	switch {
	case !a.svc.SystemScheduler():
		lines = append(lines, locales.ScheduleInApp())
	case a.svc.Settings().Background == domain.ConsentUnasked:
		lines = append(lines, locales.ScheduleAskOnSave())
	case a.svc.Settings().Background == domain.ConsentRefused:
		lines = append(lines, locales.ScheduleInAppChosen())
	case b == service.BackgroundBlocked: // blockedNote says what to know
	default:
		lines = append(lines, locales.ScheduleBySystem())
	}
	return strings.Join(lines, "\n")
}

// blockedNote says that the system refused to run the schedules, so they
// run only while the app is open, and offers the way to allow it.
func (a *App) blockedNote(c *ui.Context) {
	if b, _ := a.svc.Background(); b != service.BackgroundBlocked {
		return
	}
	p := paletteFor(c.Theme())
	ui.Row(c).Gap(10).AlignItems(ui.Center).FillWidth().Radius(10).Padding(8, 10).
		Background(p.warningSoft).Border(1, p.warning.Alpha(0.2)).Children(func() {
		ui.Icon(c, icons.Warning).Size(14, 14).TextColor(p.warning)
		ui.Text(c, locales.ScheduleBlocked()).FontSize(sizeSmall).TextColor(p.text).
			Grow(1).MinWidth(0).LineHeight(1.4)
		if accentButton(c, "blocked-settings", nil, locales.ScheduleOpenSettings(), buttonSoft).Clicked() {
			a.host.OpenBackgroundSettings()
		}
	})
}

// scheduleText says a schedule in a few words: Daily at 21:00, Every Mon,
// Thu at 9:30, Monthly on the 1st at 03:00.
func scheduleText(c *ui.Context, s domain.Schedule) string {
	return scheduleTextIn(c.Region().FormatLocale(c.Locale()), c.Region().FirstWeekday, s)
}

// scheduleTextIn is scheduleText in the formats of locale, for a week that
// begins on first.
func scheduleTextIn(locale string, first time.Weekday, s domain.Schedule) string {
	at := i18n.FormatDate(locale, time.Date(2000, 1, 1, s.Hour, s.Minute, 0, 0, time.Local),
		i18n.DateOptions{Time: i18n.ShortStyle})
	switch s.Repeat {
	case domain.RepeatHourly:
		return locales.ScheduleEveryHours(s.Every)
	case domain.RepeatDaily:
		return locales.ScheduleDaily(at)
	case domain.RepeatWeekly:
		switch s.Days {
		case domain.EveryDay:
			return locales.ScheduleDaily(at)
		case domain.WorkDays:
			return locales.ScheduleWorkdays(at)
		case domain.Weekends:
			return locales.ScheduleWeekends(at)
		}
		names := i18n.Weekdays(locale, i18n.Abbreviated, false)
		var days []string
		for i := range 7 {
			if d := (first + time.Weekday(i)) % 7; s.Days.Has(d) {
				days = append(days, names[d])
			}
		}
		return locales.ScheduleWeekly(strings.Join(days, locales.ScheduleDaySeparator()), at)
	case domain.RepeatMonthly:
		return locales.ScheduleMonthly(s.Day, at)
	}
	return locales.TasksManual()
}
