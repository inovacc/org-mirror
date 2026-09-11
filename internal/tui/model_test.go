package tui

import (
	"strings"
	"testing"

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
