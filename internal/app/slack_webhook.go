package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const slackWebhookMaxAge = 5 * time.Minute

type slackWebhookEnvelope struct {
	Type      string          `json:"type"`
	Challenge string          `json:"challenge"`
	Event     json.RawMessage `json:"event"`
}

type slackAppHomeOpenedEvent struct {
	Type string `json:"type"`
	User string `json:"user"`
}

func slackWebhookHandler(publisher SlackWebhookPublisher, secretProvider SlackWebhookSecretProvider, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		secret := ""
		if secretProvider != nil {
			secret = strings.TrimSpace(secretProvider(r.Context()))
		}
		logSlackWebhookRequest(logger, slog.LevelInfo, "received slack webhook request", r, len(secret) > 0, nil)
		if secret == "" {
			logSlackWebhookRequest(logger, slog.LevelDebug, "ignored slack webhook request because slack signing secret is not configured", r, false, []any{"status", http.StatusNotFound})
			http.NotFound(w, r)
			return
		}

		if r.Method != http.MethodPost {
			logSlackWebhookRequest(logger, slog.LevelWarn, "rejected slack webhook request with unsupported method", r, len(secret) > 0, []any{"status", http.StatusMethodNotAllowed})
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}

		body, err := ioReadAll(r.Body)
		if err != nil {
			logSlackWebhookRequest(logger, slog.LevelWarn, "failed to read slack webhook request body", r, len(secret) > 0, []any{"error", err})
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if !validSlackWebhookTimestamp(r.Header.Get("X-Slack-Request-Timestamp"), time.Now().UTC()) {
			logSlackWebhookRequest(logger, slog.LevelWarn, "rejected slack webhook request with invalid timestamp", r, true, []any{"status", http.StatusUnauthorized, "body_bytes", len(body)})
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		if !validSlackWebhookSignature(r.Header.Get("X-Slack-Signature"), r.Header.Get("X-Slack-Request-Timestamp"), body, secret) {
			logSlackWebhookRequest(logger, slog.LevelWarn, "rejected slack webhook request with invalid signature", r, true, []any{"status", http.StatusUnauthorized, "body_bytes", len(body)})
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}

		envelope, err := parseSlackWebhookEnvelope(body)
		if err != nil {
			logSlackWebhookRequest(logger, slog.LevelWarn, "rejected slack webhook request with invalid payload", r, len(secret) > 0, []any{"status", http.StatusBadRequest, "body_bytes", len(body), "error", err})
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}

		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")

		switch strings.TrimSpace(envelope.Type) {
		case "url_verification":
			logSlackWebhookRequest(logger, slog.LevelDebug, "accepted slack webhook url verification request", r, len(secret) > 0, []any{"status", http.StatusOK})
			_ = json.NewEncoder(w).Encode(map[string]string{"challenge": strings.TrimSpace(envelope.Challenge)})
			return
		case "event_callback":
			event, ok := parseSlackAppHomeOpenedEvent(envelope.Event)
			if ok {
				if publisher != nil {
					go publishSlackHomeView(logger, publisher, event)
				}
				logSlackWebhookRequest(logger, slog.LevelInfo, "queued slack home view publish from webhook", r, len(secret) > 0, []any{"event_type", event.Event, "user_id", event.UserID})
			} else {
				logSlackWebhookRequest(logger, slog.LevelDebug, "ignored slack webhook delivery that does not affect app home", r, len(secret) > 0, nil)
			}
		default:
			logSlackWebhookRequest(logger, slog.LevelDebug, "ignored slack webhook delivery with unsupported type", r, len(secret) > 0, []any{"payload_type", envelope.Type})
		}

		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

func logSlackWebhookRequest(logger *slog.Logger, level slog.Level, message string, r *http.Request, secretConfigured bool, extra []any) {
	if logger == nil || r == nil {
		return
	}
	args := []any{
		"method", r.Method,
		"path", r.URL.Path,
		"slack_signature_present", strings.TrimSpace(r.Header.Get("X-Slack-Signature")) != "",
		"slack_timestamp_present", strings.TrimSpace(r.Header.Get("X-Slack-Request-Timestamp")) != "",
		"secret_configured", secretConfigured,
	}
	args = append(args, extra...)
	logger.Log(r.Context(), level, message, args...)
}

func validSlackWebhookSignature(header string, timestamp string, body []byte, secret string) bool {
	header = strings.TrimSpace(header)
	timestamp = strings.TrimSpace(timestamp)
	secret = strings.TrimSpace(secret)
	if header == "" || timestamp == "" || secret == "" {
		return false
	}
	if !strings.HasPrefix(strings.ToLower(header), "v0=") {
		return false
	}
	expected, err := hex.DecodeString(strings.TrimSpace(header[len("v0="):]))
	if err != nil || len(expected) == 0 {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("v0:" + timestamp + ":"))
	_, _ = mac.Write(body)
	computed := mac.Sum(nil)
	if len(computed) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare(computed, expected) == 1
}

func validSlackWebhookTimestamp(header string, now time.Time) bool {
	header = strings.TrimSpace(header)
	if header == "" {
		return false
	}
	seconds, err := strconv.ParseInt(header, 10, 64)
	if err != nil || seconds <= 0 {
		return false
	}
	sentAt := time.Unix(seconds, 0).UTC()
	delta := now.Sub(sentAt)
	if delta < 0 {
		delta = -delta
	}
	return delta <= slackWebhookMaxAge
}

func parseSlackWebhookEnvelope(body []byte) (slackWebhookEnvelope, error) {
	var envelope slackWebhookEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return slackWebhookEnvelope{}, err
	}
	return envelope, nil
}

func parseSlackAppHomeOpenedEvent(raw json.RawMessage) (SlackWebhookEvent, bool) {
	if len(raw) == 0 {
		return SlackWebhookEvent{}, false
	}
	var event slackAppHomeOpenedEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return SlackWebhookEvent{}, false
	}
	if !strings.EqualFold(strings.TrimSpace(event.Type), "app_home_opened") || strings.TrimSpace(event.User) == "" {
		return SlackWebhookEvent{}, false
	}
	return SlackWebhookEvent{
		Event:  strings.TrimSpace(event.Type),
		UserID: strings.TrimSpace(event.User),
	}, true
}

func publishSlackHomeView(logger *slog.Logger, publisher SlackWebhookPublisher, event SlackWebhookEvent) {
	if publisher == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := publisher(ctx, event); err != nil {
		if logger != nil {
			logger.Warn("failed to publish slack home view from webhook", "event_type", event.Event, "user_id", event.UserID, "error", err)
		}
		return
	}
	if logger != nil {
		logger.Debug("published slack home view from webhook", "event_type", event.Event, "user_id", event.UserID)
	}
}
