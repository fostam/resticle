package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// configFile writes a configuration to a temporary file. Unless the body
// names them itself, state_dir and lock_dir are pointed at temporary
// directories: they are configuration keys rather than flags, and a test must
// not write to the real /var/lib/resticle or /run/resticle.
func configFile(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if !strings.Contains(body, "state_dir:") {
		body = "state_dir: " + filepath.Join(dir, "state") + "\n" +
			"lock_dir: " + filepath.Join(dir, "lock") + "\n" + body
	}
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const goodConfig = `
defaults:
  restic_executable: /bin/true
  backup:
    host: alpha
  forget:
    keep: {last: 15, daily: 21}
  check: {mode: subset, spread: 28}
jobs:
  ext:
    repo: /tmp/resticle-test-repo
    password_file: PASSWORD_FILE
    backup:
      paths: [/srv]
      tags: [ext]
    forget: {}          # declared, so it runs with the inherited settings
    check: {}
`

func withPasswordFile(t *testing.T, body string) string {
	t.Helper()
	pw := filepath.Join(t.TempDir(), "pw.txt")
	if err := os.WriteFile(pw, []byte("pw"), 0o600); err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(body, "PASSWORD_FILE", pw)
}

func TestConfigCheckAcceptsValidConfig(t *testing.T) {
	cfg := configFile(t, withPasswordFile(t, goodConfig))
	var out bytes.Buffer
	code := run([]string{"-c", cfg, "config", "check"}, &out)
	if code != 0 {
		t.Errorf("exit = %d, want 0\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "ext") {
		t.Errorf("output does not mention the job:\n%s", out.String())
	}
}

func TestConfigCheckRejectsInvalidConfigWithExit2(t *testing.T) {
	cfg := configFile(t, "jobs:\n  ext:\n    mount: /mnt/x\n")
	var out bytes.Buffer
	if code := run([]string{"-c", cfg, "config", "check"}, &out); code != 2 {
		t.Errorf("exit = %d, want 2\n%s", code, out.String())
	}
}

func TestConfigCheckPrintsRepoMountWarning(t *testing.T) {
	cfg := configFile(t, withPasswordFile(t, `
jobs:
  ext2:
    repo: /mnt/backup/backup-ext2/restic/ext2
    mount: /mnt/offsite
    password_file: PASSWORD_FILE
`))
	var out bytes.Buffer
	run([]string{"-c", cfg, "config", "check"}, &out)
	if !strings.Contains(out.String(), "not under its mount") {
		t.Errorf("expected a repo/mount warning:\n%s", out.String())
	}
}

func TestConfigCheckRedactsSecrets(t *testing.T) {
	dir := t.TempDir()
	pw := filepath.Join(dir, "pw.txt")
	if err := os.WriteFile(pw, []byte("supersecret"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := configFile(t, "jobs:\n  ext:\n    repo: /r\n    password_file: "+pw+"\n")
	var out bytes.Buffer
	run([]string{"-c", cfg, "config", "check"}, &out)
	if strings.Contains(out.String(), "supersecret") {
		t.Fatalf("secret leaked into config check output:\n%s", out.String())
	}
}

func TestDryRunPrintsCommandsAndRunsNothing(t *testing.T) {
	cfg := configFile(t, withPasswordFile(t, goodConfig))
	var out bytes.Buffer
	code := run([]string{"-c", cfg, "--dry-run", "run", "ext"}, &out)
	if code != 0 {
		t.Errorf("exit = %d, want 0\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "would run:") {
		t.Errorf("dry run printed no commands:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "backup") {
		t.Errorf("dry run did not include the backup command:\n%s", out.String())
	}
}

func TestRunAllUsesConfigOrderNotAlphabetical(t *testing.T) {
	cfg := configFile(t, withPasswordFile(t, `
jobs:
  zeta:
    repo: /repo-zeta
    password_file: PASSWORD_FILE
    backup: {paths: [/srv]}
  alpha:
    repo: /repo-alpha
    password_file: PASSWORD_FILE
    backup: {paths: [/srv]}
`))
	var out bytes.Buffer
	code := run([]string{"-c", cfg, "--dry-run", "run", "--all"}, &out)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, out.String())
	}
	zetaIdx := strings.Index(out.String(), "/repo-zeta")
	alphaIdx := strings.Index(out.String(), "/repo-alpha")
	if zetaIdx < 0 || alphaIdx < 0 || zetaIdx > alphaIdx {
		t.Errorf("expected zeta's commands before alpha's:\n%s", out.String())
	}
}

func TestUnknownJobIsExit2(t *testing.T) {
	cfg := configFile(t, withPasswordFile(t, goodConfig))
	var out bytes.Buffer
	if code := run([]string{"-c", cfg, "run", "nosuchjob"}, &out); code != 2 {
		t.Errorf("exit = %d, want 2\n%s", code, out.String())
	}
}

func TestUnknownSubcommandIsExit2(t *testing.T) {
	cfg := configFile(t, withPasswordFile(t, goodConfig))
	var out bytes.Buffer
	if code := run([]string{"-c", cfg, "frobnicate"}, &out); code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
}

func TestRunAllAcceptsNoJobNames(t *testing.T) {
	cfg := configFile(t, withPasswordFile(t, goodConfig))
	var out bytes.Buffer
	if code := run([]string{"-c", cfg, "--dry-run", "run", "--all"}, &out); code != 0 {
		t.Errorf("exit = %d, want 0\n%s", code, out.String())
	}
}

// R-P5: a dry run must not need a writable lock or state dir.
func TestDryRunNeedsNoWritableLockOrStateDir(t *testing.T) {
	cfg := configFile(t, withPasswordFile(t, goodConfig))
	var out bytes.Buffer
	code := run([]string{"-c", cfg, "--dry-run", "run", "ext"}, &out)
	if code != 0 {
		t.Errorf("exit = %d, want 0\n%s", code, out.String())
	}
}

func TestStatusWorksWithUnreadableSecret(t *testing.T) {
	dir := t.TempDir()
	pw := filepath.Join(dir, "pw.txt")
	if err := os.WriteFile(pw, []byte("pw"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := configFile(t, "jobs:\n  ext:\n    repo: /r\n    password_file: "+pw+"\n")
	var out bytes.Buffer
	code := run([]string{"-c", cfg, "status"}, &out)
	if code != 0 {
		t.Errorf("exit = %d, want 0\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "ext") {
		t.Errorf("output does not mention the job:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "secrets unavailable") {
		t.Errorf("expected a secrets-unavailable notice:\n%s", out.String())
	}
}

func TestQuietOnSuccessRunPrintsNothing(t *testing.T) {
	cfg := configFile(t, withPasswordFile(t, goodConfig))
	var out bytes.Buffer
	code := run([]string{"-c", cfg, "-q", "--quiet-on-success", "--dry-run", "run", "ext"}, &out)
	if code != 0 {
		t.Errorf("exit = %d, want 0\n%s", code, out.String())
	}
	if strings.Contains(out.String(), "job(s),") {
		t.Errorf("expected no summary verdict line:\n%s", out.String())
	}
}

func TestQuietOnSuccessCheckPrintsNothing(t *testing.T) {
	cfg := configFile(t, withPasswordFile(t, goodConfig))
	var out bytes.Buffer
	code := run([]string{"-c", cfg, "-q", "--quiet-on-success", "--dry-run", "check", "ext"}, &out)
	if code != 0 {
		t.Errorf("exit = %d, want 0\n%s", code, out.String())
	}
	if strings.Contains(out.String(), "job(s),") {
		t.Errorf("expected no summary verdict line:\n%s", out.String())
	}
}

// C2: --quiet-on-success must suppress event lines and restic passthrough
// too, not just the summary, when every job succeeds.
func TestQuietOnSuccessRealRunIsExactlyEmptyOnSuccess(t *testing.T) {
	repo := t.TempDir()
	binDir := t.TempDir()
	bin := binDir + "/restic"
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(goodConfig, "/tmp/resticle-test-repo", repo, 1)
	body = strings.Replace(body, "restic_executable: /bin/true", "restic_executable: "+bin, 1)
	cfg := configFile(t, withPasswordFile(t, body))

	var out bytes.Buffer
	code := run([]string{
		"-c", cfg, "--quiet-on-success",
		"run", "ext",
	}, &out)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, out.String())
	}
	if out.String() != "" {
		t.Errorf("output = %q, want exactly empty on success", out.String())
	}
}

// C2: on failure the buffered output (event lines and all) is flushed,
// followed by the summary.
func TestQuietOnSuccessRealRunPrintsEverythingOnFailure(t *testing.T) {
	repo := t.TempDir()
	binDir := t.TempDir()
	bin := binDir + "/restic"
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(goodConfig, "/tmp/resticle-test-repo", repo, 1)
	body = strings.Replace(body, "restic_executable: /bin/true", "restic_executable: "+bin, 1)
	cfg := configFile(t, withPasswordFile(t, body))

	var out bytes.Buffer
	code := run([]string{
		"-c", cfg, "--quiet-on-success",
		"run", "ext",
	}, &out)
	if code != 1 {
		t.Fatalf("exit = %d, want 1\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "failed") {
		t.Errorf("expected \"failed\" in output:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "job(s),") {
		t.Errorf("expected the summary in output:\n%s", out.String())
	}
}

// I6 + C2: a held lock skips the job, fails the run, and is printed even
// under --quiet-on-success.
func TestQuietOnSuccessStillPrintsLockHeldSkip(t *testing.T) {
	repo := t.TempDir()
	binDir := t.TempDir()
	bin := binDir + "/restic"
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	lockDir := t.TempDir()
	body := strings.Replace(goodConfig, "/tmp/resticle-test-repo", repo, 1)
	body = strings.Replace(body, "restic_executable: /bin/true", "restic_executable: "+bin, 1)
	// The config must name the lock directory this test holds a lock in.
	body = "state_dir: " + t.TempDir() + "\nlock_dir: " + lockDir + "\n" + body
	cfg := configFile(t, withPasswordFile(t, body))

	// Take the lock ourselves, as a concurrent resticle run would.
	lockFile := lockDir + "/ext.lock"
	if err := os.WriteFile(lockFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(lockFile, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	code := run([]string{
		"-c", cfg, "--quiet-on-success",
		"run", "ext",
	}, &out)
	if code != 1 {
		t.Fatalf("exit = %d, want 1\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "lock") {
		t.Errorf("expected the lock-held message even under --quiet-on_success:\n%s", out.String())
	}
}

func TestLogFormatJSONEmitsJSONLines(t *testing.T) {
	cfg := configFile(t, withPasswordFile(t, goodConfig))
	var out bytes.Buffer
	code := run([]string{"-c", cfg, "--dry-run", "--log-format", "json", "run", "ext"}, &out)
	if code != 0 {
		t.Fatalf("exit = %d\n%s", code, out.String())
	}
	sawJSON := false
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.HasPrefix(line, `{"time":`) {
			sawJSON = true
			break
		}
	}
	if !sawJSON {
		t.Errorf("no JSON log line in output:\n%s", out.String())
	}
}

func TestLogFormatJSONNonDryRunIsAllJSON(t *testing.T) {
	repo := t.TempDir()
	body := strings.Replace(goodConfig, "/tmp/resticle-test-repo", repo, 1)
	cfg := configFile(t, withPasswordFile(t, body))
	var out bytes.Buffer
	code := run([]string{
		"-c", cfg, "--log-format", "json",
		"run", "ext",
	}, &out)
	if code != 0 {
		t.Fatalf("exit = %d\n%s", code, out.String())
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if line == "" {
			continue
		}
		var v map[string]any
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Errorf("non-JSON line in json-mode output: %q", line)
		}
	}
}

func TestLogFormatRejectsUnknownValue(t *testing.T) {
	cfg := configFile(t, withPasswordFile(t, goodConfig))
	var out bytes.Buffer
	if code := run([]string{"-c", cfg, "--log-format", "yaml", "run", "ext"}, &out); code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
}

func TestStatusReportsEveryJob(t *testing.T) {
	cfg := configFile(t, withPasswordFile(t, goodConfig))
	var out bytes.Buffer
	if code := run([]string{"-c", cfg, "status"}, &out); code != 0 {
		t.Fatalf("exit = %d\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "ext") {
		t.Errorf("status did not mention the job:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "never") {
		t.Errorf("a job that never ran should say so:\n%s", out.String())
	}
}

func TestStatusSkipsLiveQueryWithoutSecrets(t *testing.T) {
	dir := t.TempDir()
	pw := filepath.Join(dir, "pw.txt")
	if err := os.WriteFile(pw, []byte("pw"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := configFile(t, "jobs:\n  ext:\n    repo: /r\n    password_file: "+pw+"\n")
	var out bytes.Buffer
	code := run([]string{"-c", cfg, "status"}, &out)
	if code != 0 {
		t.Fatalf("exit = %d\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "secrets unavailable") {
		t.Errorf("expected a secrets-unavailable notice:\n%s", out.String())
	}
	if strings.Contains(out.String(), "repository unreachable") {
		t.Errorf("must not attempt a live query without secrets:\n%s", out.String())
	}
}

func TestStatusDryRunStillShowsFreeSpace(t *testing.T) {
	repo := t.TempDir()
	cfg := configFile(t, withPasswordFile(t, "jobs:\n  ext:\n    repo: "+repo+"\n    password_file: PASSWORD_FILE\n"))
	var out bytes.Buffer
	code := run([]string{"-c", cfg, "--dry-run", "status"}, &out)
	if code != 0 {
		t.Fatalf("exit = %d\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "free=") {
		t.Errorf("dry-run status should still show free space for a local repo:\n%s", out.String())
	}
}

// I1: one job's missing secret must not abort every job.
const twoJobsOneMissingSecret = `
jobs:
  good:
    repo: REPO_DIR
    password_file: PASSWORD_FILE
    backup: {paths: [/srv]}
  bad:
    repo: /repo-bad
    backup: {paths: [/srv]}
`

func TestDryRunAllContinuesPastMissingSecret(t *testing.T) {
	cfg := configFile(t, withPasswordFile(t, twoJobsOneMissingSecret))
	var out bytes.Buffer
	code := run([]string{"-c", cfg, "--dry-run", "run", "--all"}, &out)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "/repo-bad") {
		t.Errorf("expected a would-run line for the job with a missing secret:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "REPO_DIR") {
		t.Errorf("expected a would-run line for the good job:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "secrets unavailable for bad") {
		t.Errorf("expected a secrets-unavailable warning for bad:\n%s", out.String())
	}
}

func TestRunAllContinuesPastMissingSecretNonDryRun(t *testing.T) {
	repo := t.TempDir()
	body := strings.Replace(twoJobsOneMissingSecret, "REPO_DIR", repo, 1)

	binDir := t.TempDir()
	marker := binDir + "/marker"
	bin := binDir + "/restic"
	script := "#!/bin/sh\ntouch " + marker + "\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := configFile(t, withPasswordFile(t, "defaults:\n  restic_executable: "+bin+"\n"+body))

	var out bytes.Buffer
	code := run([]string{
		"-c", cfg, "run", "--all",
	}, &out)
	if code != 1 {
		t.Fatalf("exit = %d, want 1\n%s", code, out.String())
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("the good job did not run: %v", err)
	}
}

func TestConfigCheckShowsUnavailableSecretAndExits2(t *testing.T) {
	cfg := configFile(t, withPasswordFile(t, twoJobsOneMissingSecret))
	var out bytes.Buffer
	code := run([]string{"-c", cfg, "config", "check"}, &out)
	if code != 2 {
		t.Errorf("exit = %d, want 2\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "good") || !strings.Contains(out.String(), "bad") {
		t.Errorf("expected both job blocks:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "unavailable") {
		t.Errorf("expected an unavailable-password line:\n%s", out.String())
	}
}

// I2: with no --secrets flag, a secrets.yaml next to the config file is used
// automatically.
func TestDefaultSecretsPathNextToConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("jobs:\n  ext:\n    repo: /r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	secretsPath := filepath.Join(dir, "secrets.yaml")
	if err := os.WriteFile(secretsPath, []byte("ext:\n  password: hunter2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	code := run([]string{"-c", cfgPath, "config", "check"}, &out)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "[redacted]") {
		t.Errorf("expected the password from the default secrets.yaml to be shown redacted:\n%s", out.String())
	}
}

// C1: flags after the subcommand must not be silently ignored.

func TestFlagsAfterSubcommandAreDryRun(t *testing.T) {
	repo := t.TempDir()
	marker := repo + "/marker"
	binDir := t.TempDir()
	bin := binDir + "/restic"
	script := "#!/bin/sh\ntouch " + marker + "\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(goodConfig, "/tmp/resticle-test-repo", repo, 1)
	body = strings.Replace(body, "restic_executable: /bin/true", "restic_executable: "+bin, 1)
	cfg := configFile(t, withPasswordFile(t, body))

	var out bytes.Buffer
	code := run([]string{"-c", cfg, "run", "--all", "--dry-run"}, &out)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "would run:") {
		t.Errorf("expected dry-run output:\n%s", out.String())
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("restic ran for real; --dry-run after the subcommand was ignored")
	}
}

func TestFlagAfterJobNameIsDryRun(t *testing.T) {
	cfg := configFile(t, withPasswordFile(t, goodConfig))
	var out bytes.Buffer
	code := run([]string{"-c", cfg, "run", "ext", "--dry-run"}, &out)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "would run:") {
		t.Errorf("expected dry-run output:\n%s", out.String())
	}
}

func TestAllWithJobNameIsExit2(t *testing.T) {
	cfg := configFile(t, withPasswordFile(t, goodConfig))
	var out bytes.Buffer
	if code := run([]string{"-c", cfg, "run", "--all", "ext"}, &out); code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
}

func TestUnknownFlagIsExit2(t *testing.T) {
	cfg := configFile(t, withPasswordFile(t, goodConfig))
	var out bytes.Buffer
	if code := run([]string{"-c", cfg, "run", "--bogus", "ext"}, &out); code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
}

func TestExecPassthroughArgsAfterDashDashAreNotParsed(t *testing.T) {
	cfg := configFile(t, withPasswordFile(t, goodConfig))
	var out bytes.Buffer
	code := run([]string{"-c", cfg, "--dry-run", "exec", "ext", "--", "snapshots", "--json", "-q"}, &out)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "snapshots --json -q") {
		t.Errorf("expected the passthrough args verbatim in the would-run line:\n%s", out.String())
	}
}

func TestFullCheckDryRunShowsReadData(t *testing.T) {
	cfg := configFile(t, withPasswordFile(t, goodConfig))
	var out bytes.Buffer
	code := run([]string{"-c", cfg, "check", "ext", "--full", "--dry-run"}, &out)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "--read-data") {
		t.Errorf("expected --read-data in the would-run line:\n%s", out.String())
	}
}

func TestExecWithNothingAfterJobNameIsExit2(t *testing.T) {
	cfg := configFile(t, withPasswordFile(t, goodConfig))
	var out bytes.Buffer
	if code := run([]string{"-c", cfg, "exec", "ext"}, &out); code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
}

func TestExecWithNothingAfterDashDashIsExit2(t *testing.T) {
	cfg := configFile(t, withPasswordFile(t, goodConfig))
	var out bytes.Buffer
	if code := run([]string{"-c", cfg, "exec", "ext", "--"}, &out); code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
}

func TestFullOnNonCheckIsExit2(t *testing.T) {
	cfg := configFile(t, withPasswordFile(t, goodConfig))
	var out bytes.Buffer
	if code := run([]string{"-c", cfg, "run", "ext", "--full"}, &out); code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
}

func TestExecDryRunPassesArgsThrough(t *testing.T) {
	cfg := configFile(t, withPasswordFile(t, goodConfig))
	var out bytes.Buffer
	code := run([]string{"-c", cfg, "--dry-run", "exec", "ext", "--", "snapshots", "--json"}, &out)
	if code != 0 {
		t.Errorf("exit = %d, want 0\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "would run:") {
		t.Errorf("expected dry-run output:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "snapshots --json") {
		t.Errorf("expected the passthrough args in output:\n%s", out.String())
	}
}

func TestVersionCommand(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"version"}, &out); code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, out.String())
	}
	if !strings.HasPrefix(out.String(), "resticle ") {
		t.Errorf("output = %q, want it to start with the program name", out.String())
	}
	// A development build records no time; it must say so rather than
	// printing an empty field.
	if !strings.Contains(out.String(), "not recorded") {
		t.Errorf("output = %q, want the unset build time spelled out", out.String())
	}
}

const manualConfig = `
defaults:
  restic_executable: /bin/true
jobs:
  auto:
    repo: /tmp/resticle-auto
    password_file: PASSWORD_FILE
    backup:
      paths: [/srv]
  ext:
    repo: /tmp/resticle-ext
    password_file: PASSWORD_FILE
    mode: manual
    backup:
      paths: [/srv]
`

func TestRunAllOmitsManualJobs(t *testing.T) {
	cfg := configFile(t, withPasswordFile(t, manualConfig))
	var out bytes.Buffer
	if code := run([]string{"-c", cfg, "--dry-run", "run", "--all"}, &out); code != 0 {
		t.Fatalf("exit = %d\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "resticle-auto") {
		t.Errorf("automatic job missing from --all:\n%s", out.String())
	}
	if strings.Contains(out.String(), "resticle-ext") {
		t.Errorf("manual job ran under --all:\n%s", out.String())
	}
	// Omitted, not skipped: a skip line would print on every scheduled run
	// and defeat --quiet-on-success.
	if strings.Contains(out.String(), "ext") && strings.Contains(out.String(), "skipped") {
		t.Errorf("manual job reported as skipped:\n%s", out.String())
	}
}

func TestManualJobRunsWhenNamed(t *testing.T) {
	cfg := configFile(t, withPasswordFile(t, manualConfig))
	var out bytes.Buffer
	if code := run([]string{"-c", cfg, "--dry-run", "run", "ext"}, &out); code != 0 {
		t.Fatalf("exit = %d\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "resticle-ext") {
		t.Errorf("named manual job did not run:\n%s", out.String())
	}
}

func TestRunAllWithOnlyManualJobsIsUsageError(t *testing.T) {
	cfg := configFile(t, withPasswordFile(t, `
defaults:
  restic_executable: /bin/true
jobs:
  ext:
    repo: /tmp/resticle-ext
    password_file: PASSWORD_FILE
    mode: manual
    backup:
      paths: [/srv]
`))
	var out bytes.Buffer
	if code := run([]string{"-c", cfg, "--dry-run", "run", "--all"}, &out); code != 2 {
		t.Errorf("exit = %d, want 2\n%s", code, out.String())
	}
}

func TestDryRunAndResticDryRunAreMutuallyExclusive(t *testing.T) {
	cfg := configFile(t, withPasswordFile(t, goodConfig))
	var out bytes.Buffer
	if code := run([]string{"-c", cfg, "--dry-run", "--restic-dry-run", "run", "ext"}, &out); code != 2 {
		t.Errorf("exit = %d, want 2\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "mutually exclusive") {
		t.Errorf("output = %q", out.String())
	}
}

const modesConfig = `
defaults:
  restic_executable: /bin/true
jobs:
  auto:
    repo: /tmp/resticle-auto
    password_file: PASSWORD_FILE
    backup:
      paths: [/srv]
  hand:
    repo: /tmp/resticle-hand
    password_file: PASSWORD_FILE
    mode: manual
    backup:
      paths: [/srv]
  off:
    repo: /tmp/resticle-off
    password_file: PASSWORD_FILE
    mode: disabled
    backup:
      paths: [/srv]
`

func TestRunAllSelectsOnlyScheduledJobs(t *testing.T) {
	cfg := configFile(t, withPasswordFile(t, modesConfig))
	var out bytes.Buffer
	if code := run([]string{"-c", cfg, "--dry-run", "run", "--all"}, &out); code != 0 {
		t.Fatalf("exit = %d\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "resticle-auto") {
		t.Errorf("scheduled job missing:\n%s", out.String())
	}
	for _, unwanted := range []string{"resticle-hand", "resticle-off"} {
		if strings.Contains(out.String(), unwanted) {
			t.Errorf("%s ran under --all:\n%s", unwanted, out.String())
		}
	}
}

// A disabled job refuses even when named: silently doing nothing would be
// worse than an error the caller can see.
func TestDisabledJobRefusesWhenNamed(t *testing.T) {
	cfg := configFile(t, withPasswordFile(t, modesConfig))
	for _, verb := range []string{"run", "backup", "maintain", "check"} {
		var out bytes.Buffer
		if code := run([]string{"-c", cfg, "--dry-run", verb, "off"}, &out); code != 2 {
			t.Errorf("%s: exit = %d, want 2\n%s", verb, code, out.String())
		}
		if !strings.Contains(out.String(), "is disabled") {
			t.Errorf("%s: output = %q", verb, out.String())
		}
	}
}

// Disabling stops resticle acting on the repository, not you inspecting it.
func TestExecWorksOnADisabledJob(t *testing.T) {
	cfg := configFile(t, withPasswordFile(t, modesConfig))
	var out bytes.Buffer
	if code := run([]string{"-c", cfg, "--dry-run", "exec", "off", "--", "snapshots"}, &out); code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "snapshots") {
		t.Errorf("output = %q", out.String())
	}
}

func TestManualJobStillRunsWhenNamed(t *testing.T) {
	cfg := configFile(t, withPasswordFile(t, modesConfig))
	var out bytes.Buffer
	if code := run([]string{"-c", cfg, "--dry-run", "run", "hand"}, &out); code != 0 {
		t.Fatalf("exit = %d\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "resticle-hand") {
		t.Errorf("named manual job did not run:\n%s", out.String())
	}
}

// findConfig points both jobs at a fake restic that reports a match only for
// the repository named "hit".
func findConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "restic")
	script := `#!/bin/sh
case "$*" in
  *hit*) echo "Found matching entries in snapshot abc1234" ;;
esac
exit 0
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	pw := filepath.Join(dir, "pw")
	if err := os.WriteFile(pw, []byte("pw"), 0o600); err != nil {
		t.Fatal(err)
	}
	return configFile(t, `
defaults:
  restic_executable: `+bin+`
jobs:
  miss:
    repo: /tmp/resticle-miss
    password_file: `+pw+`
  hit:
    repo: /tmp/resticle-hit
    password_file: `+pw+`
`)
}

func TestFindSearchesEveryJobAndReportsMatches(t *testing.T) {
	cfg := findConfig(t)
	var out bytes.Buffer
	if code := run([]string{"-c", cfg, "find", "wanted.txt"}, &out); code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, out.String())
	}
	for _, want := range []string{"=== miss", "no match", "=== hit", "Found matching entries"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out.String())
		}
	}
}

func TestFindExitsNonZeroWhenNothingMatches(t *testing.T) {
	cfg := findConfig(t)
	var out bytes.Buffer
	if code := run([]string{"-c", cfg, "find", "wanted.txt", "miss"}, &out); code != 1 {
		t.Errorf("exit = %d, want 1\n%s", code, out.String())
	}
	if strings.Contains(out.String(), "=== hit") {
		t.Errorf("job selection was ignored:\n%s", out.String())
	}
}

func TestFindWithoutPatternIsUsageError(t *testing.T) {
	cfg := findConfig(t)
	var out bytes.Buffer
	if code := run([]string{"-c", cfg, "find"}, &out); code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
}

// The three installation paths resolve flag → config → built-in default.
func TestInstallationPathsComeFromConfig(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	lockDir := filepath.Join(dir, "lock")
	pw := filepath.Join(dir, "pw")
	if err := os.WriteFile(pw, []byte("pw"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := configFile(t, `
defaults:
  restic_executable: /bin/true
state_dir: `+stateDir+`
lock_dir: `+lockDir+`
jobs:
  ext:
    repo: `+dir+`
    password_file: `+pw+`
    backup:
      paths: [/srv]
`)
	var out bytes.Buffer
	if code := run([]string{"-c", cfg, "run", "ext"}, &out); code != 0 {
		t.Fatalf("exit = %d\n%s", code, out.String())
	}
	// Proof the configured directories were used and not the defaults:
	// resticle cannot write to /var/lib/resticle or /run/resticle as a
	// normal user, so the run would have failed had it tried.
	if _, err := os.Stat(filepath.Join(stateDir, "ext.json")); err != nil {
		t.Errorf("state not written to the configured directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(lockDir, "ext.lock")); err != nil {
		t.Errorf("lock not taken in the configured directory: %v", err)
	}
}

func TestSecretsFileFromConfig(t *testing.T) {
	dir := t.TempDir()
	secrets := filepath.Join(dir, "elsewhere.yaml")
	if err := os.WriteFile(secrets, []byte("ext: {password: pw}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := configFile(t, `
defaults:
  restic_executable: /bin/true
secrets_file: `+secrets+`
jobs:
  ext:
    repo: /tmp/resticle-test
    backup:
      paths: [/srv]
`)
	var out bytes.Buffer
	if code := run([]string{"-c", cfg, "config", "check"}, &out); code != 0 {
		t.Fatalf("exit = %d\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "[redacted]") {
		t.Errorf("secret from the configured secrets_file not resolved:\n%s", out.String())
	}
}

func TestShortDryRunFlags(t *testing.T) {
	cfg := configFile(t, withPasswordFile(t, goodConfig))

	var out bytes.Buffer
	if code := run([]string{"-c", cfg, "-n", "run", "ext"}, &out); code != 0 {
		t.Fatalf("-n: exit = %d\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "would run:") {
		t.Errorf("-n did not behave as --dry-run:\n%s", out.String())
	}

	// -N mounts and opens the repository, so it cannot be exercised here;
	// that it is recognised and conflicts with -n is what this checks.
	out.Reset()
	if code := run([]string{"-c", cfg, "-n", "-N", "run", "ext"}, &out); code != 2 {
		t.Errorf("-n -N: exit = %d, want 2\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "mutually exclusive") {
		t.Errorf("output = %q", out.String())
	}
}

// Each job runs the executable it names, not one global binary.
func TestPerJobResticExecutable(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "wrapper-ran")
	wrapper := filepath.Join(dir, "restic-wrapper")
	script := "#!/bin/sh\ntouch " + marker + "\nexit 0\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	pw := filepath.Join(dir, "pw")
	if err := os.WriteFile(pw, []byte("pw"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := configFile(t, `
defaults:
  restic_executable: /bin/false
jobs:
  wrapped:
    repo: `+dir+`
    restic_executable: `+wrapper+`
    password_file: `+pw+`
    backup:
      paths: [/srv]
`)
	var out bytes.Buffer
	if code := run([]string{"-c", cfg, "run", "wrapped"}, &out); code != 0 {
		t.Fatalf("exit = %d\n%s", code, out.String())
	}
	// /bin/false would have failed the job; the wrapper leaves a marker.
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("the job's own executable was not used: %v", err)
	}
}

func TestForgetVerbRunsForgetOnly(t *testing.T) {
	cfg := configFile(t, withPasswordFile(t, goodConfig))
	var out bytes.Buffer
	if code := run([]string{"-c", cfg, "--dry-run", "forget", "ext"}, &out); code != 0 {
		t.Fatalf("exit = %d\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "forget --cleanup-cache") {
		t.Errorf("no forget command:\n%s", out.String())
	}
	for _, unwanted := range []string{" backup ", "check --read-data"} {
		if strings.Contains(out.String(), unwanted) {
			t.Errorf("forget ran %q:\n%s", unwanted, out.String())
		}
	}
}

// A config holding secrets inline is a secret file: same permission rule.
func TestInlineSecretRequiresATightConfigFile(t *testing.T) {
	body := `
jobs:
  a:
    repo: /tmp/r
    restic_executable: /bin/true
    password: hunter2
`
	cfg := configFile(t, body)
	if err := os.Chmod(cfg, 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := run([]string{"-c", cfg, "config", "check"}, &out); code != 2 {
		t.Errorf("exit = %d, want 2 for a world-readable config with a secret\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "world-readable") {
		t.Errorf("output = %q", out.String())
	}

	if err := os.Chmod(cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := run([]string{"-c", cfg, "config", "check"}, &out); code != 0 {
		t.Fatalf("exit = %d with 0600\n%s", code, out.String())
	}
	if strings.Contains(out.String(), "hunter2") {
		t.Errorf("the inline secret leaked into config check output:\n%s", out.String())
	}
}

// A config with no inline secret is not held to that rule: it holds nothing.
func TestConfigWithoutInlineSecretsNeedsNoTightPermissions(t *testing.T) {
	cfg := configFile(t, withPasswordFile(t, goodConfig))
	if err := os.Chmod(cfg, 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := run([]string{"-c", cfg, "config", "check"}, &out); code != 0 {
		t.Errorf("exit = %d\n%s", code, out.String())
	}
}

// A secret configured both inline and in the secrets file is broken config,
// so even a dry run refuses the job instead of printing its commands.
func TestDryRunFailsOnConflictingSecretSources(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	body := "state_dir: " + filepath.Join(dir, "state") + "\n" +
		"lock_dir: " + filepath.Join(dir, "lock") + "\n" +
		"jobs:\n  dup:\n    repo: /repo-dup\n    password: from-config\n" +
		"    backup: {paths: [/srv]}\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	secretsPath := filepath.Join(dir, "secrets.yaml")
	if err := os.WriteFile(secretsPath, []byte("dup:\n  password: from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	code := run([]string{"-c", cfgPath, "--dry-run", "run", "dup"}, &out)
	if code != 1 {
		t.Fatalf("exit = %d, want 1\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "use one source") {
		t.Errorf("expected the conflict reported as the failure:\n%s", out.String())
	}
	if strings.Contains(out.String(), "would run") {
		t.Errorf("dry run printed restic commands for a job with broken secrets:\n%s", out.String())
	}
}

// config dump: the merged configuration as YAML, with each job's secret
// shown where it is used rather than where it is stored.
func dumpFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	body := "state_dir: " + filepath.Join(dir, "state") + "\n" +
		"lock_dir: " + filepath.Join(dir, "lock") + "\n" +
		"defaults:\n  restic_executable: /bin/true\n  max_age: 48h\n" +
		"jobs:\n  nas:\n    repo: /repo-nas\n    backup: {paths: [/srv]}\n"
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	secretsPath := filepath.Join(dir, "secrets.yaml")
	if err := os.WriteFile(secretsPath, []byte("nas:\n  password: hunter2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfgPath
}

func TestConfigDumpRedactsAndMergesDefaults(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"-c", dumpFixture(t), "config", "dump"}, &out); code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, out.String())
	}
	got := out.String()
	if strings.Contains(got, "hunter2") {
		t.Errorf("dump leaked the secret without --reveal:\n%s", got)
	}
	for _, want := range []string{"password: '[redacted]'", "restic_executable: /bin/true", "max_age: 48h"} {
		if !strings.Contains(got, want) {
			t.Errorf("dump is missing %q:\n%s", want, got)
		}
	}
}

// The dumped YAML must be loadable again, so it can be compared against the
// config that produced it.
func TestConfigDumpRevealRoundTrips(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"-c", dumpFixture(t), "config", "dump", "--reveal"}, &out); code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "password: hunter2") {
		t.Fatalf("--reveal did not print the secret:\n%s", out.String())
	}

	dumped := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(dumped, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	var again bytes.Buffer
	if code := run([]string{"-c", dumped, "config", "check"}, &again); code != 0 {
		t.Fatalf("reloading the dump: exit = %d, want 0\n%s", code, again.String())
	}
}

func TestRevealRejectedOutsideConfigDump(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"-c", dumpFixture(t), "--reveal", "config", "check"}, &out); code != 2 {
		t.Fatalf("exit = %d, want 2\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "only valid with config dump") {
		t.Errorf("expected a --reveal usage error:\n%s", out.String())
	}
}
