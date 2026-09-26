# resticle

[![ci](https://github.com/fostam/resticle/actions/workflows/ci.yml/badge.svg)](https://github.com/fostam/resticle/actions/workflows/ci.yml)

resticle runs [restic](https://restic.net) backups and repository maintenance
from a single configuration file. It is a wrapper, not a replacement: restic
does the actual backups, resticle does the surrounding work that otherwise
accumulates as shell scripts or manual type-ins.

## Features

- **Declarative jobs.** Every repository — local disk, removable disk, cloud
  backend, or one another machine pushes into — is a few lines of YAML, with a
  `defaults:` block for what they share.
- **A mount lifecycle that cannot silently go wrong.** A job mounts its disk,
  refuses to run if the repository path isn't there, and unmounts only what it
  mounted, on every exit path including failure and Ctrl-C.
- **Failure semantics worth relying on.** A failed backup skips the prune that
  would otherwise expire old snapshots while the new one never arrived. Stale
  repository locks are reported, never cleared behind your back. Overlapping
  runs of the same job exit immediately instead of fighting.
- **Verification that rotates.** `check --read-data-subset` advances one slice
  per run and wraps, so the whole repository is read over time on any
  schedule.
- **An alarm for backups that stop arriving.** `max_age` turns "that machine
  hasn't pushed in three days" into a failure instead of silence.
- **Secrets outside the config.** A [SOPS](https://github.com/getsops/sops)
  -encrypted file, decrypted at startup and passed to restic through the
  environment — never on a command line, so nothing shows in `ps`.
- **Two kinds of dry run.** One prints the commands and touches nothing at
  all; the other mounts the disk for real and asks restic what it *would*
  change.
- **Quiet when it should be.** `--quiet-on-success` prints nothing on a clean
  run, so cron mails you only when something breaks.

## Quick start

Build it, or download a binary from the
[releases](https://github.com/fostam/resticle/releases):

```sh
make                    # ./resticle for this machine
make dist               # static linux/amd64 binary in dist/, with its sha256
sudo make install       # to /usr/local/bin (override PREFIX)
```

Describe a repository:

```yaml
# /etc/resticle/config.yaml
defaults:
  restic_executable: /usr/local/bin/restic

jobs:
  local-usb:
    repo: /mnt/backup/restic/local-usb
    mount: /mnt/backup          # mounted for the job, unmounted after
    password_file: /etc/resticle/local-usb.txt
    backup:
      paths: [/srv, /home]
      tags: [local-usb]
    forget:
      keep: {last: 15, daily: 21, weekly: 8, monthly: 12, yearly: 10}
    check: {mode: subset, spread: 7}
```

Check it, see what it would do, then let it run:

```sh
resticle config check
resticle --dry-run run local-usb
resticle run local-usb
```

Put it in cron and read the mail only when something fails:

```cron
0 2 * * *  /usr/local/bin/resticle --quiet-on-success run --all
```

`config.example.yaml` has four examples: a mounted local disk, a cloud
repository, a maintenance-only repository another machine pushes into, and a
disk carried off-site and plugged in by hand.

## Commands

```
resticle run <job>... | --all       backup and maintenance, in order
resticle backup <job>...            backup phase only
resticle maintain <job>...          maintenance phase only (forget, then check)
resticle forget <job>...            expire and prune only, no verification
resticle check <job>... [--full]    verify only
resticle exec <job> -- <args>       raw restic passthrough
resticle find <pattern> [<job>...]  search repositories for a file
resticle status [<job>...]          last run and repository freshness
resticle config check               validate configuration and secrets
resticle version                    version and build time
```

### `run`, `backup`, `maintain`, `forget`

`run` is the whole pipeline for a job: acquire its lock, mount if configured,
verify the repository path, expire snapshots before or after the backup as
`forget.when` says, verify the repository, check the newest snapshot's age,
unmount, write state, run hooks.

`backup`, `maintain` and `forget` run less than the whole pipeline. All of
them still mount, lock and unmount — the phase is narrower, the care around it
is the same. Naming a phase a job doesn't have (`backup` on a
maintenance-only job) reports it as skipped rather than succeeding silently.

The phases line up like this:

| command | runs |
|---|---|
| `backup` | backup |
| `forget` | forget (expire and prune) |
| `check` | verification |
| `maintain` | forget, then verification |
| `run` | backup and maintenance, in the configured order, then the freshness check |

`forget` is the one to reach for when a disk is filling up and you want the
space back now: `maintain` would also spend the verification time, which on a
large repository is the expensive part. For a job that should *never* verify
as part of maintenance, say so in the configuration with
`check: {mode: off}` (or leave the block out) instead, and verify on demand with `check --full`.

`--all` selects every job with `mode: scheduled`, in configuration order, and
runs them all even if one fails. The exit code is 1 if any job failed.

### `check`

Verifies without backing up or expiring anything. With no flag it runs the
job's configured `check:`; with `--full` it runs
`restic check --read-data` over the entire repository regardless of
configuration — the deliberate, expensive verification you run by hand.

A job with `check: {mode: off}` and no `--full` reports "no check configured"
rather than reporting success for work it didn't do.

### `exec`

Everything after `--` goes to restic verbatim, with the repository, password,
backend environment, `run_as` user and mount already arranged:

```sh
resticle exec local-usb -- snapshots
resticle exec local-usb -- unlock
resticle exec backblaze -- init
```

stdin, stdout and the terminal are connected, so pipes and interactive
commands work. `exec` exits with restic's own exit code, not resticle's, and
always shows restic's output. It works on every job including disabled ones.

### `find`

Searches repositories for a file — the question `exec` cannot answer in one
command, because each repository needs its own mount, secrets and user:

```sh
resticle find "taxes-2024.ods"            # every job
resticle find "*.kdbx" local-usb offsite   # only these jobs
```

A job whose disk isn't attached, or whose secrets are unavailable, is reported
and skipped rather than failing the search. Exit code 0 if any repository
matched, 1 if none did.

### `status`

Per job: when it last ran, its exit code, the next verification slice, and the
newest snapshot's ID and age queried live from the repository. Jobs with a
`mount:` are not mounted by `status`, so they report stored state only, and a
job whose secrets are unavailable still reports what it can.

### `config check`

Loads and validates the configuration, resolves every secret, and prints what
each job would use — repository, mountpoint, user, redacted secrets, backup
paths, mode. Exits 2 on any error, printing all of them rather than stopping
at the first, and warns about a repository path that isn't under its own
mountpoint or an exclude file it cannot read.

### Global flags

May appear anywhere before a literal `--`. These change what an invocation
does; where the installation keeps its files is configuration, not a flag —
`secrets_file`, `state_dir` and `lock_dir` live in the config, and `-c`
selects which config an invocation belongs to.

| Flag | Default | Meaning |
|---|---|---|
| `-c`, `--config PATH` | `/etc/resticle/config.yaml`, else `~/.config/resticle/config.yaml` | configuration file |
| `-n`, `--dry-run` | off | print the restic commands, with secrets redacted, and touch nothing: no mount, no lock, no state, no freshness check, no hooks |
| `-N`, `--restic-dry-run` | off | run for real — mount, lock, open the repository — but pass restic's own `--dry-run`, so `backup` and `forget --prune` report what they would change and change nothing. The check phase is skipped, state isn't written, hooks don't fire. Mutually exclusive with `--dry-run` |

Both dry runs announce themselves in the log and mark the verdict —
`local-usb: success (restic dry-run, nothing changed)`, and a `dry_run` field
in the JSON summary — so a saved log can never be mistaken for a real backup.
| `-q` | off | suppress restic's own output; resticle's event lines remain |
| `--quiet-on-success` | off | buffer everything and print it only if a job failed or was skipped |
| `--log-format text\|json` | `text` | in `json`, event lines and a per-job summary object are JSON and restic's output is suppressed, so the stream is parseable |

### Exit codes

`0` everything succeeded · `1` at least one job failed · `2` configuration or
usage error. `exec` is the exception: it returns restic's exit code.

## Configuration

One `jobs:` map, plus an optional `defaults:` block every job inherits.

A job **is** a repository. If it has a `backup:` block, resticle backs up into
it; if it doesn't, resticle only maintains it — the shape for a repository
another machine pushes into.

**Merging.** Scalars and maps merge with the job's value winning. **Lists are
replaced, never merged** — a job that sets `excludes:` replaces the inherited
list entirely. `backup:`, `forget:` and `check:` merge field by field, except
`keep:`, which is replaced as a whole unit so that `keep: {last: 2}` means
exactly that rather than inheriting the other periods.
**Defaults fill a block, they never create one.** A job runs the phases it
declares, so what a job does is readable from the job itself:

```yaml
defaults:
  forget: {keep: {last: 15, daily: 21}}
  check: {mode: subset, spread: 7}

jobs:
  storage:
    backup: {paths: [/srv]}
    forget: {}     # declared, so it runs — with everything from defaults
    check: {}
  archive:
    backup: {paths: [/srv]}
                   # neither declared: this job only backs up
```

That is how "expire nothing" stays expressible even when defaults carry a
retention policy, and it is the same rule for all three phase blocks.

Unknown keys are errors, so a typo fails at `config check` instead of silently
disabling a setting.

### Top level

| Key | Type | Default | Meaning |
|---|---|---|---|
| `secrets_file` | path | `secrets.yaml` beside the config file | where the secrets live |
| `state_dir` | path | `/var/lib/resticle` | per-job state: verification index, last run |
| `lock_dir` | path | `/run/resticle` | per-job lock files |
| `defaults` | map | — | inherited by every job; accepts every job key except `repo`, `mount` and `secrets` |
| `jobs` | map | required | job name → job. Order matters: `run --all` follows it |

### Job

| Key | Type | Default | Meaning |
|---|---|---|---|
| `repo` | string | required | restic repository: a local path, or a backend URL such as `b2:bucket:path` |
| `restic_executable` | path | `restic` from `$PATH` | the program to run. Per job, because reaching one repository can need a wrapper — a script that opens an SSH tunnel first, say — that the others don't |
| `mount` | path | — | mountpoint to ensure before the job and release after it. Mounted via `/etc/fstab` (`mount <path>`, no device) |
| `unmount_always` | bool | `false` | unmount at the end even if the mountpoint was already mounted. For a disk attached for the job and detached after. Requires `mount` |
| `mode` | `scheduled`, `manual`, `disabled` | `scheduled` | how the job is triggered — see below |
| `run_as` | user | current user | run restic as this user, via `sudo -n -u <user>` |
| `secrets` | string | the job name | which key to read from the secrets file |
| `password` | string | — | the repository password, inline. The config file must then be `chmod 600` |
| `env` | map | — | backend credentials injected into restic's environment. Requires `password` |
| `password_file` | path | — | read the repository password from this file instead |
| `env_file` | path | — | `KEY=value` lines injected into restic's environment. Requires `password_file` |
| `max_age` | duration | — | fail the job if the newest snapshot is older than this |
| `on_success` | command | — | shell command run after a successful job |
| `on_failure` | command | — | shell command run after a failed job |
| `backup` | map | — | if present, the job backs up |
| `forget` | map | — | if present, the job expires snapshots |
| `check` | map | — | if present, the job verifies the repository |

Durations are Go syntax: `48h`, `90m`, `16h30m`.

#### `mode`

| value | `run --all` | named explicitly | `exec` |
|---|---|---|---|
| `scheduled` | runs | runs | yes |
| `manual` | not selected | runs | yes |
| `disabled` | not selected | refused, exit 2 | yes |

`manual` is for a job whose disk is plugged in for the occasion: it is left
out of the scheduled sweep entirely rather than selected and skipped, so a
nightly `run --all` stays silent. `disabled` parks a job without commenting it
out; it refuses to run even when named, while `exec` keeps working so you can
still inspect the repository, and `status` still lists it.

#### `backup`

| Key | Type | Default | Meaning |
|---|---|---|---|
| `paths` | list of paths | required | what to back up |
| `pre` | command | — | run before the backup — see below |
| `post` | command | — | run after the backup — see below |
| `tags` | list | — | `--tag` per entry. Also what `forget --group-by tags` groups by |
| `excludes` | list | inherited | exclusion patterns and files — see below |
| `host` | string | system hostname | `--host`, the host recorded in snapshots |
| `one_file_system` | bool | `false` | `--one-file-system`: don't cross mount points below the listed paths |
| `timeout` | duration | — | kill the backup if it runs longer. SIGINT first, then SIGKILL after a grace period |

#### `pre` and `post`

Shell commands run around the backup, for quiescing something while its files
are read:

```yaml
    backup:
      pre:  systemctl stop postgresql
      post: systemctl start postgresql
      paths: [/var/lib/postgresql]
```

They belong to the backup rather than the job because nothing else reads the
source: `forget` and `check` never touch it, so a service stopped for them
would be down for no reason.

The rules around failure are the point:

- **If `pre` fails the backup does not run.** A database that did not stop
  yields a snapshot that looks fine and is not, so the job fails instead.
- **`post` runs whatever happened** — after a successful backup, a failed one,
  a timeout, an interrupt, and after a failed `pre`. Whatever was stopped has
  to be started again, and that cannot be conditional on success.
- **If `post` fails the job fails**, because something is left stopped and you
  want to hear about it tonight.
- **Neither runs under `--dry-run` or `--restic-dry-run`.** Both promise to
  change nothing, and stopping a service is a change; the log says they were
  skipped.

Each is run with `/bin/sh -c`, with `RESTICLE_JOB` and `RESTICLE_PHASE` in the
environment and its output captured into the log. They appear in the summary
as the phases `backup-pre` and `backup-post`, so a failure names itself
instead of hiding inside "backup failed". Neither has a timeout: a hook that
hangs holds the job's lock.

#### `excludes`

One list holds both exclusion forms, because restic's `--exclude` patterns and
the lines of an `--exclude-file` are the same language:

```yaml
    excludes:
      - "@/etc/resticle/excludes/common.txt"   # a file of patterns
      - "*.iso"                                # a pattern
      - "@*"                                   # also a pattern
      - "@@/opt"                               # the literal pattern @/opt
```

An entry beginning `@` **followed by an absolute path** is a file, passed as
`--exclude-file`; anything else is a pattern, passed as `--exclude`. The
absolute path is what disambiguates, because patterns starting with `@` are
ordinary — NAS metadata directories are commonly excluded as `@*` — and `@@`
escapes a pattern that genuinely starts with `@/`. `config check` warns about a
file it cannot read, since restic aborts a backup on a missing exclude file.

#### `forget`

| Key | Type | Default | Meaning |
|---|---|---|---|
| `keep` | map | required | retention policy — see below |
| `when` | `after`, `before` | `after` | expire after the backup, or before it (to reclaim space on a tight disk first) |
| `group_by` | string | restic's default (`host,paths`) | `--group-by`: which snapshots the retention policy is applied within |
| `prune` | bool | `true` | `--prune`: actually remove the data of expired snapshots |
| `cleanup_cache` | bool | `true` | `--cleanup-cache` |
| `timeout` | duration | — | kill the forget if it runs longer |

`keep` is required whenever a `forget:` block exists: restic refuses a
`forget` with no policy, so leaving it out would fail every run. A job that
should expire nothing simply has no `forget:` block.

**On `group_by`:** the retention policy is applied separately within each
group, so this decides what "keep the last 15" counts. Left alone, restic
groups by host and paths, which keeps a full policy's worth for each machine
and each set of backed-up directories. Set `tags` when several jobs write to
one repository and each tags its snapshots, so that each job gets its own
policy. Take care with `tags` if the repository also holds untagged snapshots
from more than one machine: they all land in a single group, and
`keep: {last: 15}` then keeps fifteen in total rather than fifteen per
machine.

Only `forget` has a `when`, because reclaiming space before writing is the
reason `before` exists. A check always runs after the backup and after any
forget — prune rewrites pack files, so verifying afterwards validates what
prune produced.

#### `forget.keep`

| Key | Type | Meaning |
|---|---|---|
| `last` | int | keep the N most recent snapshots |
| `hourly` | int | keep the last snapshot of each of the N most recent hours |
| `daily` | int | …of each of the N most recent days |
| `weekly` | int | …weeks |
| `monthly` | int | …months |
| `yearly` | int | …years |

Zero or absent means the period isn't applied. Each becomes restic's
corresponding `--keep-*` flag.

#### `check`

| Key | Type | Meaning |
|---|---|---|
| `mode` | `off`, `structure`, `subset`, `full` | how much to verify |
| `spread` | int | `subset` only: how many slices the repository is divided into |
| `timeout` | duration | kill the check if it runs longer |

Its own timeout, separate from `forget`'s, because the two differ by orders
of magnitude: expiring takes minutes, reading data back takes hours.

- **`off`** — no verification. Pair it with `resticle check <job> --full` by
  hand.
- **`structure`** — `restic check`: metadata and structure only, no data read.
- **`subset`** — `restic check --read-data-subset=<i>/<spread>`: one slice per
  run. The index advances after each successful check and wraps, so the whole
  repository is read over `spread` runs whatever the schedule. It is kept in
  the state directory, not derived from the date; losing it restarts the cycle
  and breaks nothing.
- **`full`** — `restic check --read-data` every run. Thorough and expensive.

### Secrets

Each job needs a repository password, and cloud backends need credentials.
There are three places to keep them; pick per installation, or per job.

**1. In the config file.** Simplest, and enough for a server whose config
never leaves it:

```yaml
jobs:
  backblaze:
    repo: b2:example-bucket:alpha
    password: the-repository-password
    env:
      B2_ACCOUNT_ID: ...
      B2_ACCOUNT_KEY: ...
```

A config holding a secret **is** a secret file, so resticle refuses to run if
it is group- or world-readable (`chmod 600`), and it must not be committed.

**2. In separate files.** `password_file:` and optionally `env_file:` on the
job — useful when secrets are provisioned separately from configuration, or
already exist from an earlier setup.

**3. In a SOPS-encrypted file.** The choice when the configuration lives in a
repository: `config.yaml` stays readable and committable, and the secrets sit
beside it encrypted.

```yaml
# secrets.yaml, encrypted with sops, keyed by each job's `secrets:` value
local-usb:
  password: ENC[AES256_GCM,data:...]
backblaze:
  password: ENC[...]
  env:
    B2_ACCOUNT_ID: ENC[...]
    B2_ACCOUNT_KEY: ENC[...]
```

```sh
cp secrets.example.yaml secrets.yaml
$EDITOR secrets.yaml
age-keygen -o ~/.config/sops/age/keys.txt      # once, if you have no key
sops -e -i --age <age1-public-key> secrets.yaml
chmod 600 secrets.yaml                         # sops -i leaves it 0644
```

resticle finds it as `secrets.yaml` beside the config file, or wherever
`secrets_file:` points, and shells out to the `sops` binary to decrypt — so
your existing age or KMS setup works unchanged. Edit it later with
`sops secrets.yaml`. Encrypted, it is safe to commit next to `config.yaml`;
because the encrypted and plaintext forms share a filename,
`.githooks/pre-commit` checks the content — enable it once per clone with
`git config core.hooksPath .githooks`.

**One source per job.** Each job takes its secret from exactly one of the
three: `password:` on the job, `password_file:`, or the secrets file under the
job's `secrets:` key. Two of them for the same job is an error, not a silent
precedence — otherwise rotating the password in one place would quietly have
no effect. Different jobs may use different sources, so a config can move to
or from the secrets file one job at a time. `env` goes with `password`, and
`env_file` with `password_file`, so a job's credentials all come from one
place.

Whichever you choose: everything under `env:` is injected into restic's
environment, so any backend restic supports works; secrets reach restic only
through the environment and never a command line; `--dry-run` and
`config check` redact them; and a single job's missing secret fails that job
and not the others — only an unreadable or undecryptable shared secrets file
is fatal.

### State and locks

`state_dir` (default `/var/lib/resticle`) holds one small JSON file per job:
the verification index, the last run's time and exit code, the last snapshot
ID. Deleting it is harmless — the rotation restarts at the first slice.

`lock_dir` (default `/run/resticle`) holds a `flock` per job, so a run that
starts while the previous one is still going exits immediately instead of
contending for the mount. It lives on tmpfs deliberately: a lock held by a
process that no longer exists after a reboot would be a lie.

## Restoring

resticle does not wrap restore. Restoring is supervised, one-off work where
constraining the options is the last thing you want, and `exec` already
supplies what makes it awkward otherwise: the mount, the password, the backend
environment, the right user, and a real terminal.

**Find which repository still has the file** — across all of them at once:

```sh
resticle find "taxes-2024.ods"
```

**Restore one file to a scratch directory**, the safe default, since it cannot
overwrite what you're recovering:

```sh
resticle exec local-usb -- restore latest --target /tmp/restore \
    --include /home/alice/Documents/taxes-2024.ods
```

**Restore from the snapshot `find` named:**

```sh
resticle exec local-usb -- restore 2f83aab8 --target /tmp/restore --include /srv/www
```

**Stream a single file** without unpacking a tree:

```sh
resticle exec local-usb -- dump latest /srv/www/index.html > index.html
```

**Browse before deciding.** Mount the repository, look around, copy what you
need, then Ctrl-C:

```sh
mkdir -p /mnt/restore
resticle exec local-usb -- mount /mnt/restore
```

**Restore from a repository that lives inside another backup.** If a job's
paths include a directory holding other restic repositories — an off-site disk
carrying copies of them, say — recovery takes two steps:

```sh
resticle exec offsite -- restore latest --target /tmp/r --include /mnt/net/backup/restic/nas
restic -r /tmp/r/mnt/net/backup/restic/nas restore latest --target /tmp/nas-files
```

The second command is plain restic: that restored copy is not a job, so
resticle knows nothing about it. Run `restic check` on it before trusting it —
a repository copied while another machine was pushing into it may hold a
half-written pack file.

**Restoring over the original location** is deliberately not given a recipe
here. Restore to a scratch target, look at what you got, then move it into
place yourself.

## Building and releasing

```sh
make            # ./resticle for this machine
make dist       # static linux/amd64 binary in dist/, with its sha256
make check      # gofmt, go vet, tests — run before committing
make race       # tests under the race detector
make goldens    # rewrite testdata/argv after an intended change
make help       # list targets
```

`make dist` produces a CGO-free static binary, so the target host needs no Go
toolchain and no shared libraries. Both build targets stamp the binary with a
version from `git describe` and a build time, which `resticle version` prints.

Pushing a semantic-version tag (`v1.2.3`, optionally `-rc1`) runs
`.github/workflows/release.yml`: it verifies the tree with `make check`, builds
the static binary stamped with the tag, and attaches it with its `sha256` to
the GitHub release.

`testdata/argv/*.txt` records the exact restic command lines the example
configuration produces. Any diff there is a change in what restic is asked to
do — read it before accepting it.

## AI disclaimer

Parts of this repository contain code, documentation, or assets generated or
assisted by Artificial Intelligence (AI) tools (e.g. GitHub Copilot, ChatGPT,
Claude). Everything here was reviewed and tested before it was committed, and
the behaviour that matters — what restic is actually asked to do — is pinned
by the snapshots in `testdata/argv/` and exercised against a real restic by
the test suite.

## Licence

MIT — see [LICENSE](LICENSE).
