//go:build real_e2e

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/repohost"
	repogithub "github.com/pmenglund/colin/internal/repohost/github"
	lineartracker "github.com/pmenglund/colin/internal/tracker/linear"
	"github.com/pmenglund/colin/internal/workspace"
)

const (
	realE2ELinearEndpoint = "https://api.linear.app/graphql"
	realE2EProjectSlug    = "1dc7fb4f7e89"
	realE2ERepoFullName   = "pmenglund/colin-test"
	realE2EBaseRef        = "main"
	realE2EIssuePrefix    = "E2E "
	realE2ELabelName      = "e2e"
)

func TestServiceRunsRealIssueWorkflowEndToEnd(t *testing.T) {
	apiKey := strings.TrimSpace(os.Getenv("LINEAR_API_KEY"))
	if apiKey == "" {
		t.Skip("LINEAR_API_KEY is required for real_e2e")
	}
	requireRealE2ECommand(t, "git", "git is required for real_e2e")
	requireRealE2ECommand(t, "codex", "codex is required for real_e2e")
	requireGitHubToken(t)

	runID := fmt.Sprintf("real-e2e-%d", time.Now().UTC().UnixNano())
	tempDir := t.TempDir()
	t.Setenv("COLIN_REAL_E2E_WORKSPACE_ROOT", filepath.Join(tempDir, "workspaces"))
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	t.Setenv("GIT_SSH_COMMAND", "ssh -o BatchMode=yes")
	t.Logf("starting real e2e run_id=%s", runID)

	workflowPath := realE2EWorkflowPath(t)
	t.Logf("using workflow=%s", workflowPath)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	svc, err := New(logger, workflowPath)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- svc.Run(ctx)
	}()

	waitForRealCondition(t, "dashboard startup", 30*time.Second, func() (bool, error) {
		select {
		case err := <-errCh:
			return false, fmt.Errorf("service exited before ready: %w", err)
		default:
		}
		return svc.DashboardURL() != "", nil
	})
	t.Logf("dashboard ready at %s", svc.DashboardURL())

	linearClient := newRealE2ELinearClient(t, apiKey)
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	canceled, err := linearClient.cancelLingeringE2EIssues(cleanupCtx)
	if err != nil {
		cleanupCancel()
		t.Fatalf("cancelLingeringE2EIssues(startup) error = %v", err)
	}
	cleanupCancel()
	t.Logf("startup cleanup canceled %d lingering E2E issues", canceled)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cleanupCancel()
		canceled, err := linearClient.cancelLingeringE2EIssues(cleanupCtx)
		if err != nil {
			t.Logf("cancelLingeringE2EIssues(cleanup) error = %v", err)
			return
		}
		t.Logf("cleanup canceled %d lingering E2E issues", canceled)
	})

	project, err := linearClient.lookupProjectInfo(ctx, realE2EProjectSlug)
	if err != nil {
		t.Fatalf("lookupProjectInfo() error = %v", err)
	}
	t.Logf("resolved Linear project=%s team=%s", project.ProjectID, project.TeamID)
	backlogStateID, err := linearClient.lookupTeamStateID(ctx, project.TeamID, "Backlog")
	if err != nil {
		t.Fatalf("lookupTeamStateID(Backlog) error = %v", err)
	}
	labelID, err := linearClient.ensureIssueLabelID(ctx, project.TeamID, realE2ELabelName)
	if err != nil {
		t.Fatalf("ensureIssueLabelID(%s) error = %v", realE2ELabelName, err)
	}
	t.Logf("resolved Linear label %s=%s", realE2ELabelName, labelID)

	task1, err := linearClient.createIssue(ctx, realE2EIssueCreateInput{
		TeamID:      project.TeamID,
		ProjectID:   project.ProjectID,
		StateID:     backlogStateID,
		LabelIDs:    []string{labelID},
		Title:       fmt.Sprintf("E2E %s: implement the intentionally omitted change for this run", runID),
		Description: underspecifiedIssueDescription(runID),
	})
	if err != nil {
		t.Fatalf("createIssue(task1) error = %v", err)
	}
	task2, err := linearClient.createIssue(ctx, realE2EIssueCreateInput{
		TeamID:      project.TeamID,
		ProjectID:   project.ProjectID,
		StateID:     backlogStateID,
		LabelIDs:    []string{labelID},
		Title:       fmt.Sprintf("E2E %s: add a run marker artifact to colin-test", runID),
		Description: wellSpecifiedIssueDescription(runID),
	})
	if err != nil {
		t.Fatalf("createIssue(task2) error = %v", err)
	}
	t.Logf("created issues task1=%s (%s) task2=%s (%s)", task1.Identifier, task1.ID, task2.Identifier, task2.ID)
	if err := linearClient.createBlocksRelation(ctx, task2.ID, task1.ID); err != nil {
		t.Fatalf("createBlocksRelation() error = %v", err)
	}
	t.Logf("linked blocker: %s blocks %s", task2.Identifier, task1.Identifier)
	if !issueHasLabel(task1, realE2ELabelName) || !issueHasLabel(task2, realE2ELabelName) {
		t.Fatalf("expected both issues to include label %q: task1=%v task2=%v", realE2ELabelName, task1.Labels, task2.Labels)
	}

	if err := linearClient.tracker.UpdateIssueState(ctx, task1.ID, "Todo"); err != nil {
		t.Fatalf("UpdateIssueState(task1, Todo) error = %v", err)
	}
	t.Logf("moved blocked issue %s to Todo", task1.Identifier)

	waitForRealCondition(t, "blocked issue remains idle before unblock", 20*time.Second, func() (bool, error) {
		issue, err := linearClient.fetchIssue(ctx, task1.ID)
		if err != nil {
			return false, err
		}
		if issue.State != "Todo" {
			return false, fmt.Errorf("blocked task state = %q, want Todo before unblocking", issue.State)
		}
		if issueHasColinComment(issue, "") {
			return false, fmt.Errorf("blocked task received Colin comments before blocker cleared")
		}
		return true, nil
	})

	if exists, err := githubBranchExists(ctx, expectedBranchName(task1)); err != nil {
		t.Fatalf("githubBranchExists(task1) error = %v", err)
	} else if exists {
		t.Fatalf("branch %q already exists before task1 is unblocked", expectedBranchName(task1))
	}
	if pr, err := githubPullRequestByHead(ctx, expectedBranchName(task1)); err != nil {
		t.Fatalf("githubPullRequestByHead(task1) error = %v", err)
	} else if pr != nil {
		t.Fatalf("unexpected PR for blocked task before unblocking: #%d", pr.Number)
	}
	t.Logf("confirmed blocked issue %s was not dispatched", task1.Identifier)

	if err := linearClient.tracker.UpdateIssueState(ctx, task2.ID, "Todo"); err != nil {
		t.Fatalf("UpdateIssueState(task2, Todo) error = %v", err)
	}
	t.Logf("moved implementable issue %s to Todo", task2.Identifier)

	waitForRealCondition(t, "implementable issue reaches In Progress", 2*time.Minute, func() (bool, error) {
		issue, err := linearClient.fetchIssue(ctx, task2.ID)
		if err != nil {
			return false, err
		}
		return issue.State == "In Progress", nil
	})
	t.Logf("implementable issue %s reached In Progress", task2.Identifier)

	waitForRealCondition(t, "implementable issue reaches publish handoff with PR", 20*time.Minute, func() (bool, error) {
		issue, err := linearClient.fetchIssue(ctx, task2.ID)
		if err != nil {
			return false, err
		}
		pr, err := githubPullRequestByHead(ctx, expectedBranchName(issue))
		if err != nil {
			return false, err
		}
		if pr == nil {
			return false, nil
		}
		return isPublishOrPostPublishState(issue.State), nil
	})

	task2, err = linearClient.fetchIssue(ctx, task2.ID)
	if err != nil {
		t.Fatalf("fetchIssue(task2) error = %v", err)
	}
	pr, err := githubPullRequestByHead(ctx, expectedBranchName(task2))
	if err != nil {
		t.Fatalf("githubPullRequestByHead(task2) error = %v", err)
	}
	if pr == nil {
		t.Fatal("expected PR for task2")
	}
	t.Logf("implementable issue %s reached %s with PR #%d %s", task2.Identifier, task2.State, pr.Number, pr.URL)
	if !strings.Contains(pr.Body, "Generated by Colin for "+task2.Identifier+".") {
		t.Fatalf("task2 PR body = %q, want custom workflow template marker", pr.Body)
	}

	switch task2.State {
	case "Review":
		if err := linearClient.tracker.UpdateIssueState(ctx, task2.ID, "Merge"); err != nil {
			t.Fatalf("UpdateIssueState(task2, Merge) error = %v", err)
		}
		t.Logf("moved implementable issue %s to Merge", task2.Identifier)
	case "Merge", "Merged":
		t.Logf("implementable issue %s already advanced to %s; skipping manual move to Merge", task2.Identifier, task2.State)
	default:
		t.Fatalf("implementable issue reached unexpected state %q after PR creation", task2.State)
	}

	waitForRealCondition(t, "implementable issue PR merges", 20*time.Minute, func() (bool, error) {
		issue, err := linearClient.fetchIssue(ctx, task2.ID)
		if err != nil {
			return false, err
		}
		pr, err := githubPullRequestByHead(ctx, expectedBranchName(issue))
		if err != nil {
			return false, err
		}
		return pr != nil && strings.EqualFold(pr.State, "MERGED"), nil
	})

	expectedPostMergeState, hasPostMergeState, err := linearClient.tracker.ResolveGitAutomationState(ctx, task2.ID, "merge", realE2EBaseRef)
	if err != nil {
		t.Fatalf("ResolveGitAutomationState() error = %v", err)
	}
	waitForRealCondition(t, "implementable issue reaches post-merge state", 2*time.Minute, func() (bool, error) {
		issue, err := linearClient.fetchIssue(ctx, task2.ID)
		if err != nil {
			return false, err
		}
		if hasPostMergeState {
			return issue.State == expectedPostMergeState, nil
		}
		return issue.State == "Merge", nil
	})
	if hasPostMergeState {
		t.Logf("implementable issue %s reached post-merge state %s", task2.Identifier, expectedPostMergeState)
	} else {
		t.Logf("implementable issue %s remained in Merge after PR merge", task2.Identifier)
	}

	waitForRealCondition(t, "underspecified issue reaches Refine without PR", 20*time.Minute, func() (bool, error) {
		issue, err := linearClient.fetchIssue(ctx, task1.ID)
		if err != nil {
			return false, err
		}
		if issue.State != "Refine" {
			return false, nil
		}
		if !issueHasColinComment(issue, "The spec should be improved before implementation.") {
			return false, nil
		}
		if issueHasColinComment(issue, "<!-- colin:review_publish=") {
			return false, fatalWaitErrorf("task1 clarification comment still contains legacy review_publish marker")
		}
		pr, err := githubPullRequestByHead(ctx, expectedBranchName(issue))
		if err != nil {
			return false, err
		}
		if pr != nil {
			return false, fatalWaitErrorf("task1 unexpectedly created PR #%d", pr.Number)
		}
		exists, err := githubBranchExists(ctx, expectedBranchName(issue))
		if err != nil {
			return false, err
		}
		if exists {
			return false, fatalWaitErrorf("task1 unexpectedly created branch %q", expectedBranchName(issue))
		}
		return true, nil
	})
	t.Logf("underspecified issue %s reached Refine with clarification comment and no PR", task1.Identifier)

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		t.Log("service shut down cleanly")
	case <-time.After(10 * time.Second):
		t.Fatal("service did not stop after cancellation")
	}
}

type realE2EProjectInfo struct {
	ProjectID string
	TeamID    string
}

type realE2EIssueCreateInput struct {
	TeamID      string
	ProjectID   string
	StateID     string
	LabelIDs    []string
	Title       string
	Description string
}

type realE2EIssue struct {
	ID         string
	Identifier string
	Title      string
	State      string
	URL        string
	BranchName string
	Labels     []string
	Comments   []realE2EComment
}

type realE2EComment struct {
	ID       string
	Body     string
	ParentID string
}

type realE2ELinearClient struct {
	apiKey   string
	endpoint string
	client   *http.Client
	tracker  *lineartracker.Client
}

func newRealE2ELinearClient(t *testing.T, apiKey string) *realE2ELinearClient {
	t.Helper()

	trackerClient, err := lineartracker.New(domain.ServiceConfig{
		Tracker: domain.TrackerConfig{
			Kind:        "linear",
			Endpoint:    realE2ELinearEndpoint,
			APIKey:      apiKey,
			ProjectSlug: realE2EProjectSlug,
		},
		Codex: domain.CodexConfig{
			Command: "codex app-server",
		},
	})
	if err != nil {
		t.Fatalf("lineartracker.New() error = %v", err)
	}

	return &realE2ELinearClient{
		apiKey:   apiKey,
		endpoint: realE2ELinearEndpoint,
		client:   &http.Client{Timeout: 30 * time.Second},
		tracker:  trackerClient,
	}
}

func (c *realE2ELinearClient) lookupProjectInfo(ctx context.Context, slug string) (realE2EProjectInfo, error) {
	const query = `
query ProjectInfo($slug: String!) {
  projects(first: 1, filter: { slugId: { eq: $slug } }) {
    nodes {
      id
      teams {
        nodes {
          id
        }
      }
    }
  }
}
`
	resp, err := c.doQuery(ctx, query, map[string]any{"slug": slug})
	if err != nil {
		return realE2EProjectInfo{}, err
	}
	nodes, ok := nestedSlice(resp, "data", "projects", "nodes")
	if !ok || len(nodes) == 0 {
		return realE2EProjectInfo{}, errors.New("project not found")
	}
	projectID, _ := stringValue(nodes[0]["id"])
	teamNodes, ok := nestedSlice(nodes[0], "teams", "nodes")
	if !ok || len(teamNodes) == 0 {
		return realE2EProjectInfo{}, errors.New("project has no teams")
	}
	teamID, _ := stringValue(teamNodes[0]["id"])
	if projectID == "" || teamID == "" {
		return realE2EProjectInfo{}, errors.New("project metadata incomplete")
	}
	return realE2EProjectInfo{ProjectID: projectID, TeamID: teamID}, nil
}

func (c *realE2ELinearClient) lookupTeamStateID(ctx context.Context, teamID string, stateName string) (string, error) {
	const query = `
query TeamStates($id: String!) {
  team(id: $id) {
    states {
      nodes {
        id
        name
      }
    }
  }
}
`
	resp, err := c.doQuery(ctx, query, map[string]any{"id": teamID})
	if err != nil {
		return "", err
	}
	nodes, ok := nestedSlice(resp, "data", "team", "states", "nodes")
	if !ok {
		return "", errors.New("team states not found")
	}
	for _, node := range nodes {
		name, _ := stringValue(node["name"])
		if !strings.EqualFold(strings.TrimSpace(name), strings.TrimSpace(stateName)) {
			continue
		}
		stateID, _ := stringValue(node["id"])
		if stateID != "" {
			return stateID, nil
		}
	}
	return "", fmt.Errorf("state %q not found", stateName)
}

func (c *realE2ELinearClient) ensureIssueLabelID(ctx context.Context, teamID string, labelName string) (string, error) {
	labelID, err := c.lookupIssueLabelID(ctx, teamID, labelName)
	if err != nil {
		return "", err
	}
	if labelID != "" {
		return labelID, nil
	}
	return c.createIssueLabel(ctx, teamID, labelName)
}

func (c *realE2ELinearClient) lookupIssueLabelID(ctx context.Context, teamID string, labelName string) (string, error) {
	const query = `
query IssueLabels($teamId: ID!, $name: String!) {
  issueLabels(first: 20, filter: { team: { id: { eq: $teamId } }, name: { eq: $name } }) {
    nodes {
      id
      name
    }
  }
}
`
	resp, err := c.doQuery(ctx, query, map[string]any{
		"teamId": teamID,
		"name":   labelName,
	})
	if err != nil {
		return "", err
	}
	nodes, ok := nestedSlice(resp, "data", "issueLabels", "nodes")
	if !ok {
		return "", errors.New("issue labels not found")
	}
	for _, node := range nodes {
		name, _ := stringValue(node["name"])
		if !strings.EqualFold(strings.TrimSpace(name), strings.TrimSpace(labelName)) {
			continue
		}
		labelID, _ := stringValue(node["id"])
		if strings.TrimSpace(labelID) != "" {
			return labelID, nil
		}
	}
	return "", nil
}

func (c *realE2ELinearClient) createIssueLabel(ctx context.Context, teamID string, labelName string) (string, error) {
	const query = `
mutation CreateIssueLabel($input: IssueLabelCreateInput!) {
  issueLabelCreate(input: $input) {
    success
    issueLabel {
      id
    }
  }
}
`
	resp, err := c.doQuery(ctx, query, map[string]any{
		"input": map[string]any{
			"teamId": teamID,
			"name":   labelName,
		},
	})
	if err != nil {
		return "", err
	}
	success, _ := nestedBool(resp, "data", "issueLabelCreate", "success")
	if !success {
		return "", errors.New("issueLabelCreate returned success=false")
	}
	labelID, ok := nestedString(resp, "data", "issueLabelCreate", "issueLabel", "id")
	if !ok || strings.TrimSpace(labelID) == "" {
		return "", errors.New("issueLabelCreate label missing")
	}
	return labelID, nil
}

func (c *realE2ELinearClient) createIssue(ctx context.Context, input realE2EIssueCreateInput) (realE2EIssue, error) {
	const query = `
mutation CreateIssue($input: IssueCreateInput!) {
  issueCreate(input: $input) {
    success
    issue {
      id
      identifier
      title
      url
      branchName
      state { name }
      labels { nodes { name } }
    }
  }
}
`
	variables := map[string]any{
		"teamId":      input.TeamID,
		"projectId":   input.ProjectID,
		"stateId":     input.StateID,
		"title":       input.Title,
		"description": input.Description,
	}
	if len(input.LabelIDs) > 0 {
		variables["labelIds"] = input.LabelIDs
	}
	resp, err := c.doQuery(ctx, query, map[string]any{
		"input": variables,
	})
	if err != nil {
		return realE2EIssue{}, err
	}
	success, _ := nestedBool(resp, "data", "issueCreate", "success")
	if !success {
		return realE2EIssue{}, errors.New("issueCreate returned success=false")
	}
	node, ok := nestedMap(resp, "data", "issueCreate", "issue")
	if !ok {
		return realE2EIssue{}, errors.New("issueCreate issue missing")
	}
	return parseRealE2EIssue(node), nil
}

func (c *realE2ELinearClient) createBlocksRelation(ctx context.Context, blockerIssueID string, blockedIssueID string) error {
	const query = `
mutation CreateIssueRelation($input: IssueRelationCreateInput!) {
  issueRelationCreate(input: $input) {
    success
  }
}
`
	resp, err := c.doQuery(ctx, query, map[string]any{
		"input": map[string]any{
			"type":           "blocks",
			"issueId":        blockerIssueID,
			"relatedIssueId": blockedIssueID,
		},
	})
	if err != nil {
		return err
	}
	success, _ := nestedBool(resp, "data", "issueRelationCreate", "success")
	if !success {
		return errors.New("issueRelationCreate returned success=false")
	}
	return nil
}

func (c *realE2ELinearClient) fetchIssue(ctx context.Context, issueID string) (realE2EIssue, error) {
	const query = `
query IssueForE2E($id: String!) {
  issue(id: $id) {
    id
    identifier
    title
    url
    branchName
    state { name }
    labels { nodes { name } }
    comments(first: 50) {
      nodes {
        id
        body
        parentId
        children(first: 50) {
          nodes {
            id
            body
            parentId
          }
        }
      }
    }
  }
}
`
	resp, err := c.doQuery(ctx, query, map[string]any{"id": issueID})
	if err != nil {
		return realE2EIssue{}, err
	}
	node, ok := nestedMap(resp, "data", "issue")
	if !ok {
		return realE2EIssue{}, errors.New("issue missing")
	}
	return parseRealE2EIssue(node), nil
}

func (c *realE2ELinearClient) cancelLingeringE2EIssues(ctx context.Context) (int, error) {
	issues, err := c.listProjectIssues(ctx, realE2EProjectSlug)
	if err != nil {
		return 0, err
	}
	canceled := 0
	var errs []error
	for _, issue := range issues {
		if !strings.HasPrefix(issue.Title, realE2EIssuePrefix) {
			continue
		}
		if isTerminalE2EState(issue.State) {
			continue
		}
		if err := c.tracker.UpdateIssueState(ctx, issue.ID, "Canceled"); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", issue.Identifier, err))
			continue
		}
		canceled++
	}
	return canceled, errors.Join(errs...)
}

func (c *realE2ELinearClient) listProjectIssues(ctx context.Context, projectSlug string) ([]realE2EIssue, error) {
	const query = `
query ProjectIssues($slug: String!, $after: String) {
  issues(first: 100, after: $after, filter: { project: { slugId: { eq: $slug } } }) {
    pageInfo {
      hasNextPage
      endCursor
    }
    nodes {
      id
      identifier
      title
      url
      branchName
      state { name }
      labels { nodes { name } }
    }
  }
}
`

	var (
		after *string
		out   []realE2EIssue
	)
	for {
		resp, err := c.doQuery(ctx, query, map[string]any{
			"slug":  projectSlug,
			"after": after,
		})
		if err != nil {
			return nil, err
		}
		nodes, ok := nestedSlice(resp, "data", "issues", "nodes")
		if !ok {
			return nil, errors.New("project issues missing")
		}
		for _, node := range nodes {
			out = append(out, parseRealE2EIssue(node))
		}
		hasNextPage, _ := nestedBool(resp, "data", "issues", "pageInfo", "hasNextPage")
		if !hasNextPage {
			return out, nil
		}
		endCursor, ok := nestedString(resp, "data", "issues", "pageInfo", "endCursor")
		if !ok || strings.TrimSpace(endCursor) == "" {
			return nil, errors.New("missing endCursor")
		}
		after = &endCursor
	}
}

func (c *realE2ELinearClient) doQuery(ctx context.Context, query string, variables map[string]any) (map[string]any, error) {
	body := map[string]any{
		"query":     query,
		"variables": variables,
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("linear status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil, err
	}
	if errorsField, ok := decoded["errors"]; ok && errorsField != nil {
		return nil, fmt.Errorf("linear graphql errors: %v", errorsField)
	}
	return decoded, nil
}

func parseRealE2EIssue(node map[string]any) realE2EIssue {
	issue := realE2EIssue{}
	issue.ID, _ = stringValue(node["id"])
	issue.Identifier, _ = stringValue(node["identifier"])
	issue.Title, _ = stringValue(node["title"])
	issue.URL, _ = stringValue(node["url"])
	issue.BranchName, _ = stringValue(node["branchName"])
	issue.State, _ = nestedString(node, "state", "name")
	if labelNodes, ok := nestedSlice(node, "labels", "nodes"); ok {
		for _, label := range labelNodes {
			if name, ok := stringValue(label["name"]); ok && strings.TrimSpace(name) != "" {
				issue.Labels = append(issue.Labels, name)
			}
		}
	}
	if comments, ok := nestedSlice(node, "comments", "nodes"); ok {
		seen := map[string]struct{}{}
		for _, item := range comments {
			if comment, ok := parseRealE2EComment(item); ok {
				if _, exists := seen[comment.ID]; !exists {
					issue.Comments = append(issue.Comments, comment)
					seen[comment.ID] = struct{}{}
				}
			}
			if children, ok := nestedSlice(item, "children", "nodes"); ok {
				for _, child := range children {
					if comment, ok := parseRealE2EComment(child); ok {
						if _, exists := seen[comment.ID]; !exists {
							issue.Comments = append(issue.Comments, comment)
							seen[comment.ID] = struct{}{}
						}
					}
				}
			}
		}
	}
	return issue
}

func parseRealE2EComment(node map[string]any) (realE2EComment, bool) {
	id, _ := stringValue(node["id"])
	body, _ := stringValue(node["body"])
	if id == "" {
		return realE2EComment{}, false
	}
	parentID, _ := stringValue(node["parentId"])
	return realE2EComment{ID: id, Body: body, ParentID: parentID}, true
}

func issueHasColinComment(issue realE2EIssue, contains string) bool {
	for _, comment := range issue.Comments {
		body := strings.TrimSpace(comment.Body)
		if !strings.HasPrefix(strings.ToLower(body), "[colin]") {
			continue
		}
		if contains == "" || strings.Contains(body, contains) {
			return true
		}
	}
	return false
}

func issueHasLabel(issue realE2EIssue, label string) bool {
	for _, item := range issue.Labels {
		if strings.EqualFold(strings.TrimSpace(item), strings.TrimSpace(label)) {
			return true
		}
	}
	return false
}

func expectedBranchName(issue realE2EIssue) string {
	branch := strings.TrimSpace(issue.BranchName)
	if branch == "" {
		branch = "colin/" + issue.Title
	}
	return workspace.SanitizeBranchName(branch)
}

func isPublishOrPostPublishState(state string) bool {
	switch strings.TrimSpace(state) {
	case "Review", "Merge", "Merged":
		return true
	default:
		return false
	}
}

func githubPullRequestByHead(ctx context.Context, branch string) (*repohost.PullRequest, error) {
	client, err := realE2EGitHubClient()
	if err != nil {
		return nil, err
	}
	owner, repo := realE2ERepoOwnerAndName()
	return client.PullRequestByHead(ctx, owner, repo, branch, "")
}

func githubBranchExists(ctx context.Context, branch string) (bool, error) {
	client, err := realE2EGitHubClient()
	if err != nil {
		return false, err
	}
	owner, repo := realE2ERepoOwnerAndName()
	return client.BranchExists(ctx, owner, repo, branch)
}

func waitForRealCondition(t *testing.T, label string, timeout time.Duration, condition func() (bool, error)) {
	t.Helper()

	if strings.TrimSpace(label) == "" {
		label = "condition"
	}
	t.Logf("waiting for %s (timeout=%s)", label, timeout)
	deadline := time.Now().Add(timeout)
	var lastErr error
	lastLog := time.Time{}
	for time.Now().Before(deadline) {
		ok, err := condition()
		if err != nil {
			var fatal *realE2EWaitFatalError
			if errors.As(err, &fatal) {
				t.Fatalf("%s failed: %v", label, fatal.error)
			}
			lastErr = err
			t.Logf("%s: transient error: %v", label, err)
		}
		if ok {
			t.Logf("%s: complete", label)
			return
		}
		if lastLog.IsZero() || time.Since(lastLog) >= 30*time.Second {
			t.Logf("%s: still waiting", label)
			lastLog = time.Now()
		}
		time.Sleep(5 * time.Second)
	}
	if lastErr != nil {
		t.Fatalf("%s not met before timeout: %v", label, lastErr)
	}
	t.Fatalf("%s not met before timeout", label)
}

type realE2EWaitFatalError struct {
	error error
}

func (e *realE2EWaitFatalError) Error() string {
	if e == nil || e.error == nil {
		return ""
	}
	return e.error.Error()
}

func fatalWaitErrorf(format string, args ...any) error {
	return &realE2EWaitFatalError{error: fmt.Errorf(format, args...)}
}

func requireRealE2ECommand(t *testing.T, name string, reason string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skip(reason)
	}
}

func requireGitHubToken(t *testing.T) {
	t.Helper()
	if strings.TrimSpace(os.Getenv("GITHUB_TOKEN")) == "" && strings.TrimSpace(os.Getenv("GH_TOKEN")) == "" {
		t.Skip("GITHUB_TOKEN or GH_TOKEN is required for real_e2e")
	}
}

func realE2EGitHubClient() (repohost.Client, error) {
	return repogithub.NewClientFromConfig(domain.ServiceConfig{
		Repo: domain.RepoConfig{
			APIToken: firstNonEmpty(os.Getenv("GITHUB_TOKEN"), os.Getenv("GH_TOKEN")),
		},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func realE2ERepoOwnerAndName() (string, string) {
	parts := strings.SplitN(realE2ERepoFullName, "/", 2)
	if len(parts) != 2 {
		return realE2ERepoFullName, ""
	}
	return parts[0], parts[1]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func realE2EWorkflowPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "e2e", "real", "WORKFLOW.md")
}

func underspecifiedIssueDescription(runID string) string {
	return strings.TrimSpace(fmt.Sprintf(`
This is a fresh real Colin e2e run with run ID %s.

The actual repository change for this run is intentionally omitted.

Do not choose a useful improvement on your own.
Do not infer a feature, fix, or refactor from the existing repository.
Do not make any repository changes, branch, or pull request unless the exact requested behavior is explicitly specified.

This request is intentionally underspecified. The correct outcome for this issue is to stop, explain exactly what information is missing, and ask for a better spec.

Do not treat prior implementation or older e2e artifacts as completion for this run.
`, runID))
}

func wellSpecifiedIssueDescription(runID string) string {
	return strings.TrimSpace(fmt.Sprintf(`
This is a fresh real Colin e2e run with run ID %s.

Implement exactly these run-specific changes:

1. Create the file runs/%s.md with this exact content:

# Colin Real E2E Run %s

Repository: pmenglund/colin-test
Run ID: %s

This file was created by Colin during a real end-to-end test.

2. Ensure README.md contains a section named "## Real E2E Runs". Create the section if it does not already exist.
3. Add a bullet "- %s" under that section.

Do not treat earlier runs or pre-existing implementation as completion for this run. This run is only complete when the new file and README entry for %s both exist.
`, runID, runID, runID, runID, runID, runID))
}

func runCommand(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s %v: %w: %s", name, args, err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func nestedSlice(root map[string]any, keys ...string) ([]map[string]any, bool) {
	value, ok := nestedValue(root, keys...)
	if !ok || value == nil {
		return nil, false
	}
	raw, ok := value.([]any)
	if !ok {
		return nil, false
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		asMap, ok := item.(map[string]any)
		if ok {
			out = append(out, asMap)
		}
	}
	return out, true
}

func nestedMap(root map[string]any, keys ...string) (map[string]any, bool) {
	value, ok := nestedValue(root, keys...)
	if !ok || value == nil {
		return nil, false
	}
	asMap, ok := value.(map[string]any)
	return asMap, ok
}

func nestedString(root map[string]any, keys ...string) (string, bool) {
	value, ok := nestedValue(root, keys...)
	if !ok {
		return "", false
	}
	return stringValue(value)
}

func nestedBool(root map[string]any, keys ...string) (bool, bool) {
	value, ok := nestedValue(root, keys...)
	if !ok {
		return false, false
	}
	b, ok := value.(bool)
	return b, ok
}

func nestedValue(root map[string]any, keys ...string) (any, bool) {
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
	return current, true
}

func stringValue(value any) (string, bool) {
	v, ok := value.(string)
	return v, ok
}

func isTerminalE2EState(state string) bool {
	switch strings.TrimSpace(strings.ToLower(state)) {
	case "done", "merged", "canceled", "duplicate":
		return true
	default:
		return false
	}
}
