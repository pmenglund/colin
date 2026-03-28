package codex

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/pmenglund/codex-sdk-go/rpc"
)

// shellTransport runs the configured Codex command inside the issue workspace.
type shellTransport struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	logger *slog.Logger
	mu     sync.Mutex
}

func newShellTransport(ctx context.Context, cwd string, command string, logger *slog.Logger) (*shellTransport, error) {
	cmd := exec.CommandContext(ctx, "bash", "-lc", command)
	cmd.Dir = cwd

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, ErrCodexNotFound
		}
		return nil, err
	}

	transport := &shellTransport{
		cmd:    cmd,
		stdin:  stdin,
		stdout: bufio.NewReader(stdout),
		logger: logger,
	}
	go transport.readStderr(stderr)
	return transport, nil
}

func (t *shellTransport) PID() *int {
	if t == nil || t.cmd == nil || t.cmd.Process == nil {
		return nil
	}
	pid := t.cmd.Process.Pid
	return &pid
}

func (t *shellTransport) ReadLine() (string, error) {
	line, err := t.stdout.ReadString('\n')
	if err != nil {
		if errors.Is(err, io.EOF) && line != "" {
			return strings.TrimRight(line, "\n"), nil
		}
		return "", err
	}
	return strings.TrimRight(line, "\n"), nil
}

func (t *shellTransport) WriteLine(line string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !strings.HasSuffix(line, "\n") {
		line += "\n"
	}
	_, err := io.WriteString(t.stdin, line)
	return err
}

func (t *shellTransport) Close() error {
	if t == nil {
		return nil
	}
	if t.stdin != nil {
		if err := t.stdin.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			t.logger.Debug("failed to close codex stdin during shutdown", "error", err)
		}
	}
	if t.cmd != nil && t.cmd.Process != nil {
		if err := t.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.logger.Debug("failed to kill codex process during shutdown", "error", err)
		}
	}
	if t.cmd != nil {
		if err := t.cmd.Wait(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.logger.Debug("failed waiting for codex process shutdown", "error", err)
		}
	}
	return nil
}

func (t *shellTransport) readStderr(stderr io.Reader) {
	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		t.logger.Warn("codex stderr", "pid", t.PID(), "message", text)
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, os.ErrClosed) {
		t.logger.Debug("failed reading codex stderr", "error", err)
	}
}

var _ rpc.Transport = (*shellTransport)(nil)
