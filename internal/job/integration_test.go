package job

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fostam/resticle/internal/config"
	"github.com/fostam/resticle/internal/mount"
	"github.com/fostam/resticle/internal/report"
	"github.com/fostam/resticle/internal/restic"
	"github.com/fostam/resticle/internal/secrets"
	"github.com/fostam/resticle/internal/state"
)

// TestIntegrationBackupForgetCheck drives a real restic against a temporary
// local repository: init, backup, forget --prune, check.
func TestIntegrationBackupForgetCheck(t *testing.T) {
	bin, err := exec.LookPath("restic")
	if err != nil {
		t.Skip("restic not installed")
	}

	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	src := filepath.Join(root, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	// A filename with a space: the legacy scripts would have split it.
	if err := os.WriteFile(filepath.Join(src, "Black Sails.txt"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	env := append(os.Environ(), "RESTIC_PASSWORD=test-pw")
	initCmd := exec.Command(bin, "--repo", repo, "init")
	initCmd.Env = env
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("restic init: %v: %s", err, out)
	}

	yes := true
	j := &config.Job{
		Name: "itest",
		Repo: repo,
		Backup: &config.Backup{
			Paths: []string{src},
			Tags:  []string{"itest"},
			Host:  "testhost",
		},
		Forget: &config.Forget{
			When:         config.WhenAfter,
			Keep:         &config.Keep{Last: 1},
			GroupBy:      "tags",
			Prune:        &yes,
			CleanupCache: &yes,
		},
		Check: &config.Check{Mode: config.CheckSubset, Spread: 2},
	}

	var out bytes.Buffer
	r := &Runner{
		Restic:   &restic.Runner{Bin: bin, Out: &out},
		Mounter:  mount.NewFake(),
		Secrets:  map[string]secrets.Set{"itest": {Password: "test-pw"}},
		StateDir: filepath.Join(root, "state"),
		LockDir:  filepath.Join(root, "lock"),
		Log:      &report.Logger{Out: &out},
	}

	res := r.Run(context.Background(), j, Options{})
	if res.Failed() {
		t.Fatalf("job failed: %+v\n%s", res, out.String())
	}
	if res.Snapshot == "" {
		t.Errorf("no snapshot ID captured:\n%s", out.String())
	}

	st, err := state.Load(r.StateDir, "itest")
	if err != nil {
		t.Fatal(err)
	}
	if st.CheckIndex != 1 {
		t.Errorf("CheckIndex = %d after one run, want 1", st.CheckIndex)
	}

	// A second run must rotate the index back to 0 with spread 2.
	if res2 := r.Run(context.Background(), j, Options{}); res2.Failed() {
		t.Fatalf("second run failed: %+v\n%s", res2, out.String())
	}
	st2, err := state.Load(r.StateDir, "itest")
	if err != nil {
		t.Fatal(err)
	}
	if st2.CheckIndex != 0 {
		t.Errorf("CheckIndex = %d after two runs with spread 2, want 0", st2.CheckIndex)
	}

	// The file with a space must be in the snapshot.
	ls := exec.Command(bin, "--repo", repo, "ls", "latest")
	ls.Env = env
	lsOut, err := ls.CombinedOutput()
	if err != nil {
		t.Fatalf("restic ls: %v: %s", err, lsOut)
	}
	if !bytes.Contains(lsOut, []byte("Black Sails.txt")) {
		t.Errorf("file with a space missing from snapshot:\n%s", lsOut)
	}
}

// TestIntegrationResticDryRunChangesNothing runs a full job against a real
// repository with ResticDryRun set: restic must report what it would do and
// leave the repository exactly as it was.
func TestIntegrationResticDryRunChangesNothing(t *testing.T) {
	bin, err := exec.LookPath("restic")
	if err != nil {
		t.Skip("restic not installed")
	}

	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	src := filepath.Join(root, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "RESTIC_PASSWORD=test-pw")
	initCmd := exec.Command(bin, "--repo", repo, "init")
	initCmd.Env = env
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("restic init: %v: %s", err, out)
	}

	// Three snapshots, so a keep-last 1 forget has something to remove.
	for i := 0; i < 3; i++ {
		if err := os.WriteFile(filepath.Join(src, "a"), []byte{byte('0' + i)}, 0o644); err != nil {
			t.Fatal(err)
		}
		c := exec.Command(bin, "--repo", repo, "backup", src, "--tag", "itest")
		c.Env = env
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("restic backup: %v: %s", err, out)
		}
	}

	count := func() int {
		c := exec.Command(bin, "--repo", repo, "snapshots", "--json")
		c.Env = env
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("restic snapshots: %v: %s", err, out)
		}
		return bytes.Count(out, []byte(`"short_id"`))
	}
	before := count()
	if before != 3 {
		t.Fatalf("setup produced %d snapshots, want 3", before)
	}

	yes := true
	j := &config.Job{
		Name: "itest",
		Repo: repo,
		Backup: &config.Backup{
			Paths: []string{src},
			Tags:  []string{"itest"},
			Host:  "testhost",
		},
		Forget: &config.Forget{
			When:         config.WhenAfter,
			Keep:         &config.Keep{Last: 1},
			GroupBy:      "tags",
			Prune:        &yes,
			CleanupCache: &yes,
		},
		Check: &config.Check{Mode: config.CheckSubset, Spread: 2},
	}

	var out bytes.Buffer
	r := &Runner{
		Restic:       &restic.Runner{Bin: bin, Out: &out},
		Mounter:      mount.NewFake(),
		Secrets:      map[string]secrets.Set{"itest": {Password: "test-pw"}},
		StateDir:     filepath.Join(root, "state"),
		LockDir:      filepath.Join(root, "lock"),
		Log:          &report.Logger{Out: &out},
		ResticDryRun: true,
	}

	res := r.Run(context.Background(), j, Options{})
	if res.Failed() {
		t.Fatalf("job failed: %+v\n%s", res, out.String())
	}
	if got := count(); got != before {
		t.Errorf("snapshots = %d, want %d unchanged: a dry run modified the repository", got, before)
	}
	if !strings.Contains(out.String(), "would be removed") && !strings.Contains(out.String(), "Would have removed") {
		t.Errorf("forget did not report what it would remove:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Would add to the repository") {
		t.Errorf("backup did not report what it would add:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "check skipped (restic dry-run)") {
		t.Errorf("check was not skipped:\n%s", out.String())
	}
	// Nothing happened, so nothing is recorded.
	if _, err := os.Stat(filepath.Join(root, "state", "itest.json")); !os.IsNotExist(err) {
		t.Errorf("state file written during a dry run (err=%v)", err)
	}
}
