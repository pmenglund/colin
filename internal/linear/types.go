package linear

import (
	"maps"
	"regexp"
	"strings"
	"time"
)

var metadataBlockRegexp = regexp.MustCompile(`<!-- colin:metadata (\{.*?\}) -->`)

// Issue represents the subset of Linear issue fields needed by the worker.
type Issue struct {
	ID          string
	Identifier  string
	Title       string
	ProjectID   string
	ProjectName string
	Description string
	StateName   string
	UpdatedAt   time.Time
	Blocked     bool
	Metadata    map[string]string
	BlockedBy   []string
}

// MetadataPatch describes updates to colin metadata persisted in Linear.
type MetadataPatch struct {
	Set    map[string]string
	Delete []string
}

// HasChanges reports whether the patch contains any update operation.
func (m MetadataPatch) HasChanges() bool {
	return len(m.Set) > 0 || len(m.Delete) > 0
}

// StripMetadataBlock removes the Colin metadata comment and trims surrounding whitespace.
func StripMetadataBlock(description string) string {
	return strings.TrimSpace(metadataBlockRegexp.ReplaceAllString(description, ""))
}

func applyMetadataPatch(metadata map[string]string, patch MetadataPatch) map[string]string {
	out := maps.Clone(metadata)
	if out == nil {
		out = map[string]string{}
	}
	for k, v := range patch.Set {
		trimmedKey := strings.TrimSpace(k)
		if trimmedKey == "" {
			continue
		}
		out[trimmedKey] = v
	}
	for _, k := range patch.Delete {
		trimmedKey := strings.TrimSpace(k)
		if trimmedKey == "" {
			continue
		}
		delete(out, trimmedKey)
	}
	return out
}
