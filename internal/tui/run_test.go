package tui

import (
	"context"
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
