package job

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// ErrLockHeld means another resticle invocation is already running this job.
var ErrLockHeld = errors.New("another resticle run holds this job's lock")

type jobLock struct{ f *os.File }

// acquireLock takes a non-blocking exclusive flock. Overlapping cron runs
// exit immediately rather than contending over the mount. See spec 6.7.
func acquireLock(dir, job string) (*jobLock, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create lock dir: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, job+".lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, ErrLockHeld
	}
	return &jobLock{f: f}, nil
}

func (l *jobLock) release() {
	if l == nil || l.f == nil {
		return
	}
	syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	l.f.Close()
}
