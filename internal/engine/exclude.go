package engine

import (
	"context"
	"strings"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/filter"
)

// ExcludeRule is rclone's filter rule for a pattern of a task: a pattern
// ending with a slash names a folder, which rclone matches as the folder
// and all below it.
func ExcludeRule(pattern string) string {
	if strings.HasSuffix(pattern, "/") {
		pattern += "**"
	}
	return "- " + pattern
}

// ValidExclude reports whether rclone takes pattern as a glob.
func ValidExclude(pattern string) bool {
	fi, err := filter.NewFilter(nil)
	if err != nil {
		return false
	}
	return fi.AddRule(ExcludeRule(pattern)) == nil
}

// withFilter makes the run's filter: the trash, then the job's patterns and
// its size limit. It applies to both sides, so a sync leaves alone in the
// destination what it does not copy.
func withFilter(ctx context.Context, job Job) (context.Context, error) {
	fi, err := filter.NewFilter(nil)
	if err != nil {
		return ctx, err
	}
	rules := []string{"- /" + TrashRoot + "/**"}
	for _, p := range job.Exclude {
		rules = append(rules, ExcludeRule(p))
	}
	for _, rule := range rules {
		if err := fi.AddRule(rule); err != nil {
			return ctx, err
		}
	}
	if job.MaxSize > 0 {
		fi.Opt.MaxSize = fs.SizeSuffix(job.MaxSize)
	}
	return filter.ReplaceConfig(ctx, fi), nil
}
