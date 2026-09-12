# SDD ledger — plan: docs/superpowers/plans/2026-09-11-resilient-sync.md

Spec: docs/superpowers/specs/2026-09-11-resilient-sync-design.md (read)
Branch: feat/resilient-sync (created from main)

Ruling: branch instead of a git worktree — the repo is clean with no concurrent
work, and a worktree would relocate the Go module path for every subagent on a
Windows drive-letter path. Cost if wrong: work sits on a branch of the primary
checkout rather than an isolated directory, so a `git clean` in the main tree
could disturb it.

## Pre-flight conflict scan

### Shared file / interface pairs

| Pair | Produced vs consumed | Finding |
|---|---|---|
| T1 -> T2 | Clock, SystemClock, Pacer, fakeClock/newFakeClock/sleeps | clean, names match |
| T1 -> T7/T8 | NewPacer(PacerOptions{Interval,Jitter}) | clean, same call shape in both |
| T2 -> T7 | NewTransport(TransportOptions{Pacer,MaxWait,Notify}) | clean, all three fields exist |
| T2 -> T8 | ratelimit.Wait, SystemClock{}.Sleep into RetryPolicy.Sleep | clean, signature (ctx,d) error matches |
| T3 -> T4 | Service struct gains retry; MirrorWithOptions calls s.Sync | clean, T4 does not restate the struct |
| T3 -> T4 | service_test.go scriptedRunner rewritten then extended | clean, T4 helpers use the same commandKey/scriptedResponse |
| T4 -> T5 | mirror.OutcomeSkipped used by resumableOutcomes | clean, T4 precedes T5 |
| T4 -> T8 | MirrorOptions{Report,Pacer,Skip,Limit,OnResult}, Metadata.Truncated | clean, every field consumed as declared |
| T5 -> T8 | StartRun/ResumableRun/CompletedRepositories/RecordRepository/Finish, Status consts | clean |
| T6 -> T7 | RateLimits.Ordered, RateLimit.ResetAt, RepositoryCount | clean |
| T6 vs existing | test type jsonClient vs existing scriptedRESTClient | clean, no collision |
| T7 -> T8 | NewAuthenticatedSource 5 returns; T7 patches cmd_sync, T8 rewrites it | clean, T7 flags the interim patch |

### Per-task internal agreement

| Task | Finding |
|---|---|
| T1 | clean; arithmetic in every pacer test re-derived and matches |
| T2 | clean; attempt counts and sleep sequences re-derived per test |
| T3 | clean; succeedAfter semantics match the scripted counts |
| T4 | clean; limit/skip interaction re-derived, results and Truncated match |
| T5 | DEFECT: Run.count is assigned but never read (Finish uses COUNT(*)) |
| T6 | clean |
| T7 | DEFECT: past-reset test asserts no "-" in output, but RFC3339 timestamps contain hyphens, so it can never pass |
| T8 | DEFECT: tui tests call view.String(); the existing tests use view.Content |
| T8 | DEFECT: waitReporter had no test despite being new logic (added during plan self-review) |

Ruling: fix all four in the plan before Task 1 rather than letting them reach an
implementer. A test that cannot pass and a field that cannot be read are plan
defects, not implementation choices. Cost if wrong: none, the fixes are local to
the plan text and each is verified against code already in the repo.

## Execution
Task 1: dispatched (haiku), BASE a8d8b81
Task 1: implemented, commit 2ac5d81 "feat: add injectable clock and jittered pacer", 10/10 tests reported passing
Task 1: task review dispatched (sonnet) over a8d8b81..2ac5d81
Task 1: review returned "Needs fixes" — 0 critical, 2 important (both plan-mandated), 4 minor.
Task 1: Ruling: the 1ms real sleep in TestSystemClockSleepReturnsAfterTheDuration stands — SystemClock.Sleep's real wait IS the behaviour under test, and the reviewer's proposed replacement (a zero-duration call) would assert nothing. The constraint was overbroad, so the plan's Global Constraints now name this single exception. Cost if wrong: the suite spends one millisecond.
Task 1: Ruling: the three SystemClock tests stay as separate named tests — they exercise three different branches with three different assertions, so a table would need a per-row flag, which the same rubric calls premature abstraction. The constraint now says table-driven applies to homogeneous cases. Cost if wrong: three test functions where one table was wanted.
Task 1: minor (deferred): NewPacer's negative Interval/Jitter clamps are untested.
Task 1: minor (deferred): jitter lower bound with Rand()=0 and Jitter>0 is untested.
Task 1: minor (deferred): no concurrent Wait test backing the "safe for concurrent use" doc claim.
Task 1: minor (deferred): no test cancels a wait already in flight, only one cancelled before Wait.
Task 1: verified the commit trailer myself, since the diff artifact carried only the subject line.
Task 1: complete (commits a8d8b81..1d75b78, 2 important ruled, 4 minor deferred)
Task 2: dispatched (sonnet), BASE 1d75b78
Task 2: implemented, commit 0a1cd51 "feat: add rate-limit aware HTTP transport", 12 transport tests + full suite reported green
Task 2: Ruling: the .gitignore addition of .scripts/ rides along in the feature commit rather than being split out. It is two lines of repo hygiene required by the operator's standing script-execution rule, and splitting it would cost a commit to save nothing. Cost if wrong: one feature commit carries two unrelated lines.
Task 2: task review dispatched (sonnet) over 1d75b78..0a1cd51
Task 2: review returned "Needs fixes" — 0 critical, 2 important (both plan-mandated), 3 minor.
Task 2: Ruling: FIX the holdUntil lost-update. It is a genuine time-of-check-to-time-of-use bug: honourHold clears a hold it no longer owns after a long sleep, so a newer hold armed meanwhile is wiped and a wait is skipped. Unreachable today because the mirror loop is sequential, but a rate limiter whose job is to avoid a ban should not carry a latent skip-the-wait bug, and compare-and-clear is three lines. Cost if wrong: three lines of defensive code for a path only concurrency reaches.
Task 2: Ruling: FIX the vacuous cancellation test. It asserted only err != nil, and the stub's exhaustion produces a non-nil error even if the context were ignored entirely, so the test could not fail. A test that cannot fail is not evidence. Cost if wrong: none.
Task 2: minor (deferred): one Body.Close() discards its error implicitly while the rest of the file uses the explicit `_ =` form.
Task 2: minor (deferred): drain() caps at 4096 bytes, so its doc claim about connection reuse does not hold for larger error bodies.
Task 2: minor (deferred): Reserve/MaxAttempts/MaxWait treat an explicit 0 as unset, so Reserve:0 cannot be configured. Matches the brief's documented contract.
Task 2: fix round 1 dispatched (resumed original implementer)
Task 2: plan corrected in commit 09c26ae; the resumed implementer may pick an equivalent test shape rather than the onSleep hook, reconcile at re-review
Task 2: fix round 1/5 applied, commit a4f7274; scoped re-review dispatched over 09c26ae..a4f7274. Implementer disclosed a mid-fix 'git checkout --' that reverted transport.go and was re-applied; re-reviewer asked to check for anything lost in that round trip.
Task 2: fix round 1/5 (2 addressed, 0 open; commits 0a1cd51..a4f7274). Re-reviewer confirmed the falsification transcript is real, not asserted, and found nothing lost in the git-checkout round trip.
Task 2: complete (commits 1d75b78..a4f7274, review clean, 3 minor deferred)
Task 3: dispatched (sonnet), BASE a4f7274
Task 3: implemented, commit a2d4698 "feat: retry transient git failures with backoff", full suite reported green across 7 packages
Task 3: Ruling: the implementer's concern that no production caller wires a real Sleep into NewServiceWithOptions is expected, not a defect — Task 8 does that wiring. Cost if wrong: the retry path ships unexercised outside tests, which Task 8's review would catch.
Task 3: task review dispatched (sonnet) over a4f7274..a2d4698
Task 3: review Approved — 0 critical, 0 important, 2 minor. Reviewer hand-traced all three retry tests and confirmed none is vacuous.
Task 3: minor (deferred): RetryPolicy.wait no-ops silently when Backoff or Sleep is nil, so a partly-configured policy retries with no context check between attempts. Task 8 must pass both.
Task 3: minor (deferred): only clone has retry-path tests; fetch shares the same runGit path but has none.
Task 3: complete (commits a4f7274..a2d4698, review clean)
Task 4: dispatched (sonnet), BASE a2d4698
Task 4: implemented, commit c044c47 "feat: pace, skip and cap repositories in the mirror loop", 20/20 internal/mirror tests reported passing (12 pre-existing untouched, 8 new)
Task 4: Ruling: ProgressWaiting/Message/Until being declared but not emitted here is expected — Task 8 emits them from the transport's notify hook. Cost if wrong: dead type surface if Task 8 fails to wire it, which Task 8's review would catch.
Task 4: task review dispatched (sonnet) over a2d4698..c044c47
Task 4: review Approved — 0 critical, 0 important, 3 minor. Reviewer independently re-derived the skip-plus-cap arithmetic and the cap-equals-count boundary, and confirmed no test is vacuous. Trailer verified by the controller.
Task 4: minor (deferred): MirrorWithOptions is ~66 lines carrying discovery, skip, cap, pacing, sync, checkpoint and reporting; plan-mandated, but a candidate for extracting the skip and limit branches.
Task 4: minor (deferred): metadata.json is written on a cap-triggered break but not on a pacer or OnResult abort; defensible, wants a one-line comment.
Task 4: minor (deferred): TestMirrorPacesAfterAFailedRepository is named "after" but pacing happens before Sync; cosmetic, name is plan-mandated.
Task 4: complete (commits a2d4698..c044c47, review clean)
Task 5: dispatched (sonnet), BASE c044c47
Task 5: implemented, commit 40ef1e6 "feat: checkpoint each repository through a sync run lifecycle", 7/7 internal/history tests reported passing. Controller verified independently that the schema constant is untouched and the trailer is present.
Task 5: task review dispatched (sonnet) over c044c47..40ef1e6, with SQL column-order transposition and placeholder-count checks called out explicitly.
Task 5: review Approved — 0 critical, 0 important, 3 minor. Reviewer hand-checked every SQL column/value order and the dynamic IN() placeholder mapping and found no transposition.
Task 5: Ruling: UPGRADE the reviewer's first Minor to Important on cross-task grounds and run a fix round. ResumableRun matches only status='running', but Task 8's finalStatus marks BOTH a Ctrl+C run and a --limit-capped run as 'interrupted'. As written, neither would ever be resumed, which silently kills the two headline behaviours the user asked for: resume after a connection close, and --limit as the way to spread a large organization over several sessions. The reviewer graded it Minor because from inside Task 5 it reads as a stale doc comment; I hold the Task 8 context it does not. Smallest change that unblocks: ResumableRun matches 'running' OR 'interrupted'. 'failed' stays non-resumable, matching the spec, which calls only those two resumable. Cost if wrong: a deliberately cancelled run is continued by the next sync rather than starting fresh, which is what --no-resume exists to override.
Task 5: Ruling: a second, related defect found in Task 8's plan text while checking the above. resumeSkips calls StartRun(organization, time.Now(), false), hard-coding dry_run=0 even for a --dry-run invocation. That leaves a permanently 'running' non-dry row behind after every dry run, which the next real sync would then adopt as a resumable run with zero completed repositories. Fix: pass the real dryRun flag through. Cost if wrong: none, it is strictly more correct.
Task 5: minor (deferred): no test exercises ResumableRun against a failed row (being fixed as part of the round above, which adds interrupted and failed cases).
Task 5: minor (deferred): the reimplemented Record is no longer atomic. A mid-loop insert failure now leaves a permanently 'running' row with a partial set instead of rolling back. Inherent to the per-repository-commit mandate, and Record is no longer on the sync path, but it is a real behaviour change and is untested.
Task 5: fix round 1 dispatched (resumed original implementer)
Task 5: fix round 1/5 applied, commit 57b5535, 8/8 history tests reported passing with a falsification transcript; scoped re-review dispatched over 2060b14..57b5535
Task 5: fix round 1/5 (1 addressed, 0 open; commits 40ef1e6..57b5535). Re-reviewer verified the falsification transcript against the real file:line and confirmed it is a genuine run.
Task 5: minor (deferred): two history tests call resumed.ID() as a Fatalf argument on the !found branch, so a genuine "not found" failure panics instead of printing a readable message. Pre-existing pattern, replicated into the new test.
Task 5: complete (commits c044c47..57b5535, review clean, 3 minor deferred)
Task 6: dispatched (haiku), BASE 57b5535
Task 6: implemented, commit 14309f9 "feat: read GitHub rate limits and organization size", 7 new tests reported passing, two new files only. Controller verified the file list and trailer.
Task 6: task review dispatched (sonnet), with the Ordered comparator's strict-weak-ordering property called out explicitly.
Task 6: review returned "Needs fixes" — 0 critical, 3 important (2 plan-mandated vacuous tests plus an overclaiming self-review), 3 minor.
Task 6: Ruling: FIX both vacuous tests. The ordering test's fixture happened to make core-first and plain-alphabetical identical, and the context test used a fake that errors on every path, so neither could fail if its behaviour were deleted. Adding code_search (the one real GitHub resource sorting before core) and a never-called assertion makes both discriminating. Cost if wrong: none.
Task 6: Ruling: FOLD IN the gofmt minor rather than deferring it. gofmt cleanliness is a standing rule for Go source here, build/vet/test do not catch it, and the implementer is already in the file. Cost if wrong: one extra line in a fix round.
Task 6: Note: this is the second time an implementer's self-review marked a test NOT VACUOUS on reasoning that did not hold. Later dispatches should ask for the falsification to be RUN, not argued.
Task 6: minor (deferred): Ordered's comparator is not antisymmetric if both elements were "core"; unreachable because the names come from map keys, but unsound if reused.
Task 6: minor (deferred): TestRateLimitsWrapsTheClientError asserts only err != nil, so it does not prove %w wrapping.
Task 6: fix round 1 dispatched (resumed original implementer), now also covering RepositoryCount's context check
Task 6: fix round 1/5 applied, commit 38ce8f1, 8/8 tests reported passing with falsification transcripts; controller independently confirmed gofmt -l is clean tree-wide; scoped re-review dispatched over eea0978..38ce8f1
Task 6: fix round 1/5 (3 addressed, 0 open; commits 14309f9..38ce8f1). Re-reviewer verified both fixes mechanically but judged the falsification transcripts DOUBTFUL: narrated prose, no test-runner output, no commands.
Task 6: Ruling: rather than accept a fourth chain of reasoning about tests that cannot fail, the controller ran the falsification itself via .scripts/03-B_falsify_ratelimit_tests.sh. MEASURED result, not argued:
  - naive sort.Strings comparator -> TestRateLimitsOrdersCoreFirstThenTheRestAlphabetically FAILS at ratelimit_test.go:69, got [code_search core graphql search] want [core code_search graphql search]
  - ctx.Err() guard deleted -> TestRateLimitsRespectsACancelledContext FAILS at ratelimit_test.go:132, "request was made despite cancelled context: [rate_limit]"
  - file restored from git, working tree clean, package suite green again
  Both tests are therefore genuinely discriminating. Cost if wrong: none; the probe was read-only in effect and self-restoring.
Task 6: complete (commits 57b5535..38ce8f1, review clean, 2 minor deferred)
Task 7: dispatched (sonnet), BASE 38ce8f1
Task 7: implemented, commit 80ce765 "feat: add the limit command and rate-limit the API client", DONE_WITH_CONCERNS. Controller verified cmd_sync.go changed exactly one line, as authorised.
Task 7: Ruling: the implementer's gofmt concern was real and the controller caused it. core.autocrlf=true rewrites .go files to CRLF on checkout, and my own falsification script had checked one back out. The committed blob was always LF and clean. Rather than renormalise one file, added .gitattributes pinning Go/md/yaml source to eol=lf, because gofmt -l is now a mandatory pre-commit gate and a gate that cries wolf on every Windows checkout gets ignored. Verified by deleting and re-checking-out the file: it comes back LF and gofmt is clean tree-wide, with no tracked file changed. Committed separately. Cost if wrong: a repo-wide attributes file nobody asked for, trivially revertible.
Task 7: task review dispatched (sonnet), required to assess three falsification transcripts as genuine/doubtful/absent.
Task 7: review Approved — 0 critical, 0 important, 2 minor. All three falsification transcripts assessed GENUINE, with the reviewer re-deriving each cited line number from the diff independently. Demanding run output instead of argument worked.
Task 7: minor (deferred): with an organization argument, the limits table prints before RepositoryCount runs, so a failed lookup errors after partial stdout. A note would read better than a hard error.
Task 7: minor (deferred): budgetVerdict recomputes ResetAt for its message, duplicating the countdown already in the table.
Task 7: complete (commits 38ce8f1..80ce765, review clean, plus the separate .gitattributes commit)
Task 8: dispatched (sonnet), BASE is HEAD after the gitattributes commit
Task 8: implemented, commit 0bd09e9 "feat: resume interrupted syncs and pace against rate limits". Full suite reported passing across 7 packages under -race; controller confirmed gofmt clean and the trailer present.
Task 8: Ruling: the implementer's concern was a real plan defect of mine. A patch script had written a literal newline inside the wait reporter's Go format string, so the plan's code block would not have compiled as printed. The implementer used the correct escaped form. Plan repaired in a9c7ff6. Cost if wrong: none.
Task 8: task review dispatched (sonnet), required to trace the run lifecycle for every ending and assess three falsification transcripts.
Task 8: review Approved — 0 critical, 1 important, 3 minor. All three falsification transcripts assessed GENUINE with line numbers re-derived from the diff. Reviewer traced the lifecycle for clean, capped, cancelled, discovery-failure and client-open-failure endings and found Finish called exactly once on each; it also checked the dry-run resumability loophole against internal/history and confirmed it closed.
Task 8: Ruling: FIX the Important finding. A failure to record the run's final status currently replaces the run's own error, so an operator who pressed Ctrl+C would see a database write error instead of the cancellation. The run error must always win. Cost if wrong: a finish failure is reported as a warning rather than as the command's error, which is the correct precedence anyway.
Task 8: Ruling: UPGRADE the third Minor to Important. On any error, including a clean Ctrl+C, the command returns before printing anything, so the operator sees no evidence that the repositories completed before the interruption were saved. Every one of them WAS checkpointed. This feature exists so an interruption stops losing work, and printing nothing tells the user the opposite; silence after Ctrl+C reads as "nothing happened". An interrupted run must print what completed and say the next sync continues from there, while still returning the error so the exit code stays honest. Cost if wrong: a few extra lines of output on an interrupted run.
Task 8: minor (deferred): RunE is ~85 lines and would read better with the service-assembly and result-printing extracted.
Task 8: minor (deferred): history.Database.Record is now unreachable from production code, retained only for its own test. Effectively dead application-facing code.
Task 8: fix round 1 dispatched (resumed original implementer)
Task 8: fix round 1/5 applied, commit b342813, both new pure-predicate tests falsified with real runner output.
Task 8: Ruling: the implementer's round-1 concern was correct and its "out of scope" judgement was not. Verified myself: cmd/org-mirror/org-mirror.go calls rootCmd.Execute() with context.Background() and there is no signal.NotifyContext or ExecuteContext anywhere in the repo. So graceful cancellation exists ONLY inside the TUI, where Bubble Tea catches ctrl+c. With --no-tui or redirected output, Ctrl+C kills the process outright: Finish never runs, the status stays 'running', and the interrupted summary just built is unreachable. Resume still survives because checkpoints are committed per repository and a running row is resumable, but the spec's promise that Ctrl+C records 'interrupted' is only half true, and this task owns making cancellation real from the command line. Dispatched as fix round 2 rather than deferred. Cost if wrong: ten lines of standard signal wiring in main, trivially revertible.
Task 8: fix round 2/5 dispatched (resumed original implementer) — signal.NotifyContext + ExecuteContext
Task 8: fix round 2/5 applied, commit 40cb06c (signal.NotifyContext + ExecuteContext), DONE_WITH_CONCERNS.
Task 8: Ruling: the implementer's round-2 concern is real and I verified it in internal/tui/run.go. When the run context ends, Bubble Tea's loop returns as soon as it sees the cancellation, so the model's metadata is usually never handed back and tui.Run yields an empty Metadata. The interrupted summary then reports 0 repositories while the database holds every one that finished. That is worse than the silence it replaced: it makes a false statement about the exact guarantee this feature exists to provide, in the DEFAULT interactive mode. --no-tui is accurate only by accident of the metadata surviving there. Authorised the scope the implementer correctly declined to take unilaterally: expose the COUNT(*) that Finish already runs as Run.RecordedRepositories and build the summary from the database instead of from in-memory metadata. Cost if wrong: one small exported method on Run and one extra query per interrupted run.
Task 8: Known limitation accepted, NOT verified: live OS signal delivery could not be demonstrated end to end (non-interactive Windows shell, no TTY, SIGTERM undeliverable on Windows). Carried forward as an untested path rather than implied as working.
Task 8: fix round 3/5 dispatched (resumed original implementer)
Task 8: fix round 3/5 applied, commit d6cc08d. Run.RecordedRepositories added, Finish now uses it, summary counts from the database.
Task 8: minor (deferred): when the TUI itself is interrupted the itemized per-repository list can still be sparse. The COUNT is now always accurate; only the list is short, and only in that one mode.
Task 8: scoped re-review dispatched (sonnet) over 0bd09e9..d6cc08d covering all three fix rounds and four findings.
Task 8: fix rounds 1-3 re-review: ALL FOUR findings ADDRESSED, no new critical/important breakage. Five of six falsification transcripts genuine; one (the TUI cancellation test, round 2) cited a line number six lines stale, matching a doc comment added afterwards, so it was captured against an earlier draft. Content consistent with the code; not grounds to reopen.
Task 8: complete (commits 76f86b7..d6cc08d, review clean, 5 minor deferred)
RUN ENDED EARLY BY OPERATOR: asked to stop after the in-flight re-review rather than run the whole-branch review. All 8 tasks complete and reviewed; the final cross-task pass was skipped by their decision, not by omission.
