package mount

import (
	"errors"
	"testing"
)

func TestAcquireMountsWhenNotMounted(t *testing.T) {
	f := NewFake()
	s, err := Acquire(f, "/mnt/usb", false)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if !f.Mounted["/mnt/usb"] {
		t.Error("path was not mounted")
	}
	if !s.MountedByUs {
		t.Error("MountedByUs = false, want true")
	}
	if f.MountCalls != 1 {
		t.Errorf("MountCalls = %d, want 1", f.MountCalls)
	}
}

// Only unmount what we mounted: the legacy scripts unmounted unconditionally
// and could pull a disk out from under another user. See spec defect 4.
func TestAcquireDoesNotUnmountAPreexistingMount(t *testing.T) {
	f := NewFake()
	f.Mounted["/mnt/usb"] = true

	s, err := Acquire(f, "/mnt/usb", false)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if s.MountedByUs {
		t.Error("MountedByUs = true for a pre-existing mount")
	}
	if err := s.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if !f.Mounted["/mnt/usb"] {
		t.Error("Release unmounted a mount it did not create")
	}
	if f.UnmountCalls != 0 {
		t.Errorf("UnmountCalls = %d, want 0", f.UnmountCalls)
	}
}

func TestReleaseUnmountsWhatWeMounted(t *testing.T) {
	f := NewFake()
	s, _ := Acquire(f, "/mnt/usb", false)
	if err := s.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if f.Mounted["/mnt/usb"] {
		t.Error("path still mounted after Release")
	}
}

func TestReleaseIsIdempotent(t *testing.T) {
	f := NewFake()
	s, _ := Acquire(f, "/mnt/usb", false)
	if err := s.Release(); err != nil {
		t.Fatal(err)
	}
	if err := s.Release(); err != nil {
		t.Fatalf("second Release: %v", err)
	}
	if f.UnmountCalls != 1 {
		t.Errorf("UnmountCalls = %d, want 1", f.UnmountCalls)
	}
}

func TestAcquireWithEmptyPathIsNoop(t *testing.T) {
	f := NewFake()
	s, err := Acquire(f, "", false)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if f.MountCalls != 0 {
		t.Error("mounted despite empty path")
	}
	if err := s.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

func TestAcquirePropagatesMountFailure(t *testing.T) {
	f := NewFake()
	f.MountErr = errors.New("no such device")
	if _, err := Acquire(f, "/mnt/usb", false); err == nil {
		t.Fatal("expected error when mount fails, got nil")
	}
}

func TestUsageString(t *testing.T) {
	u := Usage{Total: 2 << 40, Used: 1 << 40, Free: 1 << 40}
	if got := u.String(); got == "" {
		t.Error("Usage.String() is empty")
	}
}

// A disk that is attached for the job and detached after it should end up
// unmounted however the run started — the opt-in opposite of the default.
func TestAlwaysUnmountReleasesAPreexistingMount(t *testing.T) {
	f := NewFake()
	f.Mounted["/mnt/usb"] = true

	s, err := Acquire(f, "/mnt/usb", true)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if s.MountedByUs {
		t.Error("MountedByUs = true for a pre-existing mount")
	}
	if f.MountCalls != 0 {
		t.Errorf("MountCalls = %d, want 0", f.MountCalls)
	}
	if err := s.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if f.Mounted["/mnt/usb"] {
		t.Error("still mounted after Release with alwaysUnmount")
	}
	if f.UnmountCalls != 1 {
		t.Errorf("UnmountCalls = %d, want 1", f.UnmountCalls)
	}
}

func TestAlwaysUnmountIsStillIdempotent(t *testing.T) {
	f := NewFake()
	f.Mounted["/mnt/usb"] = true
	s, _ := Acquire(f, "/mnt/usb", true)
	if err := s.Release(); err != nil {
		t.Fatal(err)
	}
	if err := s.Release(); err != nil {
		t.Fatalf("second Release: %v", err)
	}
	if f.UnmountCalls != 1 {
		t.Errorf("UnmountCalls = %d, want 1", f.UnmountCalls)
	}
}

func TestAlwaysUnmountWithEmptyPathIsNoop(t *testing.T) {
	f := NewFake()
	s, err := Acquire(f, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if f.UnmountCalls != 0 {
		t.Errorf("UnmountCalls = %d, want 0 for a job with no mount", f.UnmountCalls)
	}
}
