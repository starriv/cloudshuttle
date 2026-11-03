package launchagent

import (
	"strings"
	"testing"

	"github.com/starriv/cloudshuttle/internal/domain"
)

func TestPlist(t *testing.T) {
	a := Agent{Label: "com.example.app.scheduler", Program: "/Applications/A & B.app/Contents/MacOS/A",
		Args: []string{"--scheduler"}, BundleID: "com.example.app"}
	got := string(a.plist([]domain.Wake{{Day: -1, Weekday: -1, Hour: 21, Minute: 0}, {Day: -1, Weekday: 1, Hour: 9, Minute: 30}}))
	for _, want := range []string{
		"<key>Label</key>\n\t<string>com.example.app.scheduler</string>",
		"<string>/Applications/A &amp; B.app/Contents/MacOS/A</string>\n\t\t<string>--scheduler</string>",
		"<key>AssociatedBundleIdentifiers</key>\n\t<array>\n\t\t<string>com.example.app</string>",
		"<key>RunAtLoad</key>\n\t<true/>",
		"<dict>\n\t\t\t<key>Hour</key>\n\t\t\t<integer>21</integer>\n\t\t\t<key>Minute</key>\n\t\t\t<integer>0</integer>\n\t\t</dict>",
		"<key>Weekday</key>\n\t\t\t<integer>1</integer>\n\t\t\t<key>Hour</key>\n\t\t\t<integer>9</integer>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the plist lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "<key>Day</key>") {
		t.Errorf("a field of any value is written:\n%s", got)
	}
}
