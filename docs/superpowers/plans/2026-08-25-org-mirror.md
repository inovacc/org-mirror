# Organization Working-Copy Mirror Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a Cobra CLI that mirrors accessible GitHub organization repositories as conflict-safe local working copies.

**Architecture:** Cobra owns argument and output handling. A testable `internal/mirror` package owns GitHub CLI discovery, Git operations, synchronization decisions, and metadata persistence through an injected command runner.

**Tech Stack:** Go 1.27, Cobra, GitHub CLI (`gh`), Git.

**Spec:** `docs/superpowers/specs/2026-08-25-org-mirror-design.md`

## Global Constraints

- Use `gh` for authenticated GitHub discovery and cloning.
- Place mirrors under `<root>/<org>/<repo>`; default root is `orgs`.
- Never reset, stash, delete, or overwrite a working copy with local changes.
- Process each discovered repository independently and write metadata atomically.
- Use UTC RFC 3339 timestamps.

---

## File Structure

- `cmd/org-mirror/`: Cobra root and `sync` command.
- `internal/mirror/model.go`: repository, result, and metadata types.
- `internal/mirror/runner.go`: command-runner interface and OS implementation.
- `internal/mirror/service.go`: discovery, synchronization, and status inspection.
- `internal/mirror/metadata.go`: atomic JSON persistence.
- `internal/mirror/service_test.go`: synchronization behavior tests with a fake runner.
- `internal/mirror/metadata_test.go`: metadata persistence tests.

### Task 1: Scaffold and model the CLI

**Files:** create generated Cobra files, `internal/mirror/model.go`, tests.

- [ ] Generate the Cobra project with `omni scaffold cobra init . --name org-mirror --module github.com/inovacc/org-mirror --description "Mirror GitHub organization repositories as working copies" --license BSD-3 --author dyammarcano`.
- [ ] Write a failing model serialization test for a metadata document containing a UTC timestamp and one conflict result.
- [ ] Run `go test ./internal/mirror` and confirm it fails because the package does not exist.
- [ ] Define JSON-tagged repository, result, and metadata structs in `internal/mirror/model.go`.
- [ ] Re-run `go test ./internal/mirror` and confirm it passes.

### Task 2: Add command execution and GitHub discovery

**Files:** create `internal/mirror/runner.go`, `internal/mirror/service.go`, `internal/mirror/service_test.go`.

- [ ] Write failing tests using a scripted runner for `gh auth status` and parsing `gh repo list` JSON.
- [ ] Run those tests and confirm the missing `Discover` behavior fails.
- [ ] Implement an `Runner` interface, OS runner, `Service`, and `Discover(context.Context, string)` that invokes the documented `gh` commands and decodes repositories.
- [ ] Re-run focused tests and confirm they pass.

### Task 3: Synchronize safely

**Files:** modify `internal/mirror/service.go`, `internal/mirror/service_test.go`.

- [ ] Write failing tests for cloning a missing repository, fast-forwarding a clean checkout, and preserving dirty/ahead/diverged checkouts as conflicts.
- [ ] Run focused tests and confirm the expected failures.
- [ ] Implement `Sync(context.Context, Repository, root string, dryRun bool) Result`; use `gh repo clone` for missing paths and `git` inspection/fetch/merge for existing paths.
- [ ] Re-run focused tests and confirm they pass.

### Task 4: Persist metadata and wire Cobra

**Files:** create `internal/mirror/metadata.go`, `internal/mirror/metadata_test.go`; modify generated Cobra command files.

- [ ] Write failing tests for atomic metadata output and for aggregation preserving an error result while subsequent results are recorded.
- [ ] Run focused tests and confirm the failures.
- [ ] Implement `WriteMetadata(path string, document Metadata) error` using a temporary sibling file and rename.
- [ ] Add `sync <org>` with `--root` and `--dry-run`; discover, sync every repository, preserve old absent results, and write metadata except in dry-run mode.
- [ ] Run `go test ./...` and `go build ./...`; confirm both pass.

### Task 5: Document operation

**Files:** modify `README.md`.

- [ ] Document prerequisites (`gh auth login`, Git), usage, output layout, dry-run, metadata schema summary, and conflict safety.
- [ ] Run `go test ./...`, `go build ./...`, and `go vet ./...`.
