package tui

import (
	"context"
	"errors"

	tea "charm.land/bubbletea/v2"
	"github.com/inovacc/org-mirror/internal/mirror"
)

func Run(ctx context.Context, organization string, dryRun bool, operation MirrorFunc, options ...tea.ProgramOption) (mirror.Metadata, error) {
	runContext, cancel := context.WithCancel(ctx)
	defer cancel()

	model := NewModel(organization, dryRun)
	model.events = make(chan tea.Msg)
	model.cancel = cancel
	model.context = runContext
	model.operation = operation

	options = append(options, tea.WithContext(runContext))
	final, runErr := tea.NewProgram(model, options...).Run()

	// Bubble Tea reports an error from Run whenever runContext ends - a
	// cancellation reaching here from a keypress, or now from an OS signal -
	// even when the model itself shut down gracefully and already recorded
	// the operation's own metadata and error. Prefer that recorded result
	// over Bubble Tea's own ErrProgramKilled wrapper when it is available.
	// In practice it rarely is: Bubble Tea's event loop watches the same
	// runContext directly and returns as soon as it sees it cancelled,
	// which reliably beats our own operation goroutine posting its
	// completion message, so result.metadata is almost always still the
	// zero value on a cancelled run. That is not a defect a caller can fix
	// from here - every repository the operation finished before the
	// cancellation was already checkpointed to the history database via
	// OnResult, which is the durable source of truth for what a resume will
	// skip, independent of what this function returns.
	result, ok := final.(Model)
	if !ok {
		if runErr != nil {
			return mirror.Metadata{}, runErr
		}
		return mirror.Metadata{}, errors.New("tui: program ended without a result")
	}
	if result.err != nil {
		return result.metadata, result.err
	}
	if runErr != nil {
		return result.metadata, runContext.Err()
	}
	return result.metadata, nil
}
