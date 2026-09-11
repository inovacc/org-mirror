package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/inovacc/org-mirror/internal/mirror"
)

type Model struct {
	organization string
	dryRun       bool
	discovering  bool
	current      string
	completed    int
	total        int
	recent       []mirror.Result
	events       chan tea.Msg
	operation    MirrorFunc
	metadata     mirror.Metadata
	err          error
	cancel       context.CancelFunc
	context      context.Context
}

type MirrorFunc func(context.Context, mirror.ProgressFunc) (mirror.Metadata, error)

type operationDoneMsg struct {
	metadata mirror.Metadata
	err      error
}

func NewModel(organization string, dryRun bool) Model {
	return Model{organization: organization, dryRun: dryRun}
}

func (m Model) Init() tea.Cmd {
	if m.operation == nil {
		return nil
	}
	return tea.Batch(m.waitForProgress(), m.runOperation())
}

func (m Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.KeyPressMsg:
		if message.String() == "q" || message.String() == "ctrl+c" {
			if m.cancel != nil {
				m.cancel()
			}
			return m, tea.Quit
		}
	case mirror.ProgressEvent:
		switch message.Kind {
		case mirror.ProgressDiscoveryStarted:
			m.discovering = true
		case mirror.ProgressDiscoveryCompleted:
			m.discovering = false
			m.total = message.Total
		case mirror.ProgressRepositoryStarted:
			m.current = message.Repository.NameWithOwner
			m.completed = message.Completed
			m.total = message.Total
		case mirror.ProgressRepositoryCompleted:
			m.current = message.Repository.NameWithOwner
			m.completed = message.Completed
			m.total = message.Total
			m.recent = append(m.recent, message.Result)
		}
		return m, m.waitForProgress()
	case operationDoneMsg:
		m.metadata = message.metadata
		m.err = message.err
		m.current = ""
		return m, tea.Quit
	}
	return m, nil
}

func (m Model) waitForProgress() tea.Cmd {
	return func() tea.Msg {
		select {
		case message := <-m.events:
			return message
		case <-m.context.Done():
			return operationDoneMsg{err: m.context.Err()}
		}
	}
}

func (m Model) runOperation() tea.Cmd {
	return func() tea.Msg {
		metadata, err := m.operation(m.context, func(event mirror.ProgressEvent) {
			select {
			case m.events <- event:
			case <-m.context.Done():
			}
		})
		return operationDoneMsg{metadata: metadata, err: err}
	}
}

func (m Model) View() tea.View {
	mode := "sync"
	if m.dryRun {
		mode = "dry run"
	}

	var content strings.Builder
	fmt.Fprintf(&content, "org-mirror  %s  (%s)\n\n", m.organization, mode)
	if m.discovering || m.total == 0 {
		content.WriteString("Discovering repositories...\n")
	} else {
		fmt.Fprintf(&content, "%s  %d / %d\n", progressBar(m.completed, m.total, 32), m.completed, m.total)
		fmt.Fprintf(&content, "Remaining: %d\n", max(m.total-m.completed, 0))
		if m.current != "" {
			fmt.Fprintf(&content, "Current:   %s\n", m.current)
		}
	}

	if len(m.recent) > 0 {
		content.WriteString("\nCompleted:\n")
		start := max(len(m.recent)-10, 0)
		for _, result := range m.recent[start:] {
			fmt.Fprintf(&content, "  %s  %-10s %s\n", outcomeSymbol(result.Outcome), result.Outcome, result.Repository.NameWithOwner)
		}
	}
	content.WriteString("\nq: cancel  ctrl+c: cancel\n")

	view := tea.NewView(content.String())
	view.AltScreen = true
	view.WindowTitle = "org-mirror"
	return view
}

func progressBar(completed, total, width int) string {
	filled := 0
	if total > 0 {
		filled = completed * width / total
	}
	filled = min(max(filled, 0), width)
	return "[" + strings.Repeat("=", filled) + strings.Repeat("-", width-filled) + "]"
}

func outcomeSymbol(outcome mirror.Outcome) string {
	switch outcome {
	case mirror.OutcomeCloned, mirror.OutcomeUpdated:
		return "✓"
	case mirror.OutcomeConflict:
		return "!"
	case mirror.OutcomeError:
		return "✗"
	default:
		return "·"
	}
}
