# org-mirror

Mirror every repository accessible to your GitHub CLI account in an organization as
normal local Git working copies. The tool uses `gh` to discover and clone repositories
and `git` to update existing copies safely.

## Installation

```bash
go install github.com/inovacc/org-mirror@latest
```

Before running it, install Git and [GitHub CLI](https://cli.github.com/), then log in:

```bash
gh auth login
```

## Usage

```bash
# List actions without cloning or updating anything.
org-mirror sync floci-io --dry-run

# Mirror into C:\Users\dyamm\Downloads\mirror\orgs\floci-io\<repository>.
org-mirror sync floci-io

# Choose another parent directory.
org-mirror sync floci-io --root D:\\mirrors
```

Each real run writes `C:\Users\dyamm\Downloads\mirror\orgs\<organization>\metadata.json`
by default. It includes the UTC run time and, for each discovered repository, its
local path, GitHub properties, default branch, sync outcome, and local/upstream commit
hashes when available.

The mirror never resets, stashes, deletes, or overwrites a working copy. A dirty,
ahead, diverged, or detached checkout is left unchanged and recorded as a `conflict`.
Failures in one repository are recorded while the remaining repositories continue.

## Commands

| Command | Description |
|---------|-------------|
| `sync <organization>` | Mirror all repositories accessible to the authenticated `gh` account |
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
