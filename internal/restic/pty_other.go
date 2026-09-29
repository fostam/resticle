//go:build !linux

package restic

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
)

// resticle is built for Linux; the pty progress path is not ported, and a
// build elsewhere falls back to the plain pipe rather than failing to compile.
func runOnPTY(cmd *exec.Cmd, out io.Writer, sizeFrom *os.File) error {
	return fmt.Errorf("no pseudo-terminal support on %s", runtime.GOOS)
}

func IsTerminal(f *os.File) bool { return false }
