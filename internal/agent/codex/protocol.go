package codex

import (
	"strings"
	"time"

	"github.com/pmenglund/colin/internal/domain"
)

func translateEvent(method string) string {
	switch method {
	case "turn/completed":
		return EventTurnCompleted
	case "turn/failed":
		return EventTurnFailed
	case "turn/cancelled":
		return EventTurnCancelled
	default:
		if strings.Contains(strings.ToLower(method), "notification") {
			return EventNotification
		}
		if strings.Contains(strings.ToLower(method), "input") {
			return EventTurnInputRequired
		}
		return EventOtherMessage
	}
}

func requiresUserInput(msg map[string]any) bool {
	method, _ := msg["method"].(string)
	method = strings.ToLower(method)
	if strings.Contains(method, "requestuserinput") || strings.Contains(method, "inputrequired") {
		return true
	}
	params, _ := msg["params"].(map[string]any)
	if flag, ok := params["inputRequired"].(bool); ok && flag {
		return true
	}
	return false
}

func summarizeMessage(msg map[string]any) string {
	if value, ok := completedItemText(msg); ok {
		return value
	}
	for _, key := range []string{"message", "summary", "text"} {
		if value, ok := stringValue(msg[key]); ok && value != "" {
			return value
		}
	}
	if params, ok := msg["params"].(map[string]any); ok {
		for _, key := range []string{"message", "summary", "text"} {
			if value, ok := stringValue(params[key]); ok && value != "" {
				return value
			}
		}
	}
	for _, path := range [][]string{
		{"params", "item", "text"},
		{"params", "turn", "error", "message"},
		{"params", "error", "message"},
	} {
		if value, ok := nestedString(msg, path...); ok && value != "" {
			return value
		}
	}
	if values := collectTextValues(msg); len(values) > 0 {
		return strings.Join(values, "\n")
	}
	return ""
}

func completedItemText(msg map[string]any) (string, bool) {
	method, _ := msg["method"].(string)
	if !strings.EqualFold(strings.TrimSpace(method), "item/completed") {
		return "", false
	}
	item, ok := msg["item"]
	if !ok {
		params, _ := msg["params"].(map[string]any)
		item, ok = params["item"]
	}
	if !ok {
		return "", false
	}
	return itemTextValue(item)
}

func itemTextValue(item any) (string, bool) {
	asMap, ok := item.(map[string]any)
	if !ok {
		return "", false
	}
	if text, ok := stringValue(asMap["text"]); ok {
		text = strings.TrimSpace(text)
		if text != "" {
			return text, true
		}
	}
	if len(asMap) != 1 {
		return "", false
	}
	for _, inner := range asMap {
		nested, ok := inner.(map[string]any)
		if !ok {
			return "", false
		}
		text, ok := stringValue(nested["text"])
		if !ok {
			return "", false
		}
		text = strings.TrimSpace(text)
		if text == "" {
			return "", false
		}
		return text, true
	}
	return "", false
}

func collectTextValues(value any) []string {
	seen := map[string]struct{}{}
	var out []string

	var walk func(any)
	walk = func(current any) {
		switch v := current.(type) {
		case map[string]any:
			for key, item := range v {
				switch strings.ToLower(key) {
				case "text", "message", "summary":
					if text, ok := stringValue(item); ok {
						text = strings.TrimSpace(text)
						if text != "" {
							if _, exists := seen[text]; !exists {
								seen[text] = struct{}{}
								out = append(out, text)
							}
						}
					}
				default:
					walk(item)
				}
			}
		case []any:
			for _, item := range v {
				walk(item)
			}
		}
	}

	walk(value)
	return out
}

func extractUsage(msg map[string]any) map[string]int64 {
	candidates := []any{}
	if params, ok := msg["params"]; ok {
		candidates = append(candidates, params)
	}
	if result, ok := msg["result"]; ok {
		candidates = append(candidates, result)
	}
	candidates = append(candidates, msg)

	for _, candidate := range candidates {
		if usage, ok := extractNestedUsage(candidate, "total_token_usage"); ok {
			return usage
		}
	}

	for _, candidate := range candidates {
		if usage, ok := extractThreadTokenUsage(candidate); ok {
			return usage
		}
	}

	for _, candidate := range candidates {
		if usage, ok := extractDirectUsage(candidate); ok {
			return usage
		}
	}

	return nil
}

func extractContextWindowUsage(msg map[string]any) *domain.ContextWindowUsage {
	candidates := []any{}
	if params, ok := msg["params"]; ok {
		candidates = append(candidates, params)
	}
	if result, ok := msg["result"]; ok {
		candidates = append(candidates, result)
	}
	candidates = append(candidates, msg)

	for _, candidate := range candidates {
		usage, ok := extractThreadTokenUsage(candidate)
		if !ok {
			continue
		}
		asMap, ok := candidate.(map[string]any)
		if !ok {
			continue
		}
		tokenUsage, ok := nestedMap(asMap, "tokenUsage")
		if !ok {
			continue
		}
		limit, ok := int64Value(tokenUsage["modelContextWindow"])
		if !ok || limit <= 0 {
			continue
		}
		total, ok := usage["total_tokens"]
		if !ok {
			continue
		}
		return &domain.ContextWindowUsage{
			UsedTokens:  total,
			LimitTokens: limit,
		}
	}

	return nil
}

func extractNestedUsage(value any, key string) (map[string]int64, bool) {
	switch v := value.(type) {
	case map[string]any:
		for nestedKey, item := range v {
			lower := strings.ToLower(nestedKey)
			switch lower {
			case "last_token_usage":
				continue
			case strings.ToLower(key):
				if usage, ok := extractDirectUsage(item); ok {
					return usage, true
				}
			}
			if usage, ok := extractNestedUsage(item, key); ok {
				return usage, true
			}
		}
	case []any:
		for _, item := range v {
			if usage, ok := extractNestedUsage(item, key); ok {
				return usage, true
			}
		}
	}

	return nil, false
}

func extractDirectUsage(value any) (map[string]int64, bool) {
	out := map[string]int64{}
	collectDirectUsage(value, out)
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

func extractThreadTokenUsage(value any) (map[string]int64, bool) {
	asMap, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}
	tokenUsage, ok := nestedMap(asMap, "tokenUsage")
	if !ok {
		return nil, false
	}
	total, ok := nestedMap(tokenUsage, "total")
	if !ok {
		return nil, false
	}
	return extractDirectUsage(total)
}

func collectDirectUsage(value any, out map[string]int64) {
	asMap, ok := value.(map[string]any)
	if !ok {
		return
	}

	for key, item := range asMap {
		switch strings.ToLower(key) {
		case "input_tokens", "inputtokens":
			if n, ok := int64Value(item); ok {
				out["input_tokens"] = n
			}
		case "output_tokens", "outputtokens":
			if n, ok := int64Value(item); ok {
				out["output_tokens"] = n
			}
		case "total_tokens", "totaltokens":
			if n, ok := int64Value(item); ok {
				out["total_tokens"] = n
			}
		}
	}
}

func extractRateLimits(msg map[string]any) domain.RateLimitSnapshot {
	out := domain.RateLimitSnapshot{}
	collectRateLimits(msg, out)
	if len(out) == 0 {
		return nil
	}
	return out
}

func collectRateLimits(value any, out domain.RateLimitSnapshot) {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "rate") && strings.Contains(lower, "limit") {
				if asMap, ok := item.(map[string]any); ok {
					for nestedKey, nestedValue := range asMap {
						if parsed, ok := rateLimitWindow(nestedValue); ok {
							out[nestedKey] = parsed
						}
					}
				}
			}
			collectRateLimits(item, out)
		}
	case []any:
		for _, item := range v {
			collectRateLimits(item, out)
		}
	}
}

func rateLimitWindow(value any) (domain.RateLimitWindow, bool) {
	asMap, ok := value.(map[string]any)
	if !ok {
		return domain.RateLimitWindow{}, false
	}

	var window domain.RateLimitWindow
	if n, ok := int64Value(asMap["limit"]); ok {
		window.Limit = int64Ptr(n)
	}
	if n, ok := int64Value(asMap["windowDurationMins"]); ok {
		window.WindowDurationMinutes = int64Ptr(n)
	}
	if n, ok := int64Value(asMap["remaining"]); ok {
		window.Remaining = int64Ptr(n)
	}
	if n, ok := int64Value(asMap["usedPercent"]); ok {
		window.UsedPercent = int64Ptr(n)
	}
	if ts, ok := rateLimitTime(asMap["resetsAt"]); ok {
		window.ResetsAt = timePtr(ts)
	}
	if ts, ok := rateLimitTime(asMap["nextAllowedAt"]); ok {
		window.NextAllowedAt = timePtr(ts)
	}
	if window.WindowDurationMinutes == nil && window.Limit == nil && window.Remaining == nil && window.UsedPercent == nil && window.ResetsAt == nil && window.NextAllowedAt == nil {
		return domain.RateLimitWindow{}, false
	}
	return window, true
}

func rateLimitTime(value any) (time.Time, bool) {
	switch v := value.(type) {
	case string:
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(v))
		if err != nil {
			return time.Time{}, false
		}
		return parsed.UTC(), true
	case int64:
		return time.Unix(v, 0).UTC(), true
	case int:
		return time.Unix(int64(v), 0).UTC(), true
	case float64:
		return time.Unix(int64(v), 0).UTC(), true
	default:
		return time.Time{}, false
	}
}

func int64Ptr(value int64) *int64 {
	return &value
}

func timePtr(value time.Time) *time.Time {
	copy := value.UTC()
	return &copy
}

func isResponse(msg map[string]any) bool {
	_, hasID := msg["id"]
	method, hasMethod := msg["method"]
	return hasID && (!hasMethod || method == nil)
}

func sameID(value any, want int) bool {
	switch v := value.(type) {
	case float64:
		return int(v) == want
	case int:
		return v == want
	default:
		return false
	}
}

func nestedString(root map[string]any, keys ...string) (string, bool) {
	current := any(root)
	for _, key := range keys {
		asMap, ok := current.(map[string]any)
		if !ok {
			return "", false
		}
		current, ok = asMap[key]
		if !ok {
			return "", false
		}
	}
	return stringValue(current)
}

func nestedMap(root map[string]any, keys ...string) (map[string]any, bool) {
	current := any(root)
	for _, key := range keys {
		asMap, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = asMap[key]
		if !ok {
			return nil, false
		}
	}
	asMap, ok := current.(map[string]any)
	return asMap, ok
}

func stringValue(value any) (string, bool) {
	v, ok := value.(string)
	return v, ok
}

func methodName(msg map[string]any) string {
	value, _ := stringValue(msg["method"])
	return value
}

func int64Value(value any) (int64, bool) {
	switch v := value.(type) {
	case int64:
		return v, true
	case int:
		return int64(v), true
	case float64:
		return int64(v), true
	default:
		return 0, false
	}
}
