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
	"strings"
	"time"
)

// LinearWebhookSecretProvider returns the configured Linear webhook secret for request validation.
type LinearWebhookSecretProvider func(context.Context) string

type linearWebhookEnvelope struct {
	WebhookTimestamp int64           `json:"webhookTimestamp"`
	Action           string          `json:"action"`
	Type             string          `json:"type"`
	Data             json.RawMessage `json:"data"`
}

func linearWebhookHandler(trigger LinearWebhookTrigger, secretProvider LinearWebhookSecretProvider, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		secret := ""
		if secretProvider != nil {
			secret = strings.TrimSpace(secretProvider(r.Context()))
		}
		logLinearWebhookRequest(logger, slog.LevelInfo, "received linear webhook request", r, len(secret) > 0, nil)

		if r.Method != http.MethodPost {
			logLinearWebhookRequest(logger, slog.LevelWarn, "rejected linear webhook request with unsupported method", r, len(secret) > 0, []any{"status", http.StatusMethodNotAllowed})
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}

		body, err := ioReadAll(r.Body)
		if err != nil {
			logLinearWebhookRequest(logger, slog.LevelWarn, "failed to read linear webhook request body", r, len(secret) > 0, []any{"error", err})
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		logLinearWebhookRequest(logger, slog.LevelDebug, "read linear webhook request body", r, len(secret) > 0, []any{"body_bytes", len(body)})
		if secret != "" {
			if !validLinearWebhookSignature(r.Header.Get("Linear-Signature"), body, secret) {
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
			logLinearWebhookRequest(logger, slog.LevelWarn, "rejected linear webhook request with invalid payload", r, len(secret) > 0, []any{"status", http.StatusBadRequest, "body_bytes", len(body), "error", err})
			http.Error(w, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}

		event := linearWebhookEventFromRequest(r, envelope)
		if shouldTriggerLinearWebhook(event) {
			queued := false
			coalesced := false
			if trigger != nil {
				queued, coalesced = trigger(r.Context(), event)
			}
			level := slog.LevelInfo
			message := "queued immediate orchestrator refresh from linear webhook"
			extra := []any{
				"action", event.Action,
				"resource_type", event.ResourceType,
				"queued", queued,
				"coalesced", coalesced,
			}
			if trigger == nil || !queued {
				level = slog.LevelWarn
				message = "could not queue immediate orchestrator refresh from linear webhook"
			}
			logLinearWebhookRequest(logger, level, message, r, len(secret) > 0, extra)
		} else {
			logLinearWebhookRequest(logger, slog.LevelDebug, "ignored linear webhook delivery that does not affect orchestration", r, len(secret) > 0, []any{"action", event.Action, "resource_type", event.ResourceType})
		}

		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		logLinearWebhookRequest(logger, slog.LevelDebug, "accepted linear webhook request", r, len(secret) > 0, []any{"status", http.StatusOK, "body_bytes", len(body)})
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
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
	return LinearWebhookEvent{
		DeliveryID:   deliveryID,
		Event:        eventName,
		Action:       strings.TrimSpace(envelope.Action),
		ResourceType: resourceType,
	}
}

func shouldTriggerLinearWebhook(event LinearWebhookEvent) bool {
	if !strings.EqualFold(strings.TrimSpace(event.ResourceType), "Issue") {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(event.Action)) {
	case "create", "update":
		return true
	default:
		return false
	}
}

func ioReadAll(body interface {
	Read([]byte) (int, error)
	Close() error
}) ([]byte, error) {
	defer body.Close()
	return io.ReadAll(body)
}
