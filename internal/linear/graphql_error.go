package linear

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

var (
	// ErrRateLimited indicates Linear rejected the request due to API rate limits.
	ErrRateLimited = errors.New("linear rate limited")
)

type graphQLErrorDetails struct {
	retryIn     time.Duration
	hasRetryIn  bool
	rateLimited bool
}

type graphQLAPIError struct {
	message     string
	retryIn     time.Duration
	hasRetryIn  bool
	rateLimited bool
}

func (e *graphQLAPIError) Error() string {
	return e.message
}

func (e *graphQLAPIError) RetryIn() (time.Duration, bool) {
	if !e.hasRetryIn || e.retryIn <= 0 {
		return 0, false
	}
	return e.retryIn, true
}

func (e *graphQLAPIError) Is(target error) bool {
	return target == ErrRateLimited && e.rateLimited
}

// RetryIn reports the retry delay attached to err when available.
func RetryIn(err error) (time.Duration, bool) {
	type retryInProvider interface {
		RetryIn() (time.Duration, bool)
	}

	var provider retryInProvider
	if !errors.As(err, &provider) {
		return 0, false
	}
	return provider.RetryIn()
}

func newGraphQLStatusError(statusCode int, responseHeaders http.Header, responseBody []byte) error {
	detail := strings.TrimSpace(string(responseBody))
	if detail == "" {
		detail = strings.TrimSpace(http.StatusText(statusCode))
	}
	return newGraphQLError(
		fmt.Sprintf("graphql status %d: %s", statusCode, detail),
		statusCode,
		responseHeaders,
		responseBody,
	)
}

func newGraphQLMessageError(message string, responseHeaders http.Header, responseBody []byte) error {
	detail := strings.TrimSpace(message)
	if detail == "" {
		detail = "unknown GraphQL error"
	}
	return newGraphQLError(
		fmt.Sprintf("graphql error: %s", detail),
		http.StatusOK,
		responseHeaders,
		responseBody,
	)
}

func newGraphQLError(message string, statusCode int, responseHeaders http.Header, responseBody []byte) error {
	details := parseGraphQLErrorDetails(statusCode, responseHeaders, responseBody)
	return &graphQLAPIError{
		message:     strings.TrimSpace(message),
		retryIn:     details.retryIn,
		hasRetryIn:  details.hasRetryIn,
		rateLimited: details.rateLimited,
	}
}

func parseGraphQLErrorDetails(statusCode int, responseHeaders http.Header, responseBody []byte) graphQLErrorDetails {
	details := graphQLErrorDetails{
		rateLimited: statusCode == http.StatusTooManyRequests,
	}

	if retryIn, ok := retryInFromHeaders(responseHeaders); ok {
		details.retryIn = retryIn
		details.hasRetryIn = true
	}

	var envelope struct {
		Errors []struct {
			Extensions map[string]any `json:"extensions"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(responseBody, &envelope); err == nil {
		for _, graphqlErr := range envelope.Errors {
			if isRateLimitedExtensions(graphqlErr.Extensions) {
				details.rateLimited = true
			}
			if !details.hasRetryIn {
				if retryIn, ok := retryInFromMap(graphqlErr.Extensions); ok {
					details.retryIn = retryIn
					details.hasRetryIn = true
				}
			}
		}
	}

	if !details.hasRetryIn {
		var raw map[string]any
		if err := json.Unmarshal(responseBody, &raw); err == nil {
			if retryIn, ok := retryInFromMap(raw); ok {
				details.retryIn = retryIn
				details.hasRetryIn = true
			}
		}
	}

	return details
}

func isRateLimitedExtensions(extensions map[string]any) bool {
	if len(extensions) == 0 {
		return false
	}
	if code, ok := stringFromAny(mapValueByNormalizedKey(extensions, "code")); ok && strings.EqualFold(code, "RATELIMITED") {
		return true
	}
	if kind, ok := stringFromAny(mapValueByNormalizedKey(extensions, "type")); ok && strings.EqualFold(kind, "ratelimited") {
		return true
	}
	if status, ok := numberFromAny(mapValueByNormalizedKey(extensions, "statuscode")); ok && int(status) == http.StatusTooManyRequests {
		return true
	}
	return false
}

func retryInFromMap(values map[string]any) (time.Duration, bool) {
	if len(values) == 0 {
		return 0, false
	}
	if retryIn, ok := retryInFromRateLimitResult(values); ok {
		return retryIn, true
	}

	for key, raw := range values {
		normalizedKey := normalizeGraphQLField(key)
		switch {
		case normalizedKey == "headers":
			if retryIn, ok := retryInFromHeaderValue(raw); ok {
				return retryIn, true
			}
		case isRetryKey(normalizedKey):
			if retryIn, ok := parseRetryValue(raw, normalizedKey); ok {
				return retryIn, true
			}
		}
	}

	for _, raw := range values {
		if retryIn, ok := retryInFromNested(raw); ok {
			return retryIn, true
		}
	}

	return 0, false
}

func retryInFromNested(raw any) (time.Duration, bool) {
	switch typed := raw.(type) {
	case map[string]any:
		return retryInFromMap(typed)
	case []any:
		for _, item := range typed {
			if retryIn, ok := retryInFromNested(item); ok {
				return retryIn, true
			}
		}
	}
	return 0, false
}

func retryInFromRateLimitResult(values map[string]any) (time.Duration, bool) {
	rawResult, ok := mapValueByNormalizedKey(values, "ratelimitresult")
	if !ok {
		return 0, false
	}
	result, ok := rawResult.(map[string]any)
	if !ok {
		return 0, false
	}

	allowed, hasAllowed := boolFromAny(mapValueByNormalizedKey(result, "allowed"))
	if hasAllowed && allowed {
		return 0, false
	}

	requested, hasRequested := numberFromAny(mapValueByNormalizedKey(result, "requested"))
	remaining, hasRemaining := numberFromAny(mapValueByNormalizedKey(result, "remaining"))
	durationMS, hasDuration := numberFromAny(mapValueByNormalizedKey(result, "duration"))
	limit, hasLimit := numberFromAny(mapValueByNormalizedKey(result, "limit"))
	if !hasRequested || !hasRemaining || !hasDuration || !hasLimit {
		return 0, false
	}
	if durationMS <= 0 || limit <= 0 {
		return 0, false
	}

	needed := requested - remaining
	if needed <= 0 {
		needed = 1
	}

	waitMS := math.Ceil((needed * durationMS) / limit)
	if waitMS <= 0 {
		return 0, false
	}
	return time.Duration(waitMS * float64(time.Millisecond)), true
}

func retryInFromHeaderValue(raw any) (time.Duration, bool) {
	headers, ok := raw.(map[string]any)
	if !ok {
		return 0, false
	}

	parsed := make(http.Header, len(headers))
	for key, value := range headers {
		values := anyToStringSlice(value)
		if len(values) == 0 {
			continue
		}
		for _, item := range values {
			parsed.Add(key, item)
		}
	}

	return retryInFromHeaders(parsed)
}

func retryInFromHeaders(headers http.Header) (time.Duration, bool) {
	if len(headers) == 0 {
		return 0, false
	}

	if retryIn, ok := retryInFromRetryAfter(headers.Values("Retry-After")); ok {
		return retryIn, true
	}

	var (
		shortest time.Duration
		found    bool
	)
	for _, headerName := range []string{
		"X-RateLimit-Requests-Reset",
		"X-RateLimit-Endpoint-Requests-Reset",
		"X-RateLimit-Complexity-Reset",
		"X-RateLimit-Reset",
	} {
		for _, value := range headers.Values(headerName) {
			retryIn, ok := retryInFromResetValue(value)
			if !ok {
				continue
			}
			if !found || retryIn < shortest {
				shortest = retryIn
				found = true
			}
		}
	}

	return shortest, found
}

func retryInFromRetryAfter(values []string) (time.Duration, bool) {
	for _, value := range values {
		delay, ok := parseRetryAfter(value)
		if ok {
			return delay, true
		}
	}
	return 0, false
}

func retryInFromResetValue(value string) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	resetRaw, err := strconv.ParseFloat(value, 64)
	if err != nil || resetRaw <= 0 {
		return 0, false
	}

	// Linear reset headers are epoch milliseconds. Accept seconds when precision
	// indicates unix seconds for defensive compatibility.
	if resetRaw < 1_000_000_000_000 {
		resetRaw *= 1000
	}

	retryIn := time.Until(time.UnixMilli(int64(resetRaw)))
	if retryIn <= 0 {
		return 0, false
	}
	return retryIn, true
}

func parseRetryAfter(value string) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}

	if duration, err := time.ParseDuration(value); err == nil && duration > 0 {
		return duration, true
	}
	if seconds, err := strconv.ParseFloat(value, 64); err == nil && seconds > 0 {
		return time.Duration(seconds * float64(time.Second)), true
	}
	if ts, err := http.ParseTime(value); err == nil {
		retryIn := time.Until(ts)
		if retryIn > 0 {
			return retryIn, true
		}
	}
	return 0, false
}

func parseRetryValue(raw any, normalizedKey string) (time.Duration, bool) {
	switch value := raw.(type) {
	case string:
		return parseRetryValueString(value, normalizedKey)
	case float64:
		return parseRetryValueNumber(value, normalizedKey)
	case float32:
		return parseRetryValueNumber(float64(value), normalizedKey)
	case int:
		return parseRetryValueNumber(float64(value), normalizedKey)
	case int64:
		return parseRetryValueNumber(float64(value), normalizedKey)
	case int32:
		return parseRetryValueNumber(float64(value), normalizedKey)
	case uint:
		return parseRetryValueNumber(float64(value), normalizedKey)
	case uint64:
		return parseRetryValueNumber(float64(value), normalizedKey)
	case uint32:
		return parseRetryValueNumber(float64(value), normalizedKey)
	case json.Number:
		number, err := value.Float64()
		if err != nil {
			return 0, false
		}
		return parseRetryValueNumber(number, normalizedKey)
	case []any:
		for _, item := range value {
			if retryIn, ok := parseRetryValue(item, normalizedKey); ok {
				return retryIn, true
			}
		}
	case []string:
		for _, item := range value {
			if retryIn, ok := parseRetryValue(item, normalizedKey); ok {
				return retryIn, true
			}
		}
	}
	return 0, false
}

func parseRetryValueString(value string, normalizedKey string) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if duration, err := time.ParseDuration(value); err == nil && duration > 0 {
		return duration, true
	}
	if normalizedKey == "retryafter" {
		return parseRetryAfter(value)
	}
	number, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, false
	}
	return parseRetryValueNumber(number, normalizedKey)
}

func parseRetryValueNumber(value float64, normalizedKey string) (time.Duration, bool) {
	if value <= 0 {
		return 0, false
	}
	if strings.HasSuffix(normalizedKey, "ms") {
		return time.Duration(value * float64(time.Millisecond)), true
	}
	if normalizedKey == "retryafter" {
		return time.Duration(value * float64(time.Second)), true
	}
	if value >= 1000 {
		return time.Duration(value * float64(time.Millisecond)), true
	}
	return time.Duration(value * float64(time.Second)), true
}

func normalizeGraphQLField(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	key = strings.ReplaceAll(key, "-", "")
	key = strings.ReplaceAll(key, "_", "")
	key = strings.ReplaceAll(key, " ", "")
	return key
}

func isRetryKey(normalizedKey string) bool {
	return strings.HasPrefix(normalizedKey, "retryin") || strings.HasPrefix(normalizedKey, "retryafter")
}

func mapValueByNormalizedKey(values map[string]any, normalizedKey string) (any, bool) {
	for key, raw := range values {
		if normalizeGraphQLField(key) == normalizedKey {
			return raw, true
		}
	}
	return nil, false
}

func stringFromAny(raw any, ok bool) (string, bool) {
	if !ok || raw == nil {
		return "", false
	}
	value, casted := raw.(string)
	if !casted {
		return "", false
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	return value, true
}

func numberFromAny(raw any, ok bool) (float64, bool) {
	if !ok || raw == nil {
		return 0, false
	}
	switch value := raw.(type) {
	case float64:
		return value, true
	case float32:
		return float64(value), true
	case int:
		return float64(value), true
	case int64:
		return float64(value), true
	case int32:
		return float64(value), true
	case uint:
		return float64(value), true
	case uint64:
		return float64(value), true
	case uint32:
		return float64(value), true
	case json.Number:
		number, err := value.Float64()
		if err != nil {
			return 0, false
		}
		return number, true
	case string:
		number, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			return 0, false
		}
		return number, true
	default:
		return 0, false
	}
}

func boolFromAny(raw any, ok bool) (bool, bool) {
	if !ok || raw == nil {
		return false, false
	}
	value, casted := raw.(bool)
	return value, casted
}

func anyToStringSlice(raw any) []string {
	switch value := raw.(type) {
	case string:
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return nil
		}
		return []string{trimmed}
	case []string:
		out := make([]string, 0, len(value))
		for _, item := range value {
			trimmed := strings.TrimSpace(item)
			if trimmed == "" {
				continue
			}
			out = append(out, trimmed)
		}
		return out
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			out = append(out, anyToStringSlice(item)...)
		}
		return out
	default:
		text := strings.TrimSpace(fmt.Sprint(raw))
		if text == "" || text == "<nil>" {
			return nil
		}
		return []string{text}
	}
}
