package config

import (
	"testing"
	"time"
)

func resolved(t *testing.T, body string) *Config {
	t.Helper()
	cfg, err := Load(writeTemp(t, body))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.Resolve()
	return cfg
}

func TestResolveInheritsScalars(t *testing.T) {
	cfg := resolved(t, `
defaults:
  run_as: root
  backup:
    host: alpha
    one_file_system: true
jobs:
  ext:
    repo: /r
    backup:
      paths: [/srv]
`)
	j := cfg.Jobs["ext"]
	if j.Backup.Host != "alpha" {
		t.Errorf("backup.Host = %q, want alpha", j.Backup.Host)
	}
	if j.RunAs != "root" {
		t.Errorf("RunAs = %q, want root", j.RunAs)
	}
	if j.Backup.OneFileSystem == nil || !*j.Backup.OneFileSystem {
		t.Error("backup.OneFileSystem not inherited")
	}
}

// defaults.backup describes how a backup is taken, never whether one
// happens: a maintenance-only job must not grow a backup block.
func TestResolveDefaultsBackupDoesNotCreateOne(t *testing.T) {
	cfg := resolved(t, `
defaults:
  backup:
    host: alpha
    paths: [/srv]
jobs:
  nas:
    repo: /r
`)
	if cfg.Jobs["nas"].Backup != nil {
		t.Errorf("maintenance-only job grew a backup block: %+v", cfg.Jobs["nas"].Backup)
	}
}

func TestResolveJobOverridesDefault(t *testing.T) {
	cfg := resolved(t, `
defaults:
  run_as: root
  backup:
    host: alpha
jobs:
  nas:
    repo: /r
    run_as: restic
    backup:
      paths: [/srv]
      host: nas
`)
	if got := cfg.Jobs["nas"].RunAs; got != "restic" {
		t.Errorf("RunAs = %q, want restic", got)
	}
	if got := cfg.Jobs["nas"].Backup.Host; got != "nas" {
		t.Errorf("backup.Host = %q, want the job's own nas", got)
	}
}

func TestResolveReplacesListsNeverMerges(t *testing.T) {
	cfg := resolved(t, `
defaults:
  backup:
    excludes: ["@/general.txt"]
jobs:
  a:
    repo: /r
    backup:
      paths: [/srv]
  b:
    repo: /r
    backup:
      paths: [/srv]
      excludes: ["@/only-mine.txt"]
`)
	a := cfg.Jobs["a"].Backup.Excludes
	if len(a) != 1 || a[0] != "@/general.txt" {
		t.Errorf("job a excludes = %v", a)
	}
	b := cfg.Jobs["b"].Backup.Excludes
	if len(b) != 1 || b[0] != "@/only-mine.txt" {
		t.Errorf("job b excludes = %v, want replacement not merge", b)
	}
}

// Each phase's timeout lives in that phase's block and inherits from the
// matching defaults block: a job overriding its backup timeout keeps the
// inherited maintenance one.
func TestResolveMergesTimeoutsPerPhase(t *testing.T) {
	cfg := resolved(t, `
defaults:
  backup:
    timeout: 16h
  forget:
    timeout: 14h
    keep: {last: 1}
jobs:
  backblaze:
    repo: /r
    backup:
      paths: [/srv]
      timeout: 12h
    forget: {}
`)
	j := cfg.Jobs["backblaze"]
	if got := j.Backup.Timeout.Std(); got != 12*time.Hour {
		t.Errorf("backup.timeout = %v, want 12h", got)
	}
	if got := j.Forget.Timeout.Std(); got != 14*time.Hour {
		t.Errorf("forget.timeout = %v, want inherited 14h", got)
	}
}

// The regression this guards: ext sets keep.last=2 and must NOT inherit
// daily/weekly/monthly/yearly, or "keep-last 2" silently becomes a full
// retention policy. See spec section 5.1.
func TestResolveReplacesKeepWholesale(t *testing.T) {
	cfg := resolved(t, `
defaults:
  forget:
    keep: {last: 15, daily: 21, weekly: 12, monthly: 12, yearly: 10}
    group_by: tags
  check: {mode: subset, spread: 28}
jobs:
  ext:
    repo: /r
    forget:
      keep: {last: 2}
    check: {}
`)
	m := cfg.Jobs["ext"].Forget
	if m.Keep.Last != 2 {
		t.Errorf("keep.last = %d, want 2", m.Keep.Last)
	}
	if m.Keep.Daily != 0 {
		t.Errorf("keep.daily = %d, want 0 (policy replaced wholesale)", m.Keep.Daily)
	}
	if m.Keep.Yearly != 0 {
		t.Errorf("keep.yearly = %d, want 0", m.Keep.Yearly)
	}
	if m.GroupBy != "tags" {
		t.Errorf("group-by = %q, want inherited tags", m.GroupBy)
	}
	if c := cfg.Jobs["ext"].Check; c == nil || c.Spread != 28 {
		t.Error("check not inherited alongside a replaced keep")
	}
}

func TestResolveDefaultsSecretsToJobName(t *testing.T) {
	cfg := resolved(t, `
jobs:
  storage:
    repo: /r
`)
	if got := cfg.Jobs["storage"].Secrets; got != "storage" {
		t.Errorf("Secrets = %q, want storage", got)
	}
}

func TestResolveDefaultsWhenToAfter(t *testing.T) {
	cfg := resolved(t, `
jobs:
  a:
    repo: /r
    forget:
      keep: {last: 1}
`)
	if got := cfg.Jobs["a"].Forget.When; got != WhenAfter {
		t.Errorf("when = %q, want after", got)
	}
}

// Controller ruling test: a config WITH NO defaults block still inherits
// backup fields from job-level fields when Backup != nil.
func TestResolveInheritsMode(t *testing.T) {
	cfg := resolved(t, `
defaults:
  mode: manual
jobs:
  a:
    repo: /r
  b:
    repo: /r
    mode: scheduled
`)
	if got := cfg.Jobs["a"].Mode; got != ModeManual {
		t.Errorf("job a mode = %q, want inherited %q", got, ModeManual)
	}
	// Unlike the boolean it replaces, a job can override a non-default
	// inherited value back to the default.
	if got := cfg.Jobs["b"].Mode; got != ModeScheduled {
		t.Errorf("job b mode = %q, want its own %q", got, ModeScheduled)
	}
}

func TestResolveDefaultsModeToScheduled(t *testing.T) {
	cfg := resolved(t, `
jobs:
  a:
    repo: /r
`)
	if got := cfg.Jobs["a"].Mode; got != ModeScheduled {
		t.Errorf("mode = %q, want %q", got, ModeScheduled)
	}
}

// The executable is a job setting so that one repository can be reached
// through a wrapper the others don't need.
func TestResolveResticExecutable(t *testing.T) {
	cfg := resolved(t, `
defaults:
  restic_executable: /usr/local/bin/restic
jobs:
  plain:
    repo: /r
  tunnelled:
    repo: /r
    restic_executable: /usr/local/bin/restic-via-ssh
  bare:
    repo: /r
`)
	if got := cfg.Jobs["plain"].ResticExecutable; got != "/usr/local/bin/restic" {
		t.Errorf("plain = %q, want the inherited default", got)
	}
	if got := cfg.Jobs["tunnelled"].ResticExecutable; got != "/usr/local/bin/restic-via-ssh" {
		t.Errorf("tunnelled = %q, want its own", got)
	}
	_ = cfg.Jobs["bare"]
}

func TestResolveDefaultsResticExecutableToPath(t *testing.T) {
	cfg := resolved(t, `
jobs:
  a:
    repo: /r
`)
	if got := cfg.Jobs["a"].ResticExecutable; got != "restic" {
		t.Errorf("ResticExecutable = %q, want %q", got, "restic")
	}
}

// The two phases carry their own timeouts because they differ by orders of
// magnitude: expiring takes minutes, reading data back takes hours.
func TestResolveSeparateForgetAndCheckTimeouts(t *testing.T) {
	cfg := resolved(t, `
defaults:
  forget:
    timeout: 1h
    keep: {last: 1}
  check:
    timeout: 6h
    mode: subset
    spread: 7
jobs:
  a:
    repo: /r
    forget: {}
    check: {}
  b:
    repo: /r
    forget: {}
    check: {timeout: 30m, mode: structure}
`)
	a := cfg.Jobs["a"]
	if got := a.Forget.Timeout.Std(); got != time.Hour {
		t.Errorf("a forget.timeout = %v, want 1h", got)
	}
	if got := a.Check.Timeout.Std(); got != 6*time.Hour {
		t.Errorf("a check.timeout = %v, want 6h", got)
	}
	b := cfg.Jobs["b"]
	if got := b.Check.Timeout.Std(); got != 30*time.Minute {
		t.Errorf("b check.timeout = %v, want its own 30m", got)
	}
	if got := b.Forget.Timeout.Std(); got != time.Hour {
		t.Errorf("b forget.timeout = %v, want the inherited 1h", got)
	}
}

// Defaults fill a block, they never create one: a job runs the phases it
// declares, so it can be read without consulting defaults. This is the same
// rule backup: follows, and it is what makes "expire nothing" expressible
// even when defaults carry a retention policy.
func TestResolveDefaultsDoNotCreatePhases(t *testing.T) {
	cfg := resolved(t, `
defaults:
  forget: {keep: {last: 5}}
  check: {mode: structure}
jobs:
  declares:
    repo: /r
    forget: {}
    check: {}
  declares-nothing:
    repo: /r
`)
	d := cfg.Jobs["declares"]
	if d.Forget == nil || d.Forget.Keep == nil || d.Forget.Keep.Last != 5 {
		t.Errorf("an empty forget block did not inherit: %+v", d.Forget)
	}
	if d.Check == nil || d.Check.Mode != CheckStructure {
		t.Errorf("an empty check block did not inherit: %+v", d.Check)
	}

	n := cfg.Jobs["declares-nothing"]
	if n.Forget != nil {
		t.Errorf("defaults created a forget phase: %+v", n.Forget)
	}
	if n.Check != nil {
		t.Errorf("defaults created a check phase: %+v", n.Check)
	}
}
