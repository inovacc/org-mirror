package tui

import (
	"context"
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/inovacc/org-mirror/internal/mirror"
)

func TestRunStreamsProgressAndReturnsMetadata(t *testing.T) {
	want := mirror.Metadata{Organization: "inovacc"}
	operation := func(_ context.Context, report mirror.ProgressFunc) (mirror.Metadata, error) {
		report(mirror.ProgressEvent{Kind: mirror.ProgressDiscoveryCompleted, Total: 1})
		repository := mirror.Repository{NameWithOwner: "inovacc/lensr"}
		report(mirror.ProgressEvent{Kind: mirror.ProgressRepositoryStarted, Repository: repository, Total: 1})
		report(mirror.ProgressEvent{Kind: mirror.ProgressRepositoryCompleted, Repository: repository, Result: mirror.Result{Repository: repository, Outcome: mirror.OutcomeCloned}, Completed: 1, Total: 1})
		return want, nil
	}

	got, err := Run(context.Background(), "inovacc", false, operation, tea.WithInput(nil), tea.WithoutRenderer())
	if err != nil {
		t.Fatalf("run TUI: %v", err)
	}
	if got.Organization != want.Organization {
		t.Fatalf("metadata organization = %q, want %q", got.Organization, want.Organization)
	}
}

// TestRunReportsCancellationWhenTheContextIsAlreadyDone covers the property
// finalStatus actually depends on: that a context-cancelled run reports an
// error errors.Is can classify as context.Canceled. It does not - and,
// empirically, cannot reliably - assert that the model's own partial
// metadata survives a cancellation raced against Bubble Tea's own context
// watcher; see the comment in Run for why that race is not ours to win.
func TestRunReportsCancellationWhenTheContextIsAlreadyDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	operation := func(ctx context.Context, _ mirror.ProgressFunc) (mirror.Metadata, error) {
		return mirror.Metadata{}, ctx.Err()
	}

	_, err := Run(ctx, "inovacc", false, operation, tea.WithInput(nil), tea.WithoutRenderer())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}
