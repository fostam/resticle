package restic

import (
	"bytes"
	"strings"
	"testing"
)

func filtered(t *testing.T, chunks ...string) string {
	t.Helper()
	var buf bytes.Buffer
	f := newTerminalFilter(&buf)
	for _, c := range chunks {
		if _, err := f.Write([]byte(c)); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	return buf.String()
}

func TestTerminalFilterKeepsCommittedLines(t *testing.T) {
	// What restic writes on a pty: an erase-line escape, the message, and a
	// newline the pty has turned into CR LF.
	got := filtered(t, "\x1b[2Ksnapshot fe47899e saved\r\n")
	if got != "snapshot fe47899e saved\n" {
		t.Errorf("got %q", got)
	}
}

func TestTerminalFilterDropsOverwrittenStatus(t *testing.T) {
	got := filtered(t,
		"\x1b[2K[0:01] 100 files 1.0 MiB\r",
		"\x1b[2K[0:02] 900 files 9.0 MiB\r",
		"\x1b[2Kprocessed 900 files\r\n")
	if got != "processed 900 files\n" {
		t.Errorf("got %q, want only the committed line", got)
	}
}

// A Write boundary must not change the meaning of CR LF, which is how a long
// backup's output actually arrives.
func TestTerminalFilterHandlesSplitCRLF(t *testing.T) {
	got := filtered(t, "snapshot abc123 saved\r", "\nprocessed 2 files\r\n")
	want := "snapshot abc123 saved\nprocessed 2 files\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestTerminalFilterStripsEscapes(t *testing.T) {
	got := filtered(t, "\x1b[1;31mFatal:\x1b[0m repository is already locked\r\n\x1b(B")
	if !strings.Contains(got, "Fatal: repository is already locked") {
		t.Errorf("got %q", got)
	}
	if strings.ContainsRune(got, 0x1b) {
		t.Errorf("an escape survived: %q", got)
	}
}
