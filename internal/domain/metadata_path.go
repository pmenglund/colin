package domain

import (
	"net/url"
	"strings"
)

const (
	colinMetadataPathPrefix          = "/linear/issues/"
	colinMetadataPathSuffix          = "/metadata"
	colinExecPlanPathSuffix          = "/exec-plan"
	colinAPIIssuesPathPrefix         = "/api/v1/issues/"
	colinCodexOutputPathSuffix       = "/codex-output"
	colinCodexOutputEventsPathSuffix = "/codex-output/events"
)

// ColinMetadataPath returns the Colin issue metadata page path for a Linear issue.
func ColinMetadataPath(issueID string) string {
	return colinMetadataPathPrefix + url.PathEscape(strings.TrimSpace(issueID)) + colinMetadataPathSuffix
}

// ColinExecPlanPath returns the Colin issue ExecPlan page path for a Linear issue.
func ColinExecPlanPath(issueID string) string {
	return colinMetadataPathPrefix + url.PathEscape(strings.TrimSpace(issueID)) + colinExecPlanPathSuffix
}

// ColinCodexOutputPath returns the dashboard Codex output fragment path for a Linear issue.
func ColinCodexOutputPath(issueID string) string {
	return colinAPIIssuesPathPrefix + url.PathEscape(strings.TrimSpace(issueID)) + colinCodexOutputPathSuffix
}

// ColinCodexOutputEventsPath returns the dashboard Codex output SSE path for a Linear issue.
func ColinCodexOutputEventsPath(issueID string) string {
	return colinAPIIssuesPathPrefix + url.PathEscape(strings.TrimSpace(issueID)) + colinCodexOutputEventsPathSuffix
}

// ParseColinMetadataPath extracts the issue ID from a Colin metadata page path.
func ParseColinMetadataPath(path string) (string, bool) {
	return parseIssuePath(path, colinMetadataPathPrefix, colinMetadataPathSuffix)
}

// ParseColinExecPlanPath extracts the issue ID from a Colin ExecPlan page path.
func ParseColinExecPlanPath(path string) (string, bool) {
	return parseIssuePath(path, colinMetadataPathPrefix, colinExecPlanPathSuffix)
}

// ParseColinCodexOutputPath extracts the issue ID from a dashboard Codex output fragment path.
func ParseColinCodexOutputPath(path string) (string, bool) {
	return parseIssuePath(path, colinAPIIssuesPathPrefix, colinCodexOutputPathSuffix)
}

// ParseColinCodexOutputEventsPath extracts the issue ID from a dashboard Codex output SSE path.
func ParseColinCodexOutputEventsPath(path string) (string, bool) {
	return parseIssuePath(path, colinAPIIssuesPathPrefix, colinCodexOutputEventsPathSuffix)
}

func parseIssuePath(path string, prefix string, suffix string) (string, bool) {
	start := strings.Index(path, prefix)
	if start < 0 || !strings.HasSuffix(path, suffix) {
		return "", false
	}
	rawIssueID := strings.TrimSuffix(path[start+len(prefix):], suffix)
	if strings.TrimSpace(rawIssueID) == "" || strings.Contains(rawIssueID, "/") {
		return "", false
	}
	issueID, err := url.PathUnescape(rawIssueID)
	if err != nil || strings.TrimSpace(issueID) == "" {
		return "", false
	}
	return issueID, true
}
