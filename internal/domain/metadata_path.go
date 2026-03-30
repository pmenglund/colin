package domain

import (
	"net/url"
	"strings"
)

const (
	colinMetadataPathPrefix = "/linear/issues/"
	colinMetadataPathSuffix = "/metadata"
)

// ColinMetadataPath returns the Colin issue metadata page path for a Linear issue.
func ColinMetadataPath(issueID string) string {
	return colinMetadataPathPrefix + url.PathEscape(strings.TrimSpace(issueID)) + colinMetadataPathSuffix
}

// ParseColinMetadataPath extracts the issue ID from a Colin metadata page path.
func ParseColinMetadataPath(path string) (string, bool) {
	start := strings.Index(path, colinMetadataPathPrefix)
	if start < 0 || !strings.HasSuffix(path, colinMetadataPathSuffix) {
		return "", false
	}
	rawIssueID := strings.TrimSuffix(path[start+len(colinMetadataPathPrefix):], colinMetadataPathSuffix)
	if strings.TrimSpace(rawIssueID) == "" || strings.Contains(rawIssueID, "/") {
		return "", false
	}
	issueID, err := url.PathUnescape(rawIssueID)
	if err != nil || strings.TrimSpace(issueID) == "" {
		return "", false
	}
	return issueID, true
}
