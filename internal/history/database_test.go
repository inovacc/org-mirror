package history

import (
	"errors"
	"testing"
	"time"

	"github.com/inovacc/org-mirror/internal/mirror"
)

func TestRecordStoresRunAndRepositoryHistory(t *testing.T) {
	database, err := Open(t.TempDir() + "/database.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	metadata := mirror.Metadata{Repositories: []mirror.Result{{
		Repository: mirror.Repository{Name: "repo", NameWithOwner: "acme/repo", DefaultBranch: "main", OpenIssues: 3},
		Outcome:    mirror.OutcomeUpdated, LocalSHA: "abc", RemoteSHA: "def",
	}}}
	if err := database.Record("acme", time.Unix(1, 0), time.Unix(2, 0), false, metadata, nil); err != nil {
		t.Fatal(err)
	}
	var count, issues int
	if err := database.db.QueryRow("SELECT repository_count FROM sync_runs").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if err := database.db.QueryRow("SELECT open_issues FROM repositories").Scan(&issues); err != nil {
		t.Fatal(err)
	}
	if count != 1 || issues != 3 {
		t.Fatalf("stored count=%d issues=%d", count, issues)
	}
}

func TestStartRunRecordsARunningRow(t *testing.T) {
	database, err := Open(t.TempDir() + "/database.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	run, err := database.StartRun("acme", time.Unix(1, 0), false)
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	if run.ID() == 0 {
		t.Fatal("a started run must have an identity")
	}

	var status string
	var count int
	if err := database.db.QueryRow("SELECT status, repository_count FROM sync_runs WHERE id = ?", run.ID()).Scan(&status, &count); err != nil {
		t.Fatal(err)
	}
	if status != string(StatusRunning) || count != 0 {
		t.Fatalf("status=%q count=%d, want running and 0", status, count)
	}
}

func TestFinishRunSetsTheStatusAndTheRepositoryCount(t *testing.T) {
	database, err := Open(t.TempDir() + "/database.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	run, err := database.StartRun("acme", time.Unix(1, 0), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := run.RecordRepository(resultFor("acme/one", mirror.OutcomeCloned), time.Unix(2, 0)); err != nil {
		t.Fatal(err)
	}
	if err := run.Finish(StatusCompleted, time.Unix(3, 0), nil); err != nil {
		t.Fatalf("finish: %v", err)
	}

	var status string
	var count int
	var completedAt string
	if err := database.db.QueryRow("SELECT status, repository_count, completed_at FROM sync_runs WHERE id = ?", run.ID()).Scan(&status, &count, &completedAt); err != nil {
		t.Fatal(err)
	}
	if status != string(StatusCompleted) || count != 1 || completedAt == "" {
		t.Fatalf("status=%q count=%d completedAt=%q", status, count, completedAt)
	}
}

func TestFinishRunStoresTheErrorText(t *testing.T) {
	database, err := Open(t.TempDir() + "/database.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	run, err := database.StartRun("acme", time.Unix(1, 0), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Finish(StatusFailed, time.Unix(3, 0), errors.New("discovery failed")); err != nil {
		t.Fatal(err)
	}

	var text string
	if err := database.db.QueryRow("SELECT error FROM sync_runs WHERE id = ?", run.ID()).Scan(&text); err != nil {
		t.Fatal(err)
	}
	if text != "discovery failed" {
		t.Fatalf("error = %q", text)
	}
}

func TestResumableRunFindsAnInterruptedRunAndItsCompletedRepositories(t *testing.T) {
	path := t.TempDir() + "/database.db"
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	run, err := database.StartRun("acme", time.Unix(1, 0), false)
	if err != nil {
		t.Fatal(err)
	}
	for name, outcome := range map[string]mirror.Outcome{
		"acme/one":   mirror.OutcomeCloned,
		"acme/two":   mirror.OutcomeConflict,
		"acme/three": mirror.OutcomeError,
	} {
		if err := run.RecordRepository(resultFor(name, outcome), time.Unix(2, 0)); err != nil {
			t.Fatal(err)
		}
	}
	// Simulate a crash: the process dies without finishing the run.
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	resumed, found, err := reopened.ResumableRun("acme")
	if err != nil {
		t.Fatalf("find resumable run: %v", err)
	}
	if !found || resumed.ID() != run.ID() {
		t.Fatalf("found=%v id=%d, want the interrupted run", found, resumed.ID())
	}

	completed, err := resumed.CompletedRepositories()
	if err != nil {
		t.Fatal(err)
	}
	if len(completed) != 2 {
		t.Fatalf("completed = %v, want the cloned and conflicted repositories only", completed)
	}
	if _, ok := completed["acme/three"]; ok {
		t.Fatal("an errored repository must be retried, not skipped")
	}
	if completed["acme/one"] == "" {
		t.Fatal("a skip reason must explain where the repository was completed")
	}
}

func TestResumableRunIgnoresFinishedAndDryRuns(t *testing.T) {
	database, err := Open(t.TempDir() + "/database.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	finished, err := database.StartRun("acme", time.Unix(1, 0), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := finished.Finish(StatusCompleted, time.Unix(2, 0), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := database.StartRun("acme", time.Unix(3, 0), true); err != nil {
		t.Fatal(err)
	}
	if _, err := database.StartRun("other", time.Unix(4, 0), false); err != nil {
		t.Fatal(err)
	}
	failed, err := database.StartRun("acme", time.Unix(5, 0), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := failed.Finish(StatusFailed, time.Unix(6, 0), errors.New("discovery failed")); err != nil {
		t.Fatal(err)
	}

	if _, found, err := database.ResumableRun("acme"); err != nil || found {
		t.Fatalf("found=%v err=%v, want no resumable run", found, err)
	}
}

func TestResumableRunFindsARunFinishedAsInterrupted(t *testing.T) {
	database, err := Open(t.TempDir() + "/database.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	run, err := database.StartRun("acme", time.Unix(1, 0), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := run.RecordRepository(resultFor("acme/one", mirror.OutcomeCloned), time.Unix(2, 0)); err != nil {
		t.Fatal(err)
	}
	if err := run.Finish(StatusInterrupted, time.Unix(3, 0), nil); err != nil {
		t.Fatal(err)
	}

	resumed, found, err := database.ResumableRun("acme")
	if err != nil {
		t.Fatalf("find resumable run: %v", err)
	}
	if !found || resumed.ID() != run.ID() {
		t.Fatalf("found=%v id=%d, want the interrupted run", found, resumed.ID())
	}

	completed, err := resumed.CompletedRepositories()
	if err != nil {
		t.Fatal(err)
	}
	if completed["acme/one"] == "" {
		t.Fatal("a skip reason must explain where the repository was completed")
	}
}

func TestResumableRunPrefersTheMostRecentInterruptedRun(t *testing.T) {
	database, err := Open(t.TempDir() + "/database.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	if _, err := database.StartRun("acme", time.Unix(1, 0), false); err != nil {
		t.Fatal(err)
	}
	newer, err := database.StartRun("acme", time.Unix(2, 0), false)
	if err != nil {
		t.Fatal(err)
	}

	resumed, found, err := database.ResumableRun("acme")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if resumed.ID() != newer.ID() {
		t.Fatalf("resumed run %d, want %d", resumed.ID(), newer.ID())
	}
}

func resultFor(fullName string, outcome mirror.Outcome) mirror.Result {
	return mirror.Result{
		Repository: mirror.Repository{Name: fullName, NameWithOwner: fullName, DefaultBranch: "main"},
		Outcome:    outcome,
	}
}

func TestRecordedRepositoriesReportsZeroForAFreshRun(t *testing.T) {
	database, err := Open(t.TempDir() + "/database.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	run, err := database.StartRun("acme", time.Unix(1, 0), false)
	if err != nil {
		t.Fatal(err)
	}

	count, err := run.RecordedRepositories()
	if err != nil {
		t.Fatalf("recorded repositories: %v", err)
	}
	if count != 0 {
		t.Fatalf("count = %d, want 0", count)
	}
}

func TestRecordedRepositoriesCountsWhatWasCheckpointed(t *testing.T) {
	database, err := Open(t.TempDir() + "/database.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	run, err := database.StartRun("acme", time.Unix(1, 0), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"acme/one", "acme/two", "acme/three"} {
		if err := run.RecordRepository(resultFor(name, mirror.OutcomeCloned), time.Unix(2, 0)); err != nil {
			t.Fatal(err)
		}
	}

	count, err := run.RecordedRepositories()
	if err != nil {
		t.Fatalf("recorded repositories: %v", err)
	}
	if count != 3 {
		t.Fatalf("count = %d, want 3", count)
	}
}

func TestRecordedRepositoriesIsNotPollutedByAnotherRun(t *testing.T) {
	database, err := Open(t.TempDir() + "/database.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	other, err := database.StartRun("acme", time.Unix(1, 0), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"acme/one", "acme/two"} {
		if err := other.RecordRepository(resultFor(name, mirror.OutcomeCloned), time.Unix(2, 0)); err != nil {
			t.Fatal(err)
		}
	}

	run, err := database.StartRun("acme", time.Unix(3, 0), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := run.RecordRepository(resultFor("acme/three", mirror.OutcomeCloned), time.Unix(4, 0)); err != nil {
		t.Fatal(err)
	}

	count, err := run.RecordedRepositories()
	if err != nil {
		t.Fatalf("recorded repositories: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1 (this run's own row only, not the other run's two)", count)
	}
}
