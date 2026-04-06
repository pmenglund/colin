package codex

import (
	"encoding/json"
	"errors"
	"testing"

	sdkprotocol "github.com/pmenglund/codex-sdk-go/protocol"
	sdkrpc "github.com/pmenglund/codex-sdk-go/rpc"
	"github.com/pmenglund/colin/internal/domain"
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

func TestNotificationMessageFallsBackToTypedParams(t *testing.T) {
	t.Parallel()

	item, err := json.Marshal(map[string]any{
		"text": domain.OutcomeReadyForReviewLine + "\n\nAlready implemented.",
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	msg := notificationMessage(sdkrpc.Notification{
		Method: "item/completed",
		Params: sdkprotocol.ItemCompletedNotification{
			ThreadID: "thread-1",
			Item:     item,
		},
	})

	if got := summarizeMessage(msg); got != domain.OutcomeReadyForReviewLine+"\n\nAlready implemented." {
		t.Fatalf("summarizeMessage() = %q, want completed item text from typed params", got)
	}
}

func TestCompletedItemTextUsesCompletedItemOnly(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(map[string]any{
		"method": "item/completed",
		"params": map[string]any{
			"threadId": "thread-1",
			"text":     "Decide whether the Linear issue below should be handled as a one-shot change or should first get an ExecPlan.",
			"item": map[string]any{
				"text": domain.ExecPlanDecisionOneShotLine + "\n\nSafe to implement directly.",
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
	got, ok := completedItemText(msg)
	if !ok {
		t.Fatal("completedItemText() = not found, want completed item text")
	}
	want := domain.ExecPlanDecisionOneShotLine + "\n\nSafe to implement directly."
	if got != want {
		t.Fatalf("completedItemText() = %q, want %q", got, want)
	}
	if got := summarizeMessage(msg); got != want {
		t.Fatalf("summarizeMessage() = %q, want completed item text %q", got, want)
	}
}

func TestCompletedItemTextSupportsWrappedItemText(t *testing.T) {
	t.Parallel()

	msg := map[string]any{
		"method": "item/completed",
		"params": map[string]any{
			"item": map[string]any{
				"assistant": map[string]any{
					"text": "# Fake ExecPlan\n\nPlan details.",
				},
			},
		},
	}

	got, ok := completedItemText(msg)
	if !ok {
		t.Fatal("completedItemText() = not found, want wrapped item text")
	}
	if got != "# Fake ExecPlan\n\nPlan details." {
		t.Fatalf("completedItemText() = %q, want wrapped exec plan body", got)
	}
}

func TestIsExplicitOutcomeSummary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		summary string
		want    bool
	}{
		{name: "ready for review", summary: domain.OutcomeReadyForReviewLine + "\n\nImplemented the requested change.", want: true},
		{name: "needs spec", summary: domain.OutcomeNeedsSpecLine + "\n\nThe spec should be improved before implementation.", want: true},
		{name: "generic prose", summary: "Implemented the requested change.", want: false},
		{name: "exec plan", summary: "# Fake ExecPlan\n\nPlan details.", want: false},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := isExplicitOutcomeSummary(test.summary); got != test.want {
				t.Fatalf("isExplicitOutcomeSummary() = %t, want %t", got, test.want)
			}
		})
	}
}

func boolPtr(value bool) *bool {
	return &value
}
