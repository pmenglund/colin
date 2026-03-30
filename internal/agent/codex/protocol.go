package codex

import "strings"

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
		if usage, ok := extractDirectUsage(candidate); ok {
			return usage
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

func collectDirectUsage(value any, out map[string]int64) {
	asMap, ok := value.(map[string]any)
	if !ok {
		return
	}

	for key, item := range asMap {
		switch strings.ToLower(key) {
		case "input_tokens":
			if n, ok := int64Value(item); ok {
				out["input_tokens"] = n
			}
		case "output_tokens":
			if n, ok := int64Value(item); ok {
				out["output_tokens"] = n
			}
		case "total_tokens":
			if n, ok := int64Value(item); ok {
				out["total_tokens"] = n
			}
		}
	}
}

func extractRateLimits(msg map[string]any) map[string]any {
	out := map[string]any{}
	collectRateLimits(msg, out)
	if len(out) == 0 {
		return nil
	}
	return out
}

func collectRateLimits(value any, out map[string]any) {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "rate") && strings.Contains(lower, "limit") {
				if asMap, ok := item.(map[string]any); ok {
					for nestedKey, nestedValue := range asMap {
						out[nestedKey] = nestedValue
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
