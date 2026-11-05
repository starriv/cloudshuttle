package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/internal/engine"
)

// maxRunLog is how much of a run's log is kept: past it, lines are counted
// only.
const maxRunLog = 1 << 20

// runLog is a run's log as it is written, one line a record:
//
//	12:08:15.123 INFO   rclone  photos/a.jpg: Copied (new) size=5
//
// Its text is technical, as rclone's is: it is shown as it is, untranslated.
type runLog struct {
	mu      sync.Mutex
	b       strings.Builder
	written int // the lines kept, for a view to tell whether it changed
	dropped int
	shared  bool // some of rclone's lines came while other runs ran too
}

func (l *runLog) add(line engine.Line) {
	l.mu.Lock()
	defer l.mu.Unlock()
	source := line.Source
	if line.Shared {
		source += "*"
		l.shared = true
	}
	text := fmt.Sprintf("%s %-6s %-7s %s\n", line.Time.Format("15:04:05.000"), line.Level, source, line.Text)
	if l.b.Len()+len(text) > maxRunLog {
		l.dropped++
		return
	}
	l.b.WriteString(text)
	l.written++
}

// app adds a line of the service's own.
func (l *runLog) app(at time.Time, level, format string, args ...any) {
	l.add(engine.Line{Time: at, Level: level, Source: "app", Text: fmt.Sprintf(format, args...)})
}

// Live returns the log so far and how many lines it holds, for a view that
// reads it as the run goes. The notes String adds are for the stored log.
func (l *runLog) Live() (string, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String(), l.written
}

func (l *runLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var b strings.Builder
	if l.shared {
		b.WriteString("note: other runs ran at the same time; rclone does not say which run a line is about, so the lines marked rclone* may be theirs\n")
	}
	b.WriteString(l.b.String())
	if l.dropped > 0 {
		fmt.Fprintf(&b, "note: %d more lines were not kept, past %d bytes\n", l.dropped, maxRunLog)
	}
	return b.String()
}

// RunLog returns the log of the run with id: as far as it goes for a run in
// progress. A run from before logs were kept has none: domain.ErrNotFound.
func (s *Service) RunLog(ctx context.Context, id string) (string, int, error) {
	s.mu.Lock()
	for _, a := range s.active {
		if a.Run.ID == id {
			log := a.log
			s.mu.Unlock()
			text, lines := log.Live()
			return text, lines, nil
		}
	}
	s.mu.Unlock()
	if text, lines, ok := s.foreignLog(ctx, id); ok {
		return text, lines, nil
	}
	log, err := s.store.RunLog(ctx, id)
	if errors.Is(err, domain.ErrNotFound) {
		return "", 0, domain.ErrNotFound
	}
	return log, 0, err
}

// RunDeletes returns what the run with id, a sync the delete check stopped,
// would have deleted; domain.ErrNotFound for any other run.
func (s *Service) RunDeletes(ctx context.Context, id string) (domain.StoppedDeletes, error) {
	return s.store.RunDeletes(ctx, id)
}
