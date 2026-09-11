package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

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
	frame        int
	startedAt    time.Time
	elapsed      time.Duration
	waiting      string
	waitingUntil time.Time
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

type activityTickMsg struct {
	at time.Time
}

func NewModel(organization string, dryRun bool) Model {
	return Model{organization: organization, dryRun: dryRun}
}

func (m Model) Init() tea.Cmd {
	if m.operation == nil {
		return nil
	}
	return tea.Batch(m.waitForProgress(), m.runOperation(), m.activityTick())
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
		if m.startedAt.IsZero() {
			m.startedAt = time.Now()
		}
		switch message.Kind {
		case mirror.ProgressDiscoveryStarted:
			m.waiting = ""
			m.discovering = true
		case mirror.ProgressDiscoveryCompleted:
			m.waiting = ""
			m.discovering = false
			m.total = message.Total
		case mirror.ProgressRepositoryStarted:
			m.waiting = ""
			m.current = message.Repository.NameWithOwner
			m.completed = message.Completed
			m.total = message.Total
		case mirror.ProgressRepositoryCompleted:
			m.waiting = ""
			m.current = message.Repository.NameWithOwner
			m.completed = message.Completed
			m.total = message.Total
			m.recent = append(m.recent, message.Result)
		case mirror.ProgressWaiting:
			m.waiting = message.Message
			m.waitingUntil = message.Until
		}
		return m, m.waitForProgress()
	case activityTickMsg:
		if m.startedAt.IsZero() {
			m.startedAt = message.at
		}
		m.frame++
		m.elapsed = message.at.Sub(m.startedAt).Round(time.Second)
		if m.elapsed < 0 {
			m.elapsed = 0
		}
		return m, m.activityTick()
	case operationDoneMsg:
		m.metadata = message.metadata
		m.err = message.err
		m.current = ""
		return m, tea.Quit
	}
	return m, nil
}

func (m Model) activityTick() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(at time.Time) tea.Msg {
		return activityTickMsg{at: at}
	})
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
	spinner := []string{"|", "/", "-", "\\"}[m.frame%4]
	if m.discovering || m.total == 0 {
		fmt.Fprintf(&content, "Discovering repositories... %s\n", spinner)
	} else {
		fmt.Fprintf(&content, "%s  %d / %d\n", progressBar(m.completed, m.total, 32), m.completed, m.total)
		fmt.Fprintf(&content, "Remaining: %d\n", max(m.total-m.completed, 0))
		if m.current != "" {
			fmt.Fprintf(&content, "Current:   %s\n", m.current)
		}
		fmt.Fprintf(&content, "Working:   %s   Elapsed: %s\n", spinner, formatElapsed(m.elapsed))
	}

	if m.waiting != "" {
		if m.waitingUntil.IsZero() {
			fmt.Fprintf(&content, "Waiting:   %s\n", m.waiting)
		} else {
			fmt.Fprintf(&content, "Waiting:   %s (until %s)\n", m.waiting, m.waitingUntil.UTC().Format("15:04:05Z"))
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

func formatElapsed(elapsed time.Duration) string {
	if elapsed < 0 {
		elapsed = 0
	}
	return elapsed.Round(time.Second).String()
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
