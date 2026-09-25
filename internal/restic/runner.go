package restic

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"syscall"
	"time"
)

// DefaultGracePeriod is how long restic gets to exit after SIGINT before it
// is killed. restic finishes writing its current pack in that window.
const DefaultGracePeriod = 30 * time.Second

// Runner executes restic.
type Runner struct {
	Bin         string
	DryRun      bool
	Quiet       bool
	Out         io.Writer
	GracePeriod time.Duration
	// RunAs, when set, runs restic through `sudo -n -u <user>`.
	RunAs string
	// Passthrough connects the child directly to the terminal (stdin,
	// stdout, stderr) instead of buffering and indenting its output; used
	// for `exec`, the verbatim restic passthrough. Result.Stdout is empty.
	Passthrough bool
}

// Result describes one finished restic invocation. A non-zero ExitCode is
// reported in Result, not as an error; err is reserved for failures to run
// the process at all.
type Result struct {
	ExitCode int
	Stdout   string
	Duration time.Duration
	TimedOut bool
}

func (r *Runner) grace() time.Duration {
	if r.GracePeriod > 0 {
		return r.GracePeriod
	}
	return DefaultGracePeriod
}

// commandLine is what actually gets executed, including any sudo prefix.
// When RunAs is set, sudo's default env_reset would otherwise strip the
// secret variables resticle added to env (RESTIC_PASSWORD, backend vars), so
// those keys are passed through --preserve-env. Only names are ever placed
// in argv, never values.
func (r *Runner) commandLine(argv, env []string) []string {
	if r.RunAs == "" {
		return append([]string{r.Bin}, argv...)
	}

	full := []string{"sudo", "-n", "-u", r.RunAs}
	if keys := addedEnvKeys(env); len(keys) > 0 {
		full = append(full, "--preserve-env="+strings.Join(keys, ","))
	}
	full = append(full, r.Bin)
	return append(full, argv...)
}

// addedEnvKeys returns the sorted, de-duplicated keys of env entries whose
// full "KEY=VALUE" string is not already present in the process environment
// — i.e. the variables the caller added on top of it.
func addedEnvKeys(env []string) []string {
	inherited := make(map[string]bool, len(os.Environ()))
	for _, kv := range os.Environ() {
		inherited[kv] = true
	}

	seen := make(map[string]bool)
	var keys []string
	for _, kv := range env {
		if inherited[kv] {
			continue
		}
		key, _, ok := strings.Cut(kv, "=")
		if !ok || seen[key] {
			continue
		}
		seen[key] = true
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// Run executes restic with argv and env. timeout of 0 means no timeout.
func (r *Runner) Run(ctx context.Context, argv, env []string, timeout time.Duration) (Result, error) {
	full := r.commandLine(argv, env)

	if r.DryRun {
		fmt.Fprintf(r.Out, "would run: %s\n", strings.Join(full, " "))
		return Result{}, nil
	}

	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, full[0], full[1:]...)
	cmd.Env = env

	// SIGINT first, SIGKILL after the grace period: restic treats SIGINT as
	// "finish the current pack and stop cleanly".
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGINT) }
	cmd.WaitDelay = r.grace()

	var buf bytes.Buffer
	if r.Passthrough {
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	} else {
		var sink io.Writer = &buf
		if !r.Quiet && r.Out != nil {
			sink = io.MultiWriter(&buf, newIndentWriter(r.Out))
		}
		cmd.Stdout = sink
		cmd.Stderr = sink
	}

	start := time.Now()
	err := cmd.Run()
	res := Result{
		Stdout:   buf.String(),
		Duration: time.Since(start),
		TimedOut: errors.Is(ctx.Err(), context.DeadlineExceeded),
	}

	var exitErr *exec.ExitError
	switch {
	case err == nil:
		res.ExitCode = 0
	case errors.As(err, &exitErr):
		res.ExitCode = exitErr.ExitCode()
	default:
		return res, fmt.Errorf("run %s: %w", full[0], err)
	}
	return res, nil
}

// IsRepoLocked reports whether restic failed because the repository is
// locked. resticle never clears such a lock automatically; the caller tells
// the user which command to run. See spec section 6.7.
func IsRepoLocked(res Result) bool {
	return res.ExitCode != 0 && strings.Contains(res.Stdout, "repository is already locked")
}

// indentWriter prefixes restic's own output so it is visually distinct from
// resticle's log lines.
type indentWriter struct {
	w       io.Writer
	atStart bool
}

func newIndentWriter(w io.Writer) io.Writer { return &indentWriter{w: w, atStart: true} }

func (iw *indentWriter) Write(p []byte) (int, error) {
	for _, b := range p {
		if iw.atStart {
			if _, err := iw.w.Write([]byte("    ")); err != nil {
				return 0, err
			}
			iw.atStart = false
		}
		if _, err := iw.w.Write([]byte{b}); err != nil {
			return 0, err
		}
		iw.atStart = b == '\n'
	}
	return len(p), nil
}
