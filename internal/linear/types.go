package linear

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	metadataBlockPrefix = "<!-- colin:metadata "
	metadataBlockSuffix = " -->"
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

func parseMetadata(description string) (map[string]string, error) {
	match := metadataBlockRegexp.FindStringSubmatch(description)
	if len(match) != 2 {
		return map[string]string{}, nil
	}

	out := map[string]string{}
	if err := json.Unmarshal([]byte(match[1]), &out); err != nil {
		return nil, fmt.Errorf("parse metadata block: %w", err)
	}
	return out, nil
}

func upsertMetadata(description string, patch MetadataPatch) (string, map[string]string, error) {
	meta, err := parseMetadata(description)
	if err != nil {
		return "", nil, err
	}
	if meta == nil {
		meta = map[string]string{}
	}

	for k, v := range patch.Set {
		if strings.TrimSpace(k) == "" {
			continue
		}
		meta[k] = v
	}
	for _, k := range patch.Delete {
		delete(meta, k)
	}

	clean := metadataBlockRegexp.ReplaceAllString(description, "")
	clean = strings.TrimRight(clean, " \n\t")

	if len(meta) == 0 {
		if clean == "" {
			return "", map[string]string{}, nil
		}
		return clean, map[string]string{}, nil
	}

	block, err := renderMetadataBlock(meta)
	if err != nil {
		return "", nil, err
	}

	if clean == "" {
		return block, meta, nil
	}
	return clean + "\n\n" + block, meta, nil
}

func renderMetadataBlock(meta map[string]string) (string, error) {
	// Keep metadata rendering stable for deterministic behavior and clean diffs.
	keys := make([]string, 0, len(meta))
	for k := range meta {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	ordered := make(map[string]string, len(meta))
	for _, k := range keys {
		ordered[k] = meta[k]
	}

	b, err := json.Marshal(ordered)
	if err != nil {
		return "", fmt.Errorf("marshal metadata: %w", err)
	}
	return metadataBlockPrefix + string(b) + metadataBlockSuffix, nil
}
