// Package job runs one configured job end to end.
package job

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/fostam/resticle/internal/config"
	"github.com/fostam/resticle/internal/mount"
	"github.com/fostam/resticle/internal/report"
	"github.com/fostam/resticle/internal/restic"
	"github.com/fostam/resticle/internal/secrets"
	"github.com/fostam/resticle/internal/state"
)

// Phase names, also accepted as Options.Only.
const (
	PhaseBackup      = "backup"
	PhaseMaintenance = "maintenance"
	PhaseBackupPre   = "backup-pre"
	PhaseBackupPost  = "backup-post"
	PhaseForget      = "forget"
	PhaseCheck       = "check"
)

type Options struct {
	// Only restricts the run to a single phase. Empty runs the whole pipeline.
	Only string
	// FullCheck forces check mode "full" regardless of configuration.
	FullCheck bool
}

type Runner struct {
	Restic   *restic.Runner
	Mounter  mount.Mounter
	Secrets  map[string]secrets.Set
	StateDir string
	LockDir  string
	Log      *report.Logger
	// ResticDryRun runs the job for real — mount, lock, repository access —
	// but passes restic's own --dry-run to the commands that would change
	// something, so the repository is left untouched. check is skipped (it
	// changes nothing, and a full read is expensive), state is not written
	// and hooks do not fire.
	ResticDryRun bool

	// SkipRepoCheck disables the "repository path must exist" guard. Tests
	// set it; production never does.
	SkipRepoCheck bool
	// DryRun makes Run print what it would do (via Restic.DryRun) without
	// taking the job lock, writing state, checking freshness, or firing
	// hooks. See spec section 7.
	DryRun bool
}

// Run executes one job. It never returns an error: every outcome is reported
// in the JobResult so that --all can continue with the next job.
func (r *Runner) Run(ctx context.Context, j *config.Job, opts Options) (res report.JobResult) {
	res.Job = j.Name
	res.Started = time.Now()
	res.DryRun = r.dryRunMode()
	switch res.DryRun {
	case "dry-run":
		r.Log.Event(j.Name, "dry-run: printing the restic commands, touching nothing")
	case "restic dry-run":
		r.Log.Event(j.Name, "restic dry-run: mounting and opening the repository for real,"+
			" restic reports what it would change without changing it")
	}

	if !r.DryRun {
		lock, err := acquireLock(r.LockDir, j.Name)
		if errors.Is(err, ErrLockHeld) {
			r.Log.Event(j.Name, "skipped: %v", ErrLockHeld)
			res.Skipped = ErrLockHeld.Error()
			res.SkipIsFailure = true
			return res
		}
		if err != nil {
			res.Err = err
			return res
		}
		defer lock.release()
	}

	// From here on, every exit path — a failed mount, a failed repo check,
	// or a normal finish — goes through this one common tail: release the
	// mount if we hold one, then record state and fire the hook. A missing
	// disk is exactly the kind of failure an operator needs the hook for.
	var session *mount.Session
	defer func() {
		if session != nil {
			if err := session.Release(); err != nil {
				r.Log.Event(j.Name, "unmount failed: %v", err)
				if res.Err == nil {
					res.Err = err
				}
			}
		}
		if !r.DryRun && !r.ResticDryRun {
			r.saveState(j, res)
			r.fireHook(j, res)
		}
	}()

	var err error
	session, err = mount.Acquire(r.Mounter, j.Mount, j.UnmountAlways)
	if err != nil {
		r.Log.Event(j.Name, "mount failed: %v", err)
		res.Err = err
		return res
	}

	if err := r.verifyRepo(j); err != nil {
		r.Log.Event(j.Name, "%v", err)
		res.Err = err
		return res
	}

	if u, err := r.usage(j); err == nil {
		res.UsageBefore = u // the summary's overall figures; the log reports per phase
	}

	r.runPhases(ctx, j, opts, &res)

	if r.DryRun {
		r.Log.Event(j.Name, "dry-run: skipping freshness check")
	} else if err := r.checkFreshness(ctx, j, &res); err != nil {
		r.Log.Event(j.Name, "%v", err)
		if res.Err == nil {
			res.Err = err
		}
	}

	if u, err := r.usage(j); err == nil {
		res.UsageAfter = u
	}

	return res
}

// Fail records a job that never ran, e.g. because its secrets could not be
// resolved, as a failure: the on_failure hook fires and state is saved with
// LastExit=1, the same as any other failed run.
func (r *Runner) Fail(j *config.Job, err error) report.JobResult {
	res := report.JobResult{Job: j.Name, Started: time.Now(), Err: err, DryRun: r.dryRunMode()}
	if !r.DryRun {
		r.saveState(j, res)
		r.fireHook(j, res)
	}
	return res
}

// checkConfigured reports whether the job's own maintenance block would run
// a check, ignoring opts.FullCheck (which forces one regardless).
func checkConfigured(j *config.Job) bool {
	return j.Check != nil && j.Check.Mode != config.CheckOff
}

func (r *Runner) runPhases(ctx context.Context, j *config.Job, opts Options, res *report.JobResult) {
	if opts.Only == PhaseCheck && !opts.FullCheck && !checkConfigured(j) {
		r.Log.Event(j.Name, "no check configured")
		res.Skipped = "no check configured"
		return
	}
	if opts.Only == PhaseBackup && j.Backup == nil {
		r.Log.Event(j.Name, "no backup configured")
		res.Skipped = "no backup configured"
		return
	}
	if opts.Only == PhaseMaintenance && j.Forget == nil && !checkConfigured(j) {
		r.Log.Event(j.Name, "no maintenance configured")
		res.Skipped = "no maintenance configured"
		return
	}
	if opts.Only == PhaseForget && j.Forget == nil {
		r.Log.Event(j.Name, "no forget configured")
		res.Skipped = "no forget configured"
		return
	}

	wantBackup := j.Backup != nil && (opts.Only == "" || opts.Only == PhaseBackup)
	phaseSelected := opts.Only == "" || opts.Only == PhaseMaintenance ||
		opts.Only == PhaseForget || opts.Only == PhaseCheck
	// FullCheck must run even without a maintenance block, since CheckArgs
	// needs one to build "restic check" from; maintain builds a synthetic
	// one in that case rather than running forget from nothing.
	wantMaint := phaseSelected && (j.Forget != nil || j.Check != nil || opts.FullCheck)
	// Only forget has a `when`: reclaiming space before writing is the
	// reason `before` exists. A check always follows the backup, and any
	// forget, since prune rewrites packs and the check validates the result.
	before := wantMaint && j.Forget != nil && j.Forget.When == config.WhenBefore &&
		opts.Only != PhaseCheck

	if before {
		r.maintain(ctx, j, opts, res)
	}
	if wantBackup {
		if !r.backup(ctx, j, res) {
			// A failed backup must not be followed by forget --prune.
			r.Log.Event(j.Name, "backup failed; skipping maintenance")
			return
		}
	}
	if wantMaint && !before {
		r.maintain(ctx, j, opts, res)
	}
}

func (r *Runner) backup(ctx context.Context, j *config.Job, res *report.JobResult) bool {
	// post runs whatever happens below — a failed backup, a failed pre, a
	// timeout, an interrupt. Whatever pre stopped has to be started again.
	if j.Backup.Post != "" {
		defer r.runHook(j, PhaseBackupPost, j.Backup.Post, res)
	}
	// A pre that fails means the backup must not run: a database that did
	// not stop produces a snapshot that looks fine and is not.
	if j.Backup.Pre != "" && !r.runHook(j, PhaseBackupPre, j.Backup.Pre, res) {
		r.Log.Event(j.Name, "pre-backup command failed; not backing up")
		return false
	}

	r.Log.Event(j.Name, "backup started")
	before := r.logSpaceBefore(j, PhaseBackup)
	out, ok := r.exec(ctx, j, PhaseBackup, restic.BackupArgs(j, r.ResticDryRun), r.timeout(j, PhaseBackup), res)
	if ok {
		res.Snapshot = parseSnapshotID(out)
	}
	r.logSpaceAfter(j, PhaseBackup, before)
	return ok
}

// runHook runs a pre- or post-backup command through /bin/sh, recording it as
// a phase so a failure names itself instead of hiding inside "backup failed".
// Neither dry run executes one: both promise to change nothing, and stopping
// a service is a change.
func (r *Runner) runHook(j *config.Job, phase, command string, res *report.JobResult) bool {
	if r.DryRun || r.ResticDryRun {
		r.Log.Event(j.Name, "%s skipped (%s): %s", phase, r.dryRunMode(), command)
		return true
	}
	r.Log.Event(j.Name, "%s: %s", phase, command)

	start := time.Now()
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Env = append(os.Environ(), "RESTICLE_JOB="+j.Name, "RESTICLE_PHASE="+phase)
	out, err := cmd.CombinedOutput()

	result := report.PhaseResult{Name: phase, Duration: time.Since(start)}
	if len(out) > 0 {
		r.Log.Event(j.Name, "%s output: %s", phase, strings.TrimSpace(string(out)))
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		}
		result.Err = fmt.Errorf("%s %q: %w", phase, command, err)
		res.Phases = append(res.Phases, result)
		r.Log.Event(j.Name, "%s failed: %v", phase, err)
		return false
	}
	res.Phases = append(res.Phases, result)
	return true
}

func (r *Runner) maintain(ctx context.Context, j *config.Job, opts Options, res *report.JobResult) {
	before := r.logSpaceBefore(j, PhaseMaintenance)
	defer func() { r.logSpaceAfter(j, PhaseMaintenance, before) }()

	// A job with no forget block expires nothing; `check --full` on such a
	// job still verifies.
	if j.Forget != nil && opts.Only != PhaseCheck {
		r.Log.Event(j.Name, "forget started")
		args := restic.ForgetArgs(j, r.ResticDryRun)
		if _, forgot := r.exec(ctx, j, PhaseForget, args, r.timeout(j, PhaseForget), res); !forgot {
			return
		}
	}
	if opts.Only == PhaseForget {
		r.Log.Event(j.Name, "check skipped (forget only)")
		return
	}

	st, err := state.Load(r.StateDir, j.Name)
	if err != nil {
		r.Log.Event(j.Name, "state unreadable, starting check index at 0: %v", err)
		st = &state.State{}
	}
	check := j.Check
	if opts.FullCheck {
		// --full overrides the configured mode, keeping whatever timeout the
		// job set for checking.
		full := config.Check{Mode: config.CheckFull}
		if j.Check != nil {
			full.Timeout = j.Check.Timeout
		}
		check = &full
	}
	spread := 0
	if check != nil {
		spread = check.Spread
	}
	index := st.NextCheckIndex(spread)

	probe := *j
	probe.Check = check

	args, ok := restic.CheckArgs(&probe, index)
	if !ok {
		return
	}
	if r.ResticDryRun {
		// A check changes nothing, so a dry run has nothing to show — and on
		// an external disk a full read costs hours.
		r.Log.Event(j.Name, "check skipped (restic dry-run)")
		return
	}
	r.Log.Event(j.Name, "check started (%s)", check.Mode)
	_, passed := r.exec(ctx, j, PhaseCheck, args, r.timeout(&probe, PhaseCheck), res)
	if passed && check.Mode == config.CheckSubset && !r.DryRun {
		st.AdvanceCheckIndex(spread)
		if err := st.Save(r.StateDir, j.Name); err != nil {
			r.Log.Event(j.Name, "could not save state: %v", err)
		}
	}
}

// exec runs one restic invocation and records it as a phase.
func (r *Runner) exec(ctx context.Context, j *config.Job, name string, args []string, timeout time.Duration, res *report.JobResult) (string, bool) {
	runner := *r.Restic
	setJobOn(&runner, j)

	out, err := runner.Run(ctx, args, restic.Env(j, r.Secrets[j.Name]), timeout)
	phase := report.PhaseResult{
		Name:     name,
		ExitCode: out.ExitCode,
		Duration: out.Duration,
		TimedOut: out.TimedOut,
		Err:      err,
	}
	if restic.IsRepoLocked(out) {
		phase.Err = fmt.Errorf("repository is locked; if no run is active, clear it with: resticle exec %s -- unlock", j.Name)
	}
	res.Phases = append(res.Phases, phase)

	ok := err == nil && out.ExitCode == 0
	switch {
	case ok:
		r.Log.Event(j.Name, "%s finished", name)
	case err != nil:
		r.Log.Event(j.Name, "%s failed: %v", name, err)
	case phase.Err != nil:
		r.Log.Event(j.Name, "%s failed: exit %d: %v", name, out.ExitCode, phase.Err)
	default:
		r.Log.Event(j.Name, "%s failed: exit %d", name, out.ExitCode)
	}
	return out.Stdout, ok
}

// timeout is the limit for a phase, taken from the block that phase belongs
// to. A phase whose block is absent — `check --full` on a job with no
// maintenance block — runs unbounded.
func (r *Runner) timeout(j *config.Job, phase string) time.Duration {
	var d *config.Duration
	switch phase {
	case PhaseBackup:
		if j.Backup != nil {
			d = j.Backup.Timeout
		}
	case PhaseForget:
		if j.Forget != nil {
			d = j.Forget.Timeout
		}
	case PhaseCheck:
		if j.Check != nil {
			d = j.Check.Timeout
		}
	}
	if d == nil {
		return 0
	}
	return d.Std()
}

// dryRunMode names the mode for the log and the summary. A restic dry run
// otherwise reads exactly like a real one — "backup finished", "success" —
// which is the last thing a log should be ambiguous about.
func (r *Runner) dryRunMode() string {
	switch {
	case r.DryRun:
		return "dry-run"
	case r.ResticDryRun:
		return "restic dry-run"
	default:
		return ""
	}
}

// setJob applies what the job says about how restic is invoked. A resolved
// job always names an executable; the zero value leaves whatever the caller
// configured, which is what the tests rely on.
func setJobOn(runner *restic.Runner, j *config.Job) {
	if j.ResticExecutable != "" {
		runner.Bin = j.ResticExecutable
	}
	runner.RunAs = j.RunAs
}

// verifyRepo is the guard that stops forget --prune from running against an
// empty directory when a disk is missing. See spec defect 3.
func (r *Runner) verifyRepo(j *config.Job) error {
	if r.DryRun {
		r.Log.Event(j.Name, "repository path check skipped in dry-run")
		return nil
	}
	if r.SkipRepoCheck || len(j.Repo) == 0 || j.Repo[0] != '/' {
		return nil // remote backends are verified by restic itself
	}
	if _, err := os.Stat(j.Repo); err != nil {
		return fmt.Errorf("repository path %s is not accessible: %w", j.Repo, err)
	}
	return nil
}

func (r *Runner) usage(j *config.Job) (*mount.Usage, error) {
	path := j.Mount
	if path == "" {
		if len(j.Repo) == 0 || j.Repo[0] != '/' {
			return nil, errors.New("no local path")
		}
		path = j.Repo
	}
	u, err := r.Mounter.Usage(path)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// checkFreshness fails the job when the newest snapshot is older than
// max_age. For push-based repositories this is the only signal that backups
// stopped arriving. See spec section 6.5.
func (r *Runner) checkFreshness(ctx context.Context, j *config.Job, res *report.JobResult) error {
	if j.MaxAge == nil {
		return nil
	}
	runner := *r.Restic
	setJobOn(&runner, j)
	runner.Quiet = true

	out, err := runner.Run(ctx, restic.SnapshotsArgs(j), restic.Env(j, r.Secrets[j.Name]), 0)
	if err != nil {
		return fmt.Errorf("freshness check: %w", err)
	}
	if out.ExitCode != 0 {
		return fmt.Errorf("freshness check: restic exited %d", out.ExitCode)
	}

	id, t, err := restic.NewestSnapshot(out.Stdout)
	if err != nil {
		return fmt.Errorf("freshness check: %w", err)
	}
	age := time.Since(t)
	if age > j.MaxAge.Std() {
		return fmt.Errorf("newest snapshot is %s old, older than max_age %s",
			age.Round(time.Minute), j.MaxAge.Std())
	}
	r.Log.Event(j.Name, "newest snapshot %s, age %s", id, age.Round(time.Minute))
	return nil
}

func (r *Runner) saveState(j *config.Job, res report.JobResult) {
	st, err := state.Load(r.StateDir, j.Name)
	if err != nil {
		st = &state.State{}
	}
	st.LastRun = time.Now()
	st.LastExit = 0
	if res.Failed() {
		st.LastExit = 1
	}
	if res.Snapshot != "" {
		st.LastSnapshot = res.Snapshot
	}
	if err := st.Save(r.StateDir, j.Name); err != nil {
		r.Log.Event(j.Name, "could not save state: %v", err)
	}
}

func (r *Runner) fireHook(j *config.Job, res report.JobResult) {
	hook := j.OnSuccess
	if res.Failed() {
		hook = j.OnFailure
	}
	if err := report.RunHook(hook, res); err != nil {
		r.Log.Event(j.Name, "hook failed: %v", err)
	}
}

// parseSnapshotID pulls the new snapshot's short ID out of restic's backup
// output line "snapshot abc1234 saved".
func parseSnapshotID(out string) string {
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[0] == "snapshot" && fields[2] == "saved" {
			return fields[1]
		}
	}
	return ""
}

// logSpaceBefore records free space as a phase begins and returns the
// reading, so logSpaceAfter can report what the phase cost or reclaimed —
// a backup consumes, a prune gives back. A repository that is not on a local
// filesystem (a cloud backend) has nothing to measure, and both lines are
// omitted rather than guessed at.
func (r *Runner) logSpaceBefore(j *config.Job, phase string) *mount.Usage {
	u, err := r.usage(j)
	if err != nil {
		return nil
	}
	r.Log.Event(j.Name, "%s space before %s", phase, u)
	return u
}

func (r *Runner) logSpaceAfter(j *config.Job, phase string, before *mount.Usage) {
	after, err := r.usage(j)
	if before == nil || err != nil {
		return
	}
	r.Log.Event(j.Name, "%s space after %s (%s)", phase, after,
		mount.FreeDelta(*before, *after))
}
