package secrets

import (
	"fmt"
	"os"
	"strings"

	"github.com/fostam/resticle/internal/config"
	"gopkg.in/yaml.v3"
)

// Set is one job's secret material.
type Set struct {
	Password string            `yaml:"password"`
	Env      map[string]string `yaml:"env"`
}

// Redact hides a secret value in output. Empty stays empty so that "unset"
// remains visibly different from "set but hidden".
func Redact(s string) string {
	if s == "" {
		return ""
	}
	return "[redacted]"
}

// Load resolves secrets for every job. A job using password_file/env_file
// takes them from those files; every other job is looked up by its Secrets
// key in secretsFile, which is decrypted with sops when it is encrypted.
//
// The returned error is set only for a failure that affects every job: the
// secrets file itself could not be read or decrypted. A single job's own
// failure (missing secret, unreadable password_file) is reported in the
// per-job error map instead, and does not stop the other jobs from loading.
func Load(secretsFile string, jobs []*config.Job) (map[string]Set, map[string]error, error) {
	var fromFile map[string]Set
	var fileErr error
	if secretsFile != "" {
		fromFile, fileErr = loadSecretsFile(secretsFile)
	}

	sets := make(map[string]Set, len(jobs))
	errs := make(map[string]error)
	for _, j := range jobs {
		if j.Password == "" && j.PasswordFile == "" && fileErr != nil {
			errs[j.Name] = fileErr
			continue
		}
		set, err := loadJob(j, fromFile)
		if err != nil {
			errs[j.Name] = err
			continue
		}
		sets[j.Name] = set
	}
	return sets, errs, fileErr
}

func loadJob(j *config.Job, fromFile map[string]Set) (Set, error) {
	// Inline in the config file, for an installation that keeps its config
	// to itself. The caller checks that file's permissions.
	if j.Password != "" {
		env := j.Env
		if env == nil {
			env = map[string]string{}
		}
		return Set{Password: j.Password, Env: env}, nil
	}
	if j.PasswordFile != "" {
		set := Set{Env: map[string]string{}}
		pw, err := readSecretFile(j.PasswordFile)
		if err != nil {
			return Set{}, err
		}
		set.Password = strings.TrimRight(pw, "\r\n")

		if j.EnvFile != "" {
			body, err := readSecretFile(j.EnvFile)
			if err != nil {
				return Set{}, err
			}
			if set.Env, err = parseEnvFile(body); err != nil {
				return Set{}, fmt.Errorf("%s: %w", j.EnvFile, err)
			}
		}
		return set, nil
	}

	if set, ok := fromFile[j.Secrets]; ok {
		if set.Env == nil {
			set.Env = map[string]string{}
		}
		return set, nil
	}
	return Set{}, fmt.Errorf("no secret found: set password or password_file on the job, or add key %q to the secrets file", j.Secrets)
}

// CheckPerms refuses a file readable beyond its owner. Exported for the
// config file, which holds secrets when a job sets password inline.
func CheckPerms(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("%s is mode %#o; secrets must not be group- or world-readable (chmod 600)", path, perm)
	}
	return nil
}

// readSecretFile refuses anything readable beyond its owner.
func readSecretFile(path string) (string, error) {
	if err := CheckPerms(path); err != nil {
		return "", err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func parseEnvFile(body string) (map[string]string, error) {
	env := map[string]string{}
	for n, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("line %d: expected KEY=value", n+1)
		}
		env[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return env, nil
}

func parseSecretsYAML(doc []byte) (map[string]Set, error) {
	var out map[string]Set
	if err := yaml.Unmarshal(doc, &out); err != nil {
		return nil, fmt.Errorf("parse secrets: %w", err)
	}
	return out, nil
}
