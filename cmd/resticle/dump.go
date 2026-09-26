package main

import (
	"fmt"

	"github.com/fostam/resticle/internal/config"
	"github.com/fostam/resticle/internal/secrets"
	"gopkg.in/yaml.v3"
)

// cmdConfigDump prints the merged configuration as YAML: defaults folded
// into every job, the installation paths resolved, and each job's secret
// filled in from wherever it actually comes from. Loading the output back
// with -c yields the same behaviour, so it can be diffed against the config
// that produced it.
//
// Secrets are redacted unless reveal is set, because the common use is
// pasting the output somewhere.
func cmdConfigDump(g *globals, reveal bool) int {
	l, code := g.loadConfig()
	if code != 0 {
		return code
	}
	sec, secErrs, fileErr := secrets.Load(g.secretsPath, l.jobs)
	if fileErr != nil {
		fmt.Fprintln(g.out, "# secrets error:", fileErr)
	}

	root := &yaml.Node{Kind: yaml.MappingNode}
	root.HeadComment = dumpHeader(reveal, g.secretsPath)
	// secrets_file is a comment rather than a key: its entries are folded
	// into the jobs below, and a dump that named the file as well would be
	// refused on reload for setting each secret twice.
	for _, p := range []struct{ key, val string }{
		{"state_dir", g.stateDir},
		{"lock_dir", g.lockDir},
	} {
		if p.val == "" {
			continue
		}
		root.Content = append(root.Content, scalar(p.key), scalar(p.val))
	}

	jobs := &yaml.Node{Kind: yaml.MappingNode}
	for _, j := range l.jobs {
		node, err := jobNode(j, sec[j.Name], secErrs[j.Name], reveal)
		if err != nil {
			fmt.Fprintln(g.out, "dump error:", err)
			return 2
		}
		jobs.Content = append(jobs.Content, scalar(j.Name), node)
	}
	root.Content = append(root.Content, scalar("jobs"), jobs)

	enc := yaml.NewEncoder(g.out)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		fmt.Fprintln(g.out, "dump error:", err)
		return 2
	}
	if err := enc.Close(); err != nil {
		fmt.Fprintln(g.out, "dump error:", err)
		return 2
	}

	if fileErr != nil || len(secErrs) > 0 {
		return 2
	}
	return 0
}

func dumpHeader(reveal bool, secretsPath string) string {
	h := " the merged configuration: defaults folded into every job,\n" +
		" installation paths resolved, every secret shown on the job that uses it."
	if secretsPath != "" {
		h += "\n secrets_file: " + secretsPath + " (its entries appear inline below)"
	}
	if !reveal {
		return h + "\n Secret values are redacted; `config dump --reveal` prints them."
	}
	return h + "\n WARNING: this output contains the secrets in plain text."
}

// jobNode encodes one resolved job, with its secret material moved onto the
// job even when it came from the secrets file — that is the question the
// command exists to answer.
func jobNode(j *config.Job, set secrets.Set, secErr error, reveal bool) (*yaml.Node, error) {
	cp := *j
	switch {
	case secErr != nil:
		cp.Password = "[unavailable: " + secErr.Error() + "]"
		cp.Env = nil
	case reveal:
		cp.Password = set.Password
		cp.Env = set.Env
	default:
		cp.Password = secrets.Redact(set.Password)
		cp.Env = redactEnv(set.Env)
	}

	var n yaml.Node
	if err := n.Encode(&cp); err != nil {
		return nil, fmt.Errorf("job %s: %w", j.Name, err)
	}
	return &n, nil
}

func redactEnv(env map[string]string) map[string]string {
	if len(env) == 0 {
		return nil
	}
	out := make(map[string]string, len(env))
	for k, v := range env {
		out[k] = secrets.Redact(v)
	}
	return out
}

func scalar(s string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Value: s}
}
