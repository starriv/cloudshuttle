// Package launchagent registers the app's scheduler with the system, so
// that scheduled tasks run whether the app is open or not: on macOS, a
// launch agent of the user that launchd starts at the times the schedules
// need and when the user logs in. Other systems have no scheduler here:
// Register fails with errors.ErrUnsupported, and the app runs schedules
// itself while it is open.
package launchagent

import (
	"errors"
	"fmt"
	"strings"

	"github.com/starriv/cloudshuttle/internal/domain"
)

// Agent is the job the system starts.
type Agent struct {
	// Label names the job, as com.example.app.scheduler.
	Label string
	// Program is the executable to start, and Args its arguments.
	Program string
	Args    []string
	// BundleID is the app the job belongs to, which System Settings shows
	// it under (Login Items, Allow in the Background).
	BundleID string
	// Dir is where the job's definition is written; the user's
	// ~/Library/LaunchAgents when empty.
	Dir string
}

// plist is the job's definition: started at each wake, and as it is
// loaded, which is when the user logs in, so that it runs what the
// computer missed while it was off.
func (a Agent) plist(wakes []domain.Wake) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
`)
	key := func(indent, k string) { fmt.Fprintf(&b, "%s<key>%s</key>\n", indent, escape(k)) }
	str := func(indent, v string) { fmt.Fprintf(&b, "%s<string>%s</string>\n", indent, escape(v)) }
	key("\t", "Label")
	str("\t", a.Label)
	key("\t", "ProgramArguments")
	b.WriteString("\t<array>\n")
	for _, arg := range append([]string{a.Program}, a.Args...) {
		str("\t\t", arg)
	}
	b.WriteString("\t</array>\n")
	if a.BundleID != "" {
		key("\t", "AssociatedBundleIdentifiers")
		b.WriteString("\t<array>\n")
		str("\t\t", a.BundleID)
		b.WriteString("\t</array>\n")
	}
	key("\t", "RunAtLoad")
	b.WriteString("\t<true/>\n")
	key("\t", "StartCalendarInterval")
	b.WriteString("\t<array>\n")
	for _, w := range wakes {
		b.WriteString("\t\t<dict>\n")
		for _, f := range []struct {
			name  string
			value int
		}{{"Day", w.Day}, {"Weekday", w.Weekday}, {"Hour", w.Hour}, {"Minute", w.Minute}} {
			if f.value >= 0 {
				key("\t\t\t", f.name)
				fmt.Fprintf(&b, "\t\t\t<integer>%d</integer>\n", f.value)
			}
		}
		b.WriteString("\t\t</dict>\n")
	}
	b.WriteString("\t</array>\n</dict>\n</plist>\n")
	return []byte(b.String())
}

var escaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")

func escape(s string) string { return escaper.Replace(s) }

// ErrDisabled is an agent the user does not let run in the background, in
// the system's settings.
var ErrDisabled = errors.New("the system does not let the scheduler run in the background")
