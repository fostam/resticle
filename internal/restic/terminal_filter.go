package restic

import "io"

// terminalFilter turns what a terminal shows into what a log should hold.
//
// On a pty, restic writes ANSI escapes and rewrites its status line with
// carriage returns. Both are display, not content: resticle parses this
// stream for the snapshot ID and the repository-lock message, so it keeps the
// lines restic committed with a newline and drops the ones it overwrote,
// which also stops an hours-long backup from buffering every status update it
// ever drew.
//
// A lone CR means "overwrite what I just drew". A CR immediately followed by
// LF is an ordinary line ending: a pty rewrites every LF that way (ONLCR), so
// treating CR alone as an overwrite would discard all of restic's output.
type terminalFilter struct {
	w    io.Writer
	line []byte
	// esc is how far into an escape sequence the filter is: 0 not in one,
	// 1 after ESC, 2 inside a CSI sequence and waiting for its final byte.
	esc int
	cr  bool // a CR is held back until the next byte decides what it meant
}

func newTerminalFilter(w io.Writer) io.Writer { return &terminalFilter{w: w} }

func (f *terminalFilter) Write(p []byte) (int, error) {
	for _, b := range p {
		if f.cr {
			f.cr = false
			if b == '\n' {
				if err := f.flush(); err != nil {
					return 0, err
				}
				continue
			}
			f.line = f.line[:0] // overwritten, so it was never content
		}
		if err := f.byte(b); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

func (f *terminalFilter) byte(b byte) error {
	switch {
	case f.esc == 1:
		// A CSI sequence ("ESC [") runs to a final byte in 0x40..0x7e; any
		// other escape is two bytes long.
		if b == '[' {
			f.esc = 2
		} else {
			f.esc = 0
		}
	case f.esc == 2:
		if b >= 0x40 && b <= 0x7e {
			f.esc = 0
		}
	case b == 0x1b:
		f.esc = 1
	case b == '\r':
		f.cr = true
	case b == '\n':
		return f.flush()
	default:
		f.line = append(f.line, b)
	}
	return nil
}

func (f *terminalFilter) flush() error {
	f.line = append(f.line, '\n')
	_, err := f.w.Write(f.line)
	f.line = f.line[:0]
	return err
}
