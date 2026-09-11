# org-mirror

Mirror every repository accessible to your GitHub CLI account in an organization as
normal local Git working copies. The tool calls the GitHub API with the same credentials
stored by GitHub CLI and uses `git` to clone and update working copies safely. It never
executes `gh` as a subprocess.

## Installation

```bash
go install github.com/inovacc/org-mirror/cmd/org-mirror@latest
```

Before running it, install Git and [GitHub CLI](https://cli.github.com/), then log in:

```bash
gh auth login
```

## Usage

```bash
# List actions without cloning or updating anything.
org-mirror sync floci-io --dry-run

# Disable the automatic interactive progress interface.
org-mirror sync floci-io --no-tui

# Mirror into C:\Users\dyamm\Downloads\mirror\orgs\floci-io\<repository>.
org-mirror sync floci-io

# Choose another parent directory.
org-mirror sync floci-io --root D:\\mirrors

# Choose another sync-history database.
org-mirror sync floci-io --database D:\\mirrors\\database.db
```

### Pacing, resume and limits

The sync paces itself between repositories. The delay protects two different
things: GitHub's secondary rate limits react to bursts of requests, and local
endpoint-protection software reacts to bursts of process creation, which is what
a fast mirror run of several hundred repositories looks like.

```bash
# Slow down to one repository every two seconds.
org-mirror sync floci-io --delay 2s

# Turn pacing off entirely, accepting both risks.
org-mirror sync floci-io --delay 0

# Process 50 repositories, then stop. Run it again to continue.
org-mirror sync floci-io --limit 50

# Ignore an interrupted run and start over.
org-mirror sync floci-io --no-resume

# Check the remaining API budget before starting.
org-mirror limit floci-io
```

Every repository is written to the history database the moment it finishes, so a
crash, a dropped connection or `Ctrl+C` loses nothing. The next sync of the same
organization continues the interrupted run and skips what was already done.
Repositories that failed are retried rather than skipped.

Each real run writes `C:\Users\dyamm\Downloads\mirror\orgs\<organization>\metadata.json`
by default. It includes the UTC run time and, for each discovered repository, its
local path, GitHub properties, default branch, sync outcome, and local/upstream commit
hashes when available.

Every run also appends history to `C:\Users\dyamm\Downloads\mirror\database.db`.
The SQLite database contains `sync_runs` (organization, start/end time, status, and
repository count) and `repositories` (branch, local/upstream commit SHA, open issue
count, outcome, path, timestamp, and message). Use `--database` to change its path.

The mirror never resets, stashes, deletes, or overwrites a working copy. A dirty,
ahead, diverged, or detached checkout is left unchanged and recorded as a `conflict`.
Failures in one repository are recorded while the remaining repositories continue.
Interactive terminals show the current repository, completed work, and remaining queue
in a full-screen interface. Redirected output and `--no-tui` use plain text instead.

## Commands

| Command | Description |
|---------|-------------|
| `sync <organization>` | Mirror all repositories accessible to the authenticated `gh` account |
| `limit [organization]` | Show the account's GitHub rate-limit budget |
| `version` | Print version information |

## Development

```bash
# Build
task build

# Run
task run

# Test
task test

# Lint
task lint
```

## License

BSD-3
