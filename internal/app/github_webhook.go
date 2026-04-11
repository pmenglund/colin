package app

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
)

type gitHubWebhookEnvelope struct {
	Action     string `json:"action"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Reaction *struct {
		Content string `json:"content"`
		User    *struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"reaction"`
	PullRequest *struct {
		Number int `json:"number"`
	} `json:"pull_request"`
	CheckRun *struct {
		ID           int64  `json:"id"`
		Name         string `json:"name"`
		Status       string `json:"status"`
		Conclusion   string `json:"conclusion"`
		HeadSHA      string `json:"head_sha"`
		PullRequests []struct {
			Number int `json:"number"`
		} `json:"pull_requests"`
	} `json:"check_run"`
	CheckSuite *struct {
		Status       string `json:"status"`
		Conclusion   string `json:"conclusion"`
		HeadSHA      string `json:"head_sha"`
		PullRequests []struct {
			Number int `json:"number"`
		} `json:"pull_requests"`
	} `json:"check_suite"`
	SHA         string `json:"sha"`
	State       string `json:"state"`
	Context     string `json:"context"`
	Description string `json:"description"`
	Issue       *struct {
		PullRequest *struct{} `json:"pull_request"`
	} `json:"issue"`
	Comment *struct {
		ID             int64  `json:"id"`
		Body           string `json:"body"`
		PullRequestURL string `json:"pull_request_url"`
		User           *struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"comment"`
}

func githubWebhookHandler(trigger GitHubWebhookTrigger, secretProvider GitHubWebhookSecretProvider, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		secret := ""
		if secretProvider != nil {
			secret = strings.TrimSpace(secretProvider(r.Context()))
		}
		logGitHubWebhookRequest(logger, slog.LevelInfo, "received github webhook request", r, len(secret) > 0, nil)

		if r.Method != http.MethodPost {
			logGitHubWebhookRequest(logger, slog.LevelWarn, "rejected github webhook request with unsupported method", r, len(secret) > 0, []any{"status", http.StatusMethodNotAllowed})
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}

		body, err := ioReadAll(r.Body)
		if err != nil {
			logGitHubWebhookRequest(logger, slog.LevelWarn, "failed to read github webhook request body", r, len(secret) > 0, []any{"error", err})
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if secret != "" && !validGitHubWebhookSignature(r.Header.Get("X-Hub-Signature-256"), body, secret) {
			logGitHubWebhookRequest(logger, slog.LevelWarn, "rejected github webhook request with invalid signature", r, true, []any{"status", http.StatusUnauthorized, "body_bytes", len(body)})
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}

		envelope, err := parseGitHubWebhookEnvelope(body)
		if err != nil {
			logGitHubWebhookRequest(logger, slog.LevelWarn, "rejected github webhook request with invalid payload", r, len(secret) > 0, []any{"status", http.StatusBadRequest, "body_bytes", len(body), "error", err})
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}

		event := gitHubWebhookEventFromRequest(r, envelope)
		if shouldTriggerGitHubWebhook(event) {
			result := GitHubWebhookTriggerResult{}
			if trigger != nil {
				result = trigger(r.Context(), event)
			}
			extra := []any{
				"action", event.Action,
				"repository", event.RepositoryFullName,
				"pull_request_number", event.PullRequestNumber,
				"has_pull_request", event.HasPullRequest,
				"queued", result.Queued,
				"coalesced", result.Coalesced,
			}
			switch {
			case !result.Relevant:
				logGitHubWebhookRequest(logger, slog.LevelDebug, "ignored github webhook delivery that does not affect orchestration", r, len(secret) > 0, extra)
			case result.Queued:
				logGitHubWebhookRequest(logger, slog.LevelInfo, "queued immediate orchestrator refresh from github webhook", r, len(secret) > 0, extra)
			case result.Suppressed:
				logGitHubWebhookRequest(logger, slog.LevelDebug, "suppressed immediate orchestrator refresh from github webhook; polling fallback remains active", r, len(secret) > 0, extra)
			default:
				logGitHubWebhookRequest(logger, slog.LevelWarn, "could not queue immediate orchestrator refresh from github webhook", r, len(secret) > 0, extra)
			}
		} else {
			logGitHubWebhookRequest(logger, slog.LevelDebug, "ignored github webhook delivery that does not affect orchestration", r, len(secret) > 0, []any{
				"action", event.Action,
				"repository", event.RepositoryFullName,
				"pull_request_number", event.PullRequestNumber,
				"has_pull_request", event.HasPullRequest,
			})
		}

		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

func logGitHubWebhookRequest(logger *slog.Logger, level slog.Level, message string, r *http.Request, secretConfigured bool, extra []any) {
	if logger == nil || r == nil {
		return
	}
	args := []any{
		"method", r.Method,
		"path", r.URL.Path,
		"github_delivery", strings.TrimSpace(r.Header.Get("X-GitHub-Delivery")),
		"github_event", strings.TrimSpace(r.Header.Get("X-GitHub-Event")),
		"signature_present", strings.TrimSpace(r.Header.Get("X-Hub-Signature-256")) != "",
		"secret_configured", secretConfigured,
	}
	args = append(args, extra...)
	logger.Log(r.Context(), level, message, args...)
}

func validGitHubWebhookSignature(header string, body []byte, secret string) bool {
	header = strings.TrimSpace(header)
	secret = strings.TrimSpace(secret)
	if header == "" || secret == "" {
		return false
	}
	if !strings.HasPrefix(strings.ToLower(header), "sha256=") {
		return false
	}
	expected, err := hex.DecodeString(strings.TrimSpace(header[len("sha256="):]))
	if err != nil || len(expected) == 0 {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	computed := mac.Sum(nil)
	if len(computed) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare(computed, expected) == 1
}

func parseGitHubWebhookEnvelope(body []byte) (gitHubWebhookEnvelope, error) {
	var envelope gitHubWebhookEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return gitHubWebhookEnvelope{}, err
	}
	return envelope, nil
}

func gitHubWebhookEventFromRequest(r *http.Request, envelope gitHubWebhookEnvelope) GitHubWebhookEvent {
	eventName := ""
	deliveryID := ""
	if r != nil {
		eventName = strings.TrimSpace(r.Header.Get("X-GitHub-Event"))
		deliveryID = strings.TrimSpace(r.Header.Get("X-GitHub-Delivery"))
	}
	event := GitHubWebhookEvent{
		DeliveryID:         deliveryID,
		Event:              eventName,
		Action:             strings.TrimSpace(envelope.Action),
		RepositoryFullName: strings.TrimSpace(envelope.Repository.FullName),
	}
	if envelope.Reaction != nil {
		event.ReactionContent = strings.TrimSpace(envelope.Reaction.Content)
		if envelope.Reaction.User != nil {
			event.ReactionUserLogin = strings.TrimSpace(envelope.Reaction.User.Login)
		}
	}
	if envelope.PullRequest != nil {
		event.PullRequestNumber = envelope.PullRequest.Number
		event.HasPullRequest = true
	}
	if envelope.CheckRun != nil {
		event.CheckRunID = envelope.CheckRun.ID
		event.CheckName = strings.TrimSpace(envelope.CheckRun.Name)
		event.CheckStatus = strings.TrimSpace(envelope.CheckRun.Status)
		event.CheckConclusion = strings.TrimSpace(envelope.CheckRun.Conclusion)
		event.HeadSHA = strings.TrimSpace(envelope.CheckRun.HeadSHA)
		for _, pr := range envelope.CheckRun.PullRequests {
			if pr.Number <= 0 {
				continue
			}
			event.PullRequestNumbers = append(event.PullRequestNumbers, pr.Number)
			if event.PullRequestNumber == 0 {
				event.PullRequestNumber = pr.Number
			}
			event.HasPullRequest = true
		}
	}
	if envelope.CheckSuite != nil {
		event.CheckStatus = strings.TrimSpace(envelope.CheckSuite.Status)
		event.CheckConclusion = strings.TrimSpace(envelope.CheckSuite.Conclusion)
		event.HeadSHA = strings.TrimSpace(envelope.CheckSuite.HeadSHA)
		for _, pr := range envelope.CheckSuite.PullRequests {
			if pr.Number <= 0 {
				continue
			}
			event.PullRequestNumbers = append(event.PullRequestNumbers, pr.Number)
			if event.PullRequestNumber == 0 {
				event.PullRequestNumber = pr.Number
			}
			event.HasPullRequest = true
		}
	}
	if strings.TrimSpace(envelope.SHA) != "" {
		event.HeadSHA = strings.TrimSpace(envelope.SHA)
	}
	if strings.TrimSpace(envelope.Context) != "" {
		event.CheckName = strings.TrimSpace(envelope.Context)
	}
	if strings.TrimSpace(envelope.State) != "" {
		event.CheckStatus = strings.TrimSpace(envelope.State)
		event.CheckConclusion = strings.TrimSpace(envelope.State)
	}
	if envelope.Issue != nil && envelope.Issue.PullRequest != nil {
		event.HasPullRequest = true
	}
	if envelope.Comment != nil {
		event.CommentID = envelope.Comment.ID
		event.CommentBody = strings.TrimSpace(envelope.Comment.Body)
		if envelope.Comment.User != nil {
			event.CommentAuthorLogin = strings.TrimSpace(envelope.Comment.User.Login)
		}
		if event.PullRequestNumber == 0 {
			event.PullRequestNumber = pullRequestNumberFromAPIURL(envelope.Comment.PullRequestURL)
		}
	}
	if envelope.Comment != nil && strings.TrimSpace(envelope.Comment.PullRequestURL) != "" {
		event.HasPullRequest = true
	}
	event.Relevant = shouldTriggerGitHubWebhook(event)
	return event
}

func pullRequestNumberFromAPIURL(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	raw = strings.TrimRight(raw, "/")
	idx := strings.LastIndex(raw, "/")
	if idx < 0 || idx == len(raw)-1 {
		return 0
	}
	value := raw[idx+1:]
	number := 0
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return 0
		}
		number = number*10 + int(ch-'0')
	}
	return number
}

func shouldTriggerGitHubWebhook(event GitHubWebhookEvent) bool {
	if strings.TrimSpace(event.RepositoryFullName) == "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(event.Event)) {
	case "pull_request":
		return event.HasPullRequest && isRelevantGitHubAction(event.Action, "opened", "reopened", "ready_for_review", "synchronize", "edited", "closed")
	case "pull_request_review":
		return event.HasPullRequest && isRelevantGitHubAction(event.Action, "submitted", "edited", "dismissed")
	case "pull_request_review_comment":
		return event.HasPullRequest && isRelevantGitHubAction(event.Action, "created", "edited", "deleted")
	case "pull_request_review_thread":
		return event.HasPullRequest && isRelevantGitHubAction(event.Action, "resolved", "unresolved")
	case "check_run":
		return event.PullRequestNumber > 0 || strings.TrimSpace(event.HeadSHA) != ""
	case "check_suite":
		return event.PullRequestNumber > 0 || strings.TrimSpace(event.HeadSHA) != ""
	case "status":
		return strings.TrimSpace(event.HeadSHA) != ""
	default:
		return false
	}
}

func isRelevantGitHubAction(action string, want ...string) bool {
	action = strings.ToLower(strings.TrimSpace(action))
	for _, candidate := range want {
		if action == candidate {
			return true
		}
	}
	return false
}
