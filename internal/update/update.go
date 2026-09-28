// Package update finds, verifies and installs a published resticle release.
//
// Everything here talks to GitHub's releases API over plain net/http: one
// static binary updating itself should not need a package manager, a shell
// pipeline or a third-party dependency.
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Repo is the repository releases are published to.
const Repo = "fostam/resticle"

// Asset is one file attached to a release.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

// Release is the subset of GitHub's release object resticle needs.
type Release struct {
	Tag    string  `json:"tag_name"`
	Assets []Asset `json:"assets"`
}

// Updater fetches releases. The zero value is usable; the fields exist so a
// test can point it at its own server and pretend to be another platform.
type Updater struct {
	Client  *http.Client
	APIBase string // default https://api.github.com
	Repo    string // default Repo
	GOOS    string // default runtime.GOOS
	GOARCH  string // default runtime.GOARCH
}

func (u *Updater) client() *http.Client {
	if u.Client != nil {
		return u.Client
	}
	return &http.Client{Timeout: 2 * time.Minute}
}

func (u *Updater) apiBase() string {
	if u.APIBase != "" {
		return u.APIBase
	}
	return "https://api.github.com"
}

func (u *Updater) repo() string {
	if u.Repo != "" {
		return u.Repo
	}
	return Repo
}

func (u *Updater) goos() string {
	if u.GOOS != "" {
		return u.GOOS
	}
	return runtime.GOOS
}

func (u *Updater) goarch() string {
	if u.GOARCH != "" {
		return u.GOARCH
	}
	return runtime.GOARCH
}

// Latest returns the newest release, excluding pre-releases: GitHub's
// /releases/latest leaves those out, which is the behaviour an unattended
// update wants.
func (u *Updater) Latest(ctx context.Context) (Release, error) {
	return u.release(ctx, u.apiBase()+"/repos/"+u.repo()+"/releases/latest")
}

// ByTag returns one named release, for installing or rolling back to a
// specific version.
func (u *Updater) ByTag(ctx context.Context, tag string) (Release, error) {
	return u.release(ctx, u.apiBase()+"/repos/"+u.repo()+"/releases/tags/"+tag)
}

func (u *Updater) release(ctx context.Context, url string) (Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := u.client().Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return Release{}, fmt.Errorf("no such release at %s", url)
	default:
		return Release{}, fmt.Errorf("%s: %s", url, resp.Status)
	}

	var rel Release
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
		return Release{}, fmt.Errorf("parse release: %w", err)
	}
	if rel.Tag == "" {
		return Release{}, fmt.Errorf("release at %s has no tag", url)
	}
	return rel, nil
}

// AssetName is the published file name for one platform, matching what the
// release workflow uploads.
func (u *Updater) AssetName(tag string) string {
	return fmt.Sprintf("resticle-%s-%s-%s", tag, u.goos(), u.goarch())
}

// Assets picks the binary for this platform and its checksum file.
func (u *Updater) Assets(rel Release) (bin, sum Asset, err error) {
	want := u.AssetName(rel.Tag)
	for _, a := range rel.Assets {
		switch a.Name {
		case want:
			bin = a
		case want + ".sha256":
			sum = a
		}
	}
	if bin.URL == "" {
		return Asset{}, Asset{}, fmt.Errorf("release %s publishes no %s", rel.Tag, want)
	}
	if sum.URL == "" {
		return Asset{}, Asset{}, fmt.Errorf("release %s publishes %s without a .sha256", rel.Tag, want)
	}
	return bin, sum, nil
}

// Download fetches the binary into dir, verifies it against the checksum
// asset, and returns the path to the verified file. The caller installs it;
// dir is the directory the binary will be installed into, so that the final
// step is a rename within one filesystem.
func (u *Updater) Download(ctx context.Context, bin, sum Asset, dir string) (string, error) {
	want, err := u.checksum(ctx, sum)
	if err != nil {
		return "", err
	}

	f, err := os.CreateTemp(dir, ".resticle-update-*")
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			os.Remove(tmp)
		}
	}()

	h := sha256.New()
	if err = u.fetch(ctx, bin.URL, io.MultiWriter(f, h)); err != nil {
		f.Close()
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}

	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		err = fmt.Errorf("checksum mismatch for %s: got %s, want %s", bin.Name, got, want)
		return "", err
	}
	if err = os.Chmod(tmp, 0o755); err != nil {
		return "", err
	}
	return tmp, nil
}

// checksum reads the expected hash from a "<hex>  <filename>" file.
func (u *Updater) checksum(ctx context.Context, sum Asset) (string, error) {
	var buf strings.Builder
	if err := u.fetch(ctx, sum.URL, &buf); err != nil {
		return "", err
	}
	fields := strings.Fields(buf.String())
	if len(fields) == 0 || len(fields[0]) != 64 {
		return "", fmt.Errorf("%s does not contain a sha256", sum.Name)
	}
	return strings.ToLower(fields[0]), nil
}

func (u *Updater) fetch(ctx context.Context, url string, w io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := u.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

// Verify runs the downloaded binary's own version command. A checksum proves
// the download is intact; this proves it can run here at all, which a binary
// for the wrong architecture cannot.
func Verify(path, wantTag string) error {
	out, err := exec.Command(path, "version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s version: %w: %s", filepath.Base(path), err, strings.TrimSpace(string(out)))
	}
	if !strings.Contains(string(out), wantTag) {
		return fmt.Errorf("%s reports %q, not %s",
			filepath.Base(path), strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0]), wantTag)
	}
	return nil
}

// Install moves the verified file over target. A rename is atomic within a
// filesystem, so an interrupted update leaves either the old binary or the
// new one, never half of either — and unlike a write, it works while the old
// binary is running.
func Install(verified, target string) error {
	return os.Rename(verified, target)
}

// Writable reports whether target can be replaced, so that an update can fail
// before it spends a download on a directory it cannot write.
func Writable(target string) error {
	dir := filepath.Dir(target)
	f, err := os.CreateTemp(dir, ".resticle-write-test-*")
	if err != nil {
		return fmt.Errorf("cannot write to %s: %w", dir, err)
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}

// Compare orders two version strings: -1 if current is older than other, 0 if
// equal, 1 if newer. An unparseable current version — a `dev` build — is an
// error, because "newer" is not a question that has an answer then.
func Compare(current, other string) (int, error) {
	c, err := parse(current)
	if err != nil {
		return 0, err
	}
	o, err := parse(other)
	if err != nil {
		return 0, err
	}
	for i := range c.num {
		if c.num[i] != o.num[i] {
			if c.num[i] < o.num[i] {
				return -1, nil
			}
			return 1, nil
		}
	}
	switch {
	case c.pre == o.pre:
		return 0, nil
	case c.pre == "": // a release outranks its own pre-releases
		return 1, nil
	case o.pre == "":
		return -1, nil
	case c.pre < o.pre:
		return -1, nil
	default:
		return 1, nil
	}
}

type version struct {
	num [3]int
	pre string
}

func parse(s string) (version, error) {
	var v version
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	if s == "" {
		return v, fmt.Errorf("empty version")
	}
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		v.pre, s = s[i+1:], s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return v, fmt.Errorf("%q is not a semantic version", s)
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, fmt.Errorf("%q is not a semantic version", s)
		}
		v.num[i] = n
	}
	return v, nil
}
