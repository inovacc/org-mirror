package history

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
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

func (d *Database) Record(organization string, started, finished time.Time, dryRun bool, metadata mirror.Metadata, runErr error) error {
	tx, err := d.db.Begin()
	if err != nil {
		return err
	}
	status := "completed"
	if runErr != nil {
		status = "failed"
	}
	result, err := tx.Exec(`INSERT INTO sync_runs (organization, started_at, completed_at, status, dry_run, repository_count, error) VALUES (?, ?, ?, ?, ?, ?, ?)`, organization, started.UTC().Format(time.RFC3339Nano), finished.UTC().Format(time.RFC3339Nano), status, dryRun, len(metadata.Repositories), errorText(runErr))
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("record sync run: %w", err)
	}
	runID, err := result.LastInsertId()
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	for _, item := range metadata.Repositories {
		_, err = tx.Exec(`INSERT INTO repositories (sync_run_id, name, full_name, clone_url, default_branch, commit_sha, remote_sha, open_issues, outcome, path, synced_at, message) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, runID, item.Repository.Name, item.Repository.NameWithOwner, item.Repository.CloneURL, item.Repository.DefaultBranch, item.LocalSHA, item.RemoteSHA, item.Repository.OpenIssues, item.Outcome, item.Path, finished.UTC().Format(time.RFC3339Nano), item.Message)
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record repository %s: %w", item.Repository.NameWithOwner, err)
		}
	}
	return tx.Commit()
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
