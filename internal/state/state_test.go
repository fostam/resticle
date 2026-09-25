package state

import (
	"path/filepath"
	"testing"
	"time"
)

func TestLoadMissingFileIsZeroValue(t *testing.T) {
	s, err := Load(t.TempDir(), "ext")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if s.CheckIndex != 0 {
		t.Errorf("CheckIndex = %d, want 0", s.CheckIndex)
	}
}

func TestSaveThenLoadRoundTrips(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	s := &State{CheckIndex: 7, LastRun: now, LastExit: 1, LastSnapshot: "abc1234"}
	if err := s.Save(dir, "ext"); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load(dir, "ext")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.CheckIndex != 7 || got.LastExit != 1 || got.LastSnapshot != "abc1234" {
		t.Errorf("round trip mismatch: %+v", got)
	}
	if !got.LastRun.Equal(now) {
		t.Errorf("LastRun = %v, want %v", got.LastRun, now)
	}
}

func TestSaveCreatesDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "deeper")
	s := &State{CheckIndex: 1}
	if err := s.Save(dir, "ext"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := Load(dir, "ext"); err != nil {
		t.Fatalf("Load after Save: %v", err)
	}
}

// Rotation replaces the legacy date-derived index, which never reached full
// coverage on a non-daily schedule and verified nothing on the 29th-31st.
// See spec section 6.4.
func TestAdvanceWrapsAtSpread(t *testing.T) {
	s := &State{}
	var seen []int
	for i := 0; i < 5; i++ {
		seen = append(seen, s.NextCheckIndex(3))
		s.AdvanceCheckIndex(3)
	}
	want := []int{0, 1, 2, 0, 1}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("indices = %v, want %v", seen, want)
		}
	}
}

func TestNextCheckIndexClampsStaleIndex(t *testing.T) {
	// spread shrank from 28 to 7 in the config; a stored index of 20 must
	// not produce an out-of-range subset.
	s := &State{CheckIndex: 20}
	if got := s.NextCheckIndex(7); got < 0 || got >= 7 {
		t.Errorf("NextCheckIndex = %d, want within [0,7)", got)
	}
}

func TestNextCheckIndexHandlesZeroSpread(t *testing.T) {
	s := &State{CheckIndex: 3}
	if got := s.NextCheckIndex(0); got != 0 {
		t.Errorf("NextCheckIndex(0) = %d, want 0", got)
	}
}
