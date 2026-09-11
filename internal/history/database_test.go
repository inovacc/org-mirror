package history

import (
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
