package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// DefaultDir is where state lives unless overridden.
const DefaultDir = "/var/lib/resticle"

// State is what resticle remembers about a job between runs. Losing it is
// harmless: rotation restarts at 0 and nothing else depends on it.
type State struct {
	CheckIndex   int       `json:"check_index"`
	LastRun      time.Time `json:"last_run"`
	LastExit     int       `json:"last_exit"`
	LastSnapshot string    `json:"last_snapshot,omitempty"`
}

func path(dir, job string) string {
	return filepath.Join(dir, job+".json")
}

// Load reads a job's state. A missing file is not an error.
func Load(dir, job string) (*State, error) {
	b, err := os.ReadFile(path(dir, job))
	if errors.Is(err, fs.ErrNotExist) {
		return &State{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("parse state %s: %w", path(dir, job), err)
	}
	return &s, nil
}

// Save writes the state atomically, so an interrupted write cannot leave a
// truncated file that fails to parse on the next run.
func (s *State) Save(dir, job string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, job+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp state: %w", err)
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path(dir, job))
}

// NextCheckIndex is the subset index to verify on this run. It clamps a
// stored index that is out of range, which happens when spread shrinks.
func (s *State) NextCheckIndex(spread int) int {
	if spread < 1 {
		return 0
	}
	if s.CheckIndex < 0 {
		return 0
	}
	return s.CheckIndex % spread
}

// AdvanceCheckIndex moves to the next subset. Call only after a check
// succeeded, so a failing check is retried on the same slice.
func (s *State) AdvanceCheckIndex(spread int) {
	if spread < 1 {
		s.CheckIndex = 0
		return
	}
	s.CheckIndex = (s.NextCheckIndex(spread) + 1) % spread
}
