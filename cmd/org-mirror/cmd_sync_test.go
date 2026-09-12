package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/inovacc/org-mirror/internal/history"
	"github.com/inovacc/org-mirror/internal/mirror"
	"github.com/inovacc/org-mirror/internal/ratelimit"
)

func TestDefaultSyncPathsUseCurrentUsersHome(t *testing.T) {
	home := filepath.Join("home", "another-user")
	root, database := defaultSyncPaths(home)

	if want := filepath.Join(home, "Downloads", "mirror", "orgs"); root != want {
		t.Fatalf("default root = %q, want %q", root, want)
	}
	if want := filepath.Join(home, "Downloads", "mirror", "database.db"); database != want {
		t.Fatalf("default database = %q, want %q", database, want)
	}
}

func TestPrepareSyncStorageCreatesDatabaseAndOrganizationsDirectory(t *testing.T) {
	root, databasePath := defaultSyncPaths(t.TempDir())

	database, err := prepareSyncStorage(root, databasePath)
	if err != nil {
		t.Fatalf("prepare sync storage: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close sync database: %v", err)
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		t.Fatalf("organizations directory was not created at %q: %v", root, err)
	}
	if info, err := os.Stat(databasePath); err != nil || info.IsDir() {
		t.Fatalf("database file was not created at %q: %v", databasePath, err)
	}
}

func TestNewSyncCommandConfiguresRequiredFlags(t *testing.T) {
	command := newSyncCommand()
	if command.Use != "sync <organization>" {
		t.Fatalf("unexpected command use: %q", command.Use)
	}
	if command.Flags().Lookup("root") == nil || command.Flags().Lookup("dry-run") == nil || command.Flags().Lookup("no-tui") == nil {
		t.Fatal("sync command must expose root, dry-run, and no-tui flags")
	}
	root, err := command.Flags().GetString("root")
	if err != nil {
		t.Fatalf("read root flag: %v", err)
	}
	if root == "" {
		t.Fatal("default root must not be empty")
	}
}

func TestSyncCommandRootFlagOverridesDefault(t *testing.T) {
	command := newSyncCommand()
	want := filepath.Join(t.TempDir(), "mirrors")

	if err := command.Flags().Parse([]string{"--root", want}); err != nil {
		t.Fatalf("parse root flag: %v", err)
	}
	got, err := command.Flags().GetString("root")
	if err != nil {
		t.Fatalf("read root flag: %v", err)
	}
	if got != want {
		t.Fatalf("root flag = %q, want %q", got, want)
	}
}

func TestFinalStatus(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		truncated bool
		want      history.Status
	}{
		{name: "clean run", want: history.StatusCompleted},
		{name: "capped run", truncated: true, want: history.StatusInterrupted},
		{name: "cancelled run", err: context.Canceled, want: history.StatusInterrupted},
		{name: "deadline", err: context.DeadlineExceeded, want: history.StatusInterrupted},
		{name: "wrapped cancellation", err: fmt.Errorf("mirror: %w", context.Canceled), want: history.StatusInterrupted},
		{name: "real failure", err: errors.New("discovery failed"), want: history.StatusFailed},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := finalStatus(testCase.err, testCase.truncated); got != testCase.want {
				t.Fatalf("finalStatus = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestResumeSkipsReturnsTheInterruptedRunsCompletedRepositories(t *testing.T) {
	root, databasePath := defaultSyncPaths(t.TempDir())
	database, err := prepareSyncStorage(root, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	previous, err := database.StartRun("acme", time.Unix(1, 0), false)
	if err != nil {
		t.Fatal(err)
	}
	done := mirror.Result{
		Repository: mirror.Repository{Name: "one", NameWithOwner: "acme/one"},
		Outcome:    mirror.OutcomeCloned,
	}
	if err := previous.RecordRepository(done, time.Unix(2, 0)); err != nil {
		t.Fatal(err)
	}

	run, skips, err := resumeSkips(database, "acme", false, false)
	if err != nil {
		t.Fatalf("resume skips: %v", err)
	}
	if run.ID() != previous.ID() {
		t.Fatalf("run %d, want the interrupted run %d", run.ID(), previous.ID())
	}
	if _, ok := skips["acme/one"]; !ok {
		t.Fatalf("skips = %v, want acme/one", skips)
	}
}

func TestResumeSkipsStartsFreshWhenResumeIsDisabled(t *testing.T) {
	root, databasePath := defaultSyncPaths(t.TempDir())
	database, err := prepareSyncStorage(root, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	previous, err := database.StartRun("acme", time.Unix(1, 0), false)
	if err != nil {
		t.Fatal(err)
	}
	done := mirror.Result{
		Repository: mirror.Repository{Name: "one", NameWithOwner: "acme/one"},
		Outcome:    mirror.OutcomeCloned,
	}
	if err := previous.RecordRepository(done, time.Unix(2, 0)); err != nil {
		t.Fatal(err)
	}

	run, skips, err := resumeSkips(database, "acme", true, false)
	if err != nil {
		t.Fatalf("resume skips: %v", err)
	}
	if run.ID() == previous.ID() {
		t.Fatal("no-resume must open a new run")
	}
	if len(skips) != 0 {
		t.Fatalf("skips = %v, want none", skips)
	}
}

func TestResumeSkipsStartsAFreshRunWhenNothingIsResumable(t *testing.T) {
	root, databasePath := defaultSyncPaths(t.TempDir())
	database, err := prepareSyncStorage(root, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	run, skips, err := resumeSkips(database, "acme", false, false)
	if err != nil {
		t.Fatalf("resume skips: %v", err)
	}
	if run == nil || run.ID() == 0 {
		t.Fatal("a fresh run must be opened")
	}
	if len(skips) != 0 {
		t.Fatalf("skips = %v, want none", skips)
	}
}

func TestNewSyncCommandExposesTheResilienceFlags(t *testing.T) {
	command := newSyncCommand()
	for _, name := range []string{"delay", "max-wait", "retries", "no-resume", "limit"} {
		if command.Flags().Lookup(name) == nil {
			t.Fatalf("sync must expose the %s flag", name)
		}
	}

	delay, err := command.Flags().GetDuration("delay")
	if err != nil {
		t.Fatalf("read delay: %v", err)
	}
	if delay != 750*time.Millisecond {
		t.Fatalf("default delay = %v, want 750ms", delay)
	}
	retries, err := command.Flags().GetInt("retries")
	if err != nil {
		t.Fatalf("read retries: %v", err)
	}
	if retries != 3 {
		t.Fatalf("default retries = %d, want 3", retries)
	}
	limit, err := command.Flags().GetInt("limit")
	if err != nil {
		t.Fatalf("read limit: %v", err)
	}
	if limit != 0 {
		t.Fatalf("default limit = %d, want 0", limit)
	}
}

func TestWaitReporterWritesToTheFallbackUntilAFrontEndAttaches(t *testing.T) {
	var out bytes.Buffer
	reporter := &waitReporter{organization: "acme", fallback: &out}

	reporter.notify(ratelimit.Wait{Reason: "GitHub rate limit reached", Duration: 30 * time.Second})
	if !strings.Contains(out.String(), "GitHub rate limit reached") {
		t.Fatalf("fallback output = %q", out.String())
	}

	var events []mirror.ProgressEvent
	reporter.attach(func(event mirror.ProgressEvent) { events = append(events, event) })
	reporter.notify(ratelimit.Wait{Reason: "waiting for the reset", Duration: time.Minute})

	if len(events) != 1 || events[0].Kind != mirror.ProgressWaiting {
		t.Fatalf("events = %#v, want one waiting event", events)
	}
	if events[0].Message != "waiting for the reset" || events[0].Organization != "acme" {
		t.Fatalf("event = %#v", events[0])
	}
	if strings.Count(out.String(), "waiting") > 1 {
		t.Fatal("once a front end is attached the fallback must stay quiet")
	}
}

func TestRunOutcomePrefersTheRunErrorOverAFinishError(t *testing.T) {
	runErr := errors.New("discovery failed")
	finishErr := errors.New("database write failed")

	cases := []struct {
		name      string
		runErr    error
		finishErr error
		want      error
	}{
		{name: "run error wins over a finish error", runErr: runErr, finishErr: finishErr, want: runErr},
		{name: "run error alone", runErr: runErr, want: runErr},
		{name: "finish error alone, nothing better to report", finishErr: finishErr, want: finishErr},
		{name: "neither failed", want: nil},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := runOutcome(testCase.runErr, testCase.finishErr); got != testCase.want {
				t.Fatalf("runOutcome(%v, %v) = %v, want %v", testCase.runErr, testCase.finishErr, got, testCase.want)
			}
		})
	}
}

func TestShouldPrintResults(t *testing.T) {
	cases := []struct {
		status history.Status
		want   bool
	}{
		{status: history.StatusCompleted, want: true},
		{status: history.StatusInterrupted, want: true},
		{status: history.StatusFailed, want: false},
	}

	for _, testCase := range cases {
		t.Run(string(testCase.status), func(t *testing.T) {
			if got := shouldPrintResults(testCase.status); got != testCase.want {
				t.Fatalf("shouldPrintResults(%q) = %v, want %v", testCase.status, got, testCase.want)
			}
		})
	}
}

func TestInterruptedSummary(t *testing.T) {
	cases := []struct {
		name     string
		recorded int
		countErr error
		want     string
	}{
		{name: "zero recorded", recorded: 0, want: "interrupted: 0 repositories recorded; run sync again to continue\n"},
		{name: "several recorded", recorded: 40, want: "interrupted: 40 repositories recorded; run sync again to continue\n"},
		{name: "count unreadable, even with a recorded value present", recorded: 40, countErr: errors.New("database is locked"), want: "interrupted: run sync again to continue (repository count unavailable)\n"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := interruptedSummary(testCase.recorded, testCase.countErr); got != testCase.want {
				t.Fatalf("interruptedSummary(%d, %v) = %q, want %q", testCase.recorded, testCase.countErr, got, testCase.want)
			}
		})
	}
}
