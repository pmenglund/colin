package logging

import (
	"io"
	"log/slog"

	charm "github.com/charmbracelet/log"
	"github.com/muesli/termenv"
)

const (
	// LevelInfo emits informational, warning, and error logs.
	LevelInfo = slog.LevelInfo
)

// NewSlog creates a slog logger backed by charmbracelet/log.
func NewSlog(w io.Writer, level slog.Level) *slog.Logger {
	handler := charm.NewWithOptions(w, charm.Options{
		Level: charm.Level(level),
	})
	handler.SetColorProfile(termenv.TrueColor)
	return slog.New(handler)
}
