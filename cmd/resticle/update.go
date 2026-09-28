package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/fostam/resticle/internal/mount"
	"github.com/fostam/resticle/internal/update"
)

// cmdUpdate replaces this binary with a published release.
//
// The steps are ordered so that nothing is touched until everything about the
// new binary is known good: resolve the target, refuse early if it cannot be
// written, download beside it, verify the checksum, run the new binary once,
// and only then rename it into place.
func cmdUpdate(g *globals, tag string, checkOnly, force bool) int {
	u := &update.Updater{}
	ctx := context.Background()

	var rel update.Release
	var err error
	if tag != "" {
		rel, err = u.ByTag(ctx, tag)
	} else {
		rel, err = u.Latest(ctx)
	}
	if err != nil {
		fmt.Fprintln(g.out, "update error:", err)
		return 2
	}

	if checkOnly {
		msg, code := checkReport(version, rel.Tag)
		fmt.Fprint(g.out, msg)
		return code
	}
	// A named version is an explicit request — installing or rolling back to
	// exactly that release — so only an unpinned update is held to "is this
	// actually newer".
	if tag == "" && !force {
		if msg, code, stop := updatePlan(version, rel.Tag); stop {
			fmt.Fprint(g.out, msg)
			return code
		}
	}

	target, err := selfPath()
	if err != nil {
		fmt.Fprintln(g.out, "update error:", err)
		return 2
	}
	if err := update.Writable(target); err != nil {
		fmt.Fprintf(g.out, "update error: %v; run it as the owner of %s (sudo)\n", err, target)
		return 2
	}

	bin, sum, err := u.Assets(rel)
	if err != nil {
		fmt.Fprintln(g.out, "update error:", err)
		return 2
	}

	fmt.Fprintf(g.out, "%s -> %s (%s/%s)\n", version, rel.Tag, runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(g.out, "downloading %s (%s)\n", bin.Name, mount.Human(uint64(bin.Size)))
	verified, err := u.Download(ctx, bin, sum, filepath.Dir(target))
	if err != nil {
		fmt.Fprintln(g.out, "update error:", err)
		return 2
	}
	defer os.Remove(verified) // a no-op once the rename has moved it

	fmt.Fprintln(g.out, "checksum ok")
	if err := update.Verify(verified, rel.Tag); err != nil {
		fmt.Fprintln(g.out, "update error: the downloaded binary does not run here:", err)
		return 2
	}
	if err := update.Install(verified, target); err != nil {
		fmt.Fprintln(g.out, "update error:", err)
		return 2
	}
	fmt.Fprintf(g.out, "installed %s to %s\n", rel.Tag, target)
	return 0
}

// updatePlan decides whether an install should go ahead, and what to say
// first. Kept apart from the download so the decision can be read — and
// tested — on its own.
// Being up to date is success; a version that cannot be compared is a usage
// problem, which --force or a named version resolves.
func updatePlan(current, tag string) (msg string, code int, stop bool) {
	cmp, err := update.Compare(current, tag)
	switch {
	case err != nil:
		return fmt.Sprintf("this build reports version %q, which cannot be compared with %s;"+
			" use --force, or name the version to install\n", current, tag), 2, true
	case cmp == 0:
		return fmt.Sprintf("already on %s, the newest release\n", current), 0, true
	case cmp > 0:
		return fmt.Sprintf("this build is %s, newer than the newest release %s;"+
			" use --force to install the release anyway\n", current, tag), 0, true
	default:
		return "", 0, false
	}
}

// checkReport is --check: say what is available and change nothing. Exit 1
// means "an update is available", so a cron line or a monitoring check can act
// on the exit code alone.
func checkReport(current, tag string) (msg string, code int) {
	cmp, err := update.Compare(current, tag)
	switch {
	case err != nil:
		return fmt.Sprintf("newest release %s; this build reports version %q and cannot be compared\n",
			tag, current), 1
	case cmp < 0:
		return fmt.Sprintf("update available: %s -> %s\n", current, tag), 1
	case cmp > 0:
		return fmt.Sprintf("this build is %s, newer than the newest release %s\n", current, tag), 0
	default:
		return fmt.Sprintf("already on %s, the newest release\n", current), 0
	}
}

// selfPath resolves the running binary, following symlinks so that an update
// replaces the file itself rather than turning a link into a binary.
func selfPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", err
	}
	return resolved, nil
}
