package engine

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/rclone/rclone/fs"
)

// Line is one line of a job's log: what the app did, or what rclone said.
type Line struct {
	Time   time.Time
	Level  string // DEBUG, INFO, NOTICE, WARN, ERROR
	Source string // "app" or "rclone"
	Text   string
	// Shared marks a line of rclone's said while other jobs ran too: rclone
	// does not tell which job a line is about, so each running job gets it.
	Shared bool
}

// logs hands rclone's log lines to the jobs running. rclone logs through
// one logger for the whole process, with no context, at the level of its
// global configuration.
var logs = struct {
	sync.Mutex
	sinks map[string]func(Line) // by job ID
	muted int                   // a preview counts what a run would do, unlogged
}{sinks: map[string]func(Line){}}

func init() {
	// What a run did, file by file, is at INFO; DEBUG is rclone's own
	// workings, too much to keep for every run.
	fs.GetConfig(context.Background()).LogLevel = fs.LogLevelInfo
	fs.SetLogger(rcloneHandler{})
}

// listen hands the job's log lines to sink until the returned function is
// called.
func listen(id string, sink func(Line)) (stop func()) {
	if sink == nil {
		return func() {}
	}
	logs.Lock()
	logs.sinks[id] = sink
	logs.Unlock()
	return func() {
		logs.Lock()
		delete(logs.sinks, id)
		logs.Unlock()
	}
}

// muteRclone drops rclone's lines until the returned function is called:
// a preview counts what a run would do by running it dry, and those lines
// belong to no run. Only the rclone lines go: the app's own go through
// Job.Log.
func muteRclone() (unmute func()) {
	logs.Lock()
	logs.muted++
	logs.Unlock()
	return func() {
		logs.Lock()
		logs.muted--
		logs.Unlock()
	}
}

// rcloneHandler is the slog handler rclone logs to.
type rcloneHandler struct{ attrs []slog.Attr }

func (rcloneHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelInfo
}

func (h rcloneHandler) Handle(_ context.Context, r slog.Record) error {
	logs.Lock()
	muted := logs.muted > 0
	sinks := slices.Collect(maps.Values(logs.sinks))
	logs.Unlock()
	if muted || len(sinks) == 0 {
		return nil // a preview, or a quick look at a server, with no run to tell
	}
	line := Line{Time: r.Time, Level: levelName(r.Level), Source: "rclone", Shared: len(sinks) > 1}
	if line.Time.IsZero() {
		line.Time = time.Now()
	}
	line.Text = h.text(r)
	for _, sink := range sinks {
		sink(line)
	}
	return nil
}

// text writes a record as rclone's command line does, "object: message",
// with the attributes rclone gives besides.
func (h rcloneHandler) text(r slog.Record) string {
	var object string
	var extra []string
	add := func(a slog.Attr) bool {
		switch a.Key {
		case "object":
			object = a.Value.String()
		case "objectType":
		default:
			extra = append(extra, a.Key+"="+a.Value.String())
		}
		return true
	}
	for _, a := range h.attrs {
		add(a)
	}
	r.Attrs(add)
	var b strings.Builder
	if object != "" {
		b.WriteString(object)
		b.WriteString(": ")
	}
	b.WriteString(strings.TrimRight(r.Message, "\n"))
	for _, e := range extra {
		b.WriteString(" ")
		b.WriteString(e)
	}
	return b.String()
}

func (h rcloneHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return rcloneHandler{attrs: slices.Concat(h.attrs, attrs)}
}

func (h rcloneHandler) WithGroup(string) slog.Handler { return h }

func levelName(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "ERROR"
	case l >= slog.LevelWarn:
		return "WARN"
	case l >= fs.SlogLevelNotice:
		return "NOTICE"
	case l >= slog.LevelInfo:
		return "INFO"
	}
	return "DEBUG"
}

// appLine is a line of the app's own about a job.
func appLine(level, format string, args ...any) Line {
	return Line{Time: time.Now(), Level: level, Source: "app", Text: fmt.Sprintf(format, args...)}
}
