package app

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/ui"
)

// SnapshotProvider returns the current dashboard snapshot for the request.
type SnapshotProvider func(context.Context) (domain.Snapshot, error)

// NewServer returns a self-contained dashboard server with demo data for tests and previews.
func NewServer() (http.Handler, error) {
	return NewObservabilityServer(newDemoSnapshotSource().Snapshot)
}

// NewObservabilityServer returns the embedded dashboard and JSON state API.
func NewObservabilityServer(provider SnapshotProvider) (http.Handler, error) {
	if provider == nil {
		provider = func(context.Context) (domain.Snapshot, error) {
			return domain.Snapshot{GeneratedAt: time.Now().UTC(), Counts: map[string]int{}}, nil
		}
	}

	assets, err := fs.Sub(embeddedAssets, "assets")
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	mux.Handle("/assets/", cacheControl("public, max-age=3600", http.StripPrefix("/assets/", http.FileServerFS(assets))))
	mux.HandleFunc("/api/v1/state", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		snapshot, err := provider(r.Context())
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if r.Method == http.MethodHead {
			return
		}
		if err := json.NewEncoder(w).Encode(snapshot); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		snapshot, err := provider(r.Context())
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err)
			return
		}

		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.Method == http.MethodHead {
			return
		}

		if isHXRequest(r) {
			if err := ui.Dashboard(snapshot).Render(w); err != nil && !errors.Is(err, context.Canceled) {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}
		if err := ui.Page(snapshot, time.Now().UTC()).Render(w); err != nil && !errors.Is(err, context.Canceled) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})

	return secureHeaders(mux), nil
}

func isHXRequest(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}

func cacheControl(value string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", value)
		next.ServeHTTP(w, r)
	})
}

func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

func writeJSONError(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

type demoSnapshotSource struct {
	requests atomic.Int64
}

func newDemoSnapshotSource() *demoSnapshotSource {
	return &demoSnapshotSource{}
}

func (s *demoSnapshotSource) Snapshot(context.Context) (domain.Snapshot, error) {
	request := s.requests.Add(1)
	now := time.Now().UTC()
	issueURL := "https://linear.app/example/issue/COLIN-7"

	return domain.Snapshot{
		GeneratedAt: now,
		Counts: map[string]int{
			"running":  1,
			"retrying": 1,
		},
		IssueStates: map[string]int{
			"Todo":        8,
			"In Progress": 3,
			"Review":      2,
			"Merge":       1,
			"Done":        14,
		},
		Running: []domain.SnapshotRunning{
			{
				IssueID:      "issue-demo-1",
				Identifier:   "COLIN-7",
				Title:        "Render live dashboard cards",
				URL:          &issueURL,
				State:        "In Progress",
				SessionID:    "session-demo",
				TurnCount:    int(request),
				LastEvent:    "turn_completed",
				LastMessage:  "Refreshed the task view fragment.",
				StartedAt:    now.Add(-7 * time.Minute),
				LastEventAt:  ptrTime(now.Add(-2 * time.Second)),
				InputTokens:  3200,
				OutputTokens: 1800,
				TotalTokens:  5000,
				OutputLog: []domain.OutputLog{
					{Timestamp: now.Add(-25 * time.Second), Event: "session_started", Message: "session started"},
					{Timestamp: now.Add(-12 * time.Second), Event: "other_message", Message: "Inspecting the orchestrator snapshot path."},
					{Timestamp: now.Add(-2 * time.Second), Event: "turn_completed", Message: "Refreshed the task view fragment."},
				},
			},
		},
		Retrying: []domain.RetryEntry{
			{
				IssueID:    "issue-demo-2",
				Identifier: "COLIN-11",
				Attempt:    2,
				DueAt:      now.Add(42 * time.Second),
				Error:      "waiting for a fresh workspace lock",
			},
		},
		CodexTotals: domain.Totals{
			InputTokens:    3200,
			OutputTokens:   1800,
			TotalTokens:    5000,
			SecondsRunning: 420,
		},
		RateLimits: map[string]any{
			"requests_remaining": 17,
			"reset_at":           now.Add(3 * time.Minute).Format(time.RFC3339),
		},
	}, nil
}

func ptrTime(value time.Time) *time.Time {
	return &value
}
