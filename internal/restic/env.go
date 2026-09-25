package restic

import (
	"os"
	"sort"

	"github.com/fostam/resticle/internal/config"
	"github.com/fostam/resticle/internal/secrets"
)

// Env builds the environment for a restic invocation. Secrets travel here
// and never in argv, so they do not appear in ps output.
func Env(j *config.Job, s secrets.Set) []string {
	return buildEnv(j, s, false)
}

// RedactedEnv is Env with every secret value replaced, for --dry-run,
// `config check`, and logs.
func RedactedEnv(j *config.Job, s secrets.Set) []string {
	return buildEnv(j, s, true)
}

func buildEnv(j *config.Job, s secrets.Set, redact bool) []string {
	env := os.Environ()

	pw := s.Password
	if redact {
		pw = secrets.Redact(pw)
	}
	if pw != "" {
		env = append(env, "RESTIC_PASSWORD="+pw)
	}

	keys := make([]string, 0, len(s.Env))
	for k := range s.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic, so tests and logs are stable
	for _, k := range keys {
		v := s.Env[k]
		if redact {
			v = secrets.Redact(v)
		}
		env = append(env, k+"="+v)
	}
	return env
}
