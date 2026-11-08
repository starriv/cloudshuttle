package view

import (
	"errors"
	"fmt"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"
	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/internal/format"
	"github.com/starriv/cloudshuttle/internal/icons"
	"github.com/starriv/cloudshuttle/locales"
)

// formatTime writes t in the window's language with the user's regional
// formats: date and time, short.
func formatTime(c *ui.Context, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return i18n.FormatDate(c.Region().FormatLocale(c.Locale()), t,
		i18n.DateOptions{Date: i18n.MediumStyle, Time: i18n.ShortStyle})
}

func providerLabel(p domain.Provider) string {
	switch p {
	case domain.ProviderAWS:
		return locales.ProviderAWS()
	case domain.ProviderCloudflare:
		return locales.ProviderCloudflare()
	case domain.ProviderAlibaba:
		return locales.ProviderAlibaba()
	case domain.ProviderTencent:
		return locales.ProviderTencentCOS()
	case domain.ProviderHuawei:
		return locales.ProviderHuaweiOBS()
	case domain.ProviderMinio:
		return locales.ProviderMinio()
	}
	return locales.ProviderOther()
}

func kindLabel(k domain.Kind) string {
	if k == domain.KindSMB {
		return locales.KindSMB()
	}
	return locales.KindS3()
}

// StatusLabel returns a run's status as the word the app shows, also where
// no window draws it, as the status menu does.
func StatusLabel(s domain.Status) string {
	switch s {
	case domain.StatusRunning:
		return locales.StatusRunning()
	case domain.StatusSuccess:
		return locales.StatusSuccess()
	case domain.StatusFailed:
		return locales.StatusFailed()
	}
	return locales.StatusCanceled()
}

func triggerLabel(t domain.Trigger) string {
	if t == domain.TriggerScheduled {
		return locales.TriggerScheduled()
	}
	return locales.TriggerManual()
}

// status shows a run's status as an icon and a word, so it never rests on
// the color alone.
func status(c *ui.Context, s domain.Status) {
	statusIn(c, s, statusColor(c.Theme(), s))
}

// statusIn shows a status in color: a run in progress turns its icon.
func statusIn(c *ui.Context, s domain.Status, color ui.Color) {
	ui.Row(c).Gap(6).AlignItems(ui.Center).TextColor(color).Children(func() {
		icon := ui.Icon(c, StatusIcon(s))
		if s == domain.StatusRunning {
			icon.Rotate(icon.Loop("spin", time.Second, ui.Linear) * 360)
		}
		ui.Text(c, StatusLabel(s)).SingleLine()
	})
}

// StatusIcon returns a run's status as the icon the app shows, also where no
// window draws it, as the status menu does.
func StatusIcon(s domain.Status) *ui.SVG {
	switch s {
	case domain.StatusRunning:
		return icons.Running
	case domain.StatusSuccess:
		return icons.Success
	case domain.StatusFailed:
		return icons.Failed
	}
	return icons.Stopped
}

func statusColor(t *ui.Theme, s domain.Status) ui.Color {
	switch s {
	case domain.StatusRunning:
		return t.Accent
	case domain.StatusSuccess:
		return t.Success
	case domain.StatusFailed:
		return t.Danger
	}
	return t.TextMuted
}

func transferred(s domain.Stats) string {
	return locales.Transferred(int(s.Transfers), format.Bytes(s.Bytes))
}

// problemText says what is wrong with a field in the user's language.
func problemText(code domain.Code) string {
	switch code {
	case "":
		return ""
	case domain.CodeRequired:
		return locales.ErrorRequired()
	case domain.CodeTooLong:
		return locales.ErrorTooLong()
	case domain.CodeInvalidURL:
		return locales.ErrorInvalidUrl()
	case domain.CodeInvalidHost:
		return locales.ErrorInvalidHost()
	case domain.CodeInvalidPort:
		return locales.ErrorInvalidPort()
	case domain.CodeShareRequired:
		return locales.ErrorShareRequired()
	case domain.CodeNotAbsolute:
		return locales.ErrorNotAbsolute()
	case domain.CodeUnknownStorage:
		return locales.ErrorUnknownStorage()
	case domain.CodeSameLocation:
		return locales.ErrorSameLocation()
	case domain.CodeNested:
		return locales.ErrorNested()
	case domain.CodeInvalidPath:
		return locales.ErrorInvalidPath()
	case domain.CodeForbiddenDestination:
		return locales.ErrorForbiddenDestination()
	case domain.CodeICloudUnavailable:
		return locales.ErrorICloudUnavailable()
	case domain.CodeSharedDestination:
		return locales.ErrorSharedDestination()
	case domain.CodeNoDays:
		return locales.ErrorNoDays()
	case domain.CodeOutOfRange:
		return locales.ErrorOutOfRange()
	case domain.CodeTaken:
		return locales.ErrorNameTaken()
	}
	return locales.ErrorInvalid()
}

// runProblemText says why a run stopped before it deleted anything, for the
// person to read beside the run's technical message.
func runProblemText(problem string) string {
	switch problem {
	case domain.RunProblemDestinationChanged:
		return locales.RunProblemDestinationChanged()
	case domain.RunProblemEmptySource:
		return locales.RunProblemEmptySource()
	case domain.RunProblemLargeDelete:
		return locales.RunProblemLargeDelete()
	}
	return ""
}

// errorText says what went wrong with an action in the user's language.
func errorText(err error) string {
	switch {
	case errors.Is(err, domain.ErrNameTaken):
		return locales.ErrorNameTaken()
	case errors.Is(err, domain.ErrStorageInUse):
		return locales.StorageInUse()
	case errors.Is(err, domain.ErrAlreadyActive):
		return locales.ErrorTaskRunning()
	case errors.Is(err, domain.ErrDestinationBusy):
		return locales.ErrorDestinationBusy()
	}
	return locales.ErrorAction(err.Error())
}

// uniqueLabels makes labels that repeat unique, by numbering the repeats,
// so a select's chosen label names one option.
func uniqueLabels(labels []string) []string {
	seen := map[string]int{}
	out := make([]string, len(labels))
	for i, l := range labels {
		seen[l]++
		if n := seen[l]; n > 1 {
			l = fmt.Sprintf("%s (%d)", l, n)
		}
		out[i] = l
	}
	return out
}

func indexOf(options []string, label string) int {
	for i, o := range options {
		if o == label {
			return i
		}
	}
	return -1
}
