package codex

import (
	"encoding/json"
	"errors"
	"testing"

	sdkprotocol "github.com/pmenglund/codex-sdk-go/protocol"
	sdkrpc "github.com/pmenglund/codex-sdk-go/rpc"
)

func TestNotificationRuntimeError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		note sdkrpc.Notification
		want error
	}{
		{
			name: "retryable error does not fail",
			note: sdkrpc.Notification{
				Method: "error",
				Params: sdkprotocol.ErrorNotification{
					WillRetry: boolPtr(true),
					Error:     &sdkprotocol.TurnNotificationError{Message: "transient"},
				},
			},
			want: nil,
		},
		{
			name: "tool input maps to input required",
			note: sdkrpc.Notification{
				Method: "error",
				Params: sdkprotocol.ErrorNotification{
					Error: &sdkprotocol.TurnNotificationError{Message: "tool user input requires a custom handler"},
				},
			},
			want: ErrTurnInputNeeded,
		},
		{
			name: "failed turn returns concrete error",
			note: sdkrpc.Notification{
				Method: "turn/completed",
				Params: sdkprotocol.TurnNotification{
					Turn: &sdkprotocol.TurnNotificationTurn{
						ID:     "turn-1",
						Status: "failed",
						Error:  &sdkprotocol.TurnNotificationError{Message: "boom"},
					},
				},
			},
			want: errors.New("boom"),
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := notificationRuntimeError(test.note)
			switch {
			case test.want == nil && err != nil:
				t.Fatalf("expected nil error, got %v", err)
			case test.want != nil && err == nil:
				t.Fatalf("expected error %v, got nil", test.want)
			case test.want == nil && err == nil:
				return
			case errors.Is(test.want, ErrTurnInputNeeded):
				if !errors.Is(err, ErrTurnInputNeeded) {
					t.Fatalf("expected ErrTurnInputNeeded, got %v", err)
				}
			case err.Error() != test.want.Error():
				t.Fatalf("expected error %q, got %q", test.want.Error(), err.Error())
			}
		})
	}
}

func TestMapRuntimeError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want error
	}{
		{name: "passthrough nil", err: nil, want: nil},
		{name: "input required", err: errors.New("tool user input requires a custom handler"), want: ErrTurnInputNeeded},
		{name: "port exit eof", err: errors.New("EOF"), want: ErrPortExit},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := mapRuntimeError(test.err)
			switch {
			case test.want == nil && err != nil:
				t.Fatalf("expected nil error, got %v", err)
			case test.want == nil:
				return
			case !errors.Is(err, test.want):
				t.Fatalf("expected %v, got %v", test.want, err)
			}
		})
	}
}

func TestNotificationMessageUsesTopLevelParams(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(map[string]any{
		"method": "item/completed",
		"params": map[string]any{
			"threadId": "thread-1",
			"item": map[string]any{
				"text": "Investigating worker output",
			},
		},
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	msg := notificationMessage(sdkrpc.Notification{
		Method: "item/completed",
		Raw:    raw,
	})
	if got := summarizeMessage(msg); got != "Investigating worker output" {
		t.Fatalf("summarizeMessage() = %q, want %q", got, "Investigating worker output")
	}
}

func boolPtr(value bool) *bool {
	return &value
}
