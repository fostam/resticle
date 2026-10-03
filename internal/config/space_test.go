package config

import (
	"strings"
	"testing"
)

func TestParseSpace(t *testing.T) {
	for _, tc := range []struct {
		in          string
		wantPercent float64
		wantBytes   uint64
	}{
		{"10%", 10, 0},
		{"0.5%", 0.5, 0},
		{" 25 % ", 25, 0},
		{"50GiB", 0, 50 << 30},
		{"50G", 0, 50 << 30},
		{"512MiB", 0, 512 << 20},
		{"2TiB", 0, 2 << 40},
		{"1024", 0, 1024},
		{"4096B", 0, 4096},
	} {
		got, err := ParseSpace(tc.in)
		if err != nil {
			t.Errorf("ParseSpace(%q): %v", tc.in, err)
			continue
		}
		if got.Percent != tc.wantPercent || got.Bytes != tc.wantBytes {
			t.Errorf("ParseSpace(%q) = %+v, want percent=%v bytes=%v", tc.in, got, tc.wantPercent, tc.wantBytes)
		}
		// The value is kept as written, trimmed, so `config dump` emits
		// something that parses back to the same threshold.
		if want := strings.TrimSpace(tc.in); got.String() != want {
			t.Errorf("String() = %q, want %q", got.String(), want)
		}
		if again, err := ParseSpace(got.String()); err != nil || again != got {
			t.Errorf("re-parsing %q gave %+v, %v", got.String(), again, err)
		}
	}
}

func TestParseSpaceRejectsNonsense(t *testing.T) {
	for _, in := range []string{"", "   ", "100%", "0%", "-5%", "ten percent", "0GiB", "-1GiB", "GiB", "0", "5 apples"} {
		if got, err := ParseSpace(in); err == nil {
			t.Errorf("ParseSpace(%q) = %+v, want an error", in, got)
		}
	}
}

// A percentage and an absolute amount must be comparable the same way, which
// is what Floor is for.
func TestSpaceFloor(t *testing.T) {
	const total = 1 << 40 // 1TiB
	pct, err := ParseSpace("10%")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := pct.Floor(total), uint64(total/10); got != want {
		t.Errorf("10%% of 1TiB = %d, want %d", got, want)
	}
	abs, err := ParseSpace("50GiB")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := abs.Floor(total), uint64(50<<30); got != want {
		t.Errorf("50GiB = %d, want %d", got, want)
	}
	// An absolute threshold ignores the filesystem's size.
	if abs.Floor(1<<50) != uint64(50<<30) {
		t.Error("an absolute threshold changed with the total")
	}
}
