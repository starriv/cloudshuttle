package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/starriv/cloudshuttle/internal/domain"
)

// scheduleJSON is how the schedule column keeps a schedule:
//
//	{"repeat":"weekly","time":"21:00","days":[1,4]}
type scheduleJSON struct {
	Repeat domain.Repeat `json:"repeat"`
	Every  int           `json:"every,omitempty"`
	Time   string        `json:"time"`           // 15:04
	Days   []int         `json:"days,omitempty"` // 0 is Sunday
	Day    int           `json:"day,omitempty"`
}

// encodeSchedule returns the column's text for s: "" for none.
func encodeSchedule(s domain.Schedule) string {
	if !s.On() {
		return ""
	}
	j := scheduleJSON{Repeat: s.Repeat, Every: s.Every, Day: s.Day,
		Time: fmt.Sprintf("%02d:%02d", s.Hour, s.Minute)}
	for _, d := range s.Days.Days() {
		j.Days = append(j.Days, int(d))
	}
	b, _ := json.Marshal(j) // a struct of strings and ints always marshals
	return string(b)
}

// decodeSchedule reads the column's text.
func decodeSchedule(text string) (domain.Schedule, error) {
	if text == "" {
		return domain.Schedule{}, nil
	}
	var j scheduleJSON
	if err := json.Unmarshal([]byte(text), &j); err != nil {
		return domain.Schedule{}, fmt.Errorf("schedule %q: %w", text, err)
	}
	s := domain.Schedule{Repeat: j.Repeat, Every: j.Every, Day: j.Day}
	if _, err := fmt.Sscanf(j.Time, "%d:%d", &s.Hour, &s.Minute); err != nil {
		return domain.Schedule{}, fmt.Errorf("schedule %q: time: %w", text, err)
	}
	for _, d := range j.Days {
		if d >= 0 && d <= 6 {
			s.Days = s.Days.With(time.Weekday(d), true)
		}
	}
	return s.Normalize(), nil
}

// convertCronSchedules turns the five-field cron expressions that earlier
// versions kept into schedules. An expression that says more than a
// schedule can (every 5 minutes, only in March) becomes the daily run
// closest to it, so that a backup that ran goes on running; the log says
// which.
func convertCronSchedules(tx *sql.Tx) error {
	rows, err := tx.Query(`SELECT id, name, schedule FROM tasks WHERE schedule != ''`)
	if err != nil {
		return err
	}
	type change struct{ id, schedule string }
	var changes []change
	for rows.Next() {
		var id, name, expr string
		if err := rows.Scan(&id, &name, &expr); err != nil {
			rows.Close()
			return err
		}
		s, exact := scheduleFromCron(expr)
		if !exact {
			slog.Warn("a schedule became the closest daily one", "task", name, "cron", expr,
				"time", fmt.Sprintf("%02d:%02d", s.Hour, s.Minute))
		}
		changes = append(changes, change{id, encodeSchedule(s)})
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, c := range changes {
		if _, err := tx.Exec(`UPDATE tasks SET schedule = ? WHERE id = ?`, c.schedule, c.id); err != nil {
			return err
		}
	}
	return nil
}

// scheduleFromCron reads a cron expression as a schedule, and reports
// whether the schedule runs at the same times.
func scheduleFromCron(expr string) (domain.Schedule, bool) {
	switch strings.TrimSpace(expr) {
	case "@hourly":
		return domain.Schedule{Repeat: domain.RepeatHourly, Every: 1}, true
	case "@daily", "@midnight":
		return domain.Schedule{Repeat: domain.RepeatDaily}, true
	case "@weekly":
		return domain.Schedule{Repeat: domain.RepeatWeekly, Days: domain.WeekdaysOf(time.Sunday)}, true
	case "@monthly":
		return domain.Schedule{Repeat: domain.RepeatMonthly, Day: 1}, true
	}
	daily := domain.Schedule{Repeat: domain.RepeatDaily, Hour: 3}
	f := strings.Fields(expr)
	if len(f) != 5 {
		return daily, false
	}
	minute, okMinute := cronNumber(f[0], 0, 59)
	if okMinute {
		daily.Minute = minute
	}
	hour, okHour := cronNumber(f[1], 0, 23)
	if okHour {
		daily.Hour = hour
	}
	dom, month, dow := f[2], f[3], f[4]
	if !okMinute || month != "*" {
		return daily, false
	}
	if !okHour {
		every := 0
		switch {
		case f[1] == "*":
			every = 1
		case strings.HasPrefix(f[1], "*/"), strings.HasPrefix(f[1], "0/"):
			every, _ = strconv.Atoi(f[1][2:])
		}
		s := domain.Schedule{Repeat: domain.RepeatHourly, Every: every, Minute: minute}
		if dom == "*" && dow == "*" && s.Validate() == "" {
			return s, true
		}
		return daily, false
	}
	switch {
	case dom == "*" && dow == "*":
		return daily, true
	case dom == "*":
		days, ok := cronWeekdays(dow)
		if !ok {
			return daily, false
		}
		return domain.Schedule{Repeat: domain.RepeatWeekly, Days: days, Hour: hour, Minute: minute}, true
	case dow == "*":
		day, ok := cronNumber(dom, 1, 31)
		if !ok {
			return daily, false
		}
		return domain.Schedule{Repeat: domain.RepeatMonthly, Day: day, Hour: hour, Minute: minute}, true
	}
	return daily, false
}

// cronNumber reads a field that is one number from lo to hi.
func cronNumber(field string, lo, hi int) (int, bool) {
	n, err := strconv.Atoi(field)
	return n, err == nil && n >= lo && n <= hi
}

var cronDayNames = map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}

// cronWeekdays reads a day-of-week field of days and ranges, as 1-5 or
// MON,WED,FRI; 7 is Sunday too.
func cronWeekdays(field string) (domain.Weekdays, bool) {
	day := func(s string) (int, bool) {
		if n, ok := cronDayNames[strings.ToLower(s)]; ok {
			return n, true
		}
		n, ok := cronNumber(s, 0, 7)
		return n % 7, ok
	}
	var days domain.Weekdays
	for _, part := range strings.Split(field, ",") {
		lo, hi, isRange := strings.Cut(part, "-")
		from, ok := day(lo)
		if !ok {
			return 0, false
		}
		to := from
		if isRange {
			if to, ok = day(hi); !ok {
				return 0, false
			}
			if hi == "7" {
				to = 7
			}
		}
		if to < from {
			return 0, false
		}
		for d := from; d <= to; d++ {
			days = days.With(time.Weekday(d%7), true)
		}
	}
	return days, days != 0
}
