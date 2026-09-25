package restic

import (
	"reflect"
	"testing"

	"github.com/fostam/resticle/internal/config"
	"github.com/fostam/resticle/internal/secrets"
)

func boolPtr(b bool) *bool { return &b }

func extJob() *config.Job {
	return &config.Job{
		Name: "ext",
		Repo: "/mnt/backup/backup-ext/restic/ext",
		Backup: &config.Backup{
			Paths:         []string{"/home", "/srv/data"},
			Tags:          []string{"ext"},
			Host:          "alpha",
			OneFileSystem: boolPtr(true),
			Excludes: []string{
				"@/etc/resticle/excludes/common.txt",
				"@/etc/resticle/excludes/local-usb.txt",
			},
		},
		Forget: &config.Forget{
			When:         config.WhenAfter,
			Keep:         &config.Keep{Last: 2},
			GroupBy:      "tags",
			Prune:        boolPtr(true),
			CleanupCache: boolPtr(true),
		},
		Check: &config.Check{Mode: config.CheckSubset, Spread: 28},
	}
}

func TestBackupArgs(t *testing.T) {
	want := []string{
		"--verbose",
		"--repo", "/mnt/backup/backup-ext/restic/ext",
		"backup",
		"--host", "alpha",
		"--tag", "ext",
		"--exclude-file", "/etc/resticle/excludes/common.txt",
		"--exclude-file", "/etc/resticle/excludes/local-usb.txt",
		"--one-file-system",
		"/home",
		"/srv/data",
	}
	if got := BackupArgs(extJob(), false); !reflect.DeepEqual(got, want) {
		t.Errorf("BackupArgs()\n got %q\nwant %q", got, want)
	}
}

func TestBackupArgsOmitsOneFileSystemWhenFalse(t *testing.T) {
	j := extJob()
	j.Backup.OneFileSystem = boolPtr(false)
	for _, a := range BackupArgs(j, false) {
		if a == "--one-file-system" {
			t.Fatal("--one-file-system present when disabled")
		}
	}
}

// Paths with spaces must survive as single argv elements. The legacy scripts
// passed them through an unquoted shell variable. See spec defect 8.
func TestBackupArgsKeepsPathsWithSpacesIntact(t *testing.T) {
	j := extJob()
	j.Backup.Paths = []string{"/mnt/net/Daten/Black Sails"}
	got := BackupArgs(j, false)
	last := got[len(got)-1]
	if last != "/mnt/net/Daten/Black Sails" {
		t.Errorf("last arg = %q, want the path as one element", last)
	}
}

func TestForgetArgs(t *testing.T) {
	want := []string{
		"--verbose",
		"--repo", "/mnt/backup/backup-ext/restic/ext",
		"forget",
		"--group-by", "tags",
		"--cleanup-cache",
		"--keep-last", "2",
		"--prune",
	}
	if got := ForgetArgs(extJob(), false); !reflect.DeepEqual(got, want) {
		t.Errorf("ForgetArgs()\n got %q\nwant %q", got, want)
	}
}

func TestForgetArgsFullRetentionOrder(t *testing.T) {
	j := extJob()
	j.Forget.Keep = &config.Keep{Last: 15, Daily: 21, Weekly: 12, Monthly: 12, Yearly: 10}
	want := []string{
		"--keep-last", "15",
		"--keep-daily", "21",
		"--keep-weekly", "12",
		"--keep-monthly", "12",
		"--keep-yearly", "10",
	}
	got := ForgetArgs(j, false)
	if !containsSeq(got, want) {
		t.Errorf("ForgetArgs()\n got %q\nwant subsequence %q", got, want)
	}
}

func TestForgetArgsOmitsZeroKeeps(t *testing.T) {
	for _, a := range ForgetArgs(extJob(), false) {
		if a == "--keep-daily" || a == "--keep-yearly" {
			t.Fatalf("zero retention emitted: %q", a)
		}
	}
}

func TestCheckArgsSubset(t *testing.T) {
	got, ok := CheckArgs(extJob(), 5)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	want := []string{
		"--verbose",
		"--repo", "/mnt/backup/backup-ext/restic/ext",
		"check",
		"--read-data-subset", "6/28",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CheckArgs()\n got %q\nwant %q", got, want)
	}
}

func TestCheckArgsStructureReadsNoData(t *testing.T) {
	j := extJob()
	j.Check = &config.Check{Mode: config.CheckStructure}
	got, ok := CheckArgs(j, 0)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	for _, a := range got {
		if a == "--read-data" || a == "--read-data-subset" {
			t.Fatalf("structure mode emitted %q", a)
		}
	}
}

func TestCheckArgsFull(t *testing.T) {
	j := extJob()
	j.Check = &config.Check{Mode: config.CheckFull}
	got, _ := CheckArgs(j, 0)
	if !containsSeq(got, []string{"--read-data"}) {
		t.Errorf("full mode args = %q, want --read-data", got)
	}
}

func TestCheckArgsOffReturnsFalse(t *testing.T) {
	j := extJob()
	j.Check = &config.Check{Mode: config.CheckOff}
	if _, ok := CheckArgs(j, 0); ok {
		t.Error("ok = true for mode off, want false")
	}
}

func TestEnvCarriesPasswordAndBackendVars(t *testing.T) {
	j := extJob()
	set := secrets.Set{Password: "hunter2", Env: map[string]string{"B2_ACCOUNT_ID": "abc"}}
	env := Env(j, set)

	if !hasEnv(env, "RESTIC_PASSWORD=hunter2") {
		t.Errorf("env missing RESTIC_PASSWORD: %q", env)
	}
	if !hasEnv(env, "B2_ACCOUNT_ID=abc") {
		t.Errorf("env missing backend var: %q", env)
	}
}

func TestRedactedEnvHidesValues(t *testing.T) {
	set := secrets.Set{Password: "hunter2", Env: map[string]string{"B2_ACCOUNT_KEY": "secret"}}
	for _, e := range RedactedEnv(extJob(), set) {
		if e == "RESTIC_PASSWORD=hunter2" || e == "B2_ACCOUNT_KEY=secret" {
			t.Fatalf("secret leaked in redacted env: %q", e)
		}
	}
	if !hasEnv(RedactedEnv(extJob(), set), "RESTIC_PASSWORD=[redacted]") {
		t.Error("redacted env missing RESTIC_PASSWORD=[redacted]")
	}
}

// No builder may ever put a secret on the command line. See spec section 5.4.
func TestNoArgvContainsSecrets(t *testing.T) {
	j := extJob()
	all := [][]string{BackupArgs(j, false), ForgetArgs(j, false), SnapshotsArgs(j)}
	if c, ok := CheckArgs(j, 0); ok {
		all = append(all, c)
	}
	for _, argv := range all {
		for _, a := range argv {
			if a == "--password-file" || a == "--password-command" {
				t.Fatalf("argv carries a password flag: %q", argv)
			}
		}
	}
}

func hasEnv(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}

// containsSeq reports whether want appears as a contiguous subsequence of got.
func containsSeq(got, want []string) bool {
	for i := 0; i+len(want) <= len(got); i++ {
		if reflect.DeepEqual(got[i:i+len(want)], want) {
			return true
		}
	}
	return false
}

// An "@" prefix means "file of patterns", but only with an absolute path:
// restic patterns beginning with "@" are ordinary (a NAS's @eaDir is
// excluded as "@*") and must not be mistaken for file references.
func TestBackupArgsMixesPatternsAndExcludeFiles(t *testing.T) {
	j := extJob()
	j.Backup.Excludes = []string{
		"@/etc/resticle/excludes/general.txt",
		"@*",
		"*.iso",
		"@@/literal-at-path",
	}
	want := []string{
		"--exclude-file", "/etc/resticle/excludes/general.txt",
		"--exclude", "@*",
		"--exclude", "*.iso",
		"--exclude", "@/literal-at-path",
	}
	if got := BackupArgs(j, false); !containsSeq(got, want) {
		t.Errorf("BackupArgs()\n got %q\nwant subsequence %q", got, want)
	}
}

func TestExcludeArg(t *testing.T) {
	for _, tc := range []struct{ in, flag, val string }{
		{"@/abs/file.txt", "--exclude-file", "/abs/file.txt"},
		{"@*", "--exclude", "@*"},
		{"@eaDir", "--exclude", "@eaDir"},
		{"@relative/file.txt", "--exclude", "@relative/file.txt"},
		{"@@/literal", "--exclude", "@/literal"},
		{"node_modules", "--exclude", "node_modules"},
	} {
		flag, val := excludeArg(tc.in)
		if flag != tc.flag || val != tc.val {
			t.Errorf("excludeArg(%q) = %q %q, want %q %q", tc.in, flag, val, tc.flag, tc.val)
		}
	}
}

func TestArgsCarryResticDryRun(t *testing.T) {
	j := extJob()
	if !containsSeq(BackupArgs(j, true), []string{"backup", "--dry-run"}) {
		t.Errorf("BackupArgs dry-run = %q", BackupArgs(j, true))
	}
	if !containsSeq(ForgetArgs(j, true), []string{"forget", "--dry-run"}) {
		t.Errorf("ForgetArgs dry-run = %q", ForgetArgs(j, true))
	}
	for _, a := range BackupArgs(j, false) {
		if a == "--dry-run" {
			t.Fatal("BackupArgs emitted --dry-run when not asked")
		}
	}
}

func TestFindArgs(t *testing.T) {
	want := []string{
		"--verbose",
		"--repo", "/mnt/backup/backup-ext/restic/ext",
		"find", "--long", "*.kdbx",
	}
	if got := FindArgs(extJob(), "*.kdbx"); !reflect.DeepEqual(got, want) {
		t.Errorf("FindArgs()\n got %q\nwant %q", got, want)
	}
}

// Unset means the flag is not passed, so restic's own default applies —
// and keeps applying if restic ever changes it.
func TestForgetArgsOmitsGroupByWhenUnset(t *testing.T) {
	j := extJob()
	j.Forget.GroupBy = ""
	for _, a := range ForgetArgs(j, false) {
		if a == "--group-by" {
			t.Fatalf("emitted --group-by with none configured: %q", ForgetArgs(j, false))
		}
	}
}
