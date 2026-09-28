package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompare(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"v0.12.0", "v0.13.0", -1},
		{"v0.13.0", "v0.13.0", 0},
		{"v0.14.0", "v0.13.0", 1},
		{"v0.9.0", "v0.10.0", -1},
		{"v1.0.0", "v0.13.0", 1},
		{"v0.13.0-rc1", "v0.13.0", -1},
		{"v0.13.0", "v0.13.0-rc1", 1},
		{"v0.13.0-rc1", "v0.13.0-rc2", -1},
	} {
		got, err := Compare(tc.a, tc.b)
		if err != nil {
			t.Errorf("Compare(%q, %q): %v", tc.a, tc.b, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// A development build has no version to compare, and saying so is the point:
// the caller turns it into "use --force".
func TestCompareRejectsNonVersions(t *testing.T) {
	for _, s := range []string{"dev", "", "v1.2", "v1.2.x", "latest"} {
		if _, err := Compare(s, "v0.13.0"); err == nil {
			t.Errorf("Compare(%q, …) = nil error, want a parse error", s)
		}
	}
}

// fakeRelease serves a release and its assets, so the whole path — API,
// checksum, download — is exercised without the network.
func fakeRelease(t *testing.T, tag string, body []byte, corruptSum bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	name := fmt.Sprintf("resticle-%s-linux-amd64", tag)
	sum := sha256.Sum256(body)
	hexsum := hex.EncodeToString(sum[:])
	if corruptSum {
		hexsum = strings.Repeat("0", 64)
	}

	mux.HandleFunc("/repos/fostam/resticle/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"tag_name":%q,"assets":[
			{"name":%q,"browser_download_url":%q,"size":%d},
			{"name":%q,"browser_download_url":%q,"size":100}]}`,
			tag, name, srv.URL+"/dl/"+name, len(body), name+".sha256", srv.URL+"/dl/"+name+".sha256")
	})
	mux.HandleFunc("/dl/"+name, func(w http.ResponseWriter, r *http.Request) { w.Write(body) })
	mux.HandleFunc("/dl/"+name+".sha256", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  %s\n", hexsum, name)
	})
	return srv
}

func newUpdater(srv *httptest.Server) *Updater {
	return &Updater{APIBase: srv.URL, GOOS: "linux", GOARCH: "amd64"}
}

func TestDownloadVerifiesAndInstalls(t *testing.T) {
	body := []byte("#!/bin/sh\necho resticle v0.14.0\n")
	srv := fakeRelease(t, "v0.14.0", body, false)
	u := newUpdater(srv)

	rel, err := u.Latest(context.Background())
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if rel.Tag != "v0.14.0" {
		t.Fatalf("tag = %q", rel.Tag)
	}
	bin, sum, err := u.Assets(rel)
	if err != nil {
		t.Fatalf("Assets: %v", err)
	}

	dir := t.TempDir()
	target := filepath.Join(dir, "resticle")
	if err := os.WriteFile(target, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	verified, err := u.Download(context.Background(), bin, sum, dir)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if err := Verify(verified, "v0.14.0"); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if err := Install(verified, target); err != nil {
		t.Fatalf("Install: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) {
		t.Errorf("installed content = %q", got)
	}
	if fi, err := os.Stat(target); err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v (err %v), want 0755", fi.Mode().Perm(), err)
	}
	// The temporary file must not be left in the install directory.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("install directory holds %d files, want just the binary", len(entries))
	}
}

// A download whose hash does not match is refused, and nothing is installed.
func TestDownloadRefusesAChecksumMismatch(t *testing.T) {
	srv := fakeRelease(t, "v0.14.0", []byte("binary"), true)
	u := newUpdater(srv)
	rel, err := u.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	bin, sum, err := u.Assets(rel)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	if _, err := u.Download(context.Background(), bin, sum, dir); err == nil {
		t.Fatal("Download accepted a checksum mismatch")
	} else if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("err = %v, want a checksum mismatch", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("a failed download left %d files behind", len(entries))
	}
}

// A platform with no published build must say so by name rather than
// installing something else.
func TestAssetsNamesTheMissingPlatform(t *testing.T) {
	srv := fakeRelease(t, "v0.14.0", []byte("binary"), false)
	u := newUpdater(srv)
	u.GOARCH = "riscv64"
	rel, err := u.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = u.Assets(rel)
	if err == nil || !strings.Contains(err.Error(), "resticle-v0.14.0-linux-riscv64") {
		t.Errorf("err = %v, want it to name the missing asset", err)
	}
}

// Verify guards against a binary that cannot run here, which is what a wrong
// architecture looks like — and against one that reports another version.
func TestVerifyRejectsAnUnexpectedVersion(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "resticle")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho resticle v0.1.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Verify(bin, "v0.14.0"); err == nil {
		t.Error("Verify accepted a binary reporting the wrong version")
	}
}

func TestWritableReportsAReadOnlyDirectory(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "bin")
	if err := os.Mkdir(sub, 0o500); err != nil {
		t.Fatal(err)
	}
	if os.Geteuid() == 0 {
		t.Skip("root can write to a read-only directory")
	}
	if err := Writable(filepath.Join(sub, "resticle")); err == nil {
		t.Error("Writable accepted a directory it cannot write to")
	}
}
