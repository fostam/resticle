package config

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// Validate reports every configuration error it can find, rather than
// stopping at the first: a single run should surface all of them.
// Call Resolve first.
func (c *Config) Validate() []error {
	var errs []error
	if len(c.Jobs) == 0 {
		errs = append(errs, fmt.Errorf("no jobs defined"))
	}
	for _, name := range c.jobNames() {
		errs = append(errs, c.Jobs[name].validate()...)
	}
	return errs
}

func (j *Job) validate() []error {
	var errs []error
	bad := func(format string, a ...any) {
		errs = append(errs, fmt.Errorf("job %q: "+format, append([]any{j.Name}, a...)...))
	}

	if j.Repo == "" {
		bad("repo is required")
	}
	if j.Password != "" && j.PasswordFile != "" {
		bad("password and password_file are both set; use one source for the secret")
	}
	if len(j.Env) > 0 && j.Password == "" {
		bad("env needs password: put both in the config, or both in the secrets file")
	}
	if j.EnvFile != "" && j.PasswordFile == "" {
		bad("env_file requires password_file; put backend variables in the secrets file's env: block instead")
	}
	switch j.Mode {
	case ModeScheduled, ModeManual, ModeDisabled:
	default:
		bad("mode = %q, want %s, %s or %s", j.Mode, ModeScheduled, ModeManual, ModeDisabled)
	}
	if j.UnmountAlways && j.Mount == "" {
		bad("unmount_always needs a mount to unmount; set mount, or drop the flag")
	}
	if j.Backup != nil && len(j.Backup.Paths) == 0 {
		bad("backup.paths is required when backup is present")
	}
	if f := j.Forget; f != nil {
		if f.When != WhenAfter && f.When != WhenBefore {
			bad("forget.when = %q, want %q or %q", f.When, WhenAfter, WhenBefore)
		}
		// Unset means the flag is not passed and restic's own default
		// applies; when set, only restic's three groupings are valid, and
		// an empty string disables grouping.
		for _, part := range strings.Split(f.GroupBy, ",") {
			if f.GroupBy == "" {
				break
			}
			switch strings.TrimSpace(part) {
			case "host", "paths", "tags":
			default:
				bad("forget.group_by = %q: want a comma-separated combination of host, paths and tags", f.GroupBy)
			}
		}
		// restic refuses a forget with no policy ("no policy was
		// specified"), so a missing keep is not a harmless omission: it
		// fails the job on every run.
		switch {
		case f.Keep == nil:
			bad("forget.keep is required; add a retention policy, or remove the forget block to expire nothing")
		case f.Keep.isEmpty():
			bad("forget.keep has no retention set; forget would delete every snapshot")
		}
	}
	if c := j.Check; c != nil {
		switch c.Mode {
		case CheckOff, CheckStructure, CheckFull:
		case CheckSubset:
			if c.Spread < 1 {
				bad("check.spread must be at least 1 when mode is %q", CheckSubset)
			}
		default:
			bad("check.mode = %q, want one of %s/%s/%s/%s",
				c.Mode, CheckOff, CheckStructure, CheckSubset, CheckFull)
		}
	}
	return errs
}

func (k *Keep) isEmpty() bool {
	return k.Last == 0 && k.Hourly == 0 && k.Daily == 0 &&
		k.Weekly == 0 && k.Monthly == 0 && k.Yearly == 0
}

// Warnings reports configurations that are legal but suspicious.
func (c *Config) Warnings() []string {
	var out []string
	for _, name := range c.jobNames() {
		j := c.Jobs[name]
		out = append(out, j.excludeWarnings()...)
		if j.Mount == "" || !strings.HasPrefix(j.Repo, "/") {
			continue
		}
		if !strings.HasPrefix(j.Repo, strings.TrimSuffix(j.Mount, "/")+"/") {
			out = append(out, fmt.Sprintf(
				"job %q: repo %s is not under its mount %s; the repository may live on a different disk than intended",
				j.Name, j.Repo, j.Mount))
		}
	}
	return out
}

func (c *Config) jobNames() []string {
	if c.JobOrder != nil {
		return c.JobOrder
	}
	names := make([]string, 0, len(c.Jobs))
	for name := range c.Jobs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// excludeWarnings reports `excludes:` file references that cannot be read.
// restic aborts the backup on a missing exclude file, so this turns a failed
// run into something `config check` says first. It is a warning rather than
// an error so that a config can be checked on a machine that is not the
// backup host.
func (j *Job) excludeWarnings() []string {
	if j.Backup == nil {
		return nil
	}
	var out []string
	for _, e := range j.Backup.Excludes {
		if !strings.HasPrefix(e, "@/") {
			continue
		}
		path := e[1:]
		if _, err := os.Stat(path); err != nil {
			out = append(out, fmt.Sprintf("job %q: exclude file %s is not readable: %v", j.Name, path, err))
		}
	}
	return out
}
