package report

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEventLineFormat(t *testing.T) {
	var buf bytes.Buffer
	l := &Logger{Out: &buf}
	l.Event("ext", "backup started")

	got := buf.String()
	// "2026-09-12 18:25 ext backup started"
	if !strings.Contains(got, "ext backup started") {
		t.Errorf("line = %q", got)
	}
	if len(got) < 17 || got[4] != '-' || got[7] != '-' || got[13] != ':' {
		t.Errorf("line = %q, want an ISO date and 24h time prefix", got)
	}
}

// I6: a lock-held skip must fail the run; a "no ... configured" skip must not.
func TestFailedForSkip(t *testing.T) {
	lockHeld := JobResult{Skipped: "lock held", SkipIsFailure: true}
	if !lockHeld.Failed() {
		t.Error("Failed = false for a lock-held skip")
	}
	noConfig := JobResult{Skipped: "no check configured"}
	if noConfig.Failed() {
		t.Error("Failed = true for a \"no ... configured\" skip")
	}
}

func TestFailedReportsPhaseFailure(t *testing.T) {
	r := JobResult{Phases: []PhaseResult{{Name: "backup", ExitCode: 1}}}
	if !r.Failed() {
		t.Error("Failed = false for a non-zero phase exit")
	}
	ok := JobResult{Phases: []PhaseResult{{Name: "backup", ExitCode: 0}}}
	if ok.Failed() {
		t.Error("Failed = true for an all-zero result")
	}
	withErr := JobResult{Err: errors.New("mount failed")}
	if !withErr.Failed() {
		t.Error("Failed = false when Err is set")
	}
}

func TestSummaryListsEveryJobAndMarksFailures(t *testing.T) {
	var buf bytes.Buffer
	Summary(&buf, []JobResult{
		{Job: "storage", Phases: []PhaseResult{{Name: "backup", Duration: time.Minute}}},
		{Job: "ext", Err: errors.New("mount failed")},
	})
	got := buf.String()
	for _, want := range []string{"storage", "ext", "mount failed"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary missing %q:\n%s", want, got)
		}
	}
}

func TestRunHookReceivesStatusAndSummary(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "out.txt")
	script := filepath.Join(dir, "hook.sh")
	body := "#!/bin/sh\n{ echo \"job=$RESTICLE_JOB status=$RESTICLE_STATUS\"; cat; } > " + marker + "\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}

	r := JobResult{Job: "ext", Phases: []PhaseResult{{Name: "backup", ExitCode: 1}}}
	if err := RunHook(script, r); err != nil {
		t.Fatalf("RunHook: %v", err)
	}

	out, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "job=ext status=failure") {
		t.Errorf("hook env = %q", out)
	}
	if !strings.Contains(string(out), "backup") {
		t.Errorf("hook stdin did not carry the summary: %q", out)
	}
}

func TestRunHookEmptyCommandIsNoop(t *testing.T) {
	if err := RunHook("", JobResult{Job: "ext"}); err != nil {
		t.Errorf("RunHook(\"\") = %v, want nil", err)
	}
}

func TestFormatTimeUsesBerlin(t *testing.T) {
	// UTC 2026-09-12 16:25:00 should be 2026-09-12 18:25:00 in Berlin (CEST is UTC+2)
	utc := time.Date(2026, 9, 12, 16, 25, 0, 0, time.UTC)
	got := FormatTime(utc)
	want := "2026-09-12 18:25"
	if got != want {
		t.Errorf("FormatTime(%v) = %q, want %q", utc, got, want)
	}
}

func TestEventJSONFormat(t *testing.T) {
	var buf bytes.Buffer
	l := &Logger{Out: &buf, Format: FormatJSON}
	l.Event("ext", "backup started")

	var got struct {
		Time string `json:"time"`
		Job  string `json:"job"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, buf.String())
	}
	if got.Job != "ext" || got.Msg != "backup started" {
		t.Errorf("got %+v", got)
	}
	if got.Time == "" {
		t.Error("time is empty")
	}
}

func TestEventDefaultFormatIsText(t *testing.T) {
	var buf bytes.Buffer
	(&Logger{Out: &buf}).Event("ext", "hello")
	if strings.HasPrefix(buf.String(), "{") {
		t.Errorf("default format produced JSON: %s", buf.String())
	}
}

func TestSummaryJSONOneObjectPerJob(t *testing.T) {
	var buf bytes.Buffer
	SummaryJSON(&buf, []JobResult{
		{Job: "storage", Phases: []PhaseResult{{Name: "backup", ExitCode: 0, Duration: time.Minute}}},
		{Job: "ext", Phases: []PhaseResult{{Name: "backup", ExitCode: 1, Err: errors.New("boom")}}},
	})

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2:\n%s", len(lines), buf.String())
	}

	type phase struct {
		Name            string  `json:"name"`
		ExitCode        int     `json:"exit_code"`
		DurationSeconds float64 `json:"duration_seconds"`
		TimedOut        bool    `json:"timed_out"`
		Error           string  `json:"error,omitempty"`
	}
	var jobs []struct {
		Job      string  `json:"job"`
		Status   string  `json:"status"`
		Skipped  string  `json:"skipped,omitempty"`
		Snapshot string  `json:"snapshot,omitempty"`
		Error    string  `json:"error,omitempty"`
		Phases   []phase `json:"phases"`
	}
	for _, line := range lines {
		var j struct {
			Job      string  `json:"job"`
			Status   string  `json:"status"`
			Skipped  string  `json:"skipped,omitempty"`
			Snapshot string  `json:"snapshot,omitempty"`
			Error    string  `json:"error,omitempty"`
			Phases   []phase `json:"phases"`
		}
		if err := json.Unmarshal([]byte(line), &j); err != nil {
			t.Fatalf("not valid JSON: %v\n%s", err, line)
		}
		jobs = append(jobs, j)
	}

	if jobs[0].Job != "storage" || jobs[0].Status != "success" {
		t.Errorf("job 0 = %+v", jobs[0])
	}
	if len(jobs[0].Phases) != 1 || jobs[0].Phases[0].Name != "backup" || jobs[0].Phases[0].ExitCode != 0 {
		t.Errorf("job 0 phases = %+v", jobs[0].Phases)
	}

	if jobs[1].Job != "ext" || jobs[1].Status != "failure" {
		t.Errorf("job 1 = %+v", jobs[1])
	}
	if len(jobs[1].Phases) != 1 || jobs[1].Phases[0].ExitCode != 1 || jobs[1].Phases[0].Error != "boom" {
		t.Errorf("job 1 phases = %+v", jobs[1].Phases)
	}
}

func TestBerlinLocationLoaded(t *testing.T) {
	// Verify tzdata is embedded and berlin location is truly Europe/Berlin, not UTC fallback.
	if berlin.String() != "Europe/Berlin" {
		t.Errorf("berlin location = %q, want \"Europe/Berlin\"", berlin.String())
	}
}

// A dry run must never read like a real one, in the verdict or in the JSON.
func TestSummaryMarksADryRun(t *testing.T) {
	var buf bytes.Buffer
	Summary(&buf, []JobResult{{Job: "demo", DryRun: "restic dry-run"}})
	if !strings.Contains(buf.String(), "demo: success (restic dry-run, nothing changed)") {
		t.Errorf("summary = %q", buf.String())
	}

	buf.Reset()
	SummaryJSON(&buf, []JobResult{{Job: "demo", DryRun: "dry-run"}})
	var got struct {
		Status string `json:"status"`
		DryRun string `json:"dry_run"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, buf.String())
	}
	if got.DryRun != "dry-run" {
		t.Errorf("dry_run = %q, want dry-run", got.DryRun)
	}
}

func TestSummaryOfARealRunSaysNothingAboutDryRuns(t *testing.T) {
	var buf bytes.Buffer
	Summary(&buf, []JobResult{{Job: "demo"}})
	if strings.Contains(buf.String(), "dry") {
		t.Errorf("a real run mentions a dry run: %q", buf.String())
	}
}
