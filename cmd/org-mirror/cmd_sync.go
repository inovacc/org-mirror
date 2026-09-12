package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/inovacc/org-mirror/internal/githubapi"
	"github.com/inovacc/org-mirror/internal/history"
	"github.com/inovacc/org-mirror/internal/mirror"
	"github.com/inovacc/org-mirror/internal/ratelimit"
	"github.com/inovacc/org-mirror/internal/tui"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func init() {
	rootCmd.AddCommand(newSyncCommand())
}

func newSyncCommand() *cobra.Command {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	root, databasePath := defaultSyncPaths(home)
	var dryRun bool
	var noTUI bool
	var noResume bool
	var delay time.Duration
	var maxWait time.Duration
	var retries int
	var limit int

	command := &cobra.Command{
		Use:   "sync <organization>",
		Short: "Mirror an organization as local working copies",
		Long: `Mirror an organization as local working copies.

The run paces itself between repositories so it trips neither GitHub's rate
limits nor local endpoint-protection heuristics, and it checkpoints every
repository as it finishes. An interrupted run is continued automatically by the
next sync of the same organization.`,
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			organization := args[0]
			database, err := prepareSyncStorage(root, databasePath)
			if err != nil {
				return err
			}
			defer database.Close()

			run, skips, err := resumeSkips(database, organization, noResume || dryRun, dryRun)
			if err != nil {
				return err
			}

			// waits carries rate-limit notices from the transport to whichever front
			// end is running, so a long wait is visible rather than looking like a
			// hang. The transport calls it from the mirroring goroutine while the
			// front end attaches from another, hence the lock inside waitReporter.
			waits := &waitReporter{organization: organization, fallback: command.ErrOrStderr()}

			source, _, token, _, err := githubapi.NewAuthenticatedSource("github.com", githubapi.ClientOptions{
				Delay:   delay,
				MaxWait: maxWait,
				Notify:  waits.notify,
			})
			if err != nil {
				_ = run.Finish(history.StatusFailed, time.Now(), err)
				return err
			}

			service := mirror.NewServiceWithOptions(
				mirror.OSRunner{GitHubToken: token},
				source,
				mirror.RetryPolicy{
					Attempts: retries,
					Backoff:  gitBackoff,
					Sleep:    ratelimit.SystemClock{}.Sleep,
				},
			)
			pacer := ratelimit.NewPacer(ratelimit.PacerOptions{Interval: delay, Jitter: 0.3})

			options := mirror.MirrorOptions{
				Pacer: pacer,
				Skip:  skips,
				Limit: limit,
				OnResult: func(result mirror.Result) error {
					if dryRun {
						return nil
					}
					return run.RecordRepository(result, time.Now())
				},
			}

			var metadata mirror.Metadata
			if shouldUseTUI(noTUI, command.OutOrStdout(), term.IsTerminal) {
				metadata, err = tui.Run(command.Context(), organization, dryRun, func(ctx context.Context, progress mirror.ProgressFunc) (mirror.Metadata, error) {
					waits.attach(progress)
					options.Report = progress
					return service.MirrorWithOptions(ctx, organization, root, dryRun, options)
				})
			} else {
				metadata, err = service.MirrorWithOptions(command.Context(), organization, root, dryRun, options)
			}

			status := finalStatus(err, metadata.Truncated)
			// Finish writes the same count this reports into the run's own
			// row, and it does not change once mirroring has stopped (no
			// repository is checkpointed after this point), so reading it
			// after Finish - rather than before - means the number in the
			// summary is always the number Finish just persisted, not a
			// second, separately-timed read of the same table.
			finishErr := run.Finish(status, time.Now(), err)
			if finishErr != nil {
				// The run's own outcome is what the operator needs to see; a
				// failure to record it is worth saying out loud but must
				// never replace the real error.
				fmt.Fprintf(command.ErrOrStderr(), "warning: could not record the run's final status: %v\n", finishErr)
			}
			outcome := runOutcome(err, finishErr)

			if shouldPrintResults(status) {
				printResults(command.OutOrStdout(), metadata.Repositories)
			}

			if outcome != nil {
				if status == history.StatusInterrupted {
					// metadata.Repositories may already be empty here - the
					// interactive front end can end before it hands its
					// result back - so the count comes from the database,
					// the one source that is never missing a checkpoint.
					recorded, countErr := run.RecordedRepositories()
					if countErr != nil {
						fmt.Fprintf(command.ErrOrStderr(), "warning: could not read how many repositories were recorded: %v\n", countErr)
					}
					fmt.Fprint(command.OutOrStdout(), interruptedSummary(recorded, countErr))
				}
				return outcome
			}

			if metadata.Truncated {
				fmt.Fprintf(command.OutOrStdout(), "stopped at the --limit of %d; run sync again to continue\n", limit)
			}
			if dryRun {
				fmt.Fprintln(command.OutOrStdout(), "dry-run: metadata was not written")
				return nil
			}
			fmt.Fprintf(command.OutOrStdout(), "metadata: %s\n", filepath.Join(root, organization, "metadata.json"))
			return nil
		},
	}

	command.Flags().StringVar(&root, "root", root, "directory that contains organization mirrors")
	command.Flags().BoolVar(&dryRun, "dry-run", false, "report actions without changing repositories or metadata")
	command.Flags().BoolVar(&noTUI, "no-tui", false, "disable the interactive progress interface")
	command.Flags().StringVar(&databasePath, "database", databasePath, "SQLite database for sync history")
	command.Flags().DurationVar(&delay, "delay", 750*time.Millisecond, "minimum interval between repositories; 0 disables pacing")
	command.Flags().DurationVar(&maxWait, "max-wait", 15*time.Minute, "longest a rate-limit wait may block before failing")
	command.Flags().IntVar(&retries, "retries", 3, "attempts for a transient git failure, counting the first")
	command.Flags().BoolVar(&noResume, "no-resume", false, "start fresh instead of continuing an interrupted run")
	command.Flags().IntVar(&limit, "limit", 0, "process at most this many repositories; 0 means no cap")
	return command
}

// waitReporter turns a transport wait into something the operator can see. It
// prints to a writer until a progress front end attaches, then reports events.
type waitReporter struct {
	mu           sync.Mutex
	report       mirror.ProgressFunc
	organization string
	fallback     io.Writer
}

func (w *waitReporter) attach(report mirror.ProgressFunc) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.report = report
}

func (w *waitReporter) notify(wait ratelimit.Wait) {
	w.mu.Lock()
	report := w.report
	w.mu.Unlock()

	if report == nil {
		fmt.Fprintf(w.fallback, "waiting %s: %s\n", wait.Duration.Round(time.Second), wait.Reason)
		return
	}
	report(mirror.ProgressEvent{
		Kind:         mirror.ProgressWaiting,
		Organization: w.organization,
		Message:      wait.Reason,
		Until:        wait.Until,
	})
}

// gitBackoff doubles from two seconds, which is long enough that a retry is not
// itself a burst.
func gitBackoff(attempt int) time.Duration {
	delay := 2 * time.Second << (attempt - 1)
	if delay > time.Minute || delay <= 0 {
		return time.Minute
	}
	return delay
}

// resumeSkips continues an interrupted run when there is one, and reports which
// repositories that run already finished.
func resumeSkips(database *history.Database, organization string, disabled, dryRun bool) (*history.Run, map[string]string, error) {
	if !disabled {
		run, found, err := database.ResumableRun(organization)
		if err != nil {
			return nil, nil, err
		}
		if found {
			skips, err := run.CompletedRepositories()
			if err != nil {
				return nil, nil, err
			}
			return run, skips, nil
		}
	}
	run, err := database.StartRun(organization, time.Now(), dryRun)
	if err != nil {
		return nil, nil, err
	}
	return run, map[string]string{}, nil
}

// printResults writes one line per repository result, in the same format the
// success and interrupted paths both use, so they share it rather than each
// keeping their own copy of the loop.
func printResults(w io.Writer, results []mirror.Result) {
	for _, result := range results {
		if result.Message == "" {
			fmt.Fprintf(w, "%s: %s\n", result.Repository.NameWithOwner, result.Outcome)
			continue
		}
		fmt.Fprintf(w, "%s: %s (%s)\n", result.Repository.NameWithOwner, result.Outcome, result.Message)
	}
}

// interruptedSummary formats the closing line printed after an interrupted
// run. recorded is the database's own count of repositories checkpointed for
// this run, not an in-memory tally, because the interactive front end can end
// before it hands one back. When countErr is non-nil that count could not be
// read at all, so the line says so instead of claiming a specific number -
// reporting recorded as if it were known would repeat the exact mistake this
// fix corrects, just with a different wrong number.
func interruptedSummary(recorded int, countErr error) string {
	if countErr != nil {
		return "interrupted: run sync again to continue (repository count unavailable)\n"
	}
	return fmt.Sprintf("interrupted: %d repositories recorded; run sync again to continue\n", recorded)
}

// runOutcome decides what the command reports when the run itself and the
// bookkeeping that closes it can fail independently. The run's own error is
// what the operator needs to act on, so it always wins; a finish error is
// surfaced only when there is no more informative error to report instead.
func runOutcome(runErr, finishErr error) error {
	if runErr != nil {
		return runErr
	}
	return finishErr
}

// shouldPrintResults reports whether the per-repository result lines are worth
// showing for a run that ended in the given status. A completed or interrupted
// run always has something the operator should see - interrupted work was
// checkpointed, and that is exactly what resuming needs the operator to trust.
// A genuine failure is usually a discovery failure, which has nothing to list.
func shouldPrintResults(status history.Status) bool {
	return status == history.StatusCompleted || status == history.StatusInterrupted
}

// finalStatus keeps a cancelled or capped run distinct from a failed one, because
// only the operator's own stop should read as deliberate.
func finalStatus(runErr error, truncated bool) history.Status {
	switch {
	case errors.Is(runErr, context.Canceled), errors.Is(runErr, context.DeadlineExceeded):
		return history.StatusInterrupted
	case runErr != nil:
		return history.StatusFailed
	case truncated:
		return history.StatusInterrupted
	default:
		return history.StatusCompleted
	}
}

func defaultSyncPaths(home string) (string, string) {
	base := filepath.Join(home, "Downloads", "mirror")
	return filepath.Join(base, "orgs"), filepath.Join(base, "database.db")
}

func prepareSyncStorage(root, databasePath string) (*history.Database, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("create organizations directory: %w", err)
	}
	return history.Open(databasePath)
}

func shouldUseTUI(disabled bool, output io.Writer, isTerminal func(int) bool) bool {
	if disabled {
		return false
	}
	file, ok := output.(interface{ Fd() uintptr })
	return ok && isTerminal(int(file.Fd()))
}
