package notify

import "context"

// IssueSummary is the Slack-visible summary Colin keeps in sync for one issue.
type IssueSummary struct {
	Identifier     string
	Title          string
	State          string
	NextAction     string
	LinearURL      string
	PullRequestURL string
	MetadataURL    string
	ExecPlanURL    string
	Fingerprint    string
}

// IssueNotificationState stores the external message reference for one issue.
type IssueNotificationState struct {
	ChannelID   string
	MessageTS   string
	Permalink   string
	Fingerprint string
}

// IssueNotifier syncs one issue summary to an external notification surface.
type IssueNotifier interface {
	SyncIssue(ctx context.Context, summary IssueSummary, existing IssueNotificationState) (IssueNotificationState, error)
}

type noopNotifier struct{}

// NewNoop returns a notifier that never sends external notifications.
func NewNoop() IssueNotifier {
	return noopNotifier{}
}

func (noopNotifier) SyncIssue(context.Context, IssueSummary, IssueNotificationState) (IssueNotificationState, error) {
	return IssueNotificationState{}, nil
}
