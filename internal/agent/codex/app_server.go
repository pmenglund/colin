package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	sdk "github.com/pmenglund/codex-sdk-go"
	sdkprotocol "github.com/pmenglund/codex-sdk-go/protocol"
	sdkrpc "github.com/pmenglund/codex-sdk-go/rpc"

	"github.com/pmenglund/colin/internal/domain"
)

type appServerClient struct {
	cfg                           domain.ServiceConfig
	logger                        *slog.Logger
	onEvent                       func(Event)
	issue                         domain.Issue
	workspace                     string
	runType                       string
	client                        *sdk.Codex
	thread                        *sdk.Thread
	threadID                      string
	sessionID                     string
	pid                           *int
	lastSummary                   string
	lastTurnText                  string
	lastTurnTextFromCompletedItem bool
	stopOnce                      sync.Once
}

func (c *appServerClient) start(ctx context.Context, cwd string) error {
	if cwd == "" {
		return ErrInvalidWorkspace
	}

	transport, err := newShellTransport(ctx, cwd, c.cfg.Codex.Command, c.logger)
	if err != nil {
		return c.startupError(err, nil)
	}
	pid := transport.PID()

	client, err := sdk.New(ctx, sdk.Options{
		Transport: transport,
		Logger:    c.logger,
		ClientInfo: sdkprotocol.ClientInfo{
			Name:    "colin",
			Title:   stringPtr("Colin"),
			Version: "0.1.0",
		},
		ApprovalHandler: approvalHandler{
			logger: c.logger,
			emit:   c.emit,
			pid:    pid,
		},
	})
	if err != nil {
		return c.startupError(err, pid)
	}

	thread, err := client.StartThread(ctx, sdk.ThreadStartOptions{
		Cwd:            cwd,
		ApprovalPolicy: c.cfg.Codex.ApprovalPolicy,
		SandboxPolicy:  c.cfg.Codex.ThreadSandbox,
	})
	if err != nil {
		_ = client.Close()
		return c.startupError(err, pid)
	}

	c.client = client
	c.thread = thread
	c.threadID = thread.ID()
	c.pid = pid
	return nil
}

func (c *appServerClient) runTurn(parent context.Context, cwd string, issue domain.Issue, prompt string) error {
	if c.thread == nil {
		return fmt.Errorf("%w: thread not started", ErrPortExit)
	}
	c.resetTurnState()

	ctx, cancel := context.WithTimeout(parent, c.cfg.Codex.TurnTimeout)
	defer cancel()

	stream, err := c.thread.RunStreamed(ctx, []sdk.Input{sdk.TextInput(prompt)}, &sdk.TurnOptions{
		Cwd:            cwd,
		ApprovalPolicy: c.cfg.Codex.ApprovalPolicy,
		SandboxPolicy:  c.cfg.Codex.TurnSandboxPolicy,
	})
	if err != nil {
		return mapRuntimeError(err)
	}
	defer stream.Close()

	var turnText []string
	appendTurnText := func(summary string) {
		summary = strings.TrimSpace(summary)
		if summary == "" {
			return
		}
		if len(turnText) > 0 && turnText[len(turnText)-1] == summary {
			return
		}
		turnText = append(turnText, summary)
	}
	finishTurn := func() {
		c.lastTurnText = strings.TrimSpace(strings.Join(turnText, "\n\n"))
	}

	for {
		note, err := stream.Next(ctx)
		if err != nil {
			finishTurn()
			return mapRuntimeError(err)
		}

		threadID, turnID := notificationIDs(note)
		if threadID != "" {
			c.threadID = threadID
		}
		if note.Method == "turn/started" {
			c.sessionID = buildSessionID(c.threadID, turnID)
			c.emit(Event{
				Event:     EventSessionStarted,
				Timestamp: time.Now().UTC(),
				SessionID: c.sessionID,
				ThreadID:  c.threadID,
				TurnID:    turnID,
				PID:       c.pid,
			})
		}

		msg := notificationMessage(note)
		eventName := translateEvent(note.Method)
		summary := summarizeMessage(msg)
		if shouldCaptureSummary(eventName, summary) {
			c.lastSummary = summary
		}
		if output, ok := completedItemText(msg); ok {
			appendTurnText(output)
			c.lastTurnTextFromCompletedItem = true
		}
		c.emit(Event{
			Event:      eventName,
			Timestamp:  time.Now().UTC(),
			SessionID:  c.sessionID,
			ThreadID:   c.threadID,
			TurnID:     turnID,
			PID:        c.pid,
			Message:    summary,
			Usage:      extractUsage(msg),
			RateLimits: extractRateLimits(msg),
			Raw:        msg,
		})

		switch note.Method {
		case "turn/completed":
			finishTurn()
			if turnErr := notificationRuntimeError(note); turnErr != nil {
				return turnErr
			}
			return nil
		case "turn/failed":
			finishTurn()
			if turnErr := notificationRuntimeError(note); turnErr != nil {
				return turnErr
			}
			return ErrTurnFailed
		case "turn/cancelled":
			finishTurn()
			return ErrTurnCancelled
		case "error":
			if turnErr := notificationRuntimeError(note); turnErr != nil {
				finishTurn()
				return turnErr
			}
		}
	}
}

func (c *appServerClient) finalSummary() string {
	return strings.TrimSpace(c.lastSummary)
}

func (c *appServerClient) clearFinalSummary() {
	c.lastSummary = ""
}

func (c *appServerClient) lastOutput() string {
	return strings.TrimSpace(c.lastTurnText)
}

func (c *appServerClient) lastOutputCapturedFromCompletedItem() bool {
	return c.lastTurnTextFromCompletedItem
}

func (c *appServerClient) resetTurnState() {
	c.lastSummary = ""
	c.lastTurnText = ""
	c.lastTurnTextFromCompletedItem = false
}

func shouldCaptureSummary(eventName, summary string) bool {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return false
	}
	switch eventName {
	case EventOtherMessage, EventNotification:
		return isExplicitOutcomeSummary(summary)
	default:
		return false
	}
}

func isExplicitOutcomeSummary(summary string) bool {
	firstLine := strings.TrimSpace(strings.Split(strings.TrimSpace(summary), "\n")[0])
	return firstLine == outcomeReadyForReview || firstLine == outcomeNeedsSpec || firstLine == outcomeReadyForMergeRetry
}

func (c *appServerClient) stop() {
	c.stopOnce.Do(func() {
		if c.client != nil {
			if err := c.client.Close(); err != nil {
				c.logger.Debug("failed to close codex client during shutdown", "error", err)
			}
		}
	})
}

func (c *appServerClient) startupError(err error, pid *int) error {
	mapped := mapRuntimeError(err)
	c.emit(Event{
		Event:     EventStartupFailed,
		Timestamp: time.Now().UTC(),
		PID:       pid,
		Message:   mapped.Error(),
	})
	return mapped
}

func (c *appServerClient) emit(event Event) {
	if c.onEvent != nil {
		event.IssueID = c.issue.ID
		event.Identifier = c.issue.Identifier
		event.Workspace = c.workspace
		event.RunType = c.runType
		if event.State == "" {
			event.State = c.issue.State
		}
		c.onEvent(event)
	}
}

type approvalHandler struct {
	logger *slog.Logger
	emit   func(Event)
	pid    *int
}

func (h approvalHandler) ApplyPatchApproval(ctx context.Context, params sdkprotocol.ApplyPatchApprovalParams) (*sdkprotocol.ApplyPatchApprovalResponse, error) {
	h.emitApproval()
	return sdk.AutoApproveHandler{Logger: h.logger}.ApplyPatchApproval(ctx, params)
}

func (h approvalHandler) ExecCommandApproval(ctx context.Context, params sdkprotocol.ExecCommandApprovalParams) (*sdkprotocol.ExecCommandApprovalResponse, error) {
	h.emitApproval()
	return sdk.AutoApproveHandler{Logger: h.logger}.ExecCommandApproval(ctx, params)
}

func (h approvalHandler) ItemCommandExecutionRequestApproval(ctx context.Context, params sdkprotocol.CommandExecutionRequestApprovalParams) (*sdkprotocol.CommandExecutionRequestApprovalResponse, error) {
	h.emitApproval()
	return sdk.AutoApproveHandler{Logger: h.logger}.ItemCommandExecutionRequestApproval(ctx, params)
}

func (h approvalHandler) ItemFileChangeRequestApproval(ctx context.Context, params sdkprotocol.FileChangeRequestApprovalParams) (*sdkprotocol.FileChangeRequestApprovalResponse, error) {
	h.emitApproval()
	return sdk.AutoApproveHandler{Logger: h.logger}.ItemFileChangeRequestApproval(ctx, params)
}

func (h approvalHandler) ItemToolRequestUserInput(context.Context, sdkprotocol.ToolRequestUserInputParams) (*sdkprotocol.ToolRequestUserInputResponse, error) {
	if h.emit != nil {
		h.emit(Event{
			Event:     EventTurnInputRequired,
			Timestamp: time.Now().UTC(),
			PID:       h.pid,
		})
	}
	return nil, ErrTurnInputNeeded
}

func (h approvalHandler) emitApproval() {
	if h.emit != nil {
		h.emit(Event{
			Event:     EventApprovalAutoApproved,
			Timestamp: time.Now().UTC(),
			PID:       h.pid,
		})
	}
}

func buildSessionID(threadID, turnID string) string {
	if threadID == "" || turnID == "" {
		return ""
	}
	return threadID + "-" + turnID
}

func notificationIDs(note sdkrpc.Notification) (threadID string, turnID string) {
	switch payload := note.Params.(type) {
	case sdkprotocol.TurnNotification:
		if payload.Turn != nil {
			turnID = payload.Turn.ID
		}
		return payload.ThreadID, turnID
	case *sdkprotocol.TurnNotification:
		if payload != nil {
			if payload.Turn != nil {
				turnID = payload.Turn.ID
			}
			return payload.ThreadID, turnID
		}
	case sdkprotocol.ItemCompletedNotification:
		return payload.ThreadID, ""
	case *sdkprotocol.ItemCompletedNotification:
		if payload != nil {
			return payload.ThreadID, ""
		}
	case sdkprotocol.ErrorNotification:
		return payload.ThreadID, ""
	case *sdkprotocol.ErrorNotification:
		if payload != nil {
			return payload.ThreadID, ""
		}
	}

	msg := notificationMessage(note)
	threadID, _ = nestedString(msg, "params", "threadId")
	turnID, _ = nestedString(msg, "params", "turn", "id")
	return threadID, turnID
}

func notificationMessage(note sdkrpc.Notification) map[string]any {
	msg := map[string]any{
		"method": note.Method,
	}
	if len(note.Raw) > 0 {
		raw := map[string]any{}
		if err := json.Unmarshal(note.Raw, &raw); err != nil {
			msg["raw"] = string(note.Raw)
			return msg
		}
		if params, ok := raw["params"].(map[string]any); ok {
			msg["params"] = params
		}
		for key, value := range raw {
			if key == "params" {
				continue
			}
			msg[key] = value
		}
		return msg
	}
	msg["params"] = map[string]any{}
	return msg
}

func notificationRuntimeError(note sdkrpc.Notification) error {
	switch payload := note.Params.(type) {
	case sdkprotocol.ErrorNotification:
		return runtimeErrorFromErrorPayload(payload)
	case *sdkprotocol.ErrorNotification:
		if payload != nil {
			return runtimeErrorFromErrorPayload(*payload)
		}
	case sdkprotocol.TurnNotification:
		return runtimeErrorFromTurnPayload(payload)
	case *sdkprotocol.TurnNotification:
		if payload != nil {
			return runtimeErrorFromTurnPayload(*payload)
		}
	}

	msg := notificationMessage(note)
	if requiresUserInput(msg) {
		return ErrTurnInputNeeded
	}
	if willRetry, ok := nestedBool(msg, "params", "willRetry"); ok && willRetry {
		return nil
	}
	if message, ok := nestedString(msg, "params", "turn", "error", "message"); ok && message != "" {
		return errors.New(message)
	}
	if message, ok := nestedString(msg, "params", "error", "message"); ok && message != "" {
		return errors.New(message)
	}
	if status, ok := nestedString(msg, "params", "turn", "status"); ok && status == "failed" {
		return ErrTurnFailed
	}
	return nil
}

func runtimeErrorFromErrorPayload(payload sdkprotocol.ErrorNotification) error {
	if payload.WillRetry != nil && *payload.WillRetry {
		return nil
	}
	if payload.Error != nil && payload.Error.Message != "" {
		if looksLikeInputRequired(payload.Error.Message) {
			return ErrTurnInputNeeded
		}
		return errors.New(payload.Error.Message)
	}
	return nil
}

func runtimeErrorFromTurnPayload(payload sdkprotocol.TurnNotification) error {
	if payload.Turn == nil {
		return nil
	}
	if payload.Turn.Error != nil && payload.Turn.Error.Message != "" {
		if looksLikeInputRequired(payload.Turn.Error.Message) {
			return ErrTurnInputNeeded
		}
		return errors.New(payload.Turn.Error.Message)
	}
	if payload.Turn.Status == "failed" {
		return ErrTurnFailed
	}
	return nil
}

func mapRuntimeError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrTurnInputNeeded):
		return ErrTurnInputNeeded
	case errors.Is(err, context.DeadlineExceeded):
		return ErrTurnTimeout
	case errors.Is(err, context.Canceled):
		return ErrTurnCancelled
	}

	if looksLikeInputRequired(err.Error()) {
		return ErrTurnInputNeeded
	}
	if looksLikePortExit(err) {
		return fmt.Errorf("%w: %v", ErrPortExit, err)
	}
	return err
}

func looksLikeInputRequired(message string) bool {
	lower := strings.ToLower(message)
	return strings.Contains(lower, ErrTurnInputNeeded.Error()) || strings.Contains(lower, "tool user input")
}

func looksLikePortExit(err error) bool {
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "broken pipe") ||
		strings.Contains(lower, "connection reset") ||
		strings.Contains(lower, "eof") ||
		strings.Contains(lower, "client closed")
}

func nestedBool(root map[string]any, keys ...string) (bool, bool) {
	current := any(root)
	for _, key := range keys {
		asMap, ok := current.(map[string]any)
		if !ok {
			return false, false
		}
		current, ok = asMap[key]
		if !ok {
			return false, false
		}
	}
	value, ok := current.(bool)
	return value, ok
}

func stringPtr(value string) *string {
	return &value
}
