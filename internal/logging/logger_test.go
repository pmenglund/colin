package logging

import (
	"bytes"
	"io"
	"strings"
	"testing"

	charm "github.com/charmbracelet/log"
)

func TestNewSlogUsesCharmHandler(t *testing.T) {
	logger := NewSlog(io.Discard, LevelInfo)

	if _, ok := logger.Handler().(*charm.Logger); !ok {
		t.Fatalf("handler type = %T, want *log.Logger", logger.Handler())
	}
}

func TestNewSlogEnablesColorByDefault(t *testing.T) {
	var logOutput bytes.Buffer
	logger := NewSlog(&logOutput, LevelInfo)

	logger.Info("hello", "status", "ok")

	if got := logOutput.String(); !strings.Contains(got, "\x1b[") {
		t.Fatalf("expected ANSI color codes in output, got %q", got)
	}
}
