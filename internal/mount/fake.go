package mount

// Fake is an in-memory Mounter for tests.
type Fake struct {
	Mounted      map[string]bool
	MountCalls   int
	UnmountCalls int
	MountErr     error
	UnmountErr   error
	UsageValue   Usage
}

func NewFake() *Fake {
	return &Fake{Mounted: map[string]bool{}}
}

func (f *Fake) IsMounted(path string) (bool, error) { return f.Mounted[path], nil }

func (f *Fake) Mount(path string) error {
	f.MountCalls++
	if f.MountErr != nil {
		return f.MountErr
	}
	f.Mounted[path] = true
	return nil
}

func (f *Fake) Unmount(path string) error {
	f.UnmountCalls++
	if f.UnmountErr != nil {
		return f.UnmountErr
	}
	delete(f.Mounted, path)
	return nil
}

func (f *Fake) Usage(path string) (Usage, error) { return f.UsageValue, nil }
