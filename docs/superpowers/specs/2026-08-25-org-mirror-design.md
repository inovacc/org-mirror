# Organization Working-Copy Mirror Design

## Goal

Provide a Go CLI that uses the authenticated GitHub CLI to mirror every repository
accessible in an organization as a normal local working copy.

## Invocation

`org-mirror sync floci-io` writes repositories under
`orgs/floci-io/<repository>` relative to the selected destination root. The root
defaults to `orgs` and can be changed with `--root`. `--dry-run` reports planned
actions and does not write repositories or metadata.

## Discovery and synchronization

The CLI verifies `gh auth status`, then invokes `gh repo list <org> --limit 1000
--json nameWithOwner,name,defaultBranchRef,isPrivate,isArchived,isFork` to discover
all repositories accessible to the authenticated user. Missing repositories are
cloned with `gh repo clone`. Existing repositories are fetched through `git fetch
origin`, then fast-forwarded only if their worktree is clean and its current branch
is neither ahead of nor diverged from its upstream. No command resets, stashes,
deletes, or overwrites local work.

Each repository is processed independently. Discovery, clone, fetch, inspection,
and update failures are captured per repository so other repositories continue.

## Metadata

After a non-dry run, `orgs/<org>/metadata.json` is written atomically. It records
the UTC run timestamp and each discovered repository's name, GitHub owner/name,
path, visibility and archived/fork flags, default branch, outcome, optional message,
the local HEAD SHA, and the upstream SHA where known. Repositories remaining on disk
but absent from a later discovery are retained and carried in metadata with the
`absent_from_discovery` outcome.

Conflicted repositories are preserved exactly as found and use the `conflict`
outcome. A conflict message explains whether uncommitted, ahead, diverged, or
detached state prevented an update.

## Testing

Command execution is injected behind a small interface. Unit tests use a scripted
fake runner to verify `gh` parsing, successful clone, clean fast-forward, conflict
preservation, per-repository failure isolation, dry-run behavior, and metadata JSON.
No test contacts GitHub or changes a real repository.
