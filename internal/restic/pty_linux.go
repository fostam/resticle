//go:build linux

package restic

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
	"unsafe"
)

// ptyDrainGrace is how long the reader gets to finish after restic exits.
// Reading the master normally ends by itself — Linux returns EIO once no
// process holds the other end — so this only matters if something inherited
// the pty and outlived restic.
const ptyDrainGrace = 2 * time.Second

// runOnPTY runs cmd with a pseudo-terminal as its stdout and stderr, copying
// everything it writes to out.
//
// The point is the terminal itself: restic draws its progress only when its
// stdout is one. A pipe would suppress it, and handing over the real terminal
// would mean resticle no longer sees the output it parses — the snapshot ID
// and the repository-lock message. A pty is both.
//
// sizeFrom, when set, is the real terminal whose window size the pty copies,
// so restic's status line is sized for the window the user is looking at.
func runOnPTY(cmd *exec.Cmd, out io.Writer, sizeFrom *os.File) error {
	master, slave, err := openPTY()
	if err != nil {
		return err
	}
	defer master.Close()
	if sizeFrom != nil {
		copyWinSize(sizeFrom, slave)
	}

	cmd.Stdout = slave
	cmd.Stderr = slave
	if err := cmd.Start(); err != nil {
		slave.Close()
		return err
	}
	// The child holds the slave now. This process must not, or reading the
	// master would never end.
	slave.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		io.Copy(out, master) // ends with EIO when the last writer is gone
	}()

	err = cmd.Wait()
	select {
	case <-done:
	case <-time.After(ptyDrainGrace):
	}
	return err
}

// openPTY allocates a pseudo-terminal pair the way openpty(3) does.
func openPTY() (master, slave *os.File, err error) {
	master, err = os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open /dev/ptmx: %w", err)
	}
	defer func() {
		if err != nil {
			master.Close()
		}
	}()

	var unlock int32 // 0 unlocks
	if err = ioctl(master.Fd(), syscall.TIOCSPTLCK, unsafe.Pointer(&unlock)); err != nil {
		return nil, nil, fmt.Errorf("unlock pty: %w", err)
	}
	var n uint32
	if err = ioctl(master.Fd(), syscall.TIOCGPTN, unsafe.Pointer(&n)); err != nil {
		return nil, nil, fmt.Errorf("pty number: %w", err)
	}

	name := fmt.Sprintf("/dev/pts/%d", n)
	slave, err = os.OpenFile(name, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("open %s: %w", name, err)
	}
	return master, slave, nil
}

type winsize struct {
	rows, cols, xpixels, ypixels uint16
}

// copyWinSize gives the pty the dimensions of the real terminal. A failure is
// not worth reporting: restic then assumes a default width, which costs at
// most a wrapped status line.
func copyWinSize(from, to *os.File) {
	var ws winsize
	if ioctl(from.Fd(), syscall.TIOCGWINSZ, unsafe.Pointer(&ws)) != nil {
		return
	}
	ioctl(to.Fd(), syscall.TIOCSWINSZ, unsafe.Pointer(&ws))
}

func ioctl(fd, request uintptr, arg unsafe.Pointer) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, request, uintptr(arg)); errno != 0 {
		return errno
	}
	return nil
}

// IsTerminal reports whether f is a terminal, which is what decides whether
// restic is given a pty at all.
func IsTerminal(f *os.File) bool {
	var ws winsize
	return ioctl(f.Fd(), syscall.TIOCGWINSZ, unsafe.Pointer(&ws)) == nil
}
