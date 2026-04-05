package repoops

import "github.com/pmenglund/colin/internal/repohost"

//go:generate go tool counterfeiter -o ./fakes/fake_github_client.go . GitHubClient

// Deprecated: use repohost.PullRequest.
type GitHubPullRequest = repohost.PullRequest

// Deprecated: use repohost.CreatePullRequestInput.
type CreatePullRequestInput = repohost.CreatePullRequestInput

// Deprecated: use repohost.ReviewComment.
type GitHubReviewComment = repohost.ReviewComment

// Deprecated: use repohost.ReviewCommentConnection.
type GitHubReviewCommentConnection = repohost.ReviewCommentConnection

// Deprecated: use repohost.ReviewThread.
type GitHubReviewThread = repohost.ReviewThread

// Deprecated: use repohost.ReviewThreadPage.
type GitHubReviewThreadPage = repohost.ReviewThreadPage

// Deprecated: use repohost.ReviewThreadCommentPage.
type GitHubReviewThreadCommentPage = repohost.ReviewThreadCommentPage

// Deprecated: use repohost.Reaction.
type GitHubReaction = repohost.Reaction

// Deprecated: use repohost.ReactionPage.
type GitHubReactionPage = repohost.ReactionPage

// Deprecated: use repohost.ReviewCommentReactionPage.
type GitHubReviewCommentReactionPage = repohost.ReviewCommentReactionPage

// Deprecated: use repohost.Client.
type GitHubClient = repohost.Client
