package tui

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/pmenglund/colin/internal/domain"
)

const (
	defaultWidth     = 100
	defaultHeight    = 24
	refreshInterval  = 500 * time.Millisecond
	refreshTimeout   = 250 * time.Millisecond
	minLogViewHeight = 8
	logHeaderHeight  = 6
	logFooterHeight  = 2
	lastMessageWidth = 48
)

type Source interface {
	DashboardURL() string
	FunnelSetupURL() string
	Snapshot(context.Context) (domain.Snapshot, error)
	BufferedLogs(context.Context, *slog.Level) (domain.BufferedLogSnapshot, error)
	FunnelSetupStatus(context.Context) domain.FunnelSetupStatus
}

type mode int

const (
	modeOverview mode = iota
	modeLogs
)

type refreshMsg struct {
	dashboardURL string
	setupURL     string
	snapshot     domain.Snapshot
	logs         domain.BufferedLogSnapshot
	setup        domain.FunnelSetupStatus
	err          error
}

type refreshTickMsg struct{}

type serviceDoneMsg struct {
	err error
}

type model struct {
	ctx        context.Context
	source     Source
	serviceErr <-chan error
	stop       func()

	mode         mode
	width        int
	height       int
	dashboardURL string
	setupURL     string
	snapshot     domain.Snapshot
	logs         domain.BufferedLogSnapshot
	setup        domain.FunnelSetupStatus
	logOffset    int
	lastRefresh  time.Time
	refreshErr   error
	fatalErr     error
	quitting     bool
}

func Run(ctx context.Context, in io.Reader, out io.Writer, source Source, serviceErrs <-chan error, stop func()) error {
	if source == nil {
		return fmt.Errorf("runtime tui source is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	program := tea.NewProgram(
		newModel(ctx, source, serviceErrs, stop),
		tea.WithInput(in),
		tea.WithOutput(out),
	)
	finalModel, err := program.Run()
	if err != nil {
		return err
	}
	final, ok := finalModel.(model)
	if !ok {
		return fmt.Errorf("unexpected runtime tui model %T", finalModel)
	}
	return final.fatalErr
}

func newModel(ctx context.Context, source Source, serviceErrs <-chan error, stop func()) model {
	if ctx == nil {
		ctx = context.Background()
	}
	return model{
		ctx:        ctx,
		source:     source,
		serviceErr: serviceErrs,
		stop:       stop,
		mode:       modeOverview,
		width:      defaultWidth,
		height:     defaultHeight,
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(
		refreshRuntime(m.ctx, m.source),
		nextRefreshTick(),
		waitForService(m.serviceErr),
	)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width > 0 {
			m.width = msg.Width
		}
		if msg.Height > 0 {
			m.height = msg.Height
		}
		if m.mode == modeLogs {
			m.clampLogOffset()
		}
		return m, nil
	case tea.KeyPressMsg:
		return m.updateKey(msg)
	case refreshTickMsg:
		if m.quitting {
			return m, nil
		}
		return m, refreshRuntime(m.ctx, m.source)
	case refreshMsg:
		atBottom := m.mode == modeLogs && m.isLogPinnedToBottom()
		m.dashboardURL = msg.dashboardURL
		m.setupURL = msg.setupURL
		m.snapshot = msg.snapshot
		m.logs = msg.logs
		m.setup = msg.setup
		m.refreshErr = msg.err
		m.lastRefresh = time.Now().UTC()
		if m.mode == modeLogs {
			if atBottom {
				m.pinLogsToBottom()
			} else {
				m.clampLogOffset()
			}
		}
		if m.quitting {
			return m, nil
		}
		return m, nextRefreshTick()
	case serviceDoneMsg:
		m.fatalErr = msg.err
		return m, tea.Quit
	default:
		return m, nil
	}
}

func (m model) updateKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := strings.ToLower(msg.String())
	switch key {
	case "ctrl+c", "esc":
		if !m.quitting {
			m.quitting = true
			if m.stop != nil {
				m.stop()
			}
		}
		return m, nil
	case "l":
		if m.mode == modeOverview {
			m.mode = modeLogs
			m.pinLogsToBottom()
		} else {
			m.mode = modeOverview
		}
		return m, nil
	}

	if m.mode != modeLogs {
		return m, nil
	}

	switch msg.Code {
	case tea.KeyUp:
		m.logOffset--
	case tea.KeyDown:
		m.logOffset++
	case tea.KeyPgUp:
		m.logOffset -= m.visibleLogLines()
	case tea.KeyPgDown:
		m.logOffset += m.visibleLogLines()
	case tea.KeyHome:
		m.logOffset = 0
	case tea.KeyEnd:
		m.pinLogsToBottom()
		return m, nil
	default:
		return m, nil
	}
	m.clampLogOffset()
	return m, nil
}

func (m model) View() tea.View {
	var view tea.View
	if m.mode == modeLogs {
		view = tea.NewView(renderLogsView(m))
	} else {
		view = tea.NewView(renderOverviewView(m))
	}
	view.AltScreen = true
	return view
}

func refreshRuntime(parent context.Context, source Source) tea.Cmd {
	return func() tea.Msg {
		if source == nil {
			return refreshMsg{err: fmt.Errorf("runtime tui source is required")}
		}
		if parent == nil {
			parent = context.Background()
		}
		ctx, cancel := context.WithTimeout(parent, refreshTimeout)
		defer cancel()

		dashboardURL := source.DashboardURL()
		setupURL := source.FunnelSetupURL()
		setup := source.FunnelSetupStatus(ctx)

		snapshot, err := source.Snapshot(ctx)
		if err != nil {
			return refreshMsg{
				dashboardURL: dashboardURL,
				setupURL:     setupURL,
				setup:        setup,
				err:          err,
			}
		}
		logs, err := source.BufferedLogs(ctx, nil)
		if err != nil {
			return refreshMsg{
				dashboardURL: dashboardURL,
				setupURL:     setupURL,
				snapshot:     snapshot,
				setup:        setup,
				err:          err,
			}
		}
		return refreshMsg{
			dashboardURL: dashboardURL,
			setupURL:     setupURL,
			snapshot:     snapshot,
			logs:         logs,
			setup:        setup,
		}
	}
}

func nextRefreshTick() tea.Cmd {
	return tea.Tick(refreshInterval, func(time.Time) tea.Msg {
		return refreshTickMsg{}
	})
}

func waitForService(serviceErrs <-chan error) tea.Cmd {
	if serviceErrs == nil {
		return nil
	}
	return func() tea.Msg {
		return serviceDoneMsg{err: <-serviceErrs}
	}
}

func (m model) visibleLogLines() int {
	height := m.height
	if height < minLogViewHeight {
		height = defaultHeight
	}
	lines := height - logHeaderHeight - logFooterHeight
	if lines < 1 {
		return 1
	}
	return lines
}

func (m *model) pinLogsToBottom() {
	maxOffset := maxInt(len(m.logs.Entries)-m.visibleLogLines(), 0)
	m.logOffset = maxOffset
}

func (m model) isLogPinnedToBottom() bool {
	return m.logOffset >= maxInt(len(m.logs.Entries)-m.visibleLogLines(), 0)
}

func (m *model) clampLogOffset() {
	maxOffset := maxInt(len(m.logs.Entries)-m.visibleLogLines(), 0)
	m.logOffset = clampInt(m.logOffset, 0, maxOffset)
}

func clampInt(value int, low int, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

func maxInt(a int, b int) int {
	if a > b {
		return a
	}
	return b
}
