package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/ui"
)

// SnapshotProvider returns the current dashboard snapshot for the request.
type SnapshotProvider func(context.Context) (domain.Snapshot, error)

// IssueProvider returns the current issue snapshot for a single tracker issue.
type IssueProvider func(context.Context, string) (domain.Issue, error)

// LogProvider returns the current buffered internal logs for the request.
type LogProvider func(context.Context, *slog.Level) (domain.BufferedLogSnapshot, error)

// FunnelSetupProvider returns the current Funnel readiness snapshot.
type FunnelSetupProvider func(context.Context) (domain.FunnelSetupStatus, error)

// NewServer returns a self-contained dashboard server with demo data for tests and previews.
func NewServer() (http.Handler, error) {
	source := newDemoSnapshotSource()
	return NewObservabilityServer(source.Snapshot, source.Issue, source.FunnelSetup, nil, nil, nil)
}

// NewObservabilityServer returns the embedded dashboard and JSON state API.
func NewObservabilityServer(provider SnapshotProvider, issueProvider IssueProvider, setupProvider FunnelSetupProvider, logProvider LogProvider, linearSecretProvider LinearWebhookSecretProvider, logger *slog.Logger) (http.Handler, error) {
	if provider == nil {
		provider = func(context.Context) (domain.Snapshot, error) {
			return domain.Snapshot{GeneratedAt: time.Now().UTC(), Counts: map[string]int{}}, nil
		}
	}
	if setupProvider == nil {
		setupProvider = func(context.Context) (domain.FunnelSetupStatus, error) {
			now := time.Now().UTC()
			return domain.FunnelSetupStatus{
				GeneratedAt: now,
			}, nil
		}
	}
	if logProvider == nil {
		logProvider = func(context.Context, *slog.Level) (domain.BufferedLogSnapshot, error) {
			return domain.BufferedLogSnapshot{}, nil
		}
	}

	assets, err := fs.Sub(embeddedAssets, "assets")
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	mux.Handle("/assets/", cacheControl("public, max-age=3600", http.StripPrefix("/assets/", http.FileServerFS(assets))))
	mux.HandleFunc("/api/v1/logs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		level, err := parseLogLevel(r.URL.Query().Get("level"))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, err)
			return
		}
		snapshot, err := logProvider(r.Context(), level)
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
	mux.HandleFunc("/api/v1/setup/funnel", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		status, err := setupProvider(r.Context())
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if r.Method == http.MethodHead {
			return
		}
		if err := json.NewEncoder(w).Encode(status); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})
	mux.HandleFunc("/setup/funnel", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		status, err := setupProvider(r.Context())
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.Method == http.MethodHead {
			return
		}
		if err := ui.FunnelSetupPage(status, time.Now().UTC()).Render(w); err != nil && !errors.Is(err, context.Canceled) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})
	mux.HandleFunc("/linear/issues/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		issueID, ok := domain.ParseColinMetadataPath(r.URL.EscapedPath())
		if !ok || issueProvider == nil {
			http.NotFound(w, r)
			return
		}
		snapshot, err := provider(r.Context())
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err)
			return
		}
		issue, err := issueProvider(r.Context(), issueID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		mergeLiveIssueOutput(&issue, snapshot)

		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.Method == http.MethodHead {
			return
		}
		if err := ui.IssueMetadataPage(issue, time.Now().UTC()).Render(w); err != nil && !errors.Is(err, context.Canceled) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})
	readyzHandler := func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if r.Method == http.MethodHead {
			return
		}
		if err := json.NewEncoder(w).Encode(map[string]string{"status": "ok"}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
	mux.HandleFunc("/webhooks/readyz", readyzHandler)
	mux.HandleFunc("/readyz", readyzHandler)
	mux.HandleFunc("/webhooks/linear", linearWebhookHandler(linearSecretProvider, logger))
	mux.HandleFunc("/linear", linearWebhookHandler(linearSecretProvider, logger))
	mux.HandleFunc("/webhooks/github", reservedWebhookHandler)
	mux.HandleFunc("/github", reservedWebhookHandler)
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

func reservedWebhookHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusNotImplemented)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error": "Webhook endpoint reserved, but not implemented yet.",
	})
}

func mergeLiveIssueOutput(issue *domain.Issue, snapshot domain.Snapshot) {
	if issue == nil {
		return
	}
	for _, entry := range snapshot.Running {
		if entry.IssueID != issue.ID {
			continue
		}
		if issue.ColinMetadata == nil {
			issue.ColinMetadata = &domain.ColinMetadata{}
		}
		issue.ColinMetadata.CodexOutput = append([]domain.OutputLog(nil), entry.OutputLog...)
		return
	}
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

func parseLogLevel(raw string) (*slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return nil, nil
	case "debug":
		level := slog.LevelDebug
		return &level, nil
	case "info":
		level := slog.LevelInfo
		return &level, nil
	case "warn":
		level := slog.LevelWarn
		return &level, nil
	case "error":
		level := slog.LevelError
		return &level, nil
	default:
		return nil, fmt.Errorf("invalid log level %q; want debug, info, warn, or error", raw)
	}
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
			"Refine":      1,
			"Review":      2,
			"Merge":       1,
			"Done":        14,
		},
		PausedIssueStates: map[string]domain.PausedStateSummary{
			"Review": {
				Count: 1,
				URL:   "https://linear.app/example/search?q=label%3Apaused+status%3A%22Review%22",
			},
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
			"primary": map[string]any{
				"resetsAt":           now.Add(5*time.Hour + 32*time.Minute).Unix(),
				"usedPercent":        5,
				"windowDurationMins": 300,
			},
			"secondary": map[string]any{
				"resetsAt":           now.Add(7 * 24 * time.Hour).Unix(),
				"usedPercent":        9,
				"windowDurationMins": 10080,
			},
			"linear_requests": map[string]any{
				"limit":         int64(100),
				"remaining":     int64(25),
				"resetsAt":      now.Add(5 * time.Minute).Unix(),
				"nextAllowedAt": now.Add(3 * time.Second).Unix(),
			},
		},
	}, nil
}

func (s *demoSnapshotSource) Issue(ctx context.Context, issueID string) (domain.Issue, error) {
	snapshot, err := s.Snapshot(ctx)
	if err != nil {
		return domain.Issue{}, err
	}
	for _, entry := range snapshot.Running {
		if entry.IssueID != issueID {
			continue
		}
		issue := domain.Issue{
			ID:         entry.IssueID,
			Identifier: entry.Identifier,
			Title:      entry.Title,
			State:      entry.State,
			URL:        entry.URL,
			ColinMetadata: &domain.ColinMetadata{
				LastRunType: "coding",
				LastOutcome: "ready_for_review",
				UpdatedAt:   ptrTime(snapshot.GeneratedAt),
			},
		}
		return issue, nil
	}
	return domain.Issue{}, errors.New("issue not found")
}

func (s *demoSnapshotSource) FunnelSetup(context.Context) (domain.FunnelSetupStatus, error) {
	now := time.Now().UTC()
	baseURL := "https://colin-demo.tail.example.ts.net"
	return domain.FunnelSetupStatus{
		GeneratedAt:       now,
		Ready:             true,
		PublicURLSource:   "funnel",
		LocalBaseURL:      "http://127.0.0.1:8888",
		LocalSetupURL:     "http://127.0.0.1:8888/setup/funnel",
		LocalReadyURL:     "http://127.0.0.1:8888/webhooks/readyz",
		PublicBaseURL:     baseURL,
		PublicReadyURL:    baseURL + "/webhooks/readyz",
		DetectedFunnelURL: baseURL,
		SuggestedCommand:  "tailscale funnel --bg --https=443 --set-path=/webhooks 8888",
		LinearWebhookURL:  baseURL + "/webhooks/linear",
		GitHubWebhookURL:  baseURL + "/webhooks/github",
		Checks: []domain.SetupCheck{
			{
				ID:        "tailscale_local_api",
				Label:     "Colin can reach the local Tailscale daemon",
				Status:    "ok",
				Detail:    "Connected to the local Tailscale daemon.",
				CheckedAt: now,
			},
			{
				ID:        "funnel_route",
				Label:     "Funnel proxies Colin at `/webhooks`",
				Status:    "ok",
				Detail:    "Detected `https://colin-demo.tail.example.ts.net` proxying Colin from `/webhooks`.",
				CheckedAt: now,
			},
		},
	}, nil
}

func ptrTime(value time.Time) *time.Time {
	return &value
}
