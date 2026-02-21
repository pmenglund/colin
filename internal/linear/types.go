package linear

import (
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
	Description string
	StateName   string
	UpdatedAt   time.Time
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
	out := copyMetadataMap(metadata)
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

func metadataMapsEqual(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, leftValue := range left {
		if rightValue, ok := right[key]; !ok || rightValue != leftValue {
			return false
		}
	}
	return true
}

func copyMetadataMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
