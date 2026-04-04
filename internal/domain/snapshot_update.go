package domain

import "time"

// SnapshotUpdate announces that a newer observability snapshot is available.
type SnapshotUpdate struct {
	Sequence    uint64    `json:"sequence"`
	GeneratedAt time.Time `json:"generated_at"`
}
