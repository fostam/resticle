// Package mount manages the mount lifecycle around a job.
//
// The rule the legacy scripts got wrong: unmount only what we mounted, and
// unmount on every exit path. See spec section 6.2.
package mount

import (
	"fmt"
	"os/exec"
	"strings"
	"syscall"
)

type Usage struct {
	Total uint64
	Used  uint64
	Free  uint64
}

func (u Usage) String() string {
	return fmt.Sprintf("total=%s used=%s free=%s", Human(u.Total), Human(u.Used), Human(u.Free))
}

// FreeDelta renders the change in free space between two readings, signed:
// a backup consumes ("-12.3GiB"), a prune reclaims ("+4.5GiB").
func FreeDelta(before, after Usage) string {
	if after.Free >= before.Free {
		return "+" + Human(after.Free-before.Free)
	}
	return "-" + Human(before.Free-after.Free)
}

// Human renders a byte count in binary units.
func Human(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

// Mounter abstracts the filesystem so the pipeline can be tested without one.
type Mounter interface {
	IsMounted(path string) (bool, error)
	Mount(path string) error
	Unmount(path string) error
	Usage(path string) (Usage, error)
}

// System is the real Mounter. Mount is invoked with no device, resolved from
// /etc/fstab, exactly as the legacy scripts did.
type System struct{}

func (System) IsMounted(path string) (bool, error) {
	err := exec.Command("mountpoint", "-q", path).Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if ok := asExitError(err, &exitErr); ok {
		return false, nil
	}
	return false, fmt.Errorf("mountpoint %s: %w", path, err)
}

func (System) Mount(path string) error {
	out, err := exec.Command("mount", path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("mount %s: %w: %s", path, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (System) Unmount(path string) error {
	out, err := exec.Command("umount", path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("umount %s: %w: %s", path, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (System) Usage(path string) (Usage, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return Usage{}, fmt.Errorf("statfs %s: %w", path, err)
	}
	bs := uint64(st.Bsize)
	total := st.Blocks * bs
	free := st.Bavail * bs
	return Usage{Total: total, Free: free, Used: total - st.Bfree*bs}, nil
}

func asExitError(err error, target **exec.ExitError) bool {
	if e, ok := err.(*exec.ExitError); ok {
		*target = e
		return true
	}
	return false
}

// Session is one job's hold on a mountpoint.
type Session struct {
	m           Mounter
	path        string
	MountedByUs bool
	always      bool
	released    bool
}

// Acquire ensures path is mounted. An empty path is a no-op, so jobs without
// a mount use the same code path. A failure to mount is fatal to the job:
// silently backing up to the underlying directory is the bug this prevents.
// alwaysUnmount makes Release unmount even a mountpoint someone else
// mounted — for a disk that is attached for the job and detached after it,
// where "unmounted" is the intended end state however the run started.
func Acquire(m Mounter, path string, alwaysUnmount bool) (*Session, error) {
	s := &Session{m: m, path: path, always: alwaysUnmount}
	if path == "" {
		s.released = true
		return s, nil
	}
	mounted, err := m.IsMounted(path)
	if err != nil {
		return nil, err
	}
	if mounted {
		return s, nil
	}
	if err := m.Mount(path); err != nil {
		return nil, err
	}
	s.MountedByUs = true
	return s, nil
}

// Release unmounts the path if Acquire mounted it, or unconditionally when
// the session was acquired with alwaysUnmount. Safe to call more than once,
// so callers can both defer it and call it explicitly.
func (s *Session) Release() error {
	if s.released || !(s.MountedByUs || s.always) {
		s.released = true
		return nil
	}
	s.released = true
	return s.m.Unmount(s.path)
}
