package domain

import (
	"net/url"
	"strings"
)

const (
	colinMetadataPathPrefix = "/linear/issues/"
	colinMetadataPathSuffix = "/metadata"
	colinExecPlanPathSuffix = "/exec-plan"
)

// ColinMetadataPath returns the Colin issue metadata page path for a Linear issue.
func ColinMetadataPath(issueID string) string {
	return colinMetadataPathPrefix + url.PathEscape(strings.TrimSpace(issueID)) + colinMetadataPathSuffix
}

// ColinExecPlanPath returns the Colin issue ExecPlan page path for a Linear issue.
func ColinExecPlanPath(issueID string) string {
	return colinMetadataPathPrefix + url.PathEscape(strings.TrimSpace(issueID)) + colinExecPlanPathSuffix
}

// ParseColinMetadataPath extracts the issue ID from a Colin metadata page path.
func ParseColinMetadataPath(path string) (string, bool) {
	return parseColinIssuePath(path, colinMetadataPathSuffix)
}

// ParseColinExecPlanPath extracts the issue ID from a Colin ExecPlan page path.
func ParseColinExecPlanPath(path string) (string, bool) {
	return parseColinIssuePath(path, colinExecPlanPathSuffix)
}

func parseColinIssuePath(path string, suffix string) (string, bool) {
	start := strings.Index(path, colinMetadataPathPrefix)
	if start < 0 || !strings.HasSuffix(path, suffix) {
		return "", false
	}
	rawIssueID := strings.TrimSuffix(path[start+len(colinMetadataPathPrefix):], suffix)
	if strings.TrimSpace(rawIssueID) == "" || strings.Contains(rawIssueID, "/") {
		return "", false
	}
	issueID, err := url.PathUnescape(rawIssueID)
	if err != nil || strings.TrimSpace(issueID) == "" {
		return "", false
	}
	return issueID, true
}
