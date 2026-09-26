package config

// Resolve merges Defaults into every job, in place. After Resolve, each job
// carries its final configuration and callers need not consult Defaults.
//
// Merge rules (spec section 5.1):
//   - scalars: the job wins if set
//   - lists: replaced, never merged
//   - Backup, Forget and Check: merged field by field
//   - Keep and Check: policy units, replaced wholesale
func (c *Config) Resolve() {
	for name, j := range c.Jobs {
		j.Name = name
		if c.Defaults != nil {
			j.inherit(c.Defaults)
		}
		j.applyBuiltinDefaults()
	}
}

func (j *Job) inherit(d *Job) {
	if j.ResticExecutable == "" {
		j.ResticExecutable = d.ResticExecutable
	}
	if j.RunAs == "" {
		j.RunAs = d.RunAs
	}
	if j.MaxAge == nil {
		j.MaxAge = d.MaxAge
	}
	if j.Mode == "" {
		j.Mode = d.Mode
	}
	if !j.UnmountAlways {
		j.UnmountAlways = d.UnmountAlways
	}
	if j.OnSuccess == "" {
		j.OnSuccess = d.OnSuccess
	}
	if j.OnFailure == "" {
		j.OnFailure = d.OnFailure
	}
	if j.PasswordFile == "" {
		j.PasswordFile = d.PasswordFile
	}
	if j.EnvFile == "" {
		j.EnvFile = d.EnvFile
	}
	j.Forget = mergeForget(j.Forget, d.Forget)
	j.Check = mergeCheck(j.Check, d.Check)
	j.Backup.inherit(d.Backup)
}

// inherit fills a job's backup block from defaults.backup, field by field.
// A job without a backup block never grows one: defaults describe how a
// backup is taken, not whether one happens.
func (b *Backup) inherit(d *Backup) {
	if b == nil || d == nil {
		return
	}
	if b.Pre == "" {
		b.Pre = d.Pre
	}
	if b.Post == "" {
		b.Post = d.Post
	}
	if b.Timeout == nil {
		b.Timeout = d.Timeout
	}
	if b.Host == "" {
		b.Host = d.Host
	}
	if b.OneFileSystem == nil {
		b.OneFileSystem = d.OneFileSystem
	}
	if b.Excludes == nil {
		b.Excludes = d.Excludes
	}
	if len(b.Paths) == 0 {
		b.Paths = d.Paths
	}
	if b.Tags == nil {
		b.Tags = d.Tags
	}
}

// A job runs the phases it declares: defaults fill a block, they never
// create one. Declaring an empty block (`forget: {}`) takes everything from
// defaults; omitting it means the phase does not run — the same rule
// `backup:` follows, so a job can be read without consulting defaults.
func mergeForget(job, def *Forget) *Forget {
	if def == nil || job == nil {
		return job
	}
	if job.Timeout == nil {
		job.Timeout = def.Timeout
	}
	if job.When == "" {
		job.When = def.When
	}
	if job.GroupBy == "" {
		job.GroupBy = def.GroupBy
	}
	if job.Prune == nil {
		job.Prune = def.Prune
	}
	if job.CleanupCache == nil {
		job.CleanupCache = def.CleanupCache
	}
	// Keep is a policy unit: inherited only when wholly absent, so that
	// `keep: {last: 2}` means exactly that.
	if job.Keep == nil {
		job.Keep = def.Keep
	}
	return job
}

// As mergeForget: `check: {}` opts in with the inherited settings, omitting
// the block means no verification.
func mergeCheck(job, def *Check) *Check {
	if def == nil || job == nil {
		return job
	}
	if job.Timeout == nil {
		job.Timeout = def.Timeout
	}
	if job.Mode == "" {
		job.Mode = def.Mode
	}
	if job.Spread == 0 {
		job.Spread = def.Spread
	}
	return job
}

func (j *Job) applyBuiltinDefaults() {
	if j.Secrets == "" {
		j.Secrets = j.Name
	}
	if j.Mode == "" {
		j.Mode = ModeScheduled
	}
	if j.ResticExecutable == "" {
		j.ResticExecutable = "restic" // found on $PATH
	}
	if j.Forget != nil {
		if j.Forget.When == "" {
			j.Forget.When = WhenAfter
		}
		if j.Forget.Prune == nil {
			j.Forget.Prune = boolPtr(true)
		}
		if j.Forget.CleanupCache == nil {
			j.Forget.CleanupCache = boolPtr(true)
		}
	}
}

func boolPtr(b bool) *bool { return &b }
