package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/starriv/cloudshuttle/internal/domain"
)

// globalExcludeCase is what one built-in rule must leave out, at the top and
// deep in a tree, and the names next to it that only look alike, which a
// run must still copy.
type globalExcludeCase struct {
	rule     string
	excluded []string
	kept     []string
}

// globalExcludeCases derives the cases from DefaultGlobalExcludes itself, so
// a rule added to the list later is checked too. A * in a rule stands for
// "x" in the sample names.
func globalExcludeCases() []globalExcludeCase {
	var cases []globalExcludeCase
	for _, rule := range domain.DefaultGlobalExcludes() {
		if folder, ok := strings.CutSuffix(rule, "/"); ok {
			name := strings.ReplaceAll(folder, "*", "x")
			c := globalExcludeCase{
				rule: rule,
				// The folder and everything below it, at any depth.
				excluded: []string{
					name + "/file",
					name + "/sub/deeper/file",
					"projects/app/" + name + "/file",
				},
				kept: []string{
					// A file with the folder's name: the rule names a folder.
					"files/" + name,
					// A name that only begins like it.
					name + "x/file",
				},
			}
			// A name that only ends like it, unless a * in the rule
			// matches that too.
			if !strings.Contains(rule, "*") {
				c.kept = append(c.kept, "projects/x"+lastSegment(name)+"/file")
			}
			cases = append(cases, c)
			continue
		}
		name := strings.ReplaceAll(rule, "*", "x")
		cases = append(cases, globalExcludeCase{
			rule:     rule,
			excluded: []string{name, "photos/2024/" + name},
			kept:     []string{"photos/" + name + ".bak", "photos/x" + name},
		})
	}
	return cases
}

func lastSegment(path string) string {
	return path[strings.LastIndex(path, "/")+1:]
}

// writeTree makes each file of names below root.
func writeTree(t *testing.T, root string, names []string, body string) {
	t.Helper()
	for _, name := range names {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func fileExists(root, name string) bool {
	_, err := os.Stat(filepath.Join(root, filepath.FromSlash(name)))
	return err == nil
}

// Every rule of the default global list leaves out exactly what it names,
// on a real copy: a file rule the file at any depth, a folder rule the
// folder and all below it at any depth, and neither a name that only looks
// alike nor a file named like a folder rule.
func TestEveryDefaultGlobalRuleLeavesOutWhatItNames(t *testing.T) {
	ctx := context.Background()
	s, _, done := newService(t)
	src, dst := t.TempDir(), t.TempDir()
	cases := globalExcludeCases()
	keptTotal := 0
	for _, c := range cases {
		writeTree(t, src, c.excluded, "excluded")
		writeTree(t, src, c.kept, "kept")
		keptTotal += len(c.kept)
	}
	task, err := s.CreateTask(ctx, domain.TaskInput{AllowRoot: true, Name: "Everything", Mode: domain.ModeCopy,
		Source: domain.Location{Path: src}, Destination: domain.Location{Path: dst},
		Exclude: domain.Exclude{}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Run(ctx, task.ID, RunOptions{Trigger: domain.TriggerManual}); err != nil {
		t.Fatal(err)
	}
	run := wait(t, done)
	if run.Status != domain.StatusSuccess {
		t.Fatalf("run = %+v", run)
	}
	for _, c := range cases {
		for _, name := range c.excluded {
			if fileExists(dst, name) {
				t.Errorf("rule %q: %s was copied", c.rule, name)
			}
		}
		for _, name := range c.kept {
			if !fileExists(dst, name) {
				t.Errorf("rule %q: %s was left out, but the rule does not name it", c.rule, name)
			}
		}
	}
	// The excluded folders are not made in the destination either, not
	// even empty.
	for _, c := range cases {
		if folder, ok := strings.CutSuffix(c.rule, "/"); ok {
			name := strings.ReplaceAll(folder, "*", "x")
			if fileExists(dst, name) {
				t.Errorf("rule %q: the folder %s was made in the destination", c.rule, name)
			}
		}
	}
	if int(run.Stats.Transfers) != keptTotal {
		t.Errorf("transfers = %d, want the %d files no rule names", run.Stats.Transfers, keptTotal)
	}
}

// A sync leaves alone what the default rules name in the destination, and
// deletes the alike names the source lacks: the rules hold on both sides.
func TestEveryDefaultGlobalRuleHoldsInTheDestinationOfASync(t *testing.T) {
	ctx := context.Background()
	s, _, done := newService(t)
	src, dst := t.TempDir(), t.TempDir()
	cases := globalExcludeCases()
	writeTree(t, src, []string{"keep.txt"}, "keep")
	for _, c := range cases {
		writeTree(t, dst, c.excluded, "theirs")
		writeTree(t, dst, c.kept, "stale")
	}
	task, err := s.CreateTask(ctx, domain.TaskInput{AllowRoot: true, Name: "Mirror", Mode: domain.ModeSync,
		Source: domain.Location{Path: src}, Destination: domain.Location{Path: dst}})
	if err != nil {
		t.Fatal(err)
	}
	// The source holds a single file, so the delete check would stop the
	// run: this test is about the rules, not about that check.
	if _, err := s.Run(ctx, task.ID, RunOptions{Trigger: domain.TriggerManual, Force: true}); err != nil {
		t.Fatal(err)
	}
	if run := wait(t, done); run.Status != domain.StatusSuccess {
		t.Fatalf("run = %+v", run)
	}
	for _, c := range cases {
		for _, name := range c.excluded {
			if !fileExists(dst, name) {
				t.Errorf("rule %q: the sync deleted %s, which the rule leaves alone", c.rule, name)
			}
		}
		for _, name := range c.kept {
			if fileExists(dst, name) {
				t.Errorf("rule %q: the sync kept %s, which the rule does not name", c.rule, name)
			}
		}
	}
}
