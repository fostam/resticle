// Package report turns job outcomes into log lines, summaries and hook calls.
package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
	_ "time/tzdata" // Embed zoneinfo so a TZ name resolves in a static binary on a host without tzdata.

	"github.com/fostam/resticle/internal/mount"
)

// local is the timezone for all human-readable timestamps: the host's own, so
// a log line matches the clock of the machine that wrote it and the other logs
// next to it. Go resolves it from TZ, else /etc/localtime, and falls back to
// UTC when neither says anything.
var local = time.Local

const timeLayout = "2006-01-02 15:04:05"

// FormatTime renders t for human-readable output in the host's timezone,
// "2006-01-02 15:04:05". Seconds are there because phases and hooks are
// often shorter than a minute, and a log whose lines all carry the same
// timestamp cannot be read in order.
func FormatTime(t time.Time) string {
	return t.In(local).Format(timeLayout)
}

type PhaseResult struct {
	Name     string
	ExitCode int
	Duration time.Duration
	TimedOut bool
	Err      error
}

type JobResult struct {
	Job         string
	Started     time.Time
	Phases      []PhaseResult
	Snapshot    string
	UsageBefore *mount.Usage
	UsageAfter  *mount.Usage
	Err         error
	Skipped     string // non-empty when the job did not run, e.g. "lock held"
	// Warnings are conditions worth reporting that did not stop the job: a
	// repository whose filesystem is nearly full, say. They never make
	// Failed true, but they do make a run worth printing — see HasWarnings.
	Warnings []string

	// DryRun names the mode when the run changed nothing, so a log or a
	// mailed summary cannot be mistaken for a real backup.
	DryRun string
	// SkipIsFailure marks a skip that should fail the run, e.g. a held lock
	// (an overlapping run exceeded its interval and cron should mail).
	// "no ... configured" skips leave this false and exit 0.
	SkipIsFailure bool
}

// HasWarnings reports whether any job warned, which the caller turns into an
// exit code of its own and into output a quiet run still prints.
func HasWarnings(results []JobResult) bool {
	for _, r := range results {
		if len(r.Warnings) > 0 {
			return true
		}
	}
	return false
}

func (r JobResult) Failed() bool {
	if r.Err != nil {
		return true
	}
	if r.Skipped != "" && r.SkipIsFailure {
		return true
	}
	for _, p := range r.Phases {
		if p.ExitCode != 0 || p.Err != nil {
			return true
		}
	}
	return false
}

func (r JobResult) Status() string {
	switch {
	case r.Skipped != "":
		return "skipped"
	case r.Failed():
		return "failure"
	default:
		return "success"
	}
}

// Log formats.
const (
	FormatText = "text"
	FormatJSON = "json"
)

// Logger writes resticle's own event lines.
type Logger struct {
	Out    io.Writer
	Format string // FormatText (default) or FormatJSON
}

// Info logs a line that belongs to the invocation rather than to a job, such
// as the version banner.
func (l *Logger) Info(msg string, a ...any) { l.Event("", msg, a...) }

func (l *Logger) Event(job, msg string, a ...any) {
	if l == nil || l.Out == nil {
		return
	}
	if len(a) > 0 {
		msg = fmt.Sprintf(msg, a...)
	}
	now := time.Now().In(local)

	if l.Format == FormatJSON {
		line, err := json.Marshal(struct {
			Time string `json:"time"`
			Job  string `json:"job"`
			Msg  string `json:"msg"`
		}{now.Format(time.RFC3339), job, msg})
		if err == nil {
			fmt.Fprintf(l.Out, "%s\n", line)
			return
		}
		// fall through to text if marshalling somehow fails
	}
	if job == "" {
		fmt.Fprintf(l.Out, "%s %s\n", now.Format(timeLayout), msg)
		return
	}
	fmt.Fprintf(l.Out, "%s %s %s\n", now.Format(timeLayout), job, msg)
}

// Summary renders one block per job, then a one-line verdict.
func Summary(w io.Writer, results []JobResult) {
	failed, warned := 0, 0
	for _, r := range results {
		fmt.Fprintf(w, "\n%s: %s%s\n", r.Job, r.Status(), r.dryRunSuffix())
		if r.Skipped != "" {
			fmt.Fprintf(w, "  skipped: %s\n", r.Skipped)
		}
		for _, p := range r.Phases {
			line := fmt.Sprintf("  %-12s exit=%d duration=%s", p.Name, p.ExitCode, p.Duration.Round(time.Second))
			if p.TimedOut {
				line += " TIMED OUT"
			}
			fmt.Fprintln(w, line)
			if p.Err != nil {
				fmt.Fprintf(w, "    error: %v\n", p.Err)
			}
		}
		if r.Snapshot != "" {
			fmt.Fprintf(w, "  snapshot     %s\n", r.Snapshot)
		}
		if r.UsageBefore != nil {
			fmt.Fprintf(w, "  space before %s\n", r.UsageBefore)
		}
		if r.UsageAfter != nil {
			fmt.Fprintf(w, "  space after  %s\n", r.UsageAfter)
		}
		for _, warn := range r.Warnings {
			fmt.Fprintf(w, "  warning: %s\n", warn)
		}
		if r.Err != nil {
			fmt.Fprintf(w, "  error: %v\n", r.Err)
		}
		if r.Failed() {
			failed++
		}
		warned += len(r.Warnings)
	}
	tally := fmt.Sprintf("\n%d job(s), %d failed", len(results), failed)
	if warned > 0 {
		tally += fmt.Sprintf(", %d warning(s)", warned)
	}
	fmt.Fprintln(w, tally)
}

// SummaryJSON writes one JSON object per job, one per line, for
// --log-format=json. Unlike Summary, it prints no verdict line.
func SummaryJSON(w io.Writer, results []JobResult) {
	type phase struct {
		Name            string  `json:"name"`
		ExitCode        int     `json:"exit_code"`
		DurationSeconds float64 `json:"duration_seconds"`
		TimedOut        bool    `json:"timed_out"`
		Error           string  `json:"error,omitempty"`
	}
	for _, r := range results {
		phases := make([]phase, len(r.Phases))
		for i, p := range r.Phases {
			ph := phase{
				Name:            p.Name,
				ExitCode:        p.ExitCode,
				DurationSeconds: p.Duration.Seconds(),
				TimedOut:        p.TimedOut,
			}
			if p.Err != nil {
				ph.Error = p.Err.Error()
			}
			phases[i] = ph
		}
		errStr := ""
		if r.Err != nil {
			errStr = r.Err.Error()
		}
		line, err := json.Marshal(struct {
			Job      string   `json:"job"`
			Status   string   `json:"status"`
			DryRun   string   `json:"dry_run,omitempty"`
			Skipped  string   `json:"skipped,omitempty"`
			Snapshot string   `json:"snapshot,omitempty"`
			Error    string   `json:"error,omitempty"`
			Warnings []string `json:"warnings,omitempty"`
			Phases   []phase  `json:"phases"`
		}{r.Job, r.Status(), r.DryRun, r.Skipped, r.Snapshot, errStr, r.Warnings, phases})
		if err != nil {
			continue
		}
		fmt.Fprintf(w, "%s\n", line)
	}
}

func (r JobResult) dryRunSuffix() string {
	if r.DryRun == "" {
		return ""
	}
	return " (" + r.DryRun + ", nothing changed)"
}

// RunHook invokes an on_success/on_failure command with the summary on stdin
// and the job status in the environment. Notification backends live outside
// resticle; see spec section 6.6.
func RunHook(command string, r JobResult) error {
	if strings.TrimSpace(command) == "" {
		return nil
	}
	var body bytes.Buffer
	Summary(&body, []JobResult{r})

	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Stdin = &body
	cmd.Env = append(os.Environ(),
		"RESTICLE_JOB="+r.Job,
		"RESTICLE_STATUS="+r.Status(),
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("hook %q: %w: %s", command, err, strings.TrimSpace(string(out)))
	}
	return nil
}
