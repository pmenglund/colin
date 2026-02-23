package linear

import (
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestParseGraphQLErrorDetailsReadsRetryIn(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"errors": [{
			"message": "Rate limit exceeded",
			"extensions": {
				"code": "RATELIMITED",
				"statusCode": 429,
				"retry_in": "2.5s"
			}
		}]
	}`)

	details := parseGraphQLErrorDetails(http.StatusBadRequest, nil, body)
	if !details.rateLimited {
		t.Fatal("rateLimited = false, want true")
	}
	if !details.hasRetryIn {
		t.Fatal("hasRetryIn = false, want true")
	}
	if details.retryIn != 2500*time.Millisecond {
		t.Fatalf("retryIn = %s, want %s", details.retryIn, 2500*time.Millisecond)
	}
}

func TestParseGraphQLErrorDetailsCalculatesRetryFromRateLimitResult(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"errors": [{
			"message": "Rate limit exceeded",
			"extensions": {
				"code": "RATELIMITED",
				"meta": {
					"rateLimitResult": {
						"allowed": false,
						"requested": 1,
						"remaining": 0,
						"duration": 3600000,
						"limit": 5000
					}
				}
			}
		}]
	}`)

	details := parseGraphQLErrorDetails(http.StatusBadRequest, nil, body)
	if !details.hasRetryIn {
		t.Fatal("hasRetryIn = false, want true")
	}
	if details.retryIn != 720*time.Millisecond {
		t.Fatalf("retryIn = %s, want %s", details.retryIn, 720*time.Millisecond)
	}
}

func TestParseGraphQLErrorDetailsUsesRetryAfterHeader(t *testing.T) {
	t.Parallel()

	headers := http.Header{
		"Retry-After": []string{"3"},
	}
	details := parseGraphQLErrorDetails(http.StatusTooManyRequests, headers, nil)
	if !details.hasRetryIn {
		t.Fatal("hasRetryIn = false, want true")
	}
	if details.retryIn != 3*time.Second {
		t.Fatalf("retryIn = %s, want %s", details.retryIn, 3*time.Second)
	}
}

func TestNewGraphQLStatusErrorCarriesRateLimitMetadata(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"errors": [{
			"message": "Rate limit exceeded",
			"extensions": {
				"code": "RATELIMITED",
				"retry_in": 5
			}
		}]
	}`)

	err := newGraphQLStatusError(http.StatusBadRequest, nil, body)
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("errors.Is(err, ErrRateLimited) = false, err=%v", err)
	}
	retryIn, ok := RetryIn(err)
	if !ok {
		t.Fatal("RetryIn(err) ok = false, want true")
	}
	if retryIn != 5*time.Second {
		t.Fatalf("RetryIn(err) = %s, want %s", retryIn, 5*time.Second)
	}
}
