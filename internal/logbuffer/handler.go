package logbuffer

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/pmenglund/colin/internal/domain"
)

type handler struct {
	next         slog.Handler
	buffer       *Buffer
	prefixFields []string
	groups       []string
}

// NewHandler wraps a slog handler and mirrors structured records into the in-memory buffer.
func NewHandler(next slog.Handler, buffer *Buffer) slog.Handler {
	if next == nil {
		next = slog.NewJSONHandler(io.Discard, nil)
	}
	return &handler{
		next:   next,
		buffer: buffer,
	}
}

func (h *handler) Enabled(ctx context.Context, level slog.Level) bool {
	return level >= slog.LevelDebug || h.next.Enabled(ctx, level)
}

func (h *handler) Handle(ctx context.Context, record slog.Record) error {
	if h.buffer != nil && record.Level >= slog.LevelDebug {
		fields := append([]string(nil), h.prefixFields...)
		record.Attrs(func(attr slog.Attr) bool {
			fields = appendFormattedAttr(fields, h.groups, attr)
			return true
		})
		h.buffer.append(storedEntry{
			timestamp: domain.BufferedLogEntry{
				Timestamp: record.Time.UTC(),
				Level:     record.Level.String(),
				Message:   record.Message,
				Fields:    fields,
			},
			level: record.Level,
		})
	}
	if !h.next.Enabled(ctx, record.Level) {
		return nil
	}
	return h.next.Handle(ctx, record.Clone())
}

func (h *handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	prefix := append([]string(nil), h.prefixFields...)
	for _, attr := range attrs {
		prefix = appendFormattedAttr(prefix, h.groups, attr)
	}
	return &handler{
		next:         h.next.WithAttrs(attrs),
		buffer:       h.buffer,
		prefixFields: prefix,
		groups:       append([]string(nil), h.groups...),
	}
}

func (h *handler) WithGroup(name string) slog.Handler {
	if strings.TrimSpace(name) == "" {
		return h
	}
	groups := append([]string(nil), h.groups...)
	groups = append(groups, name)
	return &handler{
		next:         h.next.WithGroup(name),
		buffer:       h.buffer,
		prefixFields: append([]string(nil), h.prefixFields...),
		groups:       groups,
	}
}

func appendFormattedAttr(fields []string, groups []string, attr slog.Attr) []string {
	attr.Value = attr.Value.Resolve()
	switch attr.Value.Kind() {
	case slog.KindGroup:
		nextGroups := append([]string(nil), groups...)
		if strings.TrimSpace(attr.Key) != "" {
			nextGroups = append(nextGroups, attr.Key)
		}
		for _, item := range attr.Value.Group() {
			fields = appendFormattedAttr(fields, nextGroups, item)
		}
		return fields
	default:
		key := strings.TrimSpace(attr.Key)
		if key == "" {
			return fields
		}
		if len(groups) > 0 {
			key = strings.Join(append(append([]string(nil), groups...), key), ".")
		}
		return append(fields, fmt.Sprintf("%s=%s", key, attrValueString(attr.Value)))
	}
}

func attrValueString(value slog.Value) string {
	switch value.Kind() {
	case slog.KindString:
		return value.String()
	case slog.KindTime:
		return value.Time().UTC().Format(time.RFC3339Nano)
	case slog.KindDuration:
		return value.Duration().String()
	case slog.KindInt64:
		return fmt.Sprintf("%d", value.Int64())
	case slog.KindUint64:
		return fmt.Sprintf("%d", value.Uint64())
	case slog.KindFloat64:
		return fmt.Sprintf("%g", value.Float64())
	case slog.KindBool:
		if value.Bool() {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprint(value.Any())
	}
}
