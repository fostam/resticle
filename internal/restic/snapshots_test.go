package restic

import "testing"

func TestNewestSnapshotPicksGreatestTime(t *testing.T) {
	stdout := `[
		{"short_id":"aaa1111","time":"2026-01-01T10:00:00Z"},
		{"short_id":"bbb2222","time":"2026-06-15T08:30:00Z"}
	]`
	id, tm, err := NewestSnapshot(stdout)
	if err != nil {
		t.Fatalf("NewestSnapshot: %v", err)
	}
	if id != "bbb2222" {
		t.Errorf("id = %q, want bbb2222 (the later snapshot)", id)
	}
	if tm.Year() != 2026 || tm.Month() != 6 {
		t.Errorf("time = %v, want the later snapshot's time", tm)
	}
}

func TestNewestSnapshotErrorsOnEmpty(t *testing.T) {
	if _, _, err := NewestSnapshot("[]"); err == nil {
		t.Fatal("expected an error for an empty snapshot list")
	}
}
