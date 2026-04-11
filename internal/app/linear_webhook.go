package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"
)

// LinearWebhookSecretProvider returns the configured Linear webhook secrets for request validation.
type LinearWebhookSecretProvider func(context.Context) []string

type linearWebhookEnvelope struct {
	WebhookTimestamp int64           `json:"webhookTimestamp"`
	Action           string          `json:"action"`
	Type             string          `json:"type"`
	Data             json.RawMessage `json:"data"`
	UpdatedFrom      json.RawMessage `json:"updatedFrom"`
	Raw              json.RawMessage `json:"-"`
}

func linearWebhookHandler(trigger LinearWebhookTrigger, secretProvider LinearWebhookSecretProvider, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		secrets := []string(nil)
		if secretProvider != nil {
			secrets = compactLinearWebhookSecrets(secretProvider(r.Context()))
		}
		logLinearWebhookRequest(logger, slog.LevelInfo, "received linear webhook request", r, len(secrets) > 0, nil)

		if r.Method != http.MethodPost {
			logLinearWebhookRequest(logger, slog.LevelWarn, "rejected linear webhook request with unsupported method", r, len(secrets) > 0, []any{"status", http.StatusMethodNotAllowed})
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}

		body, err := ioReadAll(r.Body)
		if err != nil {
			logLinearWebhookRequest(logger, slog.LevelWarn, "failed to read linear webhook request body", r, len(secrets) > 0, []any{"error", err})
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		logLinearWebhookRequest(logger, slog.LevelDebug, "read linear webhook request body", r, len(secrets) > 0, []any{"body_bytes", len(body)})
		if len(secrets) > 0 {
			if !validLinearWebhookSignatureAny(r.Header.Get("Linear-Signature"), body, secrets) {
				logLinearWebhookRequest(logger, slog.LevelWarn, "rejected linear webhook request with invalid signature", r, true, []any{"status", http.StatusUnauthorized, "body_bytes", len(body)})
				http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
				return
			}
			if !validLinearWebhookTimestamp(body, time.Now().UTC()) {
				logLinearWebhookRequest(logger, slog.LevelWarn, "rejected linear webhook request with invalid timestamp", r, true, []any{"status", http.StatusUnauthorized, "body_bytes", len(body)})
				http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
				return
			}
		}

		envelope, err := parseLinearWebhookEnvelope(body)
		if err != nil {
			logLinearWebhookRequest(logger, slog.LevelWarn, "rejected linear webhook request with invalid payload", r, len(secrets) > 0, []any{"status", http.StatusBadRequest, "body_bytes", len(body), "error", err})
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
		event := linearWebhookEventFromRequest(r, envelope)
		if shouldTriggerLinearWebhook(event) {
			result := LinearWebhookTriggerResult{}
			if trigger != nil {
				result = trigger(r.Context(), event)
			}
			extra := []any{
				"action", event.Action,
				"resource_type", event.ResourceType,
				"issue_id", event.IssueID,
				"project_id", event.ProjectID,
				"changed_fields", event.ChangedFields,
				"queued", result.Queued,
				"coalesced", result.Coalesced,
			}
			switch {
			case !result.Relevant:
				logLinearWebhookRequest(logger, slog.LevelDebug, "ignored linear webhook delivery that does not affect orchestration", r, len(secrets) > 0, extra)
			case result.Queued:
				logLinearWebhookRequest(logger, slog.LevelInfo, "queued immediate orchestrator refresh from linear webhook", r, len(secrets) > 0, extra)
			case result.Suppressed:
				logLinearWebhookRequest(logger, slog.LevelDebug, "suppressed immediate orchestrator refresh from linear webhook; polling fallback remains active", r, len(secrets) > 0, extra)
			default:
				logLinearWebhookRequest(logger, slog.LevelWarn, "could not queue immediate orchestrator refresh from linear webhook", r, len(secrets) > 0, extra)
			}
		} else {
			logLinearWebhookRequest(logger, slog.LevelDebug, "ignored linear webhook delivery that does not affect orchestration", r, len(secrets) > 0, []any{
				"action", event.Action,
				"resource_type", event.ResourceType,
				"issue_id", event.IssueID,
				"project_id", event.ProjectID,
				"changed_fields", event.ChangedFields,
			})
		}

		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		logLinearWebhookRequest(logger, slog.LevelDebug, "accepted linear webhook request", r, len(secrets) > 0, []any{"status", http.StatusOK, "body_bytes", len(body)})
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

func compactLinearWebhookSecrets(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func logLinearWebhookRequest(logger *slog.Logger, level slog.Level, message string, r *http.Request, secretConfigured bool, extra []any) {
	if logger == nil || r == nil {
		return
	}
	args := []any{
		"method", r.Method,
		"path", r.URL.Path,
		"linear_delivery", strings.TrimSpace(r.Header.Get("Linear-Delivery")),
		"linear_event", strings.TrimSpace(r.Header.Get("Linear-Event")),
		"signature_present", strings.TrimSpace(r.Header.Get("Linear-Signature")) != "",
		"secret_configured", secretConfigured,
	}
	args = append(args, extra...)
	logger.Log(r.Context(), level, message, args...)
}

func validLinearWebhookSignature(header string, body []byte, secret string) bool {
	header = strings.TrimSpace(header)
	secret = strings.TrimSpace(secret)
	if header == "" || secret == "" {
		return false
	}
	expected, err := hex.DecodeString(header)
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

func validLinearWebhookSignatureAny(header string, body []byte, secrets []string) bool {
	for _, secret := range compactLinearWebhookSecrets(secrets) {
		if validLinearWebhookSignature(header, body, secret) {
			return true
		}
	}
	return false
}

func validLinearWebhookTimestamp(body []byte, now time.Time) bool {
	var envelope linearWebhookEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return false
	}
	if envelope.WebhookTimestamp <= 0 {
		return false
	}
	sentAt := time.UnixMilli(envelope.WebhookTimestamp).UTC()
	delta := now.Sub(sentAt)
	if delta < 0 {
		delta = -delta
	}
	return delta <= time.Minute
}

func parseLinearWebhookEnvelope(body []byte) (linearWebhookEnvelope, error) {
	var envelope linearWebhookEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return linearWebhookEnvelope{}, err
	}
	envelope.Raw = append(json.RawMessage(nil), body...)
	return envelope, nil
}

func linearWebhookEventFromRequest(r *http.Request, envelope linearWebhookEnvelope) LinearWebhookEvent {
	resourceType := strings.TrimSpace(envelope.Type)
	headerEvent := ""
	if r != nil {
		headerEvent = strings.TrimSpace(r.Header.Get("Linear-Event"))
	}
	if resourceType == "" {
		resourceType = headerEvent
	}
	eventName := headerEvent
	if eventName == "" {
		eventName = resourceType
	}
	deliveryID := ""
	if r != nil {
		deliveryID = strings.TrimSpace(r.Header.Get("Linear-Delivery"))
	}
	subjectData := envelope.Data
	if len(subjectData) == 0 {
		subjectData = envelope.Raw
	}
	sessionID, sourceCommentID, issueID, projectID := parseLinearWebhookSubjectData(resourceType, subjectData)
	return LinearWebhookEvent{
		DeliveryID:      deliveryID,
		Event:           eventName,
		Action:          strings.TrimSpace(envelope.Action),
		ResourceType:    resourceType,
		SessionID:       sessionID,
		SourceCommentID: sourceCommentID,
		IssueID:         issueID,
		ProjectID:       projectID,
		ChangedFields:   parseLinearWebhookChangedFields(envelope.UpdatedFrom),
	}
}

func shouldTriggerLinearWebhook(event LinearWebhookEvent) bool {
	switch strings.ToLower(strings.TrimSpace(event.ResourceType)) {
	case "issue":
		if strings.TrimSpace(event.ProjectID) == "" && strings.TrimSpace(event.IssueID) == "" {
			return false
		}
		switch strings.ToLower(strings.TrimSpace(event.Action)) {
		case "create", "update":
			return true
		default:
			return false
		}
	case "issuelabel":
		if strings.TrimSpace(event.IssueID) == "" {
			return false
		}
		switch strings.ToLower(strings.TrimSpace(event.Action)) {
		case "create", "update", "remove":
			return true
		default:
			return false
		}
	case "agentsessionevent":
		if strings.TrimSpace(event.IssueID) == "" {
			return false
		}
		switch strings.ToLower(strings.TrimSpace(event.Action)) {
		case "created", "prompted":
			return true
		default:
			return false
		}
	default:
		return false
	}
}

func parseLinearWebhookSubjectData(resourceType string, raw json.RawMessage) (sessionID string, sourceCommentID string, issueID string, projectID string) {
	switch strings.ToLower(strings.TrimSpace(resourceType)) {
	case "issuelabel":
		issueID, projectID = parseLinearWebhookIssueLabelData(raw)
		return "", "", issueID, projectID
	case "agentsessionevent":
		return parseLinearWebhookAgentSessionData(raw)
	default:
		issueID, projectID = parseLinearWebhookIssueData(raw)
		return "", "", issueID, projectID
	}
}

func parseLinearWebhookIssueData(raw json.RawMessage) (issueID string, projectID string) {
	if len(raw) == 0 {
		return "", ""
	}
	var payload struct {
		ID        string `json:"id"`
		ProjectID string `json:"projectId"`
		Project   struct {
			ID string `json:"id"`
		} `json:"project"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", ""
	}
	return strings.TrimSpace(payload.ID), firstNonEmptyTrimmed(payload.ProjectID, payload.Project.ID)
}

func parseLinearWebhookIssueLabelData(raw json.RawMessage) (issueID string, projectID string) {
	if len(raw) == 0 {
		return "", ""
	}
	var payload struct {
		IssueID   string `json:"issueId"`
		ProjectID string `json:"projectId"`
		Project   struct {
			ID string `json:"id"`
		} `json:"project"`
		Issue struct {
			ID        string `json:"id"`
			ProjectID string `json:"projectId"`
			Project   struct {
				ID string `json:"id"`
			} `json:"project"`
		} `json:"issue"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", ""
	}
	issueID = strings.TrimSpace(payload.IssueID)
	if issueID == "" {
		issueID = strings.TrimSpace(payload.Issue.ID)
	}
	projectID = strings.TrimSpace(payload.ProjectID)
	if projectID == "" {
		projectID = strings.TrimSpace(payload.Issue.ProjectID)
	}
	if projectID == "" {
		projectID = firstNonEmptyTrimmed(payload.Project.ID, payload.Issue.Project.ID)
	}
	return issueID, projectID
}

func parseLinearWebhookAgentSessionData(raw json.RawMessage) (sessionID string, sourceCommentID string, issueID string, projectID string) {
	if len(raw) == 0 {
		return "", "", "", ""
	}
	var payload struct {
		ID        string `json:"id"`
		IssueID   string `json:"issueId"`
		ProjectID string `json:"projectId"`
		Issue     struct {
			ID        string `json:"id"`
			ProjectID string `json:"projectId"`
			Project   struct {
				ID string `json:"id"`
			} `json:"project"`
		} `json:"issue"`
		AgentSession struct {
			ID      string `json:"id"`
			IssueID string `json:"issueId"`
			Comment struct {
				ID      string `json:"id"`
				IssueID string `json:"issueId"`
			} `json:"comment"`
			Issue struct {
				ID        string `json:"id"`
				ProjectID string `json:"projectId"`
				Project   struct {
					ID string `json:"id"`
				} `json:"project"`
			} `json:"issue"`
		} `json:"agentSession"`
		PromptContext string `json:"promptContext"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", "", "", ""
	}
	sessionID = strings.TrimSpace(payload.AgentSession.ID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(payload.ID)
	}
	sourceCommentID = strings.TrimSpace(payload.AgentSession.Comment.ID)

	issueID = firstNonEmptyTrimmed(
		payload.IssueID,
		payload.Issue.ID,
		payload.AgentSession.IssueID,
		payload.AgentSession.Comment.IssueID,
		payload.AgentSession.Issue.ID,
	)
	projectID = firstNonEmptyTrimmed(
		payload.ProjectID,
		payload.Issue.ProjectID,
		payload.Issue.Project.ID,
		payload.AgentSession.Issue.ProjectID,
		payload.AgentSession.Issue.Project.ID,
	)
	return sessionID, sourceCommentID, issueID, projectID
}

func firstNonEmptyTrimmed(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func parseLinearWebhookChangedFields(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil
	}
	fields := make([]string, 0, len(payload))
	for key := range payload {
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			continue
		}
		fields = append(fields, key)
	}
	sort.Strings(fields)
	return fields
}

func ioReadAll(body interface {
	Read([]byte) (int, error)
	Close() error
}) ([]byte, error) {
	defer body.Close()
	return io.ReadAll(body)
}
