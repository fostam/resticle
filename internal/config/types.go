package config

import (
	"fmt"
	"strings"
	"time"
)

// Duration wraps time.Duration so YAML strings like "16h" decode directly.
type Duration time.Duration

func (d *Duration) UnmarshalYAML(unmarshal func(any) error) error {
	var s string
	if err := unmarshal(&s); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

func (d Duration) Std() time.Duration { return time.Duration(d) }

// MarshalYAML renders a duration the way it is written in the config file
// ("16h"), not as the nanosecond count the underlying int64 would produce,
// and not with the zero tail time.Duration prints ("16h0m0s"). `config dump`
// relies on this to emit YAML that can be loaded back.
func (d Duration) MarshalYAML() (any, error) {
	s := time.Duration(d).String()
	if t := strings.TrimSuffix(s, "0s"); t != s && strings.HasSuffix(t, "m") {
		s = t
	}
	if t := strings.TrimSuffix(s, "0m"); t != s && strings.HasSuffix(t, "h") {
		s = t
	}
	return s, nil
}

type Config struct {
	// Where this installation keeps things. Each has a built-in default and
	// can be overridden per invocation by the matching flag, but belongs
	// here so that every cron line agrees without repeating itself: two
	// invocations disagreeing about StateDir would silently reset the
	// verification rotation.
	SecretsFile string `yaml:"secrets_file,omitempty"`
	StateDir    string `yaml:"state_dir,omitempty"`
	LockDir     string `yaml:"lock_dir,omitempty"`

	Defaults *Job            `yaml:"defaults,omitempty"`
	Jobs     map[string]*Job `yaml:"jobs,omitempty"`
	// JobOrder is the order the `jobs:` keys appeared in the YAML mapping.
	// "All jobs" iteration uses this instead of alphabetical order.
	JobOrder []string `yaml:"-"`
}

type Job struct {
	// Name is filled in from the jobs map key; it is never read from YAML.
	Name string `yaml:"-"`

	// ResticExecutable is per job because reaching one repository can need a
	// different program than another: a wrapper script that opens an SSH
	// tunnel first, say. Inherited from defaults like everything else.
	ResticExecutable string `yaml:"restic_executable,omitempty"`

	Repo    string `yaml:"repo,omitempty"`
	Mount   string `yaml:"mount,omitempty"`
	Secrets string `yaml:"secrets,omitempty"`
	// Password and Env hold the secret inline, for an installation whose
	// config file is not committed anywhere. resticle then refuses to run
	// if that file is group- or world-readable, exactly as it does for a
	// secrets file.
	Password     string            `yaml:"password,omitempty"`
	Env          map[string]string `yaml:"env,omitempty"`
	PasswordFile string            `yaml:"password_file,omitempty"`
	EnvFile      string            `yaml:"env_file,omitempty"`
	RunAs        string            `yaml:"run_as,omitempty"`
	MaxAge       *Duration         `yaml:"max_age,omitempty"`
	OnSuccess    string            `yaml:"on_success,omitempty"`
	OnFailure    string            `yaml:"on_failure,omitempty"`

	// Mode says how the job is triggered. One axis, three positions, so the
	// meaningless combinations a pair of booleans would allow cannot be
	// written at all.
	//
	//   ModeScheduled  run under `run --all` (the default)
	//   ModeManual     excluded from --all; runs when named. For a job whose
	//                  disk is plugged in for the occasion. Omitted from
	//                  selection rather than skipped, since a nightly
	//                  "skipped" line would defeat --quiet-on-success.
	//   ModeDisabled   never runs, however invoked. `exec` still works:
	//                  disabling stops resticle acting on the repository,
	//                  not you inspecting it.
	Mode string `yaml:"mode,omitempty"`

	// UnmountAlways unmounts the mountpoint when the job finishes even if it
	// was already mounted beforehand. Requires Mount.
	UnmountAlways bool `yaml:"unmount_always,omitempty"`

	Backup *Backup `yaml:"backup,omitempty"`
	Forget *Forget `yaml:"forget,omitempty"`
	Check  *Check  `yaml:"check,omitempty"`
}

type Backup struct {
	// Pre runs before the backup and Post after it — quiescing a database
	// for the duration of the read, typically. They belong to the backup
	// rather than the job because nothing else touches the source: stopping
	// a service for a two-hour verification would be a bug.
	//
	// Pre failing means the backup does not run: a database that did not
	// stop yields a snapshot that looks fine and is not. Post runs whatever
	// happened, including after a failed Pre, because the thing that
	// restores state must not be conditional on success.
	Pre  string `yaml:"pre,omitempty"`
	Post string `yaml:"post,omitempty"`

	// Timeout bounds the backup phase; zero or unset means no limit.
	Timeout       *Duration `yaml:"timeout,omitempty"`
	Paths         []string  `yaml:"paths,omitempty"`
	Tags          []string  `yaml:"tags,omitempty"`
	Excludes      []string  `yaml:"excludes,omitempty"`
	Host          string    `yaml:"host,omitempty"`
	OneFileSystem *bool     `yaml:"one_file_system,omitempty"`
}

// Forget expires snapshots and, with Prune, reclaims their space. Present
// means the phase happens — the same rule Backup and Check follow.
type Forget struct {
	// Timeout bounds the forget call; zero or unset means no limit. It is
	// separate from Check's because the two differ by orders of magnitude:
	// expiring takes minutes, reading data back takes hours.
	Timeout      *Duration `yaml:"timeout,omitempty"`
	When         string    `yaml:"when,omitempty"`
	Keep         *Keep     `yaml:"keep,omitempty"`
	GroupBy      string    `yaml:"group_by,omitempty"`
	Prune        *bool     `yaml:"prune,omitempty"`
	CleanupCache *bool     `yaml:"cleanup_cache,omitempty"`
}

// Keep is a policy unit: a job that sets it replaces the inherited policy
// wholesale rather than merging field by field. See spec section 5.1.
type Keep struct {
	Last    int `yaml:"last,omitempty"`
	Hourly  int `yaml:"hourly,omitempty"`
	Daily   int `yaml:"daily,omitempty"`
	Weekly  int `yaml:"weekly,omitempty"`
	Monthly int `yaml:"monthly,omitempty"`
	Yearly  int `yaml:"yearly,omitempty"`
}

// Check is a policy unit, replaced wholesale like Keep.
// Check verifies the repository. It always runs after the backup and after
// any forget: prune rewrites pack files, so verifying afterwards validates
// what prune produced.
type Check struct {
	Timeout *Duration `yaml:"timeout,omitempty"`
	Mode    string    `yaml:"mode,omitempty"`
	Spread  int       `yaml:"spread,omitempty"`
}

// Check modes.
const (
	CheckOff       = "off"
	CheckStructure = "structure"
	CheckSubset    = "subset"
	CheckFull      = "full"
)

// Job modes.
const (
	ModeScheduled = "scheduled"
	ModeManual    = "manual"
	ModeDisabled  = "disabled"
)

// Maintenance ordering.
const (
	WhenAfter  = "after"
	WhenBefore = "before"
)
