package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func normalizeSandboxPolicy(policy map[string]any) map[string]any {
	if len(policy) == 0 {
		return policy
	}

	normalized := make(map[string]any, len(policy))
	for key, value := range policy {
		normalized[key] = value
	}

	if mode, ok := readSandboxMode(normalized["mode"]); ok {
		normalized["type"] = sandboxPolicyType(mode)
		delete(normalized, "mode")
	}
	if policyType, ok := readSandboxMode(normalized["type"]); ok {
		normalized["type"] = sandboxPolicyType(policyType)
	}
	return normalized
}

func readSandboxMode(value any) (string, bool) {
	text, ok := value.(string)
	if !ok {
		return "", false
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", false
	}
	return text, true
}

func sandboxPolicyType(value string) string {
	switch strings.TrimSpace(value) {
	case "danger-full-access", "dangerFullAccess":
		return "dangerFullAccess"
	case "read-only", "readOnly":
		return "readOnly"
	case "workspace-write", "workspaceWrite":
		return "workspaceWrite"
	case "external-sandbox", "externalSandbox":
		return "externalSandbox"
	default:
		return value
	}
}

func readString(raw map[string]any, key string) (string, bool) {
	value, ok := raw[key]
	if !ok || value == nil {
		return "", false
	}
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v), true
	default:
		return strings.TrimSpace(fmt.Sprint(v)), true
	}
}

func readStringSlice(raw map[string]any, key string) ([]string, bool) {
	value, ok := raw[key]
	if !ok || value == nil {
		return nil, false
	}
	items, ok := value.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		switch v := item.(type) {
		case string:
			if trimmed := strings.TrimSpace(v); trimmed != "" {
				out = append(out, trimmed)
			}
		default:
			trimmed := strings.TrimSpace(fmt.Sprint(v))
			if trimmed != "" {
				out = append(out, trimmed)
			}
		}
	}
	return out, true
}

func readInt(raw map[string]any, key string) (int, bool) {
	value, ok := raw[key]
	if !ok || value == nil {
		return 0, false
	}
	return toInt(value)
}

func readBool(raw map[string]any, key string) (bool, bool) {
	value, ok := raw[key]
	if !ok || value == nil {
		return false, false
	}
	return toBool(value)
}

func toInt(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		return n, err == nil
	default:
		return 0, false
	}
}

func toBool(value any) (bool, bool) {
	switch v := value.(type) {
	case bool:
		return v, true
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(v))
		return parsed, err == nil
	default:
		return false, false
	}
}

func readDurationMillis(raw map[string]any, key string) (time.Duration, bool) {
	value, ok := readInt(raw, key)
	if !ok {
		return 0, false
	}
	return time.Duration(value) * time.Millisecond, true
}

func resolveEnvToken(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "$") && len(value) > 1 {
		return strings.TrimSpace(os.Getenv(strings.TrimPrefix(value, "$")))
	}
	return value
}

func expandPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return value
	}
	if strings.HasPrefix(value, "$") && !strings.ContainsRune(value[1:], os.PathSeparator) {
		value = resolveEnvToken(value)
	}
	if value == "" {
		return value
	}
	if strings.HasPrefix(value, "~") {
		home, err := os.UserHomeDir()
		if err == nil {
			switch {
			case value == "~":
				value = home
			case strings.HasPrefix(value, "~/"):
				value = filepath.Join(home, strings.TrimPrefix(value, "~/"))
			}
		}
	}

	cleaned := filepath.Clean(value)
	if abs, err := filepath.Abs(cleaned); err == nil {
		return abs
	}
	return cleaned
}

func normalizeStateList(values []string) {
	for i := range values {
		values[i] = strings.TrimSpace(values[i])
	}
}
