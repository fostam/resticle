package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/fostam/resticle/internal/report"
)

// Set at link time by the Makefile and the release workflow:
//
//	-X main.version=v1.2.3 -X main.buildTime=2026-09-21T07:15:00Z
//
// A plain `go build` leaves the defaults, which is what a development
// binary should report.
var (
	version   = "dev"
	buildTime = ""
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout))
}

type globals struct {
	configPath  string
	secretsPath string
	stateDir    string
	lockDir     string
	dryRun      bool
	resticDry   bool
	quiet       bool
	quietOnOK   bool
	logFormat   string
	out         io.Writer
}

// defaultSecretsPath is used when --secrets is not given: secrets.yaml next
// to the config file, if it exists (I2).
func defaultSecretsPath(configPath string) string {
	p := filepath.Join(filepath.Dir(configPath), "secrets.yaml")
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

func defaultConfigPath() string {
	const system = "/etc/resticle/config.yaml"
	if _, err := os.Stat(system); err == nil {
		return system
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return system
	}
	return filepath.Join(home, ".config", "resticle", "config.yaml")
}

// run returns the process exit code: 0 ok, 1 a job failed, 2 config or usage.
func run(argv []string, out io.Writer) int {
	// The installation paths come from the configuration file — see
	// globals.resolvePaths. Only the config file's own location can be a
	// flag, since it cannot be configured in the file it points at.
	g := &globals{
		out:        out,
		configPath: defaultConfigPath(),
		logFormat:  report.FormatText,
	}

	positional, all, full, passthrough, hasDD, code := parseArgs(argv, out, g)
	if code != 0 {
		return code
	}
	if g.dryRun && g.resticDry {
		fmt.Fprintln(out, "--dry-run and --restic-dry-run are mutually exclusive:"+
			" --dry-run touches nothing at all, --restic-dry-run mounts and opens the repository")
		return 2
	}
	if g.logFormat != report.FormatText && g.logFormat != report.FormatJSON {
		fmt.Fprintf(out, "unknown --log-format %q; want %s or %s\n",
			g.logFormat, report.FormatText, report.FormatJSON)
		return 2
	}
	if len(positional) == 0 {
		usage(out)
		return 2
	}
	sub, rest := positional[0], positional[1:]

	if full && sub != "check" {
		fmt.Fprintln(out, "--full is only valid with check")
		return 2
	}
	if all {
		switch sub {
		case "run", "backup", "maintain", "forget", "check":
		default:
			fmt.Fprintln(out, "--all is only valid with run, backup, maintain, forget, check")
			return 2
		}
		if len(rest) > 0 {
			fmt.Fprintln(out, "--all cannot be combined with job names")
			return 2
		}
	}

	switch sub {
	case "run":
		return cmdRun(g, rest, all, "")
	case "backup":
		return cmdRun(g, rest, all, phaseBackup)
	case "maintain":
		return cmdRun(g, rest, all, phaseMaintenance)
	case "forget":
		return cmdRun(g, rest, all, phaseForget)
	case "check":
		return cmdCheck(g, rest, all, full)
	case "exec":
		if len(rest) != 1 || !hasDD || len(passthrough) == 0 {
			fmt.Fprintln(out, "usage: resticle exec <job> -- <restic args...>")
			return 2
		}
		return cmdExec(g, rest[0], passthrough)
	case "find":
		return cmdFind(g, append(rest, passthrough...))
	case "status":
		return cmdStatus(g, rest)
	case "version":
		return cmdVersion(g.out)
	case "config":
		if len(rest) > 0 && rest[0] == "check" {
			return cmdConfigCheck(g)
		}
		usage(out)
		return 2
	default:
		fmt.Fprintf(out, "unknown command %q\n\n", sub)
		usage(out)
		return 2
	}
}

// parseArgs scans argv for global flags: -c/--config, --secrets,
// --dry-run, -q, --quiet-on-success, --log-format,
// plus the subcommand options --all and --full. All of these may appear
// anywhere before a literal "--"; everything after "--" is exec's raw
// restic passthrough and is never parsed (C1). It returns the remaining
// positional arguments (the subcommand and any job names).
func parseArgs(argv []string, out io.Writer, g *globals) (positional []string, all, full bool, passthrough []string, hasDD bool, code int) {
	before := argv
	for i, a := range argv {
		if a == "--" {
			before = argv[:i]
			passthrough = argv[i+1:]
			hasDD = true
			break
		}
	}

	strVal := func(i *int, name string, dest *string) bool {
		*i++
		if *i >= len(before) {
			fmt.Fprintf(out, "flag needs an argument: %s\n", name)
			return false
		}
		*dest = before[*i]
		return true
	}

	for i := 0; i < len(before); i++ {
		a := before[i]
		switch {
		case a == "-c" || a == "--config":
			if !strVal(&i, a, &g.configPath) {
				return nil, false, false, nil, false, 2
			}
		case a == "--log-format":
			if !strVal(&i, a, &g.logFormat) {
				return nil, false, false, nil, false, 2
			}
		case a == "--dry-run" || a == "-n":
			g.dryRun = true
		case a == "--restic-dry-run" || a == "-N":
			g.resticDry = true
		case a == "-q":
			g.quiet = true
		case a == "--quiet-on-success":
			g.quietOnOK = true
		case a == "--all":
			all = true
		case a == "--full":
			full = true
		case strings.HasPrefix(a, "-"):
			fmt.Fprintf(out, "unknown flag %q\n", a)
			return nil, false, false, nil, false, 2
		default:
			positional = append(positional, a)
		}
	}
	return positional, all, full, passthrough, hasDD, 0
}

func usage(w io.Writer) {
	fmt.Fprint(w, `resticle - a wrapper around restic for backups and repository maintenance

Usage:
  resticle run <job>... | --all     full pipeline
  resticle backup <job>             backup phase only
  resticle maintain <job>           maintenance phase only (forget, then check)
  resticle forget <job>             expire and prune only, no verification
  resticle check <job> [--full]     verify only
  resticle exec <job> -- <args>     raw restic passthrough
                                     (always shows restic's output; exits with restic's own exit code)
  resticle find <pattern> [<job>...]
                                    search repositories for a file
  resticle status [<job>]           last run and repository freshness
  resticle config check             validate configuration
  resticle version                  print version and build time

Flags (may appear anywhere on the command line, before a literal "--"):
  -c, --config PATH   configuration file
  -n, --dry-run       print restic commands without running them
  -N, --restic-dry-run
                      run for real (mount, lock, repository) but pass
                      restic's own --dry-run, so nothing changes;
                      the check phase is skipped
  -q                  suppress restic output
  --quiet-on-success  print nothing when every job succeeds
  --log-format FMT    text (default) or json
                      (dry-run preview lines remain plain text)
`)
}
