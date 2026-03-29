package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/githubutil"
	"github.com/pmenglund/colin/internal/orchestrator"
)

const statusIssueCacheTTL = 30 * time.Second

type statusPageData struct {
	GeneratedAt    time.Time           `json:"generated_at"`
	VisibleIssues  int                 `json:"visible_issues"`
	RunningCount   int                 `json:"running_count"`
	RetryingCount  int                 `json:"retrying_count"`
	PendingPRCount int                 `json:"pending_pr_count"`
	OpenPRCount    int                 `json:"open_pr_count"`
	DraftPRCount   int                 `json:"draft_pr_count"`
	Issues         []statusIssueView   `json:"issues"`
	Retrying       []domain.RetryEntry `json:"retrying"`
}

type statusIssueView struct {
	Identifier string              `json:"identifier"`
	Title      string              `json:"title"`
	State      string              `json:"state"`
	Runtime    string              `json:"runtime"`
	IssueURL   string              `json:"issue_url"`
	BranchName string              `json:"branch_name"`
	PRs        []statusPullRequest `json:"prs"`
}

type statusPullRequest struct {
	URL          string `json:"url"`
	Title        string `json:"title"`
	Status       string `json:"status"`
	StatusClass  string `json:"status_class"`
	Number       string `json:"number"`
	Branch       string `json:"branch"`
	TargetBranch string `json:"target_branch"`
	Repository   string `json:"repository"`
}

var statusPageTemplate = template.Must(template.New("status").Funcs(template.FuncMap{
	"formatTime": func(value time.Time) string {
		return value.Local().Format(time.RFC1123)
	},
	"hasPRMeta": func(pr statusPullRequest) bool {
		return strings.TrimSpace(pr.Repository) != "" || strings.TrimSpace(pr.Branch) != "" || strings.TrimSpace(pr.TargetBranch) != ""
	},
}).Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta http-equiv="refresh" content="15">
  <title>Colin Status</title>
  <style>
    :root {
      color-scheme: light;
      font-family: ui-sans-serif, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
      background: #f5f2ea;
      color: #1f2933;
    }
    body {
      margin: 0;
      background:
        radial-gradient(circle at top left, rgba(203, 147, 86, 0.18), transparent 28rem),
        linear-gradient(180deg, #fbf8f1 0%, #f1ede3 100%);
    }
    main {
      max-width: 78rem;
      margin: 0 auto;
      padding: 2rem 1rem 3rem;
    }
    h1, h2 {
      margin: 0 0 0.75rem;
      font-weight: 700;
    }
    p {
      margin: 0 0 1rem;
    }
    .metrics {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(10rem, 1fr));
      gap: 0.75rem;
      margin: 1.5rem 0;
    }
    .metric, .panel {
      background: rgba(255, 255, 255, 0.86);
      border: 1px solid rgba(31, 41, 51, 0.12);
      border-radius: 0.9rem;
      box-shadow: 0 10px 30px rgba(31, 41, 51, 0.06);
    }
    .metric {
      padding: 1rem;
    }
    .metric strong {
      display: block;
      font-size: 1.7rem;
      line-height: 1.1;
      margin-bottom: 0.25rem;
    }
    .panel {
      overflow: hidden;
    }
    table {
      width: 100%;
      border-collapse: collapse;
    }
    th, td {
      padding: 0.9rem 1rem;
      text-align: left;
      vertical-align: top;
      border-top: 1px solid rgba(31, 41, 51, 0.08);
    }
    thead th {
      border-top: 0;
      font-size: 0.85rem;
      letter-spacing: 0.04em;
      text-transform: uppercase;
      color: #52606d;
    }
    .issue-title {
      font-weight: 600;
      display: block;
      margin-top: 0.2rem;
    }
    .runtime, .pr-status {
      display: inline-block;
      padding: 0.2rem 0.55rem;
      border-radius: 999px;
      font-size: 0.78rem;
      font-weight: 700;
      text-transform: uppercase;
      letter-spacing: 0.04em;
    }
    .runtime-running, .pr-open {
      background: #d9f5e5;
      color: #146c43;
    }
    .runtime-retrying, .pr-draft, .pr-pending {
      background: #fff0c2;
      color: #8d5e00;
    }
    .runtime-idle {
      background: #e7edf3;
      color: #486581;
    }
    .pr-merged {
      background: #e0dbff;
      color: #4c35a2;
    }
    .pr-closed {
      background: #f3d6d6;
      color: #9b1c1c;
    }
    ul {
      margin: 0;
      padding-left: 1.1rem;
    }
    li + li {
      margin-top: 0.55rem;
    }
    .pr-meta, .muted {
      color: #52606d;
      font-size: 0.92rem;
    }
    .empty {
      color: #7b8794;
      font-style: italic;
    }
    a {
      color: #0f4c81;
    }
    @media (max-width: 720px) {
      table, thead, tbody, th, td, tr {
        display: block;
      }
      thead {
        display: none;
      }
      td {
        border-top: 0;
        padding-top: 0.25rem;
        padding-bottom: 0.75rem;
      }
      tr {
        border-top: 1px solid rgba(31, 41, 51, 0.08);
        padding-top: 0.8rem;
      }
      tr:first-child {
        border-top: 0;
      }
    }
  </style>
</head>
<body>
  <main>
    <h1>Colin Status</h1>
    <p>Updated {{formatTime .GeneratedAt}}</p>
    <div class="metrics">
      <div class="metric"><strong>{{.VisibleIssues}}</strong><span>Visible issues</span></div>
      <div class="metric"><strong>{{.RunningCount}}</strong><span>Running</span></div>
      <div class="metric"><strong>{{.RetryingCount}}</strong><span>Retrying</span></div>
      <div class="metric"><strong>{{.PendingPRCount}}</strong><span>Pending PRs</span></div>
      <div class="metric"><strong>{{.OpenPRCount}}</strong><span>Open PRs</span></div>
      <div class="metric"><strong>{{.DraftPRCount}}</strong><span>Draft PRs</span></div>
    </div>

    <section class="panel">
      <table>
        <thead>
          <tr>
            <th>Issue</th>
            <th>State</th>
            <th>Runtime</th>
            <th>Branch</th>
            <th>GitHub PRs</th>
          </tr>
        </thead>
        <tbody>
          {{range .Issues}}
          <tr>
            <td>
              {{if .IssueURL}}<a href="{{.IssueURL}}">{{.Identifier}}</a>{{else}}{{.Identifier}}{{end}}
              <span class="issue-title">{{.Title}}</span>
            </td>
            <td>{{.State}}</td>
            <td><span class="runtime runtime-{{.Runtime}}">{{.Runtime}}</span></td>
            <td>{{if .BranchName}}<code>{{.BranchName}}</code>{{else}}<span class="empty">none</span>{{end}}</td>
            <td>
              {{if .PRs}}
              <ul>
                {{range .PRs}}
                <li>
                  <div>
                    <a href="{{.URL}}">{{if .Number}}{{.Number}} {{end}}{{.Title}}</a>
                    <span class="pr-status pr-{{.StatusClass}}">{{.Status}}</span>
                  </div>
                  {{if hasPRMeta .}}
                  <div class="pr-meta">
                    {{if .Repository}}{{.Repository}}{{end}}
                    {{if and .Repository (or .Branch .TargetBranch)}} · {{end}}
                    {{if and .Branch .TargetBranch}}<code>{{.Branch}}</code> to <code>{{.TargetBranch}}</code>{{end}}
                    {{if and .Branch (not .TargetBranch)}}branch <code>{{.Branch}}</code>{{end}}
                    {{if and .TargetBranch (not .Branch)}}target <code>{{.TargetBranch}}</code>{{end}}
                  </div>
                  {{end}}
                </li>
                {{end}}
              </ul>
              {{else}}
              <span class="empty">No PRs</span>
              {{end}}
            </td>
          </tr>
          {{end}}
        </tbody>
      </table>
    </section>

    {{if .Retrying}}
    <section>
      <h2>Retry Queue</h2>
      <div class="panel">
        <table>
          <thead>
            <tr>
              <th>Issue</th>
              <th>Attempt</th>
              <th>Due</th>
              <th>Error</th>
            </tr>
          </thead>
          <tbody>
            {{range .Retrying}}
            <tr>
              <td>{{.Identifier}}</td>
              <td>{{.Attempt}}</td>
              <td>{{formatTime .DueAt}}</td>
              <td>{{if .Error}}{{.Error}}{{else}}<span class="muted">none</span>{{end}}</td>
            </tr>
            {{end}}
          </tbody>
        </table>
      </div>
    </section>
    {{end}}
  </main>
</body>
</html>`))

func (s *Service) startStatusServer(ctx context.Context) error {
	runtime := s.currentRuntime()
	if runtime.Config.Server.Port == nil {
		return nil
	}

	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprintf("%d", *runtime.Config.Server.Port)))
	if err != nil {
		return fmt.Errorf("start status server: %w", err)
	}

	server := &http.Server{Handler: s.statusHandler()}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.Canceled) {
			s.logger.Warn("status server shutdown failed", "error", err)
		}
	}()
	go func() {
		s.logger.Info("status server started", "address", listener.Addr().String())
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Error("status server failed", "error", err)
		}
	}()
	return nil
}

func (s *Service) statusHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleStatusPage)
	mux.HandleFunc("/api/status", getOnly(s.handleStatusAPI))
	mux.HandleFunc("/api/v1/state", getOnly(s.handleStateAPI))
	return mux
}

func getOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		next(w, r)
	}
}

func (s *Service) handleStatusPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	page, err := s.buildStatusPage(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := statusPageTemplate.Execute(w, page); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Service) handleStatusAPI(w http.ResponseWriter, r *http.Request) {
	page, err := s.buildStatusPage(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(page); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Service) handleStateAPI(w http.ResponseWriter, r *http.Request) {
	snapshot, err := s.orch.SnapshotContext(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(snapshot); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Service) buildStatusPage(ctx context.Context) (statusPageData, error) {
	snapshot, err := s.orch.SnapshotContext(ctx)
	if err != nil {
		return statusPageData{}, fmt.Errorf("read orchestrator snapshot: %w", err)
	}

	runtime := s.currentRuntime()
	issues, err := s.loadStatusIssues(ctx, runtime)
	if err != nil {
		return statusPageData{}, fmt.Errorf("read tracker issues: %w", err)
	}

	return buildStatusPage(snapshot, issues, runtime.Config.Tracker.ActiveStates), nil
}

func (s *Service) loadStatusIssues(ctx context.Context, runtime orchestrator.Runtime) ([]domain.Issue, error) {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()

	if !s.statusLoaded.IsZero() && time.Since(s.statusLoaded) < statusIssueCacheTTL {
		return s.statusIssues, nil
	}

	issues, err := runtime.Tracker.FetchIssuesByStates(ctx, nil)
	if err != nil {
		return nil, err
	}
	s.statusIssues = issues
	s.statusLoaded = time.Now()
	return s.statusIssues, nil
}

func buildStatusPage(snapshot domain.Snapshot, issues []domain.Issue, activeStates []string) statusPageData {
	runningByID := make(map[string]domain.SnapshotRunning, len(snapshot.Running))
	for _, entry := range snapshot.Running {
		runningByID[entry.IssueID] = entry
	}
	retryingByID := make(map[string]domain.RetryEntry, len(snapshot.Retrying))
	for _, entry := range snapshot.Retrying {
		retryingByID[entry.IssueID] = entry
	}

	data := statusPageData{
		GeneratedAt:   snapshot.GeneratedAt,
		RunningCount:  len(snapshot.Running),
		RetryingCount: len(snapshot.Retrying),
		Retrying:      append([]domain.RetryEntry(nil), snapshot.Retrying...),
	}

	for _, issue := range issues {
		_, running := runningByID[issue.ID]
		_, retrying := retryingByID[issue.ID]
		if !running && !retrying && !isVisibleIssue(issue, activeStates) {
			continue
		}

		view := statusIssueView{
			Identifier: issue.Identifier,
			Title:      issue.Title,
			State:      issue.State,
			Runtime:    "idle",
			IssueURL:   trimmed(issue.URL),
			BranchName: trimmed(issue.BranchName),
			PRs:        make([]statusPullRequest, 0, len(issue.PullRequests)),
		}
		switch {
		case running:
			view.Runtime = "running"
		case retrying:
			view.Runtime = "retrying"
		}

		for _, pr := range issue.PullRequests {
			prView := buildStatusPullRequest(pr)
			view.PRs = append(view.PRs, prView)
			if prView.StatusClass == "open" || prView.StatusClass == "draft" || prView.StatusClass == "pending" {
				data.PendingPRCount++
			}
			if prView.StatusClass == "open" {
				data.OpenPRCount++
			}
			if prView.StatusClass == "draft" {
				data.DraftPRCount++
			}
		}
		sort.Slice(view.PRs, func(i, j int) bool {
			return statusPullRequestLess(view.PRs[i], view.PRs[j])
		})
		data.Issues = append(data.Issues, view)
	}

	sort.Slice(data.Issues, func(i, j int) bool {
		left := data.Issues[i]
		right := data.Issues[j]
		if left.Runtime != right.Runtime {
			return runtimePriority(left.Runtime) < runtimePriority(right.Runtime)
		}
		return left.Identifier < right.Identifier
	})
	data.VisibleIssues = len(data.Issues)
	return data
}

func buildStatusPullRequest(pr domain.PullRequest) statusPullRequest {
	statusClass, statusText := pullRequestStatus(pr)
	url := pr.URL
	if canonicalURL, ok := githubutil.CanonicalPullRequestURL(url); ok {
		url = canonicalURL
	}
	title := strings.TrimSpace(pr.Title)
	if title == "" {
		title = url
	}

	repoLogin := pr.RepoLogin
	repoName := pr.RepoName
	numberValue := pr.Number
	if parsedLogin, parsedRepo, parsedNumber, ok := githubutil.ParsePullRequestURL(url); ok {
		if strings.TrimSpace(repoLogin) == "" {
			repoLogin = parsedLogin
		}
		if strings.TrimSpace(repoName) == "" {
			repoName = parsedRepo
		}
		if numberValue == nil {
			numberValue = &parsedNumber
		}
	}

	number := ""
	if numberValue != nil {
		number = fmt.Sprintf("#%d", *numberValue)
	}

	repository := ""
	if repoLogin != "" && repoName != "" {
		repository = repoLogin + "/" + repoName
	}

	return statusPullRequest{
		URL:          url,
		Title:        title,
		Status:       statusText,
		StatusClass:  statusClass,
		Number:       number,
		Branch:       pr.Branch,
		TargetBranch: pr.TargetBranch,
		Repository:   repository,
	}
}

func pullRequestStatus(pr domain.PullRequest) (string, string) {
	status := strings.ToLower(strings.TrimSpace(pr.Status))
	if pr.MergedAt != nil {
		return "merged", "merged"
	}
	if pr.ClosedAt != nil {
		return "closed", "closed"
	}
	switch {
	case status == "merged":
		return "merged", "merged"
	case status == "closed":
		return "closed", "closed"
	case pr.Draft || status == "draft":
		return "draft", "draft"
	case status == "open":
		return "open", "open"
	case status != "":
		return "pending", status
	default:
		return "pending", "pending"
	}
}

func statusPullRequestLess(left, right statusPullRequest) bool {
	leftRepository := strings.ToLower(strings.TrimSpace(left.Repository))
	rightRepository := strings.ToLower(strings.TrimSpace(right.Repository))
	if leftRepository != rightRepository {
		return leftRepository < rightRepository
	}

	leftNumber, leftHasNumber := statusPullRequestSortNumber(left)
	rightNumber, rightHasNumber := statusPullRequestSortNumber(right)
	if leftHasNumber != rightHasNumber {
		return leftHasNumber
	}
	if leftHasNumber && rightHasNumber && leftNumber != rightNumber {
		return leftNumber < rightNumber
	}

	if left.URL != right.URL {
		return left.URL < right.URL
	}
	if left.Title != right.Title {
		return left.Title < right.Title
	}
	if left.StatusClass != right.StatusClass {
		return left.StatusClass < right.StatusClass
	}
	return left.Status < right.Status
}

func statusPullRequestSortNumber(pr statusPullRequest) (int, bool) {
	if _, _, number, ok := githubutil.ParsePullRequestURL(pr.URL); ok {
		return number, true
	}

	raw := strings.TrimPrefix(strings.TrimSpace(pr.Number), "#")
	if raw == "" {
		return 0, false
	}
	number, err := strconv.Atoi(raw)
	if err != nil || number <= 0 {
		return 0, false
	}
	return number, true
}

func runtimePriority(value string) int {
	switch value {
	case "running":
		return 0
	case "retrying":
		return 1
	default:
		return 2
	}
}

func isVisibleIssue(issue domain.Issue, activeStates []string) bool {
	if hasActionablePullRequest(issue.PullRequests) {
		return true
	}
	for _, state := range activeStates {
		if strings.EqualFold(strings.TrimSpace(issue.State), strings.TrimSpace(state)) {
			return true
		}
	}
	return false
}

func hasActionablePullRequest(pullRequests []domain.PullRequest) bool {
	for _, pr := range pullRequests {
		statusClass, _ := pullRequestStatus(pr)
		switch statusClass {
		case "open", "draft", "pending":
			return true
		}
	}
	return false
}

func trimmed(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}
