package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/fostam/resticle/internal/config"
	"github.com/fostam/resticle/internal/job"
	"github.com/fostam/resticle/internal/mount"
	"github.com/fostam/resticle/internal/report"
	"github.com/fostam/resticle/internal/restic"
	"github.com/fostam/resticle/internal/secrets"
	"github.com/fostam/resticle/internal/state"
)

const (
	phaseBackup      = job.PhaseBackup
	phaseMaintenance = job.PhaseMaintenance
	phaseForget      = job.PhaseForget
)

func stateDirDefault() string { return state.DefaultDir }
func lockDirDefault() string  { return "/run/resticle" }

// loaded bundles everything a command needs after configuration is resolved.
type loaded struct {
	cfg     *config.Config
	sec     map[string]secrets.Set
	secErrs map[string]error
	jobs    []*config.Job
}

// loadConfig reads, resolves and validates the configuration. Any failure
// here is exit code 2. Secrets are not resolved: callers that must work even
// when secrets are broken (status) use this directly.
func (g *globals) loadConfig() (*loaded, int) {
	cfg, err := config.Load(g.configPath)
	if err != nil {
		fmt.Fprintln(g.out, err)
		return nil, 2
	}
	cfg.Resolve()

	if errs := cfg.Validate(); len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintln(g.out, "config error:", e)
		}
		return nil, 2
	}

	jobs := make([]*config.Job, 0, len(cfg.JobOrder))
	for _, n := range cfg.JobOrder {
		jobs = append(jobs, cfg.Jobs[n])
	}

	g.resolvePaths(cfg)
	return &loaded{cfg: cfg, jobs: jobs}, 0
}

// resolvePaths settles the three installation paths from the configuration,
// falling back to the built-in defaults. They are deliberately not flags:
// they describe the installation, and -c already selects which installation
// an invocation belongs to. Two cron lines disagreeing about the state
// directory would silently restart the verification rotation.
func (g *globals) resolvePaths(cfg *config.Config) {
	g.secretsPath = cfg.SecretsFile
	if g.secretsPath == "" {
		g.secretsPath = defaultSecretsPath(g.configPath)
	}
	g.stateDir = cfg.StateDir
	if g.stateDir == "" {
		g.stateDir = stateDirDefault()
	}
	g.lockDir = cfg.LockDir
	if g.lockDir == "" {
		g.lockDir = lockDirDefault()
	}
}

// load is loadConfig plus secret resolution, for every command except
// status. A broken secrets FILE (unreadable/undecryptable) stays fatal here;
// a single job's own missing secret is recorded in secErrs instead and does
// not stop the others from loading (I1).
func (g *globals) load() (*loaded, int) {
	l, code := g.loadConfig()
	if code != 0 {
		return nil, code
	}

	sec, errs, err := secrets.Load(g.secretsPath, l.jobs)
	if err != nil {
		fmt.Fprintln(g.out, "secrets error:", err)
		return nil, 2
	}
	l.sec = sec
	l.secErrs = errs
	return l, 0
}

// selectJobs resolves job names from the command line. all selects every
// job, in configuration order.
func (l *loaded) selectJobs(names []string, all bool, out io.Writer) ([]*config.Job, int) {
	if all {
		// Only scheduled jobs are selected, and they are omitted rather than
		// skipped: a nightly "skipped" line would print, and so mail, for a
		// job that was never meant to run.
		var auto []*config.Job
		for _, j := range l.jobs {
			if j.Mode == config.ModeScheduled {
				auto = append(auto, j)
			}
		}
		if len(auto) == 0 {
			fmt.Fprintln(out, "no jobs to run: no job has mode "+config.ModeScheduled)
			return nil, 2
		}
		return auto, 0
	}
	if len(names) == 0 {
		fmt.Fprintln(out, "no job named; pass a job name or --all")
		return nil, 2
	}
	var out2 []*config.Job
	for _, n := range names {
		j, ok := l.cfg.Jobs[n]
		if !ok {
			fmt.Fprintf(out, "unknown job %q\n", n)
			return nil, 2
		}
		// A disabled job refuses even when named — that is the whole
		// difference from manual, and silently doing nothing would be worse.
		if j.Mode == config.ModeDisabled {
			fmt.Fprintf(out, "job %q is disabled (mode: %s); "+
				"change its mode to run it, or use `resticle exec %s -- ...` to inspect the repository\n",
				n, config.ModeDisabled, n)
			return nil, 2
		}
		out2 = append(out2, j)
	}
	return out2, 0
}

// runner builds a job.Runner for l. In a dry run nothing may touch the disk:
// job.Runner.DryRun already skips the repo-path stat, the lock, state
// writes, the freshness check and hooks, and the Mounter is swapped for an
// in-memory fake so no real mount is attempted.
func (g *globals) runner(l *loaded) *job.Runner {
	var mounter mount.Mounter = mount.System{}
	if g.dryRun {
		mounter = mount.NewFake()
	}
	return &job.Runner{
		// Bin is set per job by the pipeline: each job names its own
		// executable, so one repository can be reached through a wrapper.
		Restic: &restic.Runner{
			DryRun: g.dryRun,
			Quiet:  g.quiet || g.logFormat == report.FormatJSON,
			Out:    g.out,
		},
		Mounter:      mounter,
		Secrets:      l.sec,
		StateDir:     g.stateDir,
		LockDir:      g.lockDir,
		Log:          &report.Logger{Out: g.out, Format: g.logFormat},
		DryRun:       g.dryRun,
		ResticDryRun: g.resticDry,
	}
}

// summarize prints the run/check verdict in the configured log format: a
// text block with a final tally, or one JSON object per job with no verdict
// line, so json-mode output stays line-delimited JSON throughout.
func (g *globals) summarize(results []report.JobResult) {
	if g.logFormat == report.FormatJSON {
		report.SummaryJSON(g.out, results)
		return
	}
	report.Summary(g.out, results)
}

func cmdRun(g *globals, names []string, all bool, only string) int {
	l, code := g.load()
	if code != 0 {
		return code
	}
	jobs, code := l.selectJobs(names, all, g.out)
	if code != 0 {
		return code
	}
	return g.runAll(jobs, l, job.Options{Only: only})
}

// runAll runs jobs with opts and reports the verdict. With
// --quiet-on-success, all output of the run (event lines, restic
// passthrough, summary) goes to an in-memory buffer first: if every job
// succeeded and none were skipped, nothing reaches the real output;
// otherwise the whole buffer is flushed, followed by the summary. A skipped
// job is always visible, even a non-failing one (I6, C2).
func (g *globals) runAll(jobs []*config.Job, l *loaded, opts job.Options) int {
	// A signal must reach restic and then let the mount unwind, so the
	// context is cancelled rather than the process being killed outright.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	runG := g
	var buf bytes.Buffer
	if g.quietOnOK {
		cp := *g
		cp.out = &buf
		runG = &cp
	}
	r := runG.runner(l)

	var results []report.JobResult
	for _, j := range jobs {
		results = append(results, runG.runJob(ctx, r, l, j, opts))
	}

	failed, skipped := false, false
	for _, res := range results {
		if res.Failed() {
			failed = true
		}
		if res.Skipped != "" {
			skipped = true
		}
	}

	if !g.quietOnOK {
		g.summarize(results)
	} else if failed || skipped {
		buf.WriteTo(g.out)
		g.summarize(results)
	}
	if failed {
		return 1
	}
	return 0
}

// runJob runs one job, or — when its secrets failed to load (I1) — reports
// it as a failure without touching restic. A dry run still prints the job's
// commands, with a warning that its secrets are unavailable.
func (g *globals) runJob(ctx context.Context, r *job.Runner, l *loaded, j *config.Job, opts job.Options) report.JobResult {
	err, secretFailed := l.secErrs[j.Name]
	if secretFailed && g.dryRun {
		fmt.Fprintf(g.out, "secrets unavailable for %s: %v\n", j.Name, err)
	} else if secretFailed {
		return r.Fail(j, err)
	}
	return r.Run(ctx, j, opts)
}

func cmdCheck(g *globals, names []string, all, full bool) int {
	l, code := g.load()
	if code != 0 {
		return code
	}
	jobs, code := l.selectJobs(names, all, g.out)
	if code != 0 {
		return code
	}
	return g.runAll(jobs, l, job.Options{Only: job.PhaseCheck, FullCheck: full})
}

// cmdExec is the *-cmd.sh replacement: mount, repo, password and environment
// are handled; everything after "--" goes to restic verbatim.
func cmdExec(g *globals, jobName string, passthrough []string) int {
	l, code := g.load()
	if code != 0 {
		return code
	}
	j, ok := l.cfg.Jobs[jobName]
	if !ok {
		fmt.Fprintf(g.out, "unknown job %q\n", jobName)
		return 2
	}
	if err, failed := l.secErrs[j.Name]; failed {
		fmt.Fprintln(g.out, "secrets error:", err)
		return 2
	}

	var mounter mount.Mounter = mount.System{}
	if g.dryRun {
		mounter = mount.NewFake()
	}
	session, err := mount.Acquire(mounter, j.Mount, j.UnmountAlways)
	if err != nil {
		fmt.Fprintln(g.out, "mount failed:", err)
		return 1
	}
	defer func() {
		if err := session.Release(); err != nil {
			fmt.Fprintln(g.out, "unmount failed:", err)
		}
	}()

	argv := append([]string{"--repo", j.Repo}, passthrough...)
	runner := &restic.Runner{Bin: j.ResticExecutable, DryRun: g.dryRun, Out: g.out, RunAs: j.RunAs, Passthrough: true}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	res, err := runner.Run(ctx, argv, restic.Env(j, l.sec[j.Name]), 0)
	if err != nil {
		fmt.Fprintln(g.out, err)
		return 1
	}
	return res.ExitCode
}

// cmdStatus is the health check: it must still report every job's state
// even when secrets are broken, so it resolves secrets itself rather than
// through load(), and treats a secrets failure as a warning, not exit 2.
func cmdStatus(g *globals, args []string) int {
	l, code := g.loadConfig()
	if code != 0 {
		return code
	}
	sec, secErrs, err := secrets.Load(g.secretsPath, l.jobs)
	if err != nil {
		fmt.Fprintln(g.out, "secrets unavailable:", err)
	}
	l.sec = sec
	l.secErrs = secErrs

	jobs := l.jobs
	if len(args) > 0 {
		var err int
		jobs, err = l.selectJobs(args, false, g.out)
		if err != 0 {
			return err
		}
	}
	for _, j := range jobs {
		st, err := state.Load(g.stateDir, j.Name)
		if err != nil {
			fmt.Fprintf(g.out, "%-12s state unreadable: %v\n", j.Name, err)
			continue
		}
		last := "never"
		if !st.LastRun.IsZero() {
			last = report.FormatTime(st.LastRun)
		}
		manual := ""
		if j.Mode != config.ModeScheduled {
			manual = " " + j.Mode
		}
		fmt.Fprintf(g.out, "%-12s last-run=%s exit=%d check-index=%d snapshot=%s%s\n",
			j.Name, last, st.LastExit, st.CheckIndex, orDash(st.LastSnapshot), manual)

		if err, ok := l.secErrs[j.Name]; ok {
			fmt.Fprintf(g.out, "             secrets unavailable: %v\n", err)
		}
		if j.Mount != "" {
			// Querying would mean mounting; status stays read-only and cheap.
			continue
		}
		if !g.dryRun {
			if set, ok := l.sec[j.Name]; ok {
				if age, id, err := latestSnapshot(l, j, set); err != nil {
					fmt.Fprintf(g.out, "             repository unreachable: %v\n", err)
				} else {
					fmt.Fprintf(g.out, "             newest=%s age=%s\n", id, age.Round(time.Minute))
				}
			}
		}
		if u, err := (mount.System{}).Usage(j.Repo); err == nil {
			fmt.Fprintf(g.out, "             %s\n", u)
		}
	}
	return 0
}

// latestSnapshot queries the repository for its newest snapshot. Used by
// status only; the pipeline has its own freshness check.
func latestSnapshot(l *loaded, j *config.Job, set secrets.Set) (time.Duration, string, error) {
	runner := &restic.Runner{Bin: j.ResticExecutable, Out: io.Discard, Quiet: true, RunAs: j.RunAs}
	res, err := runner.Run(context.Background(), restic.SnapshotsArgs(j), restic.Env(j, set), 0)
	if err != nil {
		return 0, "", err
	}
	if res.ExitCode != 0 {
		return 0, "", fmt.Errorf("restic exited %d", res.ExitCode)
	}
	id, t, err := restic.NewestSnapshot(res.Stdout)
	if err != nil {
		return 0, "", err
	}
	return time.Since(t), id, nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// cmdConfigCheck prints the resolved configuration with secrets redacted. It
// prints every job even when some job's secret could not be resolved — the
// point of the command is to be usable before secrets exist — showing
// "unavailable" for that job's password and exiting 2 at the end (I1).
func cmdConfigCheck(g *globals) int {
	l, code := g.loadConfig()
	if code != 0 {
		return code
	}
	sec, secErrs, fileErr := secrets.Load(g.secretsPath, l.jobs)
	if fileErr != nil {
		fmt.Fprintln(g.out, "secrets error:", fileErr)
	}
	l.sec = sec

	for _, j := range l.jobs {
		fmt.Fprintf(g.out, "\njob %s\n", j.Name)
		fmt.Fprintf(g.out, "  repo         %s\n", j.Repo)
		fmt.Fprintf(g.out, "  mount        %s\n", orDash(j.Mount))
		fmt.Fprintf(g.out, "  run_as       %s\n", orDash(j.RunAs))
		fmt.Fprintf(g.out, "  restic       %s\n", j.ResticExecutable)
		switch j.Mode {
		case config.ModeManual:
			fmt.Fprintf(g.out, "  mode         manual (excluded from run --all; runs when named)\n")
		case config.ModeDisabled:
			fmt.Fprintf(g.out, "  mode         disabled (never runs; exec still works)\n")
		}
		if j.UnmountAlways {
			fmt.Fprintf(g.out, "  unmount      always (even if already mounted)\n")
		}
		if err, failed := secErrs[j.Name]; failed {
			fmt.Fprintf(g.out, "  password     unavailable: %v\n", err)
		} else {
			fmt.Fprintf(g.out, "  password     %s\n", secrets.Redact(l.sec[j.Name].Password))
			for _, e := range restic.RedactedEnv(j, l.sec[j.Name]) {
				if isBackendVar(e) {
					fmt.Fprintf(g.out, "  env          %s\n", e)
				}
			}
		}
		if j.Backup != nil {
			fmt.Fprintf(g.out, "  backup       %v\n", j.Backup.Paths)
		} else {
			fmt.Fprintf(g.out, "  backup       (maintenance only)\n")
		}
	}
	for _, w := range l.cfg.Warnings() {
		fmt.Fprintln(g.out, "\nwarning:", w)
	}

	// Each job names its own executable, so check each distinct one once.
	if !g.dryRun {
		seen := map[string]bool{}
		for _, j := range l.jobs {
			if seen[j.ResticExecutable] {
				continue
			}
			seen[j.ResticExecutable] = true
			if _, err := exec.LookPath(j.ResticExecutable); err != nil {
				fmt.Fprintf(g.out, "\nwarning: restic executable %q not found: %v\n", j.ResticExecutable, err)
			}
		}
	}
	if fileErr != nil || len(secErrs) > 0 {
		return 2
	}
	return 0
}

// isBackendVar keeps os.Environ() entries out of the config check output.
func isBackendVar(e string) bool {
	for _, prefix := range []string{"RESTIC_", "B2_", "AWS_", "AZURE_", "GOOGLE_"} {
		if len(e) > len(prefix) && e[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

// cmdVersion prints the linker-injected build metadata. The build time is
// recorded in UTC and shown in local project time, like every other
// timestamp resticle prints.
func cmdVersion(out io.Writer) int {
	fmt.Fprintf(out, "resticle %s\n", version)
	switch {
	case buildTime == "":
		fmt.Fprintln(out, "built      (not recorded)")
	default:
		if t, err := time.Parse(time.RFC3339, buildTime); err == nil {
			fmt.Fprintf(out, "built      %s\n", report.FormatTime(t))
		} else {
			fmt.Fprintf(out, "built      %s\n", buildTime)
		}
	}
	fmt.Fprintf(out, "go         %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	return 0
}

// cmdFind searches every job's repository for a pattern. The question it
// answers — "which of these repositories still has this file, and in which
// snapshot?" — is the one `exec` cannot answer in a single command, because
// each repository needs its own mount, secrets and user.
//
// A job whose disk is not attached, or whose secrets are unavailable, is
// reported and skipped rather than failing the search: finding the file in
// four of six repositories is still an answer.
func cmdFind(g *globals, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(g.out, "usage: resticle find <pattern> [<job>...]")
		return 2
	}
	pattern, jobNames := args[0], args[1:]

	l, code := g.load()
	if code != 0 {
		return code
	}
	jobs := l.jobs
	if len(jobNames) > 0 {
		var picked []*config.Job
		for _, n := range jobNames {
			j, ok := l.cfg.Jobs[n]
			if !ok {
				fmt.Fprintf(g.out, "unknown job %q\n", n)
				return 2
			}
			picked = append(picked, j)
		}
		jobs = picked
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	found := false
	for _, j := range jobs {
		fmt.Fprintf(g.out, "\n=== %s (%s)\n", j.Name, j.Repo)
		if err, failed := l.secErrs[j.Name]; failed {
			fmt.Fprintln(g.out, "  skipped: secrets unavailable:", err)
			continue
		}

		var mounter mount.Mounter = mount.System{}
		if g.dryRun {
			mounter = mount.NewFake()
		}
		session, err := mount.Acquire(mounter, j.Mount, j.UnmountAlways)
		if err != nil {
			fmt.Fprintln(g.out, "  skipped: mount failed:", err)
			continue
		}

		runner := &restic.Runner{Bin: j.ResticExecutable, DryRun: g.dryRun, Out: g.out, RunAs: j.RunAs}
		res, err := runner.Run(ctx, restic.FindArgs(j, pattern), restic.Env(j, l.sec[j.Name]), 0)
		if rerr := session.Release(); rerr != nil {
			fmt.Fprintln(g.out, "  unmount failed:", rerr)
		}
		switch {
		case err != nil:
			fmt.Fprintln(g.out, "  skipped:", err)
		case res.ExitCode != 0:
			fmt.Fprintf(g.out, "  restic exited %d\n", res.ExitCode)
		case strings.Contains(res.Stdout, "Found "):
			found = true
		default:
			fmt.Fprintln(g.out, "  no match")
		}
	}
	if !found {
		fmt.Fprintf(g.out, "\nno match for %q in any searched repository\n", pattern)
		return 1
	}
	return 0
}
