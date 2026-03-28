package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/pmenglund/colin/internal/domain"
)

type appServerClient struct {
	cfg       domain.ServiceConfig
	workflow  domain.WorkflowDefinition
	logger    *slog.Logger
	onEvent   func(Event)
	issue     domain.Issue
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	lines     chan protocolLine
	nextID    int
	threadID  string
	sessionID string
	pid       *int
	stopOnce  sync.Once
}

func (c *appServerClient) start(ctx context.Context, cwd string) error {
	if cwd == "" {
		return ErrInvalidWorkspace
	}
	cmd := exec.CommandContext(ctx, "bash", "-lc", c.cfg.Codex.Command)
	cmd.Dir = cwd
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return ErrCodexNotFound
		}
		return err
	}
	pid := cmd.Process.Pid
	c.pid = &pid
	c.cmd = cmd
	c.stdin = stdin
	c.lines = make(chan protocolLine, 128)
	c.nextID = 1
	go c.readStdout(stdout)
	go c.readStderr(stderr)

	if _, err := c.sendRequest(ctx, "initialize", map[string]any{
		"clientInfo": map[string]any{
			"name":    "colin",
			"version": "0.1.0",
		},
		"capabilities": map[string]any{},
	}); err != nil {
		c.emit(Event{Event: EventStartupFailed, Timestamp: time.Now().UTC(), PID: c.pid, Message: err.Error()})
		return err
	}
	if err := c.sendNotification("initialized", map[string]any{}); err != nil {
		return err
	}
	threadResp, err := c.sendRequest(ctx, "thread/start", map[string]any{
		"approvalPolicy": c.cfg.Codex.ApprovalPolicy,
		"sandbox":        c.cfg.Codex.ThreadSandbox,
		"cwd":            cwd,
	})
	if err != nil {
		c.emit(Event{Event: EventStartupFailed, Timestamp: time.Now().UTC(), PID: c.pid, Message: err.Error()})
		return err
	}
	threadID, ok := nestedString(threadResp, "result", "thread", "id")
	if !ok || threadID == "" {
		return fmt.Errorf("%w: missing thread id", ErrPortExit)
	}
	c.threadID = threadID
	return nil
}

func (c *appServerClient) runTurn(parent context.Context, cwd string, issue domain.Issue, prompt string) error {
	ctx, cancel := context.WithTimeout(parent, c.cfg.Codex.TurnTimeout)
	defer cancel()
	turnResp, err := c.sendRequest(ctx, "turn/start", map[string]any{
		"threadId": c.threadID,
		"input": []map[string]any{
			{"type": "text", "text": prompt},
		},
		"cwd":            cwd,
		"title":          fmt.Sprintf("%s: %s", issue.Identifier, issue.Title),
		"approvalPolicy": c.cfg.Codex.ApprovalPolicy,
		"sandboxPolicy":  c.cfg.Codex.TurnSandboxPolicy,
	})
	if err != nil {
		return err
	}
	turnID, ok := nestedString(turnResp, "result", "turn", "id")
	if !ok || turnID == "" {
		return fmt.Errorf("%w: missing turn id", ErrPortExit)
	}
	c.sessionID = c.threadID + "-" + turnID
	c.emit(Event{
		Event:     EventSessionStarted,
		Timestamp: time.Now().UTC(),
		SessionID: c.sessionID,
		ThreadID:  c.threadID,
		TurnID:    turnID,
		PID:       c.pid,
	})

	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return ErrTurnTimeout
			}
			return ctx.Err()
		case line, ok := <-c.lines:
			if !ok {
				return ErrPortExit
			}
			if line.err != nil {
				return line.err
			}
			if isResponse(line.msg) {
				continue
			}
			if id, ok := line.msg["id"]; ok && line.msg["method"] != nil {
				if err := c.handleServerRequest(id, line.msg); err != nil {
					return err
				}
				continue
			}

			method, _ := line.msg["method"].(string)
			c.emit(Event{
				Event:      translateEvent(method),
				Timestamp:  time.Now().UTC(),
				SessionID:  c.sessionID,
				ThreadID:   c.threadID,
				TurnID:     turnID,
				PID:        c.pid,
				Message:    summarizeMessage(line.msg),
				Usage:      extractUsage(line.msg),
				RateLimits: extractRateLimits(line.msg),
				Raw:        line.msg,
			})

			switch method {
			case "turn/completed":
				return nil
			case "turn/failed":
				return ErrTurnFailed
			case "turn/cancelled":
				return ErrTurnCancelled
			default:
				if requiresUserInput(line.msg) {
					return ErrTurnInputNeeded
				}
			}
		}
	}
}

func (c *appServerClient) sendRequest(parent context.Context, method string, params map[string]any) (map[string]any, error) {
	id := c.nextID
	c.nextID++
	if err := c.sendJSON(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(parent, c.cfg.Codex.ReadTimeout)
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return nil, ErrResponseTimeout
		case line, ok := <-c.lines:
			if !ok {
				return nil, ErrPortExit
			}
			if line.err != nil {
				return nil, line.err
			}
			if responseID, ok := line.msg["id"]; ok && sameID(responseID, id) {
				if errPayload, exists := line.msg["error"]; exists && errPayload != nil {
					return nil, fmt.Errorf("%w: %v", ErrPortExit, errPayload)
				}
				return line.msg, nil
			}
			if requestID, ok := line.msg["id"]; ok && line.msg["method"] != nil {
				if err := c.handleServerRequest(requestID, line.msg); err != nil {
					return nil, err
				}
				continue
			}
			c.emit(Event{
				Event:      translateEvent(methodName(line.msg)),
				Timestamp:  time.Now().UTC(),
				SessionID:  c.sessionID,
				ThreadID:   c.threadID,
				PID:        c.pid,
				Message:    summarizeMessage(line.msg),
				Usage:      extractUsage(line.msg),
				RateLimits: extractRateLimits(line.msg),
				Raw:        line.msg,
			})
		}
	}
}

func (c *appServerClient) handleServerRequest(id any, msg map[string]any) error {
	method, _ := msg["method"].(string)
	switch {
	case strings.Contains(strings.ToLower(method), "approval"):
		c.emit(Event{Event: EventApprovalAutoApproved, Timestamp: time.Now().UTC(), SessionID: c.sessionID, ThreadID: c.threadID, PID: c.pid})
		return c.sendResponse(id, map[string]any{"approved": true})
	case strings.Contains(strings.ToLower(method), "tool/call"):
		c.emit(Event{Event: EventUnsupportedToolCall, Timestamp: time.Now().UTC(), SessionID: c.sessionID, ThreadID: c.threadID, PID: c.pid})
		return c.sendResponse(id, map[string]any{"success": false, "error": "unsupported_tool_call"})
	default:
		if requiresUserInput(msg) {
			c.emit(Event{Event: EventTurnInputRequired, Timestamp: time.Now().UTC(), SessionID: c.sessionID, ThreadID: c.threadID, PID: c.pid})
			return ErrTurnInputNeeded
		}
		return c.sendResponse(id, map[string]any{"success": false, "error": "unsupported_request"})
	}
}

func (c *appServerClient) sendNotification(method string, params map[string]any) error {
	return c.sendJSON(map[string]any{"method": method, "params": params})
}

func (c *appServerClient) sendResponse(id any, result map[string]any) error {
	return c.sendJSON(map[string]any{"id": id, "result": result})
}

func (c *appServerClient) sendJSON(payload map[string]any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	_, err = c.stdin.Write(data)
	return err
}

func (c *appServerClient) readStdout(stdout io.Reader) {
	reader := bufio.NewReader(stdout)
	for {
		line, err := reader.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			var msg map[string]any
			if parseErr := json.Unmarshal(bytes.TrimSpace(line), &msg); parseErr != nil {
				c.emit(Event{Event: EventMalformed, Timestamp: time.Now().UTC(), Message: string(bytes.TrimSpace(line)), PID: c.pid})
			} else {
				c.lines <- protocolLine{msg: msg}
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				c.lines <- protocolLine{err: err}
			}
			close(c.lines)
			return
		}
	}
}

func (c *appServerClient) readStderr(stderr io.Reader) {
	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		c.logger.Warn("codex stderr", "pid", c.pid, "message", text)
	}
}

func (c *appServerClient) stop() {
	c.stopOnce.Do(func() {
		if c.stdin != nil {
			if err := c.stdin.Close(); err != nil {
				c.logger.Debug("failed to close codex stdin during shutdown", "error", err)
			}
		}
		if c.cmd != nil && c.cmd.Process != nil {
			if err := c.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				c.logger.Debug("failed to kill codex process during shutdown", "error", err)
			}
			if _, err := c.cmd.Process.Wait(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				c.logger.Debug("failed waiting for codex process shutdown", "error", err)
			}
		}
	})
}

func (c *appServerClient) emit(event Event) {
	if c.onEvent != nil {
		event.IssueID = c.issue.ID
		event.Identifier = c.issue.Identifier
		c.onEvent(event)
	}
}
