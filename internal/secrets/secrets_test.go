package secrets

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fostam/resticle/internal/config"
)

func write(t *testing.T, dir, name, body string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadFromPasswordFile(t *testing.T) {
	dir := t.TempDir()
	pw := write(t, dir, "pw.txt", "hunter2\n", 0o600)
	j := &config.Job{Name: "ext", PasswordFile: pw}

	got, errs, err := Load("", []*config.Job{j})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(errs) != 0 {
		t.Fatalf("per-job errors: %v", errs)
	}
	if got["ext"].Password != "hunter2" {
		t.Errorf("Password = %q, want hunter2 (trailing newline stripped)", got["ext"].Password)
	}
}

func TestLoadFromEnvFile(t *testing.T) {
	dir := t.TempDir()
	pw := write(t, dir, "pw.txt", "pw", 0o600)
	env := write(t, dir, "b2.env", "# comment\nB2_ACCOUNT_ID=abc\nB2_ACCOUNT_KEY=def\n\n", 0o600)
	j := &config.Job{Name: "backblaze", PasswordFile: pw, EnvFile: env}

	got, errs, err := Load("", []*config.Job{j})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(errs) != 0 {
		t.Fatalf("per-job errors: %v", errs)
	}
	if got["backblaze"].Env["B2_ACCOUNT_ID"] != "abc" {
		t.Errorf("B2_ACCOUNT_ID = %q", got["backblaze"].Env["B2_ACCOUNT_ID"])
	}
	if got["backblaze"].Env["B2_ACCOUNT_KEY"] != "def" {
		t.Errorf("B2_ACCOUNT_KEY = %q", got["backblaze"].Env["B2_ACCOUNT_KEY"])
	}
}

// Permissions are checked because the legacy setup leaked a credential from a
// world-readable file. See spec section 5.4.
func TestLoadRejectsWorldReadableSecret(t *testing.T) {
	dir := t.TempDir()
	pw := write(t, dir, "pw.txt", "pw", 0o644)
	j := &config.Job{Name: "ext", PasswordFile: pw}

	_, errs, err := Load("", []*config.Job{j})
	if err != nil {
		t.Fatalf("Load: %v, want no fatal (file-level) error", err)
	}
	if errs["ext"] == nil {
		t.Fatal("expected a per-job error for a group/world-readable secret file, got nil")
	}
}

func TestLoadMissingSecretIsError(t *testing.T) {
	j := &config.Job{Name: "ext", Secrets: "ext"}
	_, errs, err := Load("", []*config.Job{j})
	if err != nil {
		t.Fatalf("Load: %v, want no fatal (file-level) error", err)
	}
	if errs["ext"] == nil {
		t.Fatal("expected a per-job error when a job has no secret source, got nil")
	}
}

func TestParseSecretsYAML(t *testing.T) {
	doc := []byte(`
ext:
  password: pw-ext
backblaze:
  password: pw-b2
  env:
    B2_ACCOUNT_ID: abc
`)
	got, err := parseSecretsYAML(doc)
	if err != nil {
		t.Fatalf("parseSecretsYAML: %v", err)
	}
	if got["ext"].Password != "pw-ext" {
		t.Errorf("ext password = %q", got["ext"].Password)
	}
	if got["backblaze"].Env["B2_ACCOUNT_ID"] != "abc" {
		t.Errorf("b2 env = %v", got["backblaze"].Env)
	}
}

func TestRedact(t *testing.T) {
	if got := Redact("hunter2"); got != "[redacted]" {
		t.Errorf("Redact = %q", got)
	}
	if got := Redact(""); got != "" {
		t.Errorf("Redact(empty) = %q, want empty", got)
	}
}

func TestLoadSOPSEncryptedFile(t *testing.T) {
	if _, err := exec.LookPath("sops"); err != nil {
		t.Skip("sops not installed")
	}
	if _, err := exec.LookPath("age-keygen"); err != nil {
		t.Skip("age-keygen not installed")
	}
	dir := t.TempDir()

	keyFile := filepath.Join(dir, "keys.txt")
	out, err := exec.Command("age-keygen", "-o", keyFile).CombinedOutput()
	if err != nil {
		t.Fatalf("age-keygen: %v: %s", err, out)
	}
	recipient := ""
	for _, line := range strings.Split(string(out), "\n") {
		if i := strings.Index(line, "age1"); i >= 0 {
			recipient = strings.TrimSpace(line[i:])
		}
	}
	if recipient == "" {
		t.Fatalf("no recipient in age-keygen output: %s", out)
	}

	plain := write(t, dir, "secrets.yaml", "ext:\n  password: pw-ext\n", 0o600)
	enc := exec.Command("sops", "-e", "-i", "--age", recipient, plain)
	enc.Dir = dir
	enc.Env = append(os.Environ(), "SOPS_AGE_KEY_FILE="+keyFile)
	if out, err := enc.CombinedOutput(); err != nil {
		t.Fatalf("sops -e: %v: %s", err, out)
	}
	// sops -e -i may rewrite the file with a looser mode; the permission
	// guard in readSecretFile requires owner-only access.
	if err := os.Chmod(plain, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SOPS_AGE_KEY_FILE", keyFile)

	j := &config.Job{Name: "ext", Secrets: "ext"}
	got, errs, err := Load(plain, []*config.Job{j})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(errs) != 0 {
		t.Fatalf("per-job errors: %v", errs)
	}
	if got["ext"].Password != "pw-ext" {
		t.Errorf("Password = %q, want pw-ext", got["ext"].Password)
	}
}

// I1: a broken secrets FILE (unreadable/undecryptable) is a fatal error that
// affects every job, unlike a single job's own missing secret.
func TestLoadSecretsFileFailureIsFatal(t *testing.T) {
	dir := t.TempDir()
	bad := write(t, dir, "secrets.yaml", "not: [valid", 0o600)
	j := &config.Job{Name: "ext", Secrets: "ext"}

	_, _, err := Load(bad, []*config.Job{j})
	if err == nil {
		t.Fatal("expected a fatal error for a broken secrets file, got nil")
	}
}

// A secret inline in the config file, for an installation that does not
// commit its config anywhere and has no use for sops.
func TestLoadFromInlineConfig(t *testing.T) {
	j := &config.Job{
		Name:     "offsite",
		Password: "hunter2",
		Env:      map[string]string{"B2_ACCOUNT_ID": "abc"},
	}
	got, _, err := Load("", []*config.Job{j})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got["offsite"].Password != "hunter2" {
		t.Errorf("Password = %q", got["offsite"].Password)
	}
	if got["offsite"].Env["B2_ACCOUNT_ID"] != "abc" {
		t.Errorf("Env = %v", got["offsite"].Env)
	}
}

// Two sources for one job is refused, not silently resolved: rotating the
// password in the secrets file would otherwise change nothing. A neighbour
// that only uses the file is unaffected, so jobs can migrate one at a time.
func TestInlineSecretAndSecretsFileEntryIsAnError(t *testing.T) {
	dir := t.TempDir()
	file := write(t, dir, "secrets.yaml", "a:\n  password: from-file\nb:\n  password: from-file\n", 0o600)

	inline := &config.Job{Name: "a", Secrets: "a", Password: "from-config"}
	fromFile := &config.Job{Name: "b", Secrets: "b"}

	got, errs, err := Load(file, []*config.Job{inline, fromFile})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if errs["a"] == nil || !strings.Contains(errs["a"].Error(), "use one source") {
		t.Errorf("a: err = %v, want a refusal naming both sources", errs["a"])
	}
	if _, ok := got["a"]; ok {
		t.Error("a resolved to a secret despite the ambiguity")
	}
	if got["b"].Password != "from-file" {
		t.Errorf("b = %q, want the file value", got["b"].Password)
	}
}
