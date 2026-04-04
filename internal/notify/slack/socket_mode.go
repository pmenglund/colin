package slack

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pmenglund/colin/internal/domain"
	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"
)

type socketModeClient interface {
	RunContext(context.Context) error
	Ack(socketmode.Request, ...interface{})
	EventsChannel() <-chan socketmode.Event
}

type socketModeClientAdapter struct {
	client *socketmode.Client
}

// SocketModeStatusObserver receives connection-state updates from the Slack Socket Mode loop.
type SocketModeStatusObserver func(domain.SlackSocketModeStatus)

func (a *socketModeClientAdapter) RunContext(ctx context.Context) error {
	return a.client.RunContext(ctx)
}

func (a *socketModeClientAdapter) Ack(req socketmode.Request, payload ...interface{}) {
	a.client.Ack(req, payload...)
}

func (a *socketModeClientAdapter) EventsChannel() <-chan socketmode.Event {
	return a.client.Events
}

// SocketMode keeps Slack interactive link buttons acknowledged over Socket Mode.
type SocketMode struct {
	client   socketModeClient
	logger   *slog.Logger
	observer SocketModeStatusObserver
	mu       sync.Mutex
	status   domain.SlackSocketModeStatus
	host     string
}

// NewSocketMode constructs the Socket Mode runtime for Slack button interactions.
func NewSocketMode(appToken string, botToken string, logger *slog.Logger, observer SocketModeStatusObserver) *SocketMode {
	api := slackapi.New(
		strings.TrimSpace(botToken),
		slackapi.OptionAppLevelToken(strings.TrimSpace(appToken)),
	)
	return newSocketModeWithClient(&socketModeClientAdapter{client: socketmode.New(api)}, logger, observer)
}

func newSocketModeWithClient(client socketModeClient, logger *slog.Logger, observer SocketModeStatusObserver) *SocketMode {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &SocketMode{
		client:   client,
		logger:   logger,
		observer: observer,
	}
}

// Run starts the Slack Socket Mode loop until the context is canceled.
func (s *SocketMode) Run(ctx context.Context) error {
	if s == nil || s.client == nil {
		return nil
	}
	s.updateStatus(func(status *domain.SlackSocketModeStatus) {
		*status = domain.SlackSocketModeStatus{
			Enabled:     true,
			Connected:   false,
			State:       "connecting",
			LastEvent:   "starting",
			LastEventAt: time.Now().UTC(),
		}
		s.host = ""
	})

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.client.RunContext(ctx)
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-errCh:
			if err == nil || errors.Is(err, context.Canceled) {
				return nil
			}
			s.updateStatus(func(status *domain.SlackSocketModeStatus) {
				status.Enabled = true
				status.Connected = false
				status.State = "error"
				status.LastEvent = "run_error"
				status.LastEventAt = time.Now().UTC()
				status.LastError = err.Error()
			})
			return err
		case evt, ok := <-s.client.EventsChannel():
			if !ok {
				if ctx.Err() != nil {
					return nil
				}
				s.updateStatus(func(status *domain.SlackSocketModeStatus) {
					status.Enabled = true
					status.Connected = false
					status.State = "error"
					status.LastEvent = "event_channel_closed"
					status.LastEventAt = time.Now().UTC()
					status.LastError = "slack socket mode event channel closed"
				})
				return errors.New("slack socket mode event channel closed")
			}
			s.handleEvent(evt)
		}
	}
}

func (s *SocketMode) handleEvent(evt socketmode.Event) {
	now := time.Now().UTC()
	s.updateStatus(func(status *domain.SlackSocketModeStatus) {
		status.Enabled = true
		status.LastEvent = string(evt.Type)
		status.LastEventAt = now
		s.touchCurrentSocketLocked(now)
	})

	switch evt.Type {
	case socketmode.EventTypeConnected:
		s.updateStatus(func(status *domain.SlackSocketModeStatus) {
			status.Connected = true
			status.State = "connected"
			status.LastError = ""
		})
		s.logger.Info("slack socket mode connected")
	case socketmode.EventTypeHello:
		s.handleHelloEvent(evt, now)
	case socketmode.EventTypeConnectionError, socketmode.EventTypeInvalidAuth, socketmode.EventTypeErrorBadMessage, socketmode.EventTypeErrorWriteFailed:
		s.updateStatus(func(status *domain.SlackSocketModeStatus) {
			status.Connected = false
			status.State = "error"
			status.LastError = string(evt.Type)
		})
		s.logger.Warn("slack socket mode event", "type", evt.Type)
	case socketmode.EventTypeDisconnect:
		s.handleDisconnectEvent(evt, now)
	case socketmode.EventTypeInteractive:
		s.handleInteractiveEvent(evt)
	}
}

func (s *SocketMode) reportStatus(status domain.SlackSocketModeStatus) {
	if s == nil || s.observer == nil {
		return
	}
	s.observer(status)
}

func (s *SocketMode) updateStatus(update func(*domain.SlackSocketModeStatus)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	update(&s.status)
	status := cloneSlackSocketModeStatus(s.status)
	s.mu.Unlock()
	s.reportStatus(status)
}

func (s *SocketMode) handleHelloEvent(evt socketmode.Event, now time.Time) {
	if evt.Request == nil {
		return
	}
	host := strings.TrimSpace(evt.Request.DebugInfo.Host)
	s.updateStatus(func(status *domain.SlackSocketModeStatus) {
		status.Connected = true
		status.State = "connected"
		status.LastError = ""
		if host == "" {
			s.touchCurrentSocketLocked(now)
			return
		}
		s.host = host
		s.upsertSocketLocked(host, now)
	})
}

func (s *SocketMode) handleDisconnectEvent(evt socketmode.Event, now time.Time) {
	host := ""
	if evt.Request != nil {
		host = strings.TrimSpace(evt.Request.DebugInfo.Host)
	}
	s.updateStatus(func(status *domain.SlackSocketModeStatus) {
		status.Connected = false
		status.State = "connecting"
		if host != "" {
			s.upsertSocketLocked(host, now)
			if s.host == host {
				s.host = ""
			}
		}
		s.touchCurrentSocketLocked(now)
		s.clearCurrentSocketLocked()
	})
}

func (s *SocketMode) upsertSocketLocked(host string, now time.Time) {
	if strings.TrimSpace(host) == "" {
		return
	}
	for i := range s.status.Sockets {
		s.status.Sockets[i].Current = false
		if s.status.Sockets[i].Host == host {
			s.status.Sockets[i].Current = true
			s.status.Sockets[i].LastMessageAt = now
			return
		}
	}
	s.status.Sockets = append(s.status.Sockets, domain.SlackWebSocketStatus{
		Host:          host,
		Current:       true,
		LastMessageAt: now,
	})
	sort.Slice(s.status.Sockets, func(i, j int) bool {
		return s.status.Sockets[i].Host < s.status.Sockets[j].Host
	})
}

func (s *SocketMode) touchCurrentSocketLocked(now time.Time) {
	if strings.TrimSpace(s.host) == "" {
		return
	}
	for i := range s.status.Sockets {
		if s.status.Sockets[i].Host == s.host {
			s.status.Sockets[i].LastMessageAt = now
			return
		}
	}
}

func (s *SocketMode) clearCurrentSocketLocked() {
	for i := range s.status.Sockets {
		s.status.Sockets[i].Current = false
	}
}

func cloneSlackSocketModeStatus(input domain.SlackSocketModeStatus) domain.SlackSocketModeStatus {
	out := input
	out.Sockets = append([]domain.SlackWebSocketStatus(nil), input.Sockets...)
	return out
}

func (s *SocketMode) handleInteractiveEvent(evt socketmode.Event) {
	if evt.Request == nil {
		s.logger.Warn("slack interactive event missing request envelope")
		return
	}

	s.client.Ack(*evt.Request)

	callback, ok := evt.Data.(slackapi.InteractionCallback)
	if !ok {
		s.logger.Warn("slack interactive event had unexpected payload type")
		return
	}
	if callback.Type != slackapi.InteractionTypeBlockActions {
		return
	}

	for _, action := range callback.ActionCallback.BlockActions {
		if action == nil {
			continue
		}
		actionID := strings.TrimSpace(action.ActionID)
		if !isKnownLinkActionID(actionID) {
			s.logger.Warn("ignoring unknown slack button action", "action_id", actionID)
			continue
		}
		s.logger.Info(
			"acknowledged slack button click",
			"action_id", actionID,
			"user_id", strings.TrimSpace(callback.User.ID),
			"channel_id", strings.TrimSpace(callback.Channel.ID),
		)
	}
}
