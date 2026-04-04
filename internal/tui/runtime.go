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

type logLevelFilter int

const (
	logLevelFilterDebug logLevelFilter = iota
	logLevelFilterInfo
	logLevelFilterWarn
	logLevelFilterError
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
	ctx                  context.Context
	source               Source
	serviceErr           <-chan error
	requestShutdownDrain func() bool
	forceStop            func()

	mode              mode
	width             int
	height            int
	dashboardURL      string
	setupURL          string
	snapshot          domain.Snapshot
	logs              domain.BufferedLogSnapshot
	setup             domain.FunnelSetupStatus
	logOffset         int
	selectedLog       int
	logFilter         logLevelFilter
	viewedAlerts      logAlertState
	lastRefresh       time.Time
	refreshErr        error
	fatalErr          error
	shutdownRequested bool
	forceStopIssued   bool
}

type logAlertState struct {
	count     int
	timestamp time.Time
}

func Run(ctx context.Context, in io.Reader, out io.Writer, source Source, serviceErrs <-chan error, requestShutdownDrain func() bool, forceStop func()) error {
	if source == nil {
		return fmt.Errorf("runtime tui source is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	program := tea.NewProgram(
		newModel(ctx, source, serviceErrs, requestShutdownDrain, forceStop),
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

func newModel(ctx context.Context, source Source, serviceErrs <-chan error, requestShutdownDrain func() bool, forceStop func()) model {
	if ctx == nil {
		ctx = context.Background()
	}
	return model{
		ctx:                  ctx,
		source:               source,
		serviceErr:           serviceErrs,
		requestShutdownDrain: requestShutdownDrain,
		forceStop:            forceStop,
		mode:                 modeOverview,
		width:                defaultWidth,
		height:               defaultHeight,
		selectedLog:          -1,
		logFilter:            logLevelFilterDebug,
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
		return m, refreshRuntime(m.ctx, m.source)
	case refreshMsg:
		followLatest := m.mode == modeLogs && m.isFollowingLatestLog()
		m.dashboardURL = msg.dashboardURL
		m.setupURL = msg.setupURL
		m.snapshot = msg.snapshot
		m.logs = msg.logs
		m.setup = msg.setup
		if m.mode == modeLogs && msg.err == nil {
			m.markVisibleLogAlertsViewed()
		}
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
		if m.shutdownRequested && !m.forceStopIssued && len(m.snapshot.Running) == 0 {
			m.forceStopIssued = true
			if m.forceStop != nil {
				m.forceStop()
			}
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
		if !m.forceStopIssued {
			m.forceStopIssued = true
			if m.forceStop != nil {
				m.forceStop()
			}
		}
		return m, nil
	case "q":
		if !m.shutdownRequested {
			m.shutdownRequested = true
			if len(m.snapshot.Running) == 0 {
				m.forceStopIssued = true
				if m.forceStop != nil {
					m.forceStop()
				}
				return m, nil
			}
			if m.requestShutdownDrain != nil {
				m.requestShutdownDrain()
			}
			return m, nil
		}
		if !m.forceStopIssued {
			m.forceStopIssued = true
			if m.forceStop != nil {
				m.forceStop()
			}
		}
		return m, tea.Quit
	case "l":
		if m.mode == modeOverview {
			m.mode = modeLogs
			m.markVisibleLogAlertsViewed()
			if m.selectedLog < 0 || m.selectedLog >= m.filteredLogCount() {
				m.selectLastLog()
			} else {
				m.ensureSelectedLogVisible()
			}
		} else {
			m.mode = modeOverview
		}
		return m, nil
	case "f":
		if m.mode != modeLogs {
			return m, nil
		}
		followLatest := m.isFollowingLatestLog()
		m.logFilter = m.logFilter.next()
		if followLatest {
			m.selectLastLog()
		} else {
			m.clampSelectedLog()
			m.ensureSelectedLogVisible()
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
	count := m.filteredLogCount()
	if count == 0 {
		m.selectedLog = -1
		m.logOffset = 0
		return
	}
	m.selectedLog = count - 1
	m.ensureSelectedLogVisible()
}

func (m model) isLogPinnedToBottom() bool {
	return m.logOffset >= maxInt(m.filteredLogCount()-m.visibleLogLines(), 0)
}

func (m model) isFollowingLatestLog() bool {
	count := m.filteredLogCount()
	if count == 0 {
		return true
	}
	return m.selectedLog >= count-1 && m.isLogPinnedToBottom()
}

func (m *model) clampSelectedLog() {
	count := m.filteredLogCount()
	if count == 0 {
		m.selectedLog = -1
		m.logOffset = 0
		return
	}
	m.selectedLog = clampInt(m.selectedLog, 0, count-1)
}

func (m *model) ensureSelectedLogVisible() {
	m.clampSelectedLog()
	maxOffset := maxInt(m.filteredLogCount()-m.visibleLogLines(), 0)
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

func (m model) currentLogAlerts() logAlertState {
	return currentLogAlertsFor(m.logs.Entries)
}

func (m model) currentVisibleLogAlerts() logAlertState {
	return currentLogAlertsFor(m.filteredLogEntries())
}

func currentLogAlertsFor(entries []domain.BufferedLogEntry) logAlertState {
	var state logAlertState
	for _, entry := range entries {
		if !isLogAlertLevel(entry.Level) {
			continue
		}
		state.count++
		state.timestamp = entry.Timestamp
	}
	return state
}

func (m *model) markVisibleLogAlertsViewed() {
	m.viewedAlerts = m.currentVisibleLogAlerts()
}

func (m model) hasUnseenLogAlerts() bool {
	current := m.currentLogAlerts()
	if current.count == 0 {
		return false
	}
	if current.count > m.viewedAlerts.count {
		return true
	}
	return current.timestamp.After(m.viewedAlerts.timestamp)
}

func (m model) filteredLogEntries() []domain.BufferedLogEntry {
	if m.logFilter == logLevelFilterDebug {
		return m.logs.Entries
	}

	entries := make([]domain.BufferedLogEntry, 0, len(m.logs.Entries))
	for _, entry := range m.logs.Entries {
		if bufferedLogLevel(entry.Level) < m.logFilter.minLevel() {
			continue
		}
		entries = append(entries, entry)
	}
	return entries
}

func (m model) filteredLogCount() int {
	return len(m.filteredLogEntries())
}

func (f logLevelFilter) next() logLevelFilter {
	switch f {
	case logLevelFilterInfo:
		return logLevelFilterWarn
	case logLevelFilterWarn:
		return logLevelFilterError
	case logLevelFilterError:
		return logLevelFilterDebug
	default:
		return logLevelFilterInfo
	}
}

func (f logLevelFilter) minLevel() slog.Level {
	switch f {
	case logLevelFilterInfo:
		return slog.LevelInfo
	case logLevelFilterWarn:
		return slog.LevelWarn
	case logLevelFilterError:
		return slog.LevelError
	default:
		return slog.LevelDebug
	}
}

func (f logLevelFilter) label() string {
	switch f {
	case logLevelFilterInfo:
		return "info+"
	case logLevelFilterWarn:
		return "warn+"
	case logLevelFilterError:
		return "error"
	default:
		return "debug+"
	}
}

func isLogAlertLevel(level string) bool {
	switch strings.ToUpper(strings.TrimSpace(level)) {
	case "WARN", "ERROR":
		return true
	default:
		return false
	}
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

func bufferedLogLevel(raw string) slog.Level {
	var level slog.Level
	if err := level.UnmarshalText([]byte(strings.TrimSpace(raw))); err == nil {
		return level
	}
	return slog.LevelInfo
}
