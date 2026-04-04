package slack

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

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
	return newSocketModeWithClient(client, slog.New(slog.NewTextHandler(io.Discard, nil)))
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
