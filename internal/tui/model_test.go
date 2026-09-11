package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/inovacc/org-mirror/internal/mirror"
)

func TestModelShowsCurrentCompletedAndRemainingRepositories(t *testing.T) {
	model := NewModel("inovacc", false)

	updated, _ := model.Update(mirror.ProgressEvent{Kind: mirror.ProgressDiscoveryCompleted, Total: 3})
	model = updated.(Model)
	updated, _ = model.Update(mirror.ProgressEvent{
		Kind:       mirror.ProgressRepositoryStarted,
		Repository: mirror.Repository{NameWithOwner: "inovacc/lensr"},
		Completed:  0,
		Total:      3,
	})
	model = updated.(Model)
	updated, _ = model.Update(mirror.ProgressEvent{
		Kind:       mirror.ProgressRepositoryCompleted,
		Repository: mirror.Repository{NameWithOwner: "inovacc/lensr"},
		Result: mirror.Result{
			Repository: mirror.Repository{NameWithOwner: "inovacc/lensr"},
			Outcome:    mirror.OutcomeCloned,
		},
		Completed: 1,
		Total:     3,
	})
	model = updated.(Model)

	view := model.View().Content
	for _, expected := range []string{"inovacc/lensr", "1 / 3", "Remaining: 2", "cloned"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("view does not contain %q:\n%s", expected, view)
		}
	}
}

func TestModelShowsActivityWhileRepositoryIsRunning(t *testing.T) {
	model := NewModel("charmbracelet", false)
	updated, _ := model.Update(mirror.ProgressEvent{Kind: mirror.ProgressDiscoveryCompleted, Total: 1})
	model = updated.(Model)
	updated, _ = model.Update(mirror.ProgressEvent{
		Kind:       mirror.ProgressRepositoryStarted,
		Repository: mirror.Repository{NameWithOwner: "charmbracelet/bubbletea"},
		Total:      1,
	})
	model = updated.(Model)
	updated, _ = model.Update(activityTickMsg{at: time.Now().Add(2 * time.Second)})
	model = updated.(Model)

	view := model.View().Content
	for _, expected := range []string{"Working", "Elapsed: 2s", "charmbracelet/bubbletea"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("view does not contain %q:\n%s", expected, view)
		}
	}
}

func TestModelRendersAWaitingLine(t *testing.T) {
	model := NewModel("acme", false)
	model.total = 10
	model.completed = 3

	updated, _ := model.Update(mirror.ProgressEvent{
		Kind:    mirror.ProgressWaiting,
		Message: "GitHub rate limit reached, waiting for the reset",
		Until:   time.Unix(1700003600, 0).UTC(),
	})
	view := updated.(Model).View().Content

	if !strings.Contains(view, "Waiting:") {
		t.Fatalf("waiting line missing from view:\n%s", view)
	}
	if !strings.Contains(view, "rate limit") {
		t.Fatalf("waiting reason missing from view:\n%s", view)
	}
}

func TestModelClearsTheWaitingLineWhenWorkResumes(t *testing.T) {
	model := NewModel("acme", false)
	model.total = 10

	waiting, _ := model.Update(mirror.ProgressEvent{Kind: mirror.ProgressWaiting, Message: "waiting"})
	resumed, _ := waiting.(Model).Update(mirror.ProgressEvent{
		Kind:       mirror.ProgressRepositoryStarted,
		Repository: mirror.Repository{NameWithOwner: "acme/one"},
		Completed:  1,
		Total:      10,
	})

	if strings.Contains(resumed.(Model).View().Content, "Waiting:") {
		t.Fatal("the waiting line must clear once work resumes")
	}
}
