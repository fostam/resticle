package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validated(t *testing.T, body string) []error {
	t.Helper()
	cfg, err := Load(writeTemp(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.Resolve()
	return cfg.Validate()
}

func mustContain(t *testing.T, errs []error, want string) {
	t.Helper()
	for _, e := range errs {
		if strings.Contains(e.Error(), want) {
			return
		}
	}
	t.Errorf("no error containing %q; got %v", want, errs)
}

func TestValidateRequiresRepo(t *testing.T) {
	mustContain(t, validated(t, `
jobs:
  a:
    mount: /mnt/x
`), "repo")
}

func TestValidateRequiresBackupPaths(t *testing.T) {
	mustContain(t, validated(t, `
jobs:
  a:
    repo: /r
    backup:
      tags: [a]
`), "paths")
}

func TestValidateRejectsUnknownCheckMode(t *testing.T) {
	mustContain(t, validated(t, `
jobs:
  a:
    repo: /r
    check: {mode: sometimes}
`), "check.mode")
}

func TestValidateRequiresSpreadForSubset(t *testing.T) {
	mustContain(t, validated(t, `
jobs:
  a:
    repo: /r
    check: {mode: subset}
`), "spread")
}

func TestValidateRejectsUnknownWhen(t *testing.T) {
	mustContain(t, validated(t, `
jobs:
  a:
    repo: /r
    forget:
      when: eventually
      keep: {last: 1}
`), "when")
}

func TestValidateRejectsEmptyKeepPolicy(t *testing.T) {
	mustContain(t, validated(t, `
jobs:
  a:
    repo: /r
    forget:
      keep: {}
`), "keep")
}

// Triage MUST-FIX: env-file without password-file was silently ignored,
// so backend variables never reached restic.
func TestValidateRejectsEnvFileWithoutPasswordFile(t *testing.T) {
	mustContain(t, validated(t, `
jobs:
  a:
    repo: /r
    env_file: /some/env
`), "env_file requires password_file")
}

func TestValidateAcceptsGoodConfig(t *testing.T) {
	errs := validated(t, `
jobs:
  a:
    repo: /mnt/usb/r
    mount: /mnt/usb
    backup:
      paths: [/srv]
    forget:
      keep: {last: 2}
    check: {mode: subset, spread: 28}
`)
	if len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
}

// The warning that would have made the ext2 defect obvious: a local repo
// path that does not live under its own mountpoint. See spec defect 1.
func TestWarnsWhenRepoNotUnderMount(t *testing.T) {
	cfg, err := Load(writeTemp(t, `
jobs:
  ext2:
    repo: /mnt/backup/backup-ext2/restic/ext2
    mount: /mnt/offsite
`))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Resolve()
	warns := cfg.Warnings()
	if len(warns) == 0 || !strings.Contains(warns[0], "ext2") {
		t.Errorf("expected a warning naming ext2, got %v", warns)
	}
}

func TestNoWarningWhenRepoUnderMount(t *testing.T) {
	cfg, err := Load(writeTemp(t, `
jobs:
  ext:
    repo: /mnt/backup/backup-ext/restic/ext
    mount: /mnt/backup
`))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Resolve()
	if warns := cfg.Warnings(); len(warns) != 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
}

// A forget block without keep passes YAML validation but fails every run:
// restic refuses a policy-less forget ("no policy was specified").
func TestValidateRejectsForgetWithoutKeep(t *testing.T) {
	mustContain(t, validated(t, `
jobs:
  nas:
    repo: /r
    forget:
      group_by: tags
`), "forget.keep is required")
}

// Verifying without expiring is now expressible: a check block on its own,
// which the single maintenance block could not say.
func TestValidateAcceptsCheckWithoutForget(t *testing.T) {
	errs := validated(t, `
jobs:
  archive:
    repo: /r
    check: {mode: subset, spread: 7}
`)
	if len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
}

func TestValidateAcceptsJobWithoutMaintenanceBlock(t *testing.T) {
	errs := validated(t, `
jobs:
  webhost:
    repo: /r
    backup:
      paths: [/etc]
`)
	if len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
}

func TestWarnsWhenExcludeFileIsMissing(t *testing.T) {
	cfg, err := Load(writeTemp(t, `
jobs:
  a:
    repo: /r
    backup:
      paths: [/srv]
      excludes: ["@/nope/missing.txt", "@*", "*.iso"]
`))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Resolve()
	warns := cfg.Warnings()
	if len(warns) != 1 || !strings.Contains(warns[0], "/nope/missing.txt") {
		t.Fatalf("warnings = %v, want exactly one naming the missing file", warns)
	}
}

func TestNoWarningForReadableExcludeFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "general.txt")
	if err := os.WriteFile(f, []byte("*.tmp\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(writeTemp(t, `
jobs:
  a:
    repo: /r
    backup:
      paths: [/srv]
      excludes: ["@`+f+`"]
`))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Resolve()
	if warns := cfg.Warnings(); len(warns) != 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
}

func TestValidateRejectsUnmountAlwaysWithoutMount(t *testing.T) {
	mustContain(t, validated(t, `
jobs:
  a:
    repo: /r
    unmount_always: true
`), "unmount_always needs a mount")
}

func TestValidateRejectsUnknownMode(t *testing.T) {
	mustContain(t, validated(t, `
jobs:
  a:
    repo: /r
    mode: paused
`), "mode =")
}

func TestValidateRejectsUnknownGroupBy(t *testing.T) {
	mustContain(t, validated(t, `
jobs:
  a:
    repo: /r
    forget:
      keep: {last: 1}
      group_by: tag
`), "forget.group_by")
}

func TestValidateAcceptsGroupByCombinations(t *testing.T) {
	for _, v := range []string{"tags", "host,paths", "host,paths,tags", ""} {
		errs := validated(t, `
jobs:
  a:
    repo: /r
    forget:
      keep: {last: 1}
      group_by: "`+v+`"
`)
		if len(errs) != 0 {
			t.Errorf("group_by %q: unexpected errors: %v", v, errs)
		}
	}
}

func TestValidateRejectsTwoSecretSources(t *testing.T) {
	mustContain(t, validated(t, `
jobs:
  a:
    repo: /r
    password: inline
    password_file: /etc/resticle/a.txt
`), "use one source")
}

func TestValidateRejectsEnvWithoutPassword(t *testing.T) {
	mustContain(t, validated(t, `
jobs:
  a:
    repo: /r
    env: {B2_ACCOUNT_ID: abc}
`), "env needs password")
}

func TestValidateAcceptsInlineSecret(t *testing.T) {
	errs := validated(t, `
jobs:
  a:
    repo: /r
    password: hunter2
    env: {B2_ACCOUNT_ID: abc, B2_ACCOUNT_KEY: def}
`)
	if len(errs) != 0 {
		t.Errorf("unexpected errors: %v", errs)
	}
}
