package config

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/repohost"
)

func normalizeSandboxPolicy(policy domain.SandboxPolicy) domain.SandboxPolicy {
	policy.Type = sandboxPolicyType(policy.Type)
	return policy
}

func currentRepoToken(backend string) string {
	return repohost.CurrentToken(backend)
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func intValue(value *int) (int, bool) {
	if value == nil {
		return 0, false
	}
	return *value, true
}

func durationMillisValue(value *int) (time.Duration, bool) {
	number, ok := intValue(value)
	if !ok {
		return 0, false
	}
	return time.Duration(number) * time.Millisecond, true
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
