package slack

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/pmenglund/colin/internal/domain"
	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"
)

type fakeSocketModeClient struct {
	events    chan socketmode.Event
	runFunc   func(context.Context) error
	ackMu     sync.Mutex
	ackedEnvs []string
}

func (f *fakeSocketModeClient) RunContext(ctx context.Context) error {
	if f.runFunc != nil {
		return f.runFunc(ctx)
	}
	<-ctx.Done()
	return nil
}

func (f *fakeSocketModeClient) Ack(req socketmode.Request, _ ...interface{}) {
	f.ackMu.Lock()
	defer f.ackMu.Unlock()
	f.ackedEnvs = append(f.ackedEnvs, req.EnvelopeID)
}

func (f *fakeSocketModeClient) EventsChannel() <-chan socketmode.Event {
	return f.events
}

func (f *fakeSocketModeClient) acked() []string {
	f.ackMu.Lock()
	defer f.ackMu.Unlock()
	out := make([]string, len(f.ackedEnvs))
	copy(out, f.ackedEnvs)
	return out
}

func newTestSocketMode(client socketModeClient) *SocketMode {
	return newSocketModeWithClient(client, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
}

func TestSocketModeRunStopsOnContextCancel(t *testing.T) {
	t.Parallel()

	client := &fakeSocketModeClient{
		events: make(chan socketmode.Event),
		runFunc: func(ctx context.Context) error {
			<-ctx.Done()
			return nil
		},
	}
	runtime := newTestSocketMode(client)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- runtime.Run(ctx)
	}()

	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run() error = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not stop after context cancellation")
	}
}

func TestSocketModeAcksKnownLinkButtonActions(t *testing.T) {
	t.Parallel()

	client := &fakeSocketModeClient{events: make(chan socketmode.Event)}
	runtime := newTestSocketMode(client)

	runtime.handleEvent(socketmode.Event{
		Type: socketmode.EventTypeInteractive,
		Data: slackapi.InteractionCallback{
			Type: slackapi.InteractionTypeBlockActions,
			User: slackapi.User{ID: "U123"},
			Channel: slackapi.Channel{
				GroupConversation: slackapi.GroupConversation{
					Conversation: slackapi.Conversation{ID: "C123"},
				},
			},
			ActionCallback: slackapi.ActionCallbacks{
				BlockActions: []*slackapi.BlockAction{{
					ActionID: actionIDLinearIssue,
				}},
			},
		},
		Request: &socketmode.Request{EnvelopeID: "env-1"},
	})

	if got := client.acked(); len(got) != 1 || got[0] != "env-1" {
		t.Fatalf("acked envelopes = %v, want [env-1]", got)
	}
}

func TestSocketModeAcksUnknownLinkButtonActions(t *testing.T) {
	t.Parallel()

	client := &fakeSocketModeClient{events: make(chan socketmode.Event)}
	runtime := newTestSocketMode(client)

	runtime.handleEvent(socketmode.Event{
		Type: socketmode.EventTypeInteractive,
		Data: slackapi.InteractionCallback{
			Type: slackapi.InteractionTypeBlockActions,
			ActionCallback: slackapi.ActionCallbacks{
				BlockActions: []*slackapi.BlockAction{{
					ActionID: "unexpected_action",
				}},
			},
		},
		Request: &socketmode.Request{EnvelopeID: "env-2"},
	})

	if got := client.acked(); len(got) != 1 || got[0] != "env-2" {
		t.Fatalf("acked envelopes = %v, want [env-2]", got)
	}
}

func TestSocketModeAcksNonBlockInteractions(t *testing.T) {
	t.Parallel()

	client := &fakeSocketModeClient{events: make(chan socketmode.Event)}
	runtime := newTestSocketMode(client)

	runtime.handleEvent(socketmode.Event{
		Type:    socketmode.EventTypeInteractive,
		Data:    slackapi.InteractionCallback{Type: slackapi.InteractionTypeShortcut},
		Request: &socketmode.Request{EnvelopeID: "env-3"},
	})

	if got := client.acked(); len(got) != 1 || got[0] != "env-3" {
		t.Fatalf("acked envelopes = %v, want [env-3]", got)
	}
}

func TestSocketModeReportsConnectedStatus(t *testing.T) {
	t.Parallel()

	client := &fakeSocketModeClient{events: make(chan socketmode.Event)}
	var statuses []domain.SlackSocketModeStatus
	runtime := newSocketModeWithClient(
		client,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		func(status domain.SlackSocketModeStatus) {
			statuses = append(statuses, status)
		},
	)

	runtime.handleEvent(socketmode.Event{Type: socketmode.EventTypeConnected})

	if len(statuses) == 0 {
		t.Fatal("expected status update")
	}
	last := statuses[len(statuses)-1]
	if !last.Connected {
		t.Fatalf("Connected = false, want true")
	}
	if got := last.State; got != "connected" {
		t.Fatalf("State = %q, want connected", got)
	}
}

func TestSocketModeReportsErrorStatus(t *testing.T) {
	t.Parallel()

	client := &fakeSocketModeClient{events: make(chan socketmode.Event)}
	var statuses []domain.SlackSocketModeStatus
	runtime := newSocketModeWithClient(
		client,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		func(status domain.SlackSocketModeStatus) {
			statuses = append(statuses, status)
		},
	)

	runtime.handleEvent(socketmode.Event{Type: socketmode.EventTypeInvalidAuth})

	if len(statuses) == 0 {
		t.Fatal("expected status update")
	}
	last := statuses[len(statuses)-1]
	if last.Connected {
		t.Fatalf("Connected = true, want false")
	}
	if got := last.State; got != "error" {
		t.Fatalf("State = %q, want error", got)
	}
	if got := last.LastError; got != string(socketmode.EventTypeInvalidAuth) {
		t.Fatalf("LastError = %q, want %q", got, string(socketmode.EventTypeInvalidAuth))
	}
}

func TestSocketModeTracksHelloHostAndLastMessage(t *testing.T) {
	t.Parallel()

	client := &fakeSocketModeClient{events: make(chan socketmode.Event)}
	var statuses []domain.SlackSocketModeStatus
	runtime := newSocketModeWithClient(
		client,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		func(status domain.SlackSocketModeStatus) {
			statuses = append(statuses, status)
		},
	)

	runtime.handleEvent(socketmode.Event{
		Type: socketmode.EventTypeHello,
		Request: &socketmode.Request{
			Type: socketmode.RequestTypeHello,
			DebugInfo: socketmode.DebugInfo{
				Host: "applink-1",
			},
		},
	})

	if len(statuses) == 0 {
		t.Fatal("expected status update")
	}
	got := statuses[len(statuses)-1]
	if len(got.Sockets) != 1 {
		t.Fatalf("len(Sockets) = %d, want 1", len(got.Sockets))
	}
	if got.Sockets[0].Host != "applink-1" {
		t.Fatalf("Sockets[0].Host = %q, want applink-1", got.Sockets[0].Host)
	}
	if !got.Sockets[0].Current {
		t.Fatalf("Sockets[0].Current = false, want true")
	}
	if got.Sockets[0].LastMessageAt.IsZero() {
		t.Fatal("Sockets[0].LastMessageAt = zero, want timestamp")
	}
}

func TestSocketModeTouchesCurrentSocketOnInteractiveEvent(t *testing.T) {
	t.Parallel()

	client := &fakeSocketModeClient{events: make(chan socketmode.Event)}
	var statuses []domain.SlackSocketModeStatus
	runtime := newSocketModeWithClient(
		client,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		func(status domain.SlackSocketModeStatus) {
			statuses = append(statuses, status)
		},
	)

	runtime.handleEvent(socketmode.Event{
		Type: socketmode.EventTypeHello,
		Request: &socketmode.Request{
			Type: socketmode.RequestTypeHello,
			DebugInfo: socketmode.DebugInfo{
				Host: "applink-1",
			},
		},
	})
	first := statuses[len(statuses)-1].Sockets[0].LastMessageAt

	time.Sleep(10 * time.Millisecond)
	runtime.handleEvent(socketmode.Event{
		Type: socketmode.EventTypeInteractive,
		Data: slackapi.InteractionCallback{
			Type: slackapi.InteractionTypeShortcut,
		},
		Request: &socketmode.Request{EnvelopeID: "env-4"},
	})
	last := statuses[len(statuses)-1].Sockets[0].LastMessageAt

	if !last.After(first) {
		t.Fatalf("LastMessageAt = %v, want after %v", last, first)
	}
}
