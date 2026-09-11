package history

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/inovacc/org-mirror/internal/mirror"
	_ "modernc.org/sqlite"
)

type Database struct{ db *sql.DB }

func Open(path string) (*Database, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sync database: %w", err)
	}
	d := &Database{db: db}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize sync database: %w", err)
	}
	return d, nil
}

func (d *Database) Close() error { return d.db.Close() }

// Status is the lifecycle state of a sync run.
type Status string

const (
	// StatusRunning marks a run in flight. A run left in this state is resumable.
	StatusRunning Status = "running"
	// StatusCompleted marks a run that processed every repository.
	StatusCompleted Status = "completed"
	// StatusFailed marks a run that stopped on an error.
	StatusFailed Status = "failed"
	// StatusInterrupted marks a run cancelled by the operator or stopped by a
	// repository cap. It is resumable and is deliberately distinct from failed.
	StatusInterrupted Status = "interrupted"
)

// Run is one sync run's row, open for checkpointing.
type Run struct {
	db *sql.DB
	id int64
}

func (r *Run) ID() int64 { return r.id }

// StartRun opens a run in the running state. The repository count and the
// completion time are filled in by Finish.
func (d *Database) StartRun(organization string, started time.Time, dryRun bool) (*Run, error) {
	result, err := d.db.Exec(
		`INSERT INTO sync_runs (organization, started_at, completed_at, status, dry_run, repository_count, error) VALUES (?, ?, '', ?, ?, 0, NULL)`,
		organization, started.UTC().Format(time.RFC3339Nano), string(StatusRunning), dryRun,
	)
	if err != nil {
		return nil, fmt.Errorf("start sync run: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("start sync run: %w", err)
	}
	return &Run{db: d.db, id: id}, nil
}

// ResumableRun returns the most recent real run for the organization that was
// never finished, which is what an interrupted process leaves behind.
func (d *Database) ResumableRun(organization string) (*Run, bool, error) {
	var id int64
	err := d.db.QueryRow(
		`SELECT id FROM sync_runs WHERE organization = ? AND status = ? AND dry_run = 0 ORDER BY id DESC LIMIT 1`,
		organization, string(StatusRunning),
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("find resumable run: %w", err)
	}
	return &Run{db: d.db, id: id}, true, nil
}

// resumableOutcomes are the outcomes that mean a repository does not need to be
// visited again. An error is absent on purpose: it may have been the interruption.
var resumableOutcomes = []mirror.Outcome{
	mirror.OutcomeCloned,
	mirror.OutcomeUpdated,
	mirror.OutcomeUnchanged,
	mirror.OutcomeConflict,
	mirror.OutcomeSkipped,
}

// CompletedRepositories maps each already-finished repository to the reason a
// resumed run will give for skipping it.
func (r *Run) CompletedRepositories() (map[string]string, error) {
	placeholders := make([]string, 0, len(resumableOutcomes))
	arguments := make([]any, 0, len(resumableOutcomes)+1)
	arguments = append(arguments, r.id)
	for _, outcome := range resumableOutcomes {
		placeholders = append(placeholders, "?")
		arguments = append(arguments, string(outcome))
	}
	query := fmt.Sprintf(
		`SELECT full_name, outcome FROM repositories WHERE sync_run_id = ? AND outcome IN (%s)`,
		strings.Join(placeholders, ","),
	)
	rows, err := r.db.Query(query, arguments...)
	if err != nil {
		return nil, fmt.Errorf("read completed repositories: %w", err)
	}
	defer rows.Close()

	completed := map[string]string{}
	for rows.Next() {
		var fullName, outcome string
		if err := rows.Scan(&fullName, &outcome); err != nil {
			return nil, fmt.Errorf("read completed repositories: %w", err)
		}
		completed[fullName] = fmt.Sprintf("%s in run %d", outcome, r.id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read completed repositories: %w", err)
	}
	return completed, nil
}

// RecordRepository commits one repository immediately, so a process killed at
// any instant leaves behind exactly the work that finished.
func (r *Run) RecordRepository(result mirror.Result, at time.Time) error {
	_, err := r.db.Exec(
		`INSERT INTO repositories (sync_run_id, name, full_name, clone_url, default_branch, commit_sha, remote_sha, open_issues, outcome, path, synced_at, message) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.id, result.Repository.Name, result.Repository.NameWithOwner, result.Repository.CloneURL,
		result.Repository.DefaultBranch, result.LocalSHA, result.RemoteSHA, result.Repository.OpenIssues,
		string(result.Outcome), result.Path, at.UTC().Format(time.RFC3339Nano), result.Message,
	)
	if err != nil {
		return fmt.Errorf("record repository %s: %w", result.Repository.NameWithOwner, err)
	}
	return nil
}

// Finish closes the run. After this the run is no longer resumable unless the
// status says otherwise.
func (r *Run) Finish(status Status, finished time.Time, runErr error) error {
	var total int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM repositories WHERE sync_run_id = ?`, r.id).Scan(&total); err != nil {
		return fmt.Errorf("count run repositories: %w", err)
	}
	_, err := r.db.Exec(
		`UPDATE sync_runs SET status = ?, completed_at = ?, repository_count = ?, error = ? WHERE id = ?`,
		string(status), finished.UTC().Format(time.RFC3339Nano), total, errorText(runErr), r.id,
	)
	if err != nil {
		return fmt.Errorf("finish sync run: %w", err)
	}
	return nil
}

// Record writes a whole run at once. It is retained for callers that already
// have complete metadata and do not need checkpointing.
func (d *Database) Record(organization string, started, finished time.Time, dryRun bool, metadata mirror.Metadata, runErr error) error {
	run, err := d.StartRun(organization, started, dryRun)
	if err != nil {
		return err
	}
	for _, item := range metadata.Repositories {
		if err := run.RecordRepository(item, finished); err != nil {
			return err
		}
	}
	status := StatusCompleted
	if runErr != nil {
		status = StatusFailed
	}
	return run.Finish(status, finished, runErr)
}

func errorText(err error) any {
	if err == nil {
		return nil
	}
	return err.Error()
}

const schema = `
CREATE TABLE IF NOT EXISTS sync_runs (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 organization TEXT NOT NULL,
 started_at TEXT NOT NULL,
 completed_at TEXT NOT NULL,
 status TEXT NOT NULL,
 dry_run INTEGER NOT NULL DEFAULT 0,
 repository_count INTEGER NOT NULL,
 error TEXT
);
CREATE TABLE IF NOT EXISTS repositories (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 sync_run_id INTEGER NOT NULL REFERENCES sync_runs(id) ON DELETE CASCADE,
 name TEXT NOT NULL,
 full_name TEXT NOT NULL,
 clone_url TEXT,
 default_branch TEXT,
 commit_sha TEXT,
 remote_sha TEXT,
 open_issues INTEGER NOT NULL DEFAULT 0,
 outcome TEXT NOT NULL,
 path TEXT,
 synced_at TEXT NOT NULL,
 message TEXT
);
CREATE INDEX IF NOT EXISTS idx_sync_runs_org_time ON sync_runs(organization, completed_at DESC);
CREATE INDEX IF NOT EXISTS idx_repositories_run ON repositories(sync_run_id);
`
