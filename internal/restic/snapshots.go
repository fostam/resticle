package restic

import (
	"encoding/json"
	"errors"
	"time"
)

// NewestSnapshot parses the output of `restic snapshots --json` and returns
// the id and time of the snapshot with the greatest Time. Used by both
// job.checkFreshness and `resticle status`.
func NewestSnapshot(stdout string) (id string, t time.Time, err error) {
	var snaps []struct {
		ShortID string    `json:"short_id"`
		Time    time.Time `json:"time"`
	}
	if err := json.Unmarshal([]byte(stdout), &snaps); err != nil {
		return "", time.Time{}, err
	}
	if len(snaps) == 0 {
		return "", time.Time{}, errors.New("no snapshots")
	}
	newest := snaps[0]
	for _, s := range snaps[1:] {
		if s.Time.After(newest.Time) {
			newest = s
		}
	}
	return newest.ShortID, newest.Time, nil
}
