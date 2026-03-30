package domain

import "time"

const DefaultLogBufferLines = 1000

// BufferedLogEntry is one structured log record retained in Colin's in-memory log buffer.
type BufferedLogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
	Fields    []string  `json:"fields,omitempty"`
}

// BufferedLogSnapshot is the JSON payload returned by the buffered log endpoint.
type BufferedLogSnapshot struct {
	Capacity int                `json:"capacity"`
	Count    int                `json:"count"`
	Entries  []BufferedLogEntry `json:"entries"`
}
