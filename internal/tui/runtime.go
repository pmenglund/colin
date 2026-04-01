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
	logDetailMinRows = 4
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
	selectedLog  int
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
		ctx:         ctx,
		source:      source,
		serviceErr:  serviceErrs,
		stop:        stop,
		mode:        modeOverview,
		width:       defaultWidth,
		height:      defaultHeight,
		selectedLog: -1,
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
			m.ensureSelectedLogVisible()
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
		followLatest := m.mode == modeLogs && m.isFollowingLatestLog()
		m.dashboardURL = msg.dashboardURL
		m.setupURL = msg.setupURL
		m.snapshot = msg.snapshot
		m.logs = msg.logs
		m.setup = msg.setup
		m.refreshErr = msg.err
		m.lastRefresh = time.Now().UTC()
		if m.mode == modeLogs {
			if followLatest {
				m.selectLastLog()
			} else {
				m.clampSelectedLog()
				m.ensureSelectedLogVisible()
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
	case "ctrl+c", "esc", "q":
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
			if m.selectedLog < 0 || m.selectedLog >= len(m.logs.Entries) {
				m.selectLastLog()
			} else {
				m.ensureSelectedLogVisible()
			}
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
		m.selectedLog--
	case tea.KeyDown:
		m.selectedLog++
	case tea.KeyPgUp:
		m.selectedLog -= m.visibleLogLines()
	case tea.KeyPgDown:
		m.selectedLog += m.visibleLogLines()
	case tea.KeyHome:
		m.selectedLog = 0
	case tea.KeyEnd:
		m.selectLastLog()
		return m, nil
	default:
		return m, nil
	}
	m.clampSelectedLog()
	m.ensureSelectedLogVisible()
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
	lines := height - logHeaderHeight - logFooterHeight - m.logDetailRows()
	if lines < 1 {
		return 1
	}
	return lines
}

func (m model) logDetailRows() int {
	height := m.height
	if height < minLogViewHeight {
		height = defaultHeight
	}
	available := height - logHeaderHeight - logFooterHeight
	if available <= 1 {
		return 1
	}
	rows := available / 3
	if rows < logDetailMinRows {
		rows = logDetailMinRows
	}
	if rows >= available {
		rows = available - 1
	}
	if rows < 1 {
		return 1
	}
	return rows
}

func (m *model) selectLastLog() {
	if len(m.logs.Entries) == 0 {
		m.selectedLog = -1
		m.logOffset = 0
		return
	}
	m.selectedLog = len(m.logs.Entries) - 1
	m.ensureSelectedLogVisible()
}

func (m model) isLogPinnedToBottom() bool {
	return m.logOffset >= maxInt(len(m.logs.Entries)-m.visibleLogLines(), 0)
}

func (m model) isFollowingLatestLog() bool {
	if len(m.logs.Entries) == 0 {
		return true
	}
	return m.selectedLog >= len(m.logs.Entries)-1 && m.isLogPinnedToBottom()
}

func (m *model) clampSelectedLog() {
	if len(m.logs.Entries) == 0 {
		m.selectedLog = -1
		m.logOffset = 0
		return
	}
	m.selectedLog = clampInt(m.selectedLog, 0, len(m.logs.Entries)-1)
}

func (m *model) ensureSelectedLogVisible() {
	m.clampSelectedLog()
	maxOffset := maxInt(len(m.logs.Entries)-m.visibleLogLines(), 0)
	if m.selectedLog < 0 {
		m.logOffset = 0
		return
	}
	if m.selectedLog < m.logOffset {
		m.logOffset = m.selectedLog
	}
	if m.selectedLog >= m.logOffset+m.visibleLogLines() {
		m.logOffset = m.selectedLog - m.visibleLogLines() + 1
	}
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
