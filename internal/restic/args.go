// Package restic builds and runs restic command lines.
//
// The builders in this file are pure functions: a resolved job in, an argv
// slice out. Keeping them free of I/O is what makes the legacy-parity test
// possible and makes --dry-run the same code path as a real run.
package restic

import (
	"strconv"
	"strings"

	"github.com/fostam/resticle/internal/config"
)

// global returns the flags that precede every restic subcommand.
func global(j *config.Job) []string {
	return []string{"--verbose", "--repo", j.Repo}
}

// BackupArgs builds `restic backup`.
// j.Backup must be non-nil; callers are responsible for checking it.
// dryRun adds restic's own --dry-run, which reports what the command would
// change without changing it. Supported by backup and forget; check needs
// nothing, being read-only already.
func BackupArgs(j *config.Job, dryRun bool) []string {
	b := j.Backup
	args := append(global(j), "backup")
	if dryRun {
		args = append(args, "--dry-run")
	}

	if b.Host != "" {
		args = append(args, "--host", b.Host)
	}
	for _, tag := range b.Tags {
		args = append(args, "--tag", tag)
	}
	for _, e := range b.Excludes {
		flag, value := excludeArg(e)
		args = append(args, flag, value)
	}
	if b.OneFileSystem != nil && *b.OneFileSystem {
		args = append(args, "--one-file-system")
	}
	// Paths come last, each its own argv element, so spaces are safe.
	return append(args, b.Paths...)
}

// ForgetArgs builds `restic forget`, optionally with --prune.
// j.Forget must be non-nil; callers are responsible for checking it.
func ForgetArgs(j *config.Job, dryRun bool) []string {
	m := j.Forget
	args := append(global(j), "forget")
	if dryRun {
		args = append(args, "--dry-run")
	}

	// Unset means the flag is omitted entirely, so restic's own default
	// applies — and keeps applying if restic ever changes it. resticle does
	// not restate another tool's defaults.
	if m.GroupBy != "" {
		args = append(args, "--group-by", m.GroupBy)
	}
	if m.CleanupCache != nil && *m.CleanupCache {
		args = append(args, "--cleanup-cache")
	}
	if k := m.Keep; k != nil {
		for _, p := range []struct {
			flag string
			n    int
		}{
			{"--keep-last", k.Last},
			{"--keep-hourly", k.Hourly},
			{"--keep-daily", k.Daily},
			{"--keep-weekly", k.Weekly},
			{"--keep-monthly", k.Monthly},
			{"--keep-yearly", k.Yearly},
		} {
			if p.n > 0 {
				args = append(args, p.flag, strconv.Itoa(p.n))
			}
		}
	}
	if m.Prune != nil && *m.Prune {
		args = append(args, "--prune")
	}
	return args
}

// CheckArgs builds `restic check` for the job's check mode. index is the
// zero-based subset index; restic's --read-data-subset is one-based, so it is
// rendered as index+1. ok is false when there is no check to run.
func CheckArgs(j *config.Job, index int) (args []string, ok bool) {
	if j.Check == nil {
		return nil, false
	}
	c := j.Check
	args = append(global(j), "check")

	switch c.Mode {
	case config.CheckOff:
		return nil, false
	case config.CheckStructure:
		return args, true
	case config.CheckFull:
		return append(args, "--read-data"), true
	case config.CheckSubset:
		subset := strconv.Itoa(index+1) + "/" + strconv.Itoa(c.Spread)
		return append(args, "--read-data-subset", subset), true
	default:
		return nil, false
	}
}

// SnapshotsArgs builds `restic snapshots --json`, used by the freshness check
// and by `resticle status`.
func SnapshotsArgs(j *config.Job) []string {
	return append(global(j), "snapshots", "--json", "--latest", "1")
}

// excludeArg renders one `excludes:` entry.
//
// An entry prefixed with "@" and an absolute path is a file of patterns
// (`--exclude-file`); anything else is a pattern (`--exclude`). The absolute
// path is what disambiguates: restic patterns starting with "@" are common —
// a NAS's `@eaDir` is excluded as `@*` — and those must stay patterns.
// A pattern that genuinely begins with "@/" is escaped as "@@/".
func excludeArg(entry string) (flag, value string) {
	switch {
	case strings.HasPrefix(entry, "@@"):
		return "--exclude", entry[1:]
	case strings.HasPrefix(entry, "@/"):
		return "--exclude-file", entry[1:]
	default:
		return "--exclude", entry
	}
}

// FindArgs builds `restic find`, the cross-repository search behind
// `resticle find`. The pattern is restic's own: a filename, a path, or a
// glob such as "*.kdbx".
func FindArgs(j *config.Job, pattern string) []string {
	return append(global(j), "find", "--long", pattern)
}
