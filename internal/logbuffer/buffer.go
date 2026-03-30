package logbuffer

import (
	"log/slog"
	"sync"

	"github.com/pmenglund/colin/internal/domain"
)

type storedEntry struct {
	timestamp domain.BufferedLogEntry
	level     slog.Level
}

// Buffer retains the newest structured log entries in memory.
type Buffer struct {
	mu       sync.RWMutex
	entries  []storedEntry
	start    int
	size     int
	capacity int
}

// New constructs an in-memory log buffer with the supplied capacity.
func New(capacity int) *Buffer {
	if capacity <= 0 {
		capacity = domain.DefaultLogBufferLines
	}
	return &Buffer{
		entries:  make([]storedEntry, capacity),
		capacity: capacity,
	}
}

func (b *Buffer) append(entry storedEntry) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.capacity <= 0 {
		return
	}
	if b.size < b.capacity {
		index := (b.start + b.size) % b.capacity
		b.entries[index] = cloneEntry(entry)
		b.size++
		return
	}
	b.entries[b.start] = cloneEntry(entry)
	b.start = (b.start + 1) % b.capacity
}

// Snapshot returns the current buffer contents from oldest to newest.
func (b *Buffer) Snapshot(minLevel *slog.Level) domain.BufferedLogSnapshot {
	b.mu.RLock()
	defer b.mu.RUnlock()

	entries := make([]domain.BufferedLogEntry, 0, b.size)
	for i := 0; i < b.size; i++ {
		index := (b.start + i) % b.capacity
		entry := b.entries[index]
		if minLevel != nil && entry.level < *minLevel {
			continue
		}
		entries = append(entries, cloneEntry(entry).timestamp)
	}

	return domain.BufferedLogSnapshot{
		Capacity: b.capacity,
		Count:    len(entries),
		Entries:  entries,
	}
}

// Resize changes the number of retained log entries while preserving the newest entries.
func (b *Buffer) Resize(capacity int) {
	if capacity <= 0 {
		capacity = domain.DefaultLogBufferLines
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if capacity == b.capacity {
		return
	}

	keep := b.size
	if keep > capacity {
		keep = capacity
	}

	next := make([]storedEntry, capacity)
	start := 0
	if b.size > keep {
		start = b.size - keep
	}
	for i := 0; i < keep; i++ {
		oldIndex := (b.start + start + i) % b.capacity
		next[i] = cloneEntry(b.entries[oldIndex])
	}

	b.entries = next
	b.start = 0
	b.size = keep
	b.capacity = capacity
}

func cloneEntry(entry storedEntry) storedEntry {
	entry.timestamp.Fields = append([]string(nil), entry.timestamp.Fields...)
	return entry
}
