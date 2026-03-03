package worker

import "context"

// GitHubTokenProvider returns GitHub installation access tokens.
type GitHubTokenProvider interface {
	Token(ctx context.Context) (string, error)
}
