package job

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fostam/resticle/internal/config"
	"github.com/fostam/resticle/internal/mount"
	"github.com/fostam/resticle/internal/report"
	"github.com/fostam/resticle/internal/restic"
	"github.com/fostam/resticle/internal/secrets"
	"github.com/fostam/resticle/internal/state"
)

// fakeRestic writes a script that appends each subcommand to a log file and
// exits non-zero for any subcommand named in failOn.
func fakeRestic(t *testing.T, dir string, failOn ...string) (bin, logFile string) {
	t.Helper()
	bin = filepath.Join(dir, "restic")
	logFile = filepath.Join(dir, "calls.log")

	var fails strings.Builder
	for _, f := range failOn {
		fmt.Fprintf(&fails, "  %s) exit 1 ;;\n", f)
	}
	script := `#!/bin/sh
for a in "$@"; do
  case "$a" in
    backup|forget|check|snapshots) echo "$a" >> ` + logFile + `; sub="$a"; break ;;
  esac
done
if [ "$sub" = snapshots ]; then
  echo '[{"short_id":"abc1234","time":"` + time.Now().UTC().Format(time.RFC3339) + `"}]'
fi
case "$sub" in
` + fails.String() + `esac
exit 0
`
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin, logFile
}

func calls(t *testing.T, logFile string) []string {
	t.Helper()
	b, err := os.ReadFile(logFile)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(b))
}

func testJob() *config.Job {
	yes := true
	return &config.Job{
		Name:  "ext",
		Repo:  "/repo",
		Mount: "/mnt/usb",
		Backup: &config.Backup{
			Paths: []string{"/srv"},
			Tags:  []string{"ext"},
		},
		Forget: &config.Forget{
			When:  config.WhenAfter,
			Keep:  &config.Keep{Last: 2},
			Prune: &yes,
		},
		Check: &config.Check{Mode: config.CheckSubset, Spread: 28},
	}
}

func newRunner(t *testing.T, bin string, m mount.Mounter) *Runner {
	t.Helper()
	dir := t.TempDir()
	return &Runner{
		Restic:   &restic.Runner{Bin: bin, Out: &bytes.Buffer{}, Quiet: true},
		Mounter:  m,
		Secrets:  map[string]secrets.Set{"ext": {Password: "pw"}},
		StateDir: filepath.Join(dir, "state"),
		LockDir:  filepath.Join(dir, "lock"),
		Log:      &report.Logger{Out: &bytes.Buffer{}},
		// SkipRepoCheck: the fake repo path does not exist on disk.
		SkipRepoCheck: true,
	}
}

func TestRunOrderBackupThenMaintenance(t *testing.T) {
	dir := t.TempDir()
	bin, logFile := fakeRestic(t, dir)
	r := newRunner(t, bin, mount.NewFake())

	res := r.Run(context.Background(), testJob(), Options{})
	if res.Failed() {
		t.Fatalf("job failed: %+v", res)
	}
	got := calls(t, logFile)
	want := []string{"backup", "forget", "check"}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("calls = %v, want prefix %v", got, want)
		}
	}
}

func TestRunWhenBeforeRunsMaintenanceFirst(t *testing.T) {
	dir := t.TempDir()
	bin, logFile := fakeRestic(t, dir)
	j := testJob()
	j.Forget.When = config.WhenBefore
	r := newRunner(t, bin, mount.NewFake())

	r.Run(context.Background(), j, Options{})
	got := calls(t, logFile)
	if len(got) == 0 || got[0] != "forget" {
		t.Fatalf("calls = %v, want forget first", got)
	}
}

// Pruning a repository whose backup just failed expires old snapshots while
// the new one never arrived. See spec section 6.7.
func TestFailedBackupSkipsMaintenance(t *testing.T) {
	dir := t.TempDir()
	bin, logFile := fakeRestic(t, dir, "backup")
	r := newRunner(t, bin, mount.NewFake())

	res := r.Run(context.Background(), testJob(), Options{})
	if !res.Failed() {
		t.Error("Failed = false after a failing backup")
	}
	for _, c := range calls(t, logFile) {
		if c == "forget" || c == "check" {
			t.Fatalf("maintenance ran after a failed backup: %v", calls(t, logFile))
		}
	}
}

func TestUnmountHappensEvenWhenBackupFails(t *testing.T) {
	dir := t.TempDir()
	bin, _ := fakeRestic(t, dir, "backup")
	f := mount.NewFake()
	r := newRunner(t, bin, f)

	r.Run(context.Background(), testJob(), Options{})
	if f.Mounted["/mnt/usb"] {
		t.Error("mount left behind after a failed job")
	}
	if f.UnmountCalls != 1 {
		t.Errorf("UnmountCalls = %d, want 1", f.UnmountCalls)
	}
}

func TestMountFailureRunsNoPhases(t *testing.T) {
	dir := t.TempDir()
	bin, logFile := fakeRestic(t, dir)
	f := mount.NewFake()
	f.MountErr = errors.New("no such device")
	r := newRunner(t, bin, f)

	res := r.Run(context.Background(), testJob(), Options{})
	if !res.Failed() {
		t.Error("Failed = false when the mount failed")
	}
	if got := calls(t, logFile); len(got) != 0 {
		t.Errorf("phases ran despite mount failure: %v", got)
	}
}

func TestMaintenanceOnlyJobStillMounts(t *testing.T) {
	dir := t.TempDir()
	bin, logFile := fakeRestic(t, dir)
	j := testJob()
	j.Backup = nil
	f := mount.NewFake()
	r := newRunner(t, bin, f)

	r.Run(context.Background(), j, Options{})
	if f.MountCalls != 1 {
		t.Errorf("MountCalls = %d, want 1 for a maintenance-only job", f.MountCalls)
	}
	got := calls(t, logFile)
	if len(got) == 0 || got[0] != "forget" {
		t.Fatalf("calls = %v, want forget", got)
	}
}

func TestOnlyBackupSkipsMaintenance(t *testing.T) {
	dir := t.TempDir()
	bin, logFile := fakeRestic(t, dir)
	r := newRunner(t, bin, mount.NewFake())

	r.Run(context.Background(), testJob(), Options{Only: PhaseBackup})
	for _, c := range calls(t, logFile) {
		if c == "forget" {
			t.Fatalf("forget ran under Only=backup: %v", calls(t, logFile))
		}
	}
}

func TestCheckIndexAdvancesOnlyOnSuccess(t *testing.T) {
	dir := t.TempDir()
	bin, _ := fakeRestic(t, dir)
	r := newRunner(t, bin, mount.NewFake())

	r.Run(context.Background(), testJob(), Options{})
	s1, err := state.Load(r.StateDir, "ext")
	if err != nil {
		t.Fatal(err)
	}
	if s1.CheckIndex != 1 {
		t.Errorf("CheckIndex = %d after one successful check, want 1", s1.CheckIndex)
	}

	failBin, _ := fakeRestic(t, t.TempDir(), "check")
	r.Restic = &restic.Runner{Bin: failBin, Out: &bytes.Buffer{}, Quiet: true}
	r.Run(context.Background(), testJob(), Options{})
	s2, err := state.Load(r.StateDir, "ext")
	if err != nil {
		t.Fatal(err)
	}
	if s2.CheckIndex != 1 {
		t.Errorf("CheckIndex = %d after a failing check, want it to stay at 1", s2.CheckIndex)
	}
}

func TestMaxAgeFailsOnStaleSnapshot(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "restic")
	old := time.Now().Add(-72 * time.Hour).UTC().Format(time.RFC3339)
	script := "#!/bin/sh\nfor a in \"$@\"; do case \"$a\" in snapshots) echo '[{\"short_id\":\"old1234\",\"time\":\"" + old + "\"}]'; exit 0;; esac; done\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	j := testJob()
	j.Backup = nil
	j.Forget = nil
	j.Check = nil
	maxAge := config.Duration(48 * time.Hour)
	j.MaxAge = &maxAge

	r := newRunner(t, bin, mount.NewFake())
	res := r.Run(context.Background(), j, Options{})
	if !res.Failed() {
		t.Error("Failed = false for a snapshot older than max-age")
	}
}

func TestLockHeldSkipsTheJob(t *testing.T) {
	dir := t.TempDir()
	bin, logFile := fakeRestic(t, dir)
	r := newRunner(t, bin, mount.NewFake())

	held, err := acquireLock(r.LockDir, "ext")
	if err != nil {
		t.Fatal(err)
	}
	defer held.release()

	res := r.Run(context.Background(), testJob(), Options{})
	if res.Skipped == "" {
		t.Error("Skipped is empty; expected the job to be skipped")
	}
	if !res.Failed() {
		t.Error("Failed = false for a lock-held skip; want it to fail the run (I6)")
	}
	if got := calls(t, logFile); len(got) != 0 {
		t.Errorf("phases ran while the lock was held: %v", got)
	}
}

// R-P4: an unmount failure must fail the job even though the phases and the
// freshness/state work already succeeded.
func TestUnmountFailureFailsTheJob(t *testing.T) {
	dir := t.TempDir()
	bin, _ := fakeRestic(t, dir)
	f := mount.NewFake()
	f.UnmountErr = errors.New("target is busy")
	r := newRunner(t, bin, f)

	res := r.Run(context.Background(), testJob(), Options{})
	if !res.Failed() {
		t.Error("Failed = false after an unmount error")
	}
	if res.Err == nil || !strings.Contains(res.Err.Error(), "target is busy") {
		t.Errorf("Err = %v, want it to mention the unmount error", res.Err)
	}
}

// R-P5: dry-run must not acquire the per-job lock or write any state.
func TestDryRunIgnoresHeldLockAndWritesNoState(t *testing.T) {
	dir := t.TempDir()
	bin, _ := fakeRestic(t, dir)
	r := newRunner(t, bin, mount.NewFake())
	r.DryRun = true
	r.Restic.DryRun = true

	held, err := acquireLock(r.LockDir, "ext")
	if err != nil {
		t.Fatal(err)
	}
	defer held.release()

	res := r.Run(context.Background(), testJob(), Options{})
	if res.Skipped != "" {
		t.Errorf("Skipped = %q, want dry-run to ignore the held lock", res.Skipped)
	}

	st, err := state.Load(r.StateDir, "ext")
	if err != nil {
		t.Fatal(err)
	}
	if st.CheckIndex != 0 {
		t.Errorf("CheckIndex = %d, want 0 (dry-run must not advance it)", st.CheckIndex)
	}
	if _, err := os.Stat(filepath.Join(r.StateDir, "ext.json")); !os.IsNotExist(err) {
		t.Errorf("ext.json exists after a dry run: err = %v", err)
	}
}

// R-P5: dry-run must skip the freshness check entirely, since a dry-run
// restic invocation produces no snapshots JSON to parse.
func TestDryRunSkipsFreshnessCheck(t *testing.T) {
	dir := t.TempDir()
	bin, _ := fakeRestic(t, dir)
	r := newRunner(t, bin, mount.NewFake())
	r.DryRun = true
	r.Restic.DryRun = true

	j := testJob()
	j.Backup = nil
	j.Forget = nil
	j.Check = nil
	maxAge := config.Duration(48 * time.Hour)
	j.MaxAge = &maxAge

	res := r.Run(context.Background(), j, Options{})
	if res.Failed() {
		t.Errorf("Failed = true, want dry-run to skip the freshness check: %+v", res)
	}
}

// failureHookScript writes a shell script that touches a marker file, for
// use as a job's on-failure hook.
func failureHookScript(t *testing.T, dir string) (cmd, marker string) {
	t.Helper()
	marker = filepath.Join(dir, "marker")
	cmd = filepath.Join(dir, "onfail.sh")
	body := "#!/bin/sh\ntouch " + marker + "\n"
	if err := os.WriteFile(cmd, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return cmd, marker
}

// Fix round 1, finding 1: a mount failure is a hard failure the operator
// needs to hear about, not a silent early return.
func TestMountFailureFiresFailureHookAndRecordsState(t *testing.T) {
	dir := t.TempDir()
	bin, _ := fakeRestic(t, dir)
	f := mount.NewFake()
	f.MountErr = errors.New("no such device")
	r := newRunner(t, bin, f)

	hook, marker := failureHookScript(t, t.TempDir())
	j := testJob()
	j.OnFailure = hook

	res := r.Run(context.Background(), j, Options{})
	if !res.Failed() {
		t.Fatal("Failed = false when mount failed")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("on-failure hook did not run: %v", err)
	}
	st, err := state.Load(r.StateDir, "ext")
	if err != nil {
		t.Fatal(err)
	}
	if st.LastExit != 1 {
		t.Errorf("LastExit = %d, want 1", st.LastExit)
	}
}

// Fix round 1, finding 1: same for a repository path that does not exist.
func TestRepoPathFailureFiresFailureHookAndRecordsState(t *testing.T) {
	dir := t.TempDir()
	bin, _ := fakeRestic(t, dir)
	r := newRunner(t, bin, mount.NewFake())
	r.SkipRepoCheck = false

	hook, marker := failureHookScript(t, t.TempDir())
	j := testJob()
	j.Repo = "/nonexistent/repo"
	j.OnFailure = hook

	res := r.Run(context.Background(), j, Options{})
	if !res.Failed() {
		t.Fatal("Failed = false for an inaccessible repo path")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("on-failure hook did not run: %v", err)
	}
	st, err := state.Load(r.StateDir, "ext")
	if err != nil {
		t.Fatal(err)
	}
	if st.LastExit != 1 {
		t.Errorf("LastExit = %d, want 1", st.LastExit)
	}
}

// Fix round 1, finding 2: dry-run must not require the repo path to exist,
// since the CLI passes a fake mounter and the real mountpoint's repo is not
// there.
func TestDryRunDoesNotRequireRepoPath(t *testing.T) {
	dir := t.TempDir()
	bin, _ := fakeRestic(t, dir)
	r := newRunner(t, bin, mount.NewFake())
	r.SkipRepoCheck = false
	r.DryRun = true
	r.Restic.DryRun = true

	j := testJob()
	j.Repo = "/nonexistent/repo"

	res := r.Run(context.Background(), j, Options{})
	if res.Failed() {
		t.Errorf("Failed = true, want dry-run to skip the repo path check: %+v", res)
	}
}

// Fix round 1, finding 3: Only=check with nothing to check must not silently
// report success.
func TestOnlyCheckWithoutCheckConfiguredIsSkipped(t *testing.T) {
	dir := t.TempDir()
	bin, logFile := fakeRestic(t, dir)
	j := testJob()
	j.Forget = nil
	j.Check = nil
	r := newRunner(t, bin, mount.NewFake())

	res := r.Run(context.Background(), j, Options{Only: PhaseCheck})
	if res.Skipped == "" {
		t.Error("Skipped is empty; expected the check phase to be skipped")
	}
	if got := calls(t, logFile); len(got) != 0 {
		t.Errorf("restic ran despite no check being configured: %v", got)
	}
}

// I5: backup on a job with no backup block must skip, not silently succeed.
func TestOnlyBackupWithoutBackupConfiguredIsSkipped(t *testing.T) {
	dir := t.TempDir()
	bin, logFile := fakeRestic(t, dir)
	j := testJob()
	j.Backup = nil
	r := newRunner(t, bin, mount.NewFake())

	res := r.Run(context.Background(), j, Options{Only: PhaseBackup})
	if res.Skipped == "" {
		t.Error("Skipped is empty; expected the backup phase to be skipped")
	}
	if got := calls(t, logFile); len(got) != 0 {
		t.Errorf("restic ran despite no backup being configured: %v", got)
	}
}

// I5: maintain on a job with no maintenance block must skip, not silently
// succeed.
func TestOnlyMaintainWithoutMaintenanceConfiguredIsSkipped(t *testing.T) {
	dir := t.TempDir()
	bin, logFile := fakeRestic(t, dir)
	j := testJob()
	j.Forget = nil
	j.Check = nil
	r := newRunner(t, bin, mount.NewFake())

	res := r.Run(context.Background(), j, Options{Only: PhaseMaintenance})
	if res.Skipped == "" {
		t.Error("Skipped is empty; expected the maintenance phase to be skipped")
	}
	if got := calls(t, logFile); len(got) != 0 {
		t.Errorf("restic ran despite no maintenance being configured: %v", got)
	}
}

// I1: Fail records a job that never ran (e.g. a missing secret) as a
// failure, still firing the on-failure hook and saving state.
func TestFailRecordsErrorAndFiresHook(t *testing.T) {
	dir := t.TempDir()
	bin, logFile := fakeRestic(t, dir)
	r := newRunner(t, bin, mount.NewFake())

	hook, marker := failureHookScript(t, t.TempDir())
	j := testJob()
	j.OnFailure = hook

	secretErr := errors.New("no secret found")
	res := r.Fail(j, secretErr)
	if !res.Failed() {
		t.Fatal("Failed = false after Fail")
	}
	if res.Err != secretErr {
		t.Errorf("Err = %v, want %v", res.Err, secretErr)
	}
	if got := calls(t, logFile); len(got) != 0 {
		t.Errorf("restic ran despite Fail: %v", got)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("on-failure hook did not run: %v", err)
	}
	st, err := state.Load(r.StateDir, "ext")
	if err != nil {
		t.Fatal(err)
	}
	if st.LastExit != 1 {
		t.Errorf("LastExit = %d, want 1", st.LastExit)
	}
}

// Fix round 1, finding 3: FullCheck must run even without a maintenance
// block, and must not run forget.
func TestFullCheckRunsWithoutMaintenanceBlock(t *testing.T) {
	dir := t.TempDir()
	bin, logFile := fakeRestic(t, dir)
	j := testJob()
	j.Backup = nil
	j.Forget = nil
	j.Check = nil
	r := newRunner(t, bin, mount.NewFake())

	res := r.Run(context.Background(), j, Options{FullCheck: true})
	if res.Failed() {
		t.Fatalf("job failed: %+v", res)
	}
	got := calls(t, logFile)
	if len(got) != 1 || got[0] != "check" {
		t.Errorf("calls = %v, want exactly one check call", got)
	}
}

// unmount_always reaches the pipeline: a disk that was already mounted when
// the job started is unmounted when it finishes.
func TestUnmountAlwaysReleasesAPreexistingMount(t *testing.T) {
	dir := t.TempDir()
	bin, _ := fakeRestic(t, dir)
	f := mount.NewFake()
	f.Mounted["/mnt/usb"] = true

	j := testJob()
	j.UnmountAlways = true
	r := newRunner(t, bin, f)

	res := r.Run(context.Background(), j, Options{})
	if res.Failed() {
		t.Fatalf("job failed: %+v", res)
	}
	if f.Mounted["/mnt/usb"] {
		t.Error("mount left behind despite unmount_always")
	}
	if f.UnmountCalls != 1 {
		t.Errorf("UnmountCalls = %d, want 1", f.UnmountCalls)
	}
}

// Without the flag the default stands: a mount we did not make is left alone.
func TestWithoutUnmountAlwaysPreexistingMountSurvives(t *testing.T) {
	dir := t.TempDir()
	bin, _ := fakeRestic(t, dir)
	f := mount.NewFake()
	f.Mounted["/mnt/usb"] = true

	r := newRunner(t, bin, f)
	if res := r.Run(context.Background(), testJob(), Options{}); res.Failed() {
		t.Fatalf("job failed: %+v", res)
	}
	if !f.Mounted["/mnt/usb"] {
		t.Error("a pre-existing mount was unmounted without unmount_always")
	}
}

// Free space is reported around each phase, not only around the job, so the
// cost of the backup and the reclaim from the prune are separable.
func TestSpaceIsLoggedPerPhase(t *testing.T) {
	dir := t.TempDir()
	bin, _ := fakeRestic(t, dir)
	f := mount.NewFake()
	f.UsageValue = mount.Usage{Total: 100, Used: 40, Free: 60}

	var log bytes.Buffer
	r := newRunner(t, bin, f)
	r.Log = &report.Logger{Out: &log}

	if res := r.Run(context.Background(), testJob(), Options{}); res.Failed() {
		t.Fatalf("job failed: %+v\n%s", res, log.String())
	}
	for _, want := range []string{
		"backup space before",
		"backup space after",
		"maintenance space before",
		"maintenance space after",
	} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("missing %q in:\n%s", want, log.String())
		}
	}
}

func TestFreeDeltaIsSigned(t *testing.T) {
	before := mount.Usage{Free: 100 << 30}
	if got := mount.FreeDelta(before, mount.Usage{Free: 88 << 30}); got != "-12.0GiB" {
		t.Errorf("consumed = %q, want -12.0GiB", got)
	}
	if got := mount.FreeDelta(before, mount.Usage{Free: 104 << 30}); got != "+4.0GiB" {
		t.Errorf("reclaimed = %q, want +4.0GiB", got)
	}
}

// A restic dry run otherwise reads exactly like a real one: same phase
// lines, same "success". It has to say what it is.
func TestResticDryRunIsAnnouncedAndMarked(t *testing.T) {
	dir := t.TempDir()
	bin, _ := fakeRestic(t, dir)
	var log bytes.Buffer

	r := newRunner(t, bin, mount.NewFake())
	r.Log = &report.Logger{Out: &log}
	r.ResticDryRun = true

	res := r.Run(context.Background(), testJob(), Options{})
	if !strings.Contains(log.String(), "restic dry-run:") {
		t.Errorf("no announcement in the log:\n%s", log.String())
	}
	if res.DryRun != "restic dry-run" {
		t.Errorf("JobResult.DryRun = %q", res.DryRun)
	}
}

func TestRealRunIsNotMarkedAsADryRun(t *testing.T) {
	dir := t.TempDir()
	bin, _ := fakeRestic(t, dir)
	res := newRunner(t, bin, mount.NewFake()).Run(context.Background(), testJob(), Options{})
	if res.DryRun != "" {
		t.Errorf("JobResult.DryRun = %q, want empty", res.DryRun)
	}
}

// forget expires and prunes without spending the verification time — the
// ad-hoc counterpart to configuring check: {mode: off}.
func TestOnlyForgetRunsNoCheck(t *testing.T) {
	dir := t.TempDir()
	bin, logFile := fakeRestic(t, dir)
	r := newRunner(t, bin, mount.NewFake())

	res := r.Run(context.Background(), testJob(), Options{Only: PhaseForget})
	if res.Failed() {
		t.Fatalf("job failed: %+v", res)
	}
	got := calls(t, logFile)
	if len(got) != 1 || got[0] != "forget" {
		t.Errorf("calls = %v, want exactly [forget]", got)
	}
}

// The rotation belongs to the check, so a forget must leave it alone.
func TestOnlyForgetLeavesTheCheckIndexAlone(t *testing.T) {
	dir := t.TempDir()
	bin, _ := fakeRestic(t, dir)
	r := newRunner(t, bin, mount.NewFake())

	if res := r.Run(context.Background(), testJob(), Options{}); res.Failed() {
		t.Fatalf("setup run failed: %+v", res)
	}
	after, err := state.Load(r.StateDir, "ext")
	if err != nil {
		t.Fatal(err)
	}
	if after.CheckIndex != 1 {
		t.Fatalf("setup: CheckIndex = %d, want 1", after.CheckIndex)
	}

	if res := r.Run(context.Background(), testJob(), Options{Only: PhaseForget}); res.Failed() {
		t.Fatalf("forget failed: %+v", res)
	}
	got, err := state.Load(r.StateDir, "ext")
	if err != nil {
		t.Fatal(err)
	}
	if got.CheckIndex != 1 {
		t.Errorf("CheckIndex = %d, want it untouched at 1", got.CheckIndex)
	}
}

func TestOnlyForgetWithoutMaintenanceIsSkipped(t *testing.T) {
	dir := t.TempDir()
	bin, logFile := fakeRestic(t, dir)
	j := testJob()
	j.Forget = nil
	j.Check = nil
	r := newRunner(t, bin, mount.NewFake())

	res := r.Run(context.Background(), j, Options{Only: PhaseForget})
	if res.Skipped == "" {
		t.Error("Skipped is empty; a job with no maintenance has nothing to forget")
	}
	if got := calls(t, logFile); len(got) != 0 {
		t.Errorf("restic ran anyway: %v", got)
	}
}

// A pre that fails must stop the backup: a database that did not stop yields
// a snapshot that looks fine and is not. The post still runs, because
// whatever pre stopped has to be started again.
func TestBackupPreFailureStopsTheBackupButRunsPost(t *testing.T) {
	dir := t.TempDir()
	bin, logFile := fakeRestic(t, dir)
	marker := filepath.Join(dir, "post-ran")

	j := testJob()
	j.Backup.Pre = "exit 3"
	j.Backup.Post = "touch " + marker
	r := newRunner(t, bin, mount.NewFake())

	res := r.Run(context.Background(), j, Options{})
	if !res.Failed() {
		t.Error("Failed = false after a failing pre")
	}
	if got := calls(t, logFile); len(got) != 0 {
		t.Errorf("restic ran despite the failed pre: %v", got)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("post did not run after a failed pre: %v", err)
	}
}

func TestBackupPostRunsAfterAFailedBackup(t *testing.T) {
	dir := t.TempDir()
	bin, _ := fakeRestic(t, dir, "backup")
	marker := filepath.Join(dir, "post-ran")

	j := testJob()
	j.Backup.Post = "touch " + marker
	r := newRunner(t, bin, mount.NewFake())

	if res := r.Run(context.Background(), j, Options{}); !res.Failed() {
		t.Error("Failed = false after a failing backup")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("post did not run after a failed backup: %v", err)
	}
}

func TestBackupHooksRunInOrderAndAreRecorded(t *testing.T) {
	dir := t.TempDir()
	bin, _ := fakeRestic(t, dir)
	order := filepath.Join(dir, "order")

	j := testJob()
	j.Backup.Pre = "echo pre >> " + order
	j.Backup.Post = "echo post >> " + order
	r := newRunner(t, bin, mount.NewFake())

	res := r.Run(context.Background(), j, Options{})
	if res.Failed() {
		t.Fatalf("job failed: %+v", res)
	}
	b, err := os.ReadFile(order)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Fields(string(b)); len(got) != 2 || got[0] != "pre" || got[1] != "post" {
		t.Errorf("order = %v, want [pre post]", got)
	}

	var names []string
	for _, p := range res.Phases {
		names = append(names, p.Name)
	}
	want := []string{PhaseBackupPre, PhaseBackup, PhaseBackupPost}
	for _, w := range want {
		found := false
		for _, n := range names {
			if n == w {
				found = true
			}
		}
		if !found {
			t.Errorf("phase %q missing from %v", w, names)
		}
	}
}

func TestBackupPostFailureFailsTheJob(t *testing.T) {
	dir := t.TempDir()
	bin, _ := fakeRestic(t, dir)

	j := testJob()
	j.Backup.Post = "exit 1"
	r := newRunner(t, bin, mount.NewFake())

	if res := r.Run(context.Background(), j, Options{}); !res.Failed() {
		t.Error("Failed = false though the post failed; something is left stopped")
	}
}

// Both dry runs promise to change nothing, and stopping a service is a change.
func TestBackupHooksDoNotRunInDryRuns(t *testing.T) {
	for _, mode := range []string{"dry-run", "restic dry-run"} {
		dir := t.TempDir()
		bin, _ := fakeRestic(t, dir)
		marker := filepath.Join(dir, "hook-ran")

		j := testJob()
		j.Backup.Pre = "touch " + marker
		r := newRunner(t, bin, mount.NewFake())
		if mode == "dry-run" {
			r.DryRun = true
			r.Restic.DryRun = true
		} else {
			r.ResticDryRun = true
		}

		if res := r.Run(context.Background(), j, Options{}); res.Failed() {
			t.Fatalf("%s: job failed: %+v", mode, res)
		}
		if _, err := os.Stat(marker); err == nil {
			t.Errorf("%s: the pre hook ran", mode)
		}
	}
}
