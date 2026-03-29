package githubutil

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// ParsePullRequestURL extracts owner, repository, and pull request number from a GitHub pull request URL.
func ParsePullRequestURL(raw string) (repoLogin string, repoName string, number int, ok bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", "", 0, false
	}
	scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
	if scheme != "http" && scheme != "https" {
		return "", "", 0, false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "github.com" && host != "www.github.com" {
		return "", "", 0, false
	}
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(parts) < 4 || parts[2] != "pull" {
		return "", "", 0, false
	}
	number, err = strconv.Atoi(parts[3])
	if err != nil || number <= 0 {
		return "", "", 0, false
	}
	return parts[0], parts[1], number, true
}

// CanonicalPullRequestURL returns the normalized GitHub pull request URL when raw is a valid PR URL.
func CanonicalPullRequestURL(raw string) (string, bool) {
	repoLogin, repoName, number, ok := ParsePullRequestURL(raw)
	if !ok {
		return "", false
	}
	return fmt.Sprintf("https://github.com/%s/%s/pull/%d", repoLogin, repoName, number), true
}
