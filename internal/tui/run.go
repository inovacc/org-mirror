package tui

import (
	"context"

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
	final, err := tea.NewProgram(model, options...).Run()
	if err != nil {
		return mirror.Metadata{}, err
	}
	result := final.(Model)
	return result.metadata, result.err
}
