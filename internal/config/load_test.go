package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadParsesJobsAndDurations(t *testing.T) {
	p := writeTemp(t, `
jobs:
  ext:
    restic_executable: /usr/local/bin/restic
    repo: /mnt/usb/ext
    mount: /mnt/usb
    backup:
      timeout: 16h
      paths: [/srv]
      tags: [ext]
    forget:
      keep: {last: 2}
    check: {mode: subset, spread: 28}
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	j, ok := cfg.Jobs["ext"]
	if !ok {
		t.Fatal("job ext missing")
	}
	if j.ResticExecutable != "/usr/local/bin/restic" {
		t.Errorf("ResticExecutable = %q", j.ResticExecutable)
	}
	if j.Name != "ext" {
		t.Errorf("Name = %q, want ext", j.Name)
	}
	if got := j.Backup.Timeout.Std(); got != 16*time.Hour {
		t.Errorf("backup.timeout = %v, want 16h", got)
	}
	if j.Forget.Keep.Last != 2 {
		t.Errorf("keep.last = %d, want 2", j.Forget.Keep.Last)
	}
	if j.Check.Spread != 28 {
		t.Errorf("check.spread = %d, want 28", j.Check.Spread)
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	p := writeTemp(t, `
jobs:
  ext:
    repo: /mnt/usb/ext
    repoo: typo
`)
	if _, err := Load(p); err == nil {
		t.Fatal("expected error for unknown field, got nil")
	}
}

func TestLoadRecordsJobOrder(t *testing.T) {
	p := writeTemp(t, `
jobs:
  zeta:
    repo: /r
  alpha:
    repo: /r
  mid:
    repo: /r
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"zeta", "alpha", "mid"}
	if len(cfg.JobOrder) != len(want) {
		t.Fatalf("JobOrder = %v, want %v", cfg.JobOrder, want)
	}
	for i, name := range want {
		if cfg.JobOrder[i] != name {
			t.Errorf("JobOrder = %v, want %v", cfg.JobOrder, want)
			break
		}
	}
}

func TestLoadRejectsBadDuration(t *testing.T) {
	p := writeTemp(t, `
jobs:
  ext:
    repo: /mnt/usb/ext
    max_age: sometime
`)
	if _, err := Load(p); err == nil {
		t.Fatal("expected error for bad duration, got nil")
	}
}
