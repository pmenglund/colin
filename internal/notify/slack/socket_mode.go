package slack

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"

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
	client socketModeClient
	logger *slog.Logger
}

// NewSocketMode constructs the Socket Mode runtime for Slack button interactions.
func NewSocketMode(appToken string, botToken string, logger *slog.Logger) *SocketMode {
	api := slackapi.New(
		strings.TrimSpace(botToken),
		slackapi.OptionAppLevelToken(strings.TrimSpace(appToken)),
	)
	return newSocketModeWithClient(&socketModeClientAdapter{client: socketmode.New(api)}, logger)
}

func newSocketModeWithClient(client socketModeClient, logger *slog.Logger) *SocketMode {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &SocketMode{
		client: client,
		logger: logger,
	}
}

// Run starts the Slack Socket Mode loop until the context is canceled.
func (s *SocketMode) Run(ctx context.Context) error {
	if s == nil || s.client == nil {
		return nil
	}

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
			return err
		case evt, ok := <-s.client.EventsChannel():
			if !ok {
				if ctx.Err() != nil {
					return nil
				}
				return errors.New("slack socket mode event channel closed")
			}
			s.handleEvent(evt)
		}
	}
}

func (s *SocketMode) handleEvent(evt socketmode.Event) {
	switch evt.Type {
	case socketmode.EventTypeConnected:
		s.logger.Info("slack socket mode connected")
	case socketmode.EventTypeConnectionError, socketmode.EventTypeInvalidAuth, socketmode.EventTypeErrorBadMessage, socketmode.EventTypeErrorWriteFailed:
		s.logger.Warn("slack socket mode event", "type", evt.Type)
	case socketmode.EventTypeInteractive:
		s.handleInteractiveEvent(evt)
	}
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
