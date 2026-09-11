# Resilient Sync Design

Resume after interruption, rate-limit protection against GitHub abuse detection, and a
`limit` verb. Extends `docs/superpowers/specs/2026-08-25-org-mirror-design.md`.

## Goal

A `sync` run must survive a crash, a dropped connection, or `Ctrl+C` without losing the
work it already did, and must not trip GitHub's primary or secondary rate limits when it
walks a large organization. The operator must be able to see the account's remaining
budget before starting, and to cap how much a single run consumes.

## Rate limiting

### API requests

`internal/ratelimit` provides `Transport`, an `http.RoundTripper` installed through
`api.ClientOptions.Transport`. Calling code keeps the existing `RESTClient.Get` interface
and changes nothing.

Per request the transport:

1. Waits on a minimum-interval pacer shared with the git side.
2. Sends the request through the wrapped round tripper.
3. Reads `x-ratelimit-limit`, `x-ratelimit-remaining`, `x-ratelimit-used` and
   `x-ratelimit-reset` from the response and stores them in a snapshot.
4. Sleeps until the reset instant when remaining falls to or below a reserve of 5, then
   retries.
5. On `429`, and on `403` carrying either `retry-after` or `x-ratelimit-remaining: 0`,
   honours `retry-after` when present, otherwise sleeps until `x-ratelimit-reset`,
   otherwise backs off exponentially. A `403` carrying neither header is a permission
   error and is returned unchanged. Response bodies are never inspected, so the body a
   caller receives is untouched.
6. On `5xx` and on transport errors, backs off exponentially.

Retries are capped at 5 attempts with a backoff of 1s doubling to a 60s ceiling, plus
jitter of up to 20 percent. A wait longer than a `--max-wait` ceiling (default 15
minutes) fails the request rather than hanging. Every sleep selects on the request
context, so cancellation is immediate.

Only idempotent requests are retried. The mirror issues `GET` only, but the transport
checks the method rather than assuming.

### Git operations

Clone and fetch do not consume the REST budget but still reach GitHub, and are the larger
share of the traffic. The same `Pacer` gates each repository before `Service.Sync` runs
any git command. Its interval is `--delay`, defaulting to 750ms.

The pacer gates **every** repository that runs a git command, regardless of what the
previous one did. A clone, a fast-forward, a conflict and an error all pace identically.
The error path in particular must not shortcut the wait, because a failing organization is
exactly where the loop would otherwise spin fastest. The wait is applied between
repositories, so the first repository does not pay it and a run of one repository has no
added latency.

A repository skipped by resume is the one exception: it spawns no process and issues no
request, so it does not pace. Pacing skips would make resuming a large organization spend
minutes doing nothing.

Two reasons the wait exists, and the second sets the default. GitHub's secondary rate
limits react to request bursts. Endpoint-protection software on the local machine reacts
to bursts of *process creation*, and a mirror run spawns several short-lived `git`
processes per repository. A few hundred repositories at full speed is thousands of
process creations in a minute, which is a behavioural signature antivirus heuristics
score against. Pacing at 750ms with jitter keeps the spawn rate in the range ordinary
developer tooling produces.

Jitter of up to 30 percent of the interval is added to each wait. A perfectly regular
interval is itself a machine signature, and the jitter costs nothing.

`--delay 0` disables pacing for operators who know their environment. It is documented as
the setting that reintroduces both risks.

Git failures are classified before any retry. A failure is transient when the combined
output matches a connection reset, an early EOF, an unresolved host, a timeout, an HTTP
429 or 5xx from the smart HTTP transport, or `RPC failed`. Transient failures retry up to
3 times with the same backoff. Authentication failures, missing repositories, and every
conflict outcome are permanent and are never retried.

### Determinism in tests

`Transport` and `Pacer` take a `Clock` with `Now() time.Time` and
`Sleep(ctx context.Context, d time.Duration) error`. Production uses a real clock; tests
use a fake one, so no test sleeps.

## Resume

### Database lifecycle

`history.Database` replaces the single terminal `Record` call with three:

- `StartRun(organization string, started time.Time, dryRun bool) (*Run, error)` inserts a
  `sync_runs` row with status `running` and returns its identity.
- `(*Run).RecordRepository(result mirror.Result, at time.Time) error` commits one
  `repositories` row immediately.
- `(*Run).Finish(status Status, finished time.Time, runErr error) error` sets the final
  status, one of `completed`, `failed` or `interrupted`.

Each repository row is its own transaction, so a process killed at any instant leaves a
`running` run row plus exactly the repositories that finished.

`sync_runs` gains no columns. The existing `status` column carries the two new values,
`running` and `interrupted`. `StartRun` inserts `repository_count` as zero and
`completed_at` as empty, and `Finish` updates both. Existing databases need no migration:
every schema statement is `CREATE TABLE IF NOT EXISTS` and no column type changes.

### Resume decision

Before discovery, `sync` looks for the most recent `sync_runs` row for the organization
with `dry_run = 0` whose status is either `running` or `interrupted`. Both are resumable
and they record different histories: `running` means the process died, `interrupted`
means the operator stopped it with Ctrl+C or a repository cap hit. A `failed` run is not
resumable, because it stopped on a real error and a rerun should start clean. When one exists and `--no-resume` was not passed,
the run attaches to it: the same run id receives the new repository rows, and every
repository already recorded under that run with an outcome of `cloned`, `updated`,
`unchanged` or `conflict` is skipped.

A repository recorded as `error` is retried, because the error may have been the
interruption itself.

Skipped repositories are reported with a new outcome, `skipped`, and a message naming the
run they were completed in. They appear in `metadata.json` so the file still describes the
whole organization, not only the tail that this invocation processed.

When no interrupted run exists, or `--no-resume` was passed, a fresh run starts and
nothing is skipped.

A dry run neither resumes nor is resumable. Its run row is recorded with `dry_run = 1`,
which the resume query excludes, so a `--dry-run` invocation cannot leave behind a row
that a later real sync would adopt as unfinished work.

### Cancellation

`Ctrl+C` cancels the context. The command distinguishes `context.Canceled` from a real
failure and calls `Finish` with `interrupted`, which the resume query treats as
continuable. `failed` is not continuable, so the distinction is not merely cosmetic. Recording the final
status uses a short context detached from the cancelled one so the write completes.

## The `limit` verb

`org-mirror limit` calls `GET /rate_limit` through the same authenticated, rate-limited
client and prints one line per resource for `core`, `search`, `graphql` and
`code_search`, each with limit, used, remaining, and the reset instant as both a UTC
timestamp and a countdown. `--json` emits the parsed structure instead. It prints only
the resources the endpoint actually returns, so a payload without `code_search` is not an
error.

The command exists to answer "can I start a sync right now", so it prints a final line
stating whether the core budget is sufficient for the organization size when an
organization argument is supplied, and omits that line otherwise.

## The `--limit` flag

`sync --limit N` caps the number of repositories a single invocation processes. It applies
to the work remaining after the resume skip, so a resumed run with `--limit 50` processes
50 more repositories, not 50 including the ones already done. Zero, the default, means no
cap.

A capped run finishes with status `interrupted` rather than `completed` when repositories
remain, so the next invocation resumes into the same run and continues. This makes
`--limit` the mechanism for spreading a very large organization across several sessions.

Repositories left unprocessed by the cap are absent from that invocation's metadata
repository list rather than recorded with a placeholder outcome.

## Flags

| Flag | Default | Meaning |
|---|---|---|
| `--delay` | `750ms` | Minimum interval between repositories, jittered; `0` disables |
| `--max-wait` | `15m` | Longest a rate-limit wait may block before failing |
| `--retries` | `3` | Transient git failure retry attempts |
| `--no-resume` | `false` | Start fresh instead of continuing an interrupted run |
| `--limit` | `0` | Cap repositories processed this invocation, 0 for no cap |

## Error handling

A rate-limit wait is progress, not failure: it emits a progress event so the TUI shows
what it is waiting for and until when, rather than appearing hung. The plain-text path
prints the same information to stderr.

A repository that exhausts its retries is recorded as `error` with the final message and
the run continues, as it does today.

A database write failure during the run is fatal, because continuing would silently lose
the checkpoint guarantee the feature exists to provide.

## Testing

- `ratelimit`: table-driven tests against `httptest` servers returning low remaining, 403
  with `retry-after`, 403 without rate-limit headers, 429, 500 then 200, and a transport
  error then success. A fake clock asserts the exact sleep durations requested. A
  cancelled context aborts a pending wait.
- `Pacer`: interval spacing, jitter staying inside its bound, a zero interval waiting
  not at all, and cancellation, all on a fake clock.
- The sync loop paces after an errored and after a skipped repository, not only after a
  successful one, asserted through the fake clock rather than by timing.
- Git classification: a table of real git stderr strings mapped to transient or permanent.
- `history`: start, record three repositories, simulate a crash by reopening the file,
  find the interrupted run, and assert the skip set. Also a completed run is not resumed.
- `Service`: a fake runner failing transiently then succeeding; a fake runner failing
  permanently; a resume skip set producing `skipped` results without invoking the runner.
- `limit` command: a fake REST client returning a known payload, asserting both the table
  and the JSON output.
- `sync --limit`: a discovery of five repositories with `--limit 2` processes two and
  finishes `interrupted`.

## Out of scope

Concurrent repository processing. The sequential loop is retained, which is also the
safest posture against abuse detection. A `--max-concurrent` flag is a later change with
its own design, because it reworks the progress counters and the TUI in-flight display.
