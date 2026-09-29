package restic

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunCapturesExitCodeAndStdout(t *testing.T) {
	var out bytes.Buffer
	r := &Runner{Bin: "/bin/sh", Out: &out}
	res, err := r.Run(context.Background(), []string{"-c", "echo hello; exit 3"}, nil, time.Minute)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3", res.ExitCode)
	}
	if !strings.Contains(res.Stdout, "hello") {
		t.Errorf("Stdout = %q, want it to contain hello", res.Stdout)
	}
	if !strings.Contains(out.String(), "hello") {
		t.Errorf("passthrough Out = %q, want it to contain hello", out.String())
	}
}

func TestRunDryRunDoesNotExecute(t *testing.T) {
	var out bytes.Buffer
	r := &Runner{Bin: "/bin/sh", DryRun: true, Out: &out}
	res, err := r.Run(context.Background(), []string{"-c", "exit 9"}, nil, time.Minute)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0 in dry-run", res.ExitCode)
	}
	if !strings.Contains(out.String(), "/bin/sh") {
		t.Errorf("dry-run should print the command line, got %q", out.String())
	}
}

func TestRunTimesOut(t *testing.T) {
	r := &Runner{Bin: "/bin/sh", Out: &bytes.Buffer{}, GracePeriod: 200 * time.Millisecond}
	start := time.Now()
	res, err := r.Run(context.Background(), []string{"-c", "sleep 30"}, nil, 300*time.Millisecond)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.TimedOut {
		t.Error("TimedOut = false, want true")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("took %v; the process was not killed promptly", elapsed)
	}
}

func TestRunQuietSuppressesPassthrough(t *testing.T) {
	var out bytes.Buffer
	r := &Runner{Bin: "/bin/sh", Out: &out, Quiet: true}
	if _, err := r.Run(context.Background(), []string{"-c", "echo noisy"}, nil, time.Minute); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "noisy") {
		t.Errorf("quiet mode passed output through: %q", out.String())
	}
}

func TestCommandLineRunAsPreservesAddedEnvKeys(t *testing.T) {
	r := &Runner{Bin: "/usr/bin/restic", RunAs: "restic"}
	env := append(os.Environ(), "RESTIC_PASSWORD=pw", "B2_ACCOUNT_ID=abc")
	full := r.commandLine([]string{"backup"}, env)

	want := []string{"sudo", "-n", "-u", "restic", "--preserve-env=B2_ACCOUNT_ID,RESTIC_PASSWORD", "/usr/bin/restic", "backup"}
	if len(full) != len(want) {
		t.Fatalf("commandLine = %v, want %v", full, want)
	}
	for i := range want {
		if full[i] != want[i] {
			t.Fatalf("commandLine = %v, want %v", full, want)
		}
	}
	for _, arg := range full {
		if strings.Contains(arg, "pw") || strings.Contains(arg, "abc") {
			t.Errorf("commandLine leaked a secret value: %q", arg)
		}
	}
}

func TestCommandLineRunAsWithoutAddedEnvOmitsFlag(t *testing.T) {
	r := &Runner{Bin: "/usr/bin/restic", RunAs: "restic"}
	full := r.commandLine([]string{"backup"}, os.Environ())
	for _, arg := range full {
		if strings.HasPrefix(arg, "--preserve-env") {
			t.Errorf("commandLine = %v, want no --preserve-env flag", full)
		}
	}
}

func TestCommandLineWithoutRunAsHasNoSudo(t *testing.T) {
	r := &Runner{Bin: "/usr/bin/restic"}
	full := r.commandLine([]string{"backup"}, nil)
	if full[0] != "/usr/bin/restic" {
		t.Errorf("commandLine[0] = %q, want %q", full[0], "/usr/bin/restic")
	}
}

// I4: Passthrough connects the child directly to the terminal and returns
// its exit code, for `exec`.
func TestPassthroughReturnsExitCode(t *testing.T) {
	r := &Runner{Bin: "/bin/sh", Passthrough: true}
	res, err := r.Run(context.Background(), []string{"-c", "exit 3"}, nil, 0)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3", res.ExitCode)
	}
}

func TestPassthroughDoesNotWriteToOut(t *testing.T) {
	// The command writes to a file rather than stdout: in passthrough mode
	// stdout is the real one, and a test must not scribble on it.
	marker := filepath.Join(t.TempDir(), "wrote")
	var out bytes.Buffer
	r := &Runner{Bin: "/bin/sh", Out: &out, Passthrough: true}
	if _, err := r.Run(context.Background(), []string{"-c", "echo hello > " + marker}, nil, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the command did not run: %v", err)
	}
	if out.String() != "" {
		t.Errorf("Out = %q, want nothing written in passthrough mode", out.String())
	}
}

func TestIsRepoLocked(t *testing.T) {
	locked := Result{ExitCode: 1, Stdout: "unable to create lock in backend: repository is already locked exclusively"}
	if !IsRepoLocked(locked) {
		t.Error("IsRepoLocked = false for a locked-repo message")
	}
	if IsRepoLocked(Result{ExitCode: 1, Stdout: "some other failure"}) {
		t.Error("IsRepoLocked = true for an unrelated failure")
	}
}

// restic draws its progress only when its stdout is a terminal, so the
// interactive path must hand it one — and must still capture what it writes,
// which is where the snapshot ID and the lock message come from.
func TestInteractiveGivesResticATerminalAndStillCaptures(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "restic")
	script := "#!/bin/sh\n" +
		"if [ -t 1 ]; then echo TTY; else echo PIPE; fi\n" +
		"echo 'to stderr' >&2\n" +
		"printf 'snapshot fe47899e saved\\n'\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name        string
		interactive bool
		want        string
	}{
		{"interactive", true, "TTY"},
		{"piped", false, "PIPE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mirror bytes.Buffer
			r := &Runner{Bin: bin, Out: &mirror, Interactive: tc.interactive}
			res, err := r.Run(context.Background(), []string{"backup"}, os.Environ(), 0)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if !strings.Contains(res.Stdout, tc.want) {
				t.Errorf("captured %q, want it to contain %q", res.Stdout, tc.want)
			}
			for _, want := range []string{"snapshot fe47899e saved", "to stderr"} {
				if !strings.Contains(res.Stdout, want) {
					t.Errorf("captured output is missing %q:\n%s", want, res.Stdout)
				}
			}
			if !strings.Contains(mirror.String(), tc.want) {
				t.Errorf("mirror got %q, want it to contain %q", mirror.String(), tc.want)
			}
		})
	}
}

// Quiet means restic's output is not shown, so there is nothing to draw on a
// terminal and the plain pipe is used.
func TestQuietDefeatsInteractive(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "restic")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nif [ -t 1 ]; then echo TTY; else echo PIPE; fi\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	var mirror bytes.Buffer
	r := &Runner{Bin: bin, Out: &mirror, Interactive: true, Quiet: true}
	res, err := r.Run(context.Background(), []string{"backup"}, os.Environ(), 0)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Stdout, "PIPE") {
		t.Errorf("captured %q, want PIPE", res.Stdout)
	}
	if mirror.Len() != 0 {
		t.Errorf("quiet run wrote %q to the mirror", mirror.String())
	}
}
