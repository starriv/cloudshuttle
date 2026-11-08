package service

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/starriv/cloudshuttle/internal/domain"
	"github.com/starriv/cloudshuttle/internal/engine"
)

// DestinationExists asks the view to choose explicitly before reusing a folder.
// Directory is false for a file: that name can only be avoided, never reused.
type DestinationExists struct {
	Path      string
	Directory bool
}

func (e *DestinationExists) Error() string { return "destination exists" }

func childLocation(parent domain.Location, name string) domain.Location {
	if parent.Local() {
		parent.Path = filepath.Join(parent.Path, name)
	} else {
		parent.Path = path.Join(parent.Path, name)
	}
	return parent
}

// DestinationParent returns the selected parent, while Destination always holds
// the final path used by transfers, safety checks, history and the scheduler.
func DestinationParent(t domain.Task) domain.Location {
	l := t.Destination
	if t.DestinationFolder != "" {
		if l.Local() {
			l.Path = filepath.Dir(l.Path)
		} else {
			l.Path = path.Dir(l.Path)
			if l.Path == "." {
				l.Path = ""
			}
		}
	}
	return l
}

func (s *Service) prepareDestination(ctx context.Context, id string, in domain.TaskInput) (domain.TaskInput, error) {
	in = in.Normalize()
	// A form always submits a parent. Run/restore validation uses the stored final
	// destination directly and never calls this preparation step.
	submittedFolder := in.DestinationFolder
	in.DestinationFolder = ""
	in.DestinationPending = false
	if in.AllowRoot {
		return in, nil
	}
	storages, err := s.store.Storages(ctx)
	if err != nil {
		return in, err
	}
	// Root protection applies to the final child, not its selected parent.
	check := in
	check.Mode = domain.ModeCopy
	if err := check.Validate(storages); err != nil {
		return in, err
	}
	in, err = resolveTask(in)
	if err != nil {
		return in, err
	}
	name := path.Base(strings.ReplaceAll(in.Source.Path, "\\", "/"))
	if name == "." || name == "/" || name == "" || strings.Contains(name, ":") {
		return in, domain.Problems{{Field: domain.FieldSource, Code: domain.CodeInvalidPath}}
	}
	if id != "" {
		old, err := s.store.Task(ctx, id)
		if err != nil {
			return in, err
		}
		if !old.AllowRoot && old.Source == in.Source && (DestinationParent(old) == in.Destination || (old.Destination == in.Destination && old.DestinationFolder == submittedFolder)) {
			if !old.DestinationPending {
				in.Destination, in.DestinationFolder = old.Destination, old.DestinationFolder
				return in, nil
			}
			// Keep a numbered target fixed while allowing the user to approve a
			// collision that appeared since saving.
			in.Destination = DestinationParent(old)
			name = old.DestinationFolder
		}
	}
	loc, err := s.location(ctx, in.Destination)
	if err != nil {
		return in, err
	}
	entries, err := engine.DestinationEntries(ctx, loc)
	if err != nil {
		return in, connectionError(err)
	}
	// Saved tasks reserve their paths even before their first transfer creates a
	// directory, so two new tasks cannot independently choose the same name.
	tasks, err := s.store.Tasks(ctx)
	if err != nil {
		return in, err
	}
	for _, t := range tasks {
		if t.ID == id || t.Destination.StorageID != in.Destination.StorageID {
			continue
		}
		parent := t.Destination
		if parent.Local() {
			parent.Path = filepath.Dir(parent.Path)
		} else {
			parent.Path = path.Dir(parent.Path)
			if parent.Path == "." {
				parent.Path = ""
			}
		}
		if strings.EqualFold(parent.Path, in.Destination.Path) {
			n := path.Base(strings.ReplaceAll(t.Destination.Path, "\\", "/"))
			if _, ok := entries[strings.ToLower(n)]; !ok {
				entries[strings.ToLower(n)] = true
			}
		}
	}
	directory, exists := entries[strings.ToLower(name)]
	if exists {
		switch in.DestinationChoice {
		case "reuse":
			if !directory {
				return in, &DestinationExists{Path: childLocation(in.Destination, name).Path}
			}
		case "unique":
			base := name
			for n := 1; ; n++ {
				if err := ctx.Err(); err != nil {
					return in, err
				}
				name = fmt.Sprintf("%s%02d", base, n)
				if _, exists := entries[strings.ToLower(name)]; !exists {
					break
				}
			}
		default:
			return in, &DestinationExists{Path: childLocation(in.Destination, name).Path, Directory: directory}
		}
	}
	in.DestinationPending = !exists || in.DestinationChoice == "unique"
	in.Destination = childLocation(in.Destination, name)
	in.DestinationFolder = name
	return in, nil
}
