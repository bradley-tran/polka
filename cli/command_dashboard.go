package cli

import (
	"io"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"polka/backend"
)

// dashboardPollInterval is how often the dashboard refreshes its snapshot.
const dashboardPollInterval = 2 * time.Second

func newDashboardCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:  "dashboard",
		Args: exactArgsError("dashboard does not take arguments", 0),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := ctx.store()
			if err != nil {
				return &statusError{code: 1, err: err}
			}

			return runDashboard(cmd.OutOrStdout(), cmd.ErrOrStderr(), store)
		},
	}
	configureCommand(cmd, dashboardUsage)

	return cmd
}

// runDashboard opens the live status view. Without a terminal on both stdin
// and stdout it degrades to the one-shot status output, so piping the command
// still produces useful text.
func runDashboard(stdout, stderr io.Writer, store backend.Store) error {
	if !isTTY(stdout) || !stdinIsTTY() {
		return runStatus(stdout, stderr, store)
	}

	program := tea.NewProgram(dashboardModel{store: store}, tea.WithAltScreen(), tea.WithOutput(stdout), tea.WithInput(os.Stdin))
	if _, err := program.Run(); err != nil {
		return &statusError{code: 1, err: err}
	}

	return nil
}

// dashboardSnapshotMsg delivers a freshly gathered snapshot to the model.
type dashboardSnapshotMsg dashboardSnapshot

// dashboardTickMsg fires when the poll interval elapses.
type dashboardTickMsg time.Time

// dashboardModel is the bubbletea model behind the dashboard: a snapshot of
// the environment's runtime state that is re-gathered on every tick.
type dashboardModel struct {
	store    backend.Store
	snapshot dashboardSnapshot
	loaded   bool
}

// snapshotCmd gathers a snapshot as a tea command, keeping the TCP liveness
// probes off the rendering goroutine.
func (m dashboardModel) snapshotCmd() tea.Cmd {
	store := m.store
	return func() tea.Msg {
		return dashboardSnapshotMsg(gatherDashboardSnapshot(store, time.Now()))
	}
}

// dashboardTickCmd schedules the next poll.
func dashboardTickCmd() tea.Cmd {
	return tea.Tick(dashboardPollInterval, func(t time.Time) tea.Msg {
		return dashboardTickMsg(t)
	})
}

func (m dashboardModel) Init() tea.Cmd {
	return tea.Batch(m.snapshotCmd(), dashboardTickCmd())
}

func (m dashboardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		}
	case dashboardTickMsg:
		return m, tea.Batch(m.snapshotCmd(), dashboardTickCmd())
	case dashboardSnapshotMsg:
		m.snapshot = dashboardSnapshot(msg)
		m.loaded = true
	}

	return m, nil
}

func (m dashboardModel) View() string {
	if !m.loaded {
		return "Gathering status...\n"
	}

	return renderDashboard(m.snapshot)
}
