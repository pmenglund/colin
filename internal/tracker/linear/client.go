package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pmenglund/colin/internal/config"
	"github.com/pmenglund/colin/internal/domain"
	"github.com/pmenglund/colin/internal/repohost"
	"github.com/pmenglund/colin/internal/tracker"
)

var (
	ErrAPIRequest              = errors.New("linear_api_request")
	ErrAPIStatus               = errors.New("linear_api_status")
	ErrGraphQLErrors           = errors.New("linear_graphql_errors")
	ErrUnknownPayload          = errors.New("linear_unknown_payload")
	ErrMissingEndCursor        = errors.New("linear_missing_end_cursor")
	ErrUnknownState            = errors.New("linear_unknown_state")
	ErrMissingWorkflowState    = errors.New("linear_missing_workflow_state")
	ErrCodexThreadNotFound     = errors.New("linear_codex_thread_not_found")
	ErrIssueIdentifierNotFound = errors.New("linear_issue_identifier_not_found")
)

type AmbiguousCodexThreadError struct {
	ThreadID         string
	IssueIdentifiers []string
}

func (e *AmbiguousCodexThreadError) Error() string {
	return fmt.Sprintf("codex thread %q is linked from multiple watched issues: %s", e.ThreadID, strings.Join(e.IssueIdentifiers, ", "))
}

const watchedIssuesQuery = `
query IssuesByCodexThreadID($projectIDs: [ID!], $after: String) {
  issues(
    first: 50
    after: $after
    filter: {
      project: { id: { in: $projectIDs } }
    }
  ) {
    pageInfo { hasNextPage endCursor }
    nodes {
      id
      identifier
      title
      description
      priority
      project {
        id
        slugId
      }
      team {
        id
      }
      branchName
      url
      createdAt
      updatedAt
      state { name }
      labels { nodes { name } }
      inverseRelations {
        nodes {
          type
          issue {
            id
            identifier
            state { name }
          }
        }
      }
      attachments(first: 50) {
        nodes {
          id
          title
          url
          createdAt
          updatedAt
          metadata
        }
      }
      comments(first: 50) {
        nodes {
          id
          body
          createdAt
          parentId
          children(first: 50) {
            nodes {
              id
              body
              createdAt
              parentId
            }
          }
        }
      }
      history(first: 100) {
        nodes {
          createdAt
          fromState { name }
          toState { name }
        }
      }
    }
  }
}
`

const (
	defaultEndpoint              = "https://api.linear.app/graphql"
	linearIssueBatchSize         = 250
	colinMetadataAttachmentTitle = "Colin metadata"
	colinMetadataURLPrefix       = "https://colin.invalid/linear/issues/"
	colinMetadataURLSuffix       = "/metadata"
	colinExecPlanAttachmentTitle = "Colin ExecPlan"
	colinExecPlanURLSuffix       = "/exec-plan"
	refineStateName              = "Refine"
)

type graphQLErrorResponse struct {
	raw []map[string]any
}

func (e *graphQLErrorResponse) Error() string {
	if e == nil {
		return ErrGraphQLErrors.Error()
	}
	return fmt.Sprintf("%s: %v", ErrGraphQLErrors, e.raw)
}

func (e *graphQLErrorResponse) Unwrap() error {
	return ErrGraphQLErrors
}

// ProjectSummary is the minimal project metadata used in setup selectors.
type ProjectSummary struct {
	Name      string
	Slug      string
	TeamNames []string
}

type linearActorIdentity struct {
	ID                    string
	Name                  string
	IsApp                 bool
	SupportsAgentSessions bool
}

func (a linearActorIdentity) Type() string {
	if a.IsApp {
		return "app"
	}
	return "user"
}

// Client is the Linear-backed implementation of the tracker.Client interface.
type Client struct {
	endpoint           string
	apiKey             string
	auth               authorizationProvider
	appMode            bool
	primaryProjectSlug string
	projectsByID       map[string]string
	watchedProjectIDs  []string
	active             []string
	repoAdapter        repohost.Adapter
	client             *http.Client
	uiBaseURL          string
	uiBaseURLResolver  func(context.Context) string
	rateMu             sync.RWMutex
	rateInfo           domain.RateLimitSnapshot
	labelMu            sync.RWMutex
	labelIDs           map[string]string
	actorMu            sync.RWMutex
	actorIdentity      *linearActorIdentity
}

// New constructs a Linear-backed tracker client from the current service config.
func New(cfg domain.ServiceConfig) (*Client, error) {
	if err := config.ValidateDispatch(cfg); err != nil {
		return nil, err
	}
	repoAdapter, err := repohost.Lookup(cfg.Repo.Backend)
	if err != nil {
		return nil, err
	}
	client, err := newConfiguredAPIClient(cfg)
	if err != nil {
		return nil, err
	}
	client.appMode = cfg.Tracker.AppMode
	client.primaryProjectSlug = cfg.Tracker.ProjectSlug
	client.active = slices.Clone(config.CandidateStates(cfg))
	client.repoAdapter = repoAdapter
	client.uiBaseURL = uiBaseURL(cfg.Server)
	return client, nil
}

// ValidateWorkflowStates validates the configured workflow states against Linear and refreshes watched project metadata.
func (c *Client) ValidateWorkflowStates(ctx context.Context, cfg domain.ServiceConfig) error {
	if c == nil {
		return errors.New("nil linear client")
	}
	projectIDs, projectsByID, err := c.validateWorkflowStates(ctx, cfg)
	if err != nil {
		return err
	}
	c.watchedProjectIDs = projectIDs
	c.projectsByID = projectsByID
	return nil
}

// ListProjects returns the caller's accessible Linear projects for setup-time selection.
func ListProjects(ctx context.Context, endpoint string, apiKey string) ([]ProjectSummary, error) {
	client := newStaticAPIClient(endpoint, apiKey)

	projectsBySlug := map[string]ProjectSummary{}
	if err := client.appendWorkspaceProjects(ctx, projectsBySlug); err != nil {
		return nil, err
	}
	if err := client.appendTeamProjects(ctx, projectsBySlug); err != nil {
		return nil, err
	}

	projects := make([]ProjectSummary, 0, len(projectsBySlug))
	for _, project := range projectsBySlug {
		projects = append(projects, project)
	}
	sort.Slice(projects, func(i, j int) bool {
		leftName := strings.ToLower(strings.TrimSpace(projects[i].Name))
		rightName := strings.ToLower(strings.TrimSpace(projects[j].Name))
		if leftName == rightName {
			return strings.ToLower(projects[i].Slug) < strings.ToLower(projects[j].Slug)
		}
		return leftName < rightName
	})
	return projects, nil
}

func (c *Client) appendWorkspaceProjects(ctx context.Context, projects map[string]ProjectSummary) error {
	const query = `
query ProjectList($after: String) {
  projects(first: 50, after: $after) {
    pageInfo {
      hasNextPage
      endCursor
    }
    nodes {
      name
      slugId
      teams(first: 10) {
        nodes {
          name
        }
      }
    }
  }
}
`

	var after any
	for {
		resp, err := c.doQuery(ctx, query, map[string]any{"after": after})
		if err != nil {
			return err
		}
		nodes, ok := nestedSlice(resp, "data", "projects", "nodes")
		if !ok {
			return ErrUnknownPayload
		}
		for _, node := range nodes {
			appendProjectSummary(projects, node)
		}

		hasNext, _ := nestedBool(resp, "data", "projects", "pageInfo", "hasNextPage")
		if !hasNext {
			break
		}
		endCursor, _ := nestedString(resp, "data", "projects", "pageInfo", "endCursor")
		if strings.TrimSpace(endCursor) == "" {
			return ErrMissingEndCursor
		}
		after = endCursor
	}
	return nil
}

func (c *Client) appendTeamProjects(ctx context.Context, projects map[string]ProjectSummary) error {
	const query = `
query TeamProjectList($after: String) {
  teams(first: 50, after: $after) {
    pageInfo {
      hasNextPage
      endCursor
    }
    nodes {
      id
      name
      children {
        id
        name
      }
    }
  }
}
`

	var after any
	seenTeamIDs := map[string]struct{}{}
	for {
		resp, err := c.doQuery(ctx, query, map[string]any{"after": after})
		if err != nil {
			return err
		}
		nodes, ok := nestedSlice(resp, "data", "teams", "nodes")
		if !ok {
			return ErrUnknownPayload
		}
		var teamIDs []string
		for _, teamNode := range nodes {
			teamID, _ := stringValue(teamNode["id"])
			teamIDs = appendTrimmedNonEmpty(teamIDs, teamID)
			if err := c.appendTeamAndChildProjects(ctx, projects, teamID, seenTeamIDs); err != nil {
				return err
			}
			if childNodes, ok := nestedSlice(teamNode, "children"); ok {
				for _, childNode := range childNodes {
					childID, _ := stringValue(childNode["id"])
					teamIDs = appendTrimmedNonEmpty(teamIDs, childID)
					if err := c.appendTeamAndChildProjects(ctx, projects, childID, seenTeamIDs); err != nil {
						return err
					}
				}
			}
		}
		if err := c.appendProjectsForTeamIDs(ctx, projects, teamIDs); err != nil {
			return err
		}

		hasNext, _ := nestedBool(resp, "data", "teams", "pageInfo", "hasNextPage")
		if !hasNext {
			break
		}
		endCursor, _ := nestedString(resp, "data", "teams", "pageInfo", "endCursor")
		if strings.TrimSpace(endCursor) == "" {
			return ErrMissingEndCursor
		}
		after = endCursor
	}
	return nil
}

func (c *Client) appendProjectsForTeamIDs(ctx context.Context, projects map[string]ProjectSummary, teamIDs []string) error {
	if len(teamIDs) == 0 {
		return nil
	}
	const query = `
query TeamFilteredProjectList($teamIDs: [ID!], $after: String) {
  projects(
    first: 50
    after: $after
    filter: {
      accessibleTeams: {
        some: {
          or: [
            { id: { in: $teamIDs } }
            { ancestors: { some: { id: { in: $teamIDs } } } }
          ]
        }
      }
    }
  ) {
    pageInfo {
      hasNextPage
      endCursor
    }
    nodes {
      name
      slugId
      teams(first: 10) {
        nodes {
          name
        }
      }
    }
  }
}
`

	var after any
	for {
		resp, err := c.doQuery(ctx, query, map[string]any{"teamIDs": teamIDs, "after": after})
		if err != nil {
			return err
		}
		nodes, ok := nestedSlice(resp, "data", "projects", "nodes")
		if !ok {
			return ErrUnknownPayload
		}
		for _, node := range nodes {
			appendProjectSummary(projects, node)
		}

		hasNext, _ := nestedBool(resp, "data", "projects", "pageInfo", "hasNextPage")
		if !hasNext {
			break
		}
		endCursor, _ := nestedString(resp, "data", "projects", "pageInfo", "endCursor")
		if strings.TrimSpace(endCursor) == "" {
			return ErrMissingEndCursor
		}
		after = endCursor
	}
	return nil
}

func (c *Client) appendTeamAndChildProjects(ctx context.Context, projects map[string]ProjectSummary, teamID string, seenTeamIDs map[string]struct{}) error {
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return ErrUnknownPayload
	}
	if _, ok := seenTeamIDs[teamID]; ok {
		return nil
	}
	seenTeamIDs[teamID] = struct{}{}

	if err := c.appendTeamProjectPages(ctx, projects, teamID, nil); err != nil {
		if isMissingTeamLookupError(err) {
			return nil
		}
		return err
	}
	if err := c.appendChildTeamProjects(ctx, projects, teamID, seenTeamIDs); err != nil {
		if isMissingTeamLookupError(err) {
			return nil
		}
		return err
	}
	return nil
}

func (c *Client) appendChildTeamProjects(ctx context.Context, projects map[string]ProjectSummary, teamID string, seenTeamIDs map[string]struct{}) error {
	const query = `
query TeamChildren($teamID: String!) {
  team(id: $teamID) {
    children {
      id
      name
    }
  }
}
`

	resp, err := c.doQuery(ctx, query, map[string]any{"teamID": teamID})
	if err != nil {
		return err
	}
	teamNode, ok := nestedMap(resp, "data", "team")
	if !ok {
		return ErrUnknownPayload
	}
	childNodes, ok := nestedSlice(teamNode, "children")
	if !ok {
		return ErrUnknownPayload
	}
	for _, childNode := range childNodes {
		childID, _ := stringValue(childNode["id"])
		if err := c.appendTeamAndChildProjects(ctx, projects, childID, seenTeamIDs); err != nil {
			return err
		}
	}
	return nil
}

func isMissingTeamLookupError(err error) bool {
	if err == nil || !errors.Is(err, ErrGraphQLErrors) {
		return false
	}
	var response *graphQLErrorResponse
	if !errors.As(err, &response) || len(response.raw) == 0 {
		return false
	}
	for _, item := range response.raw {
		if !isMissingTeamLookupEntry(item) {
			return false
		}
	}
	return true
}

func isMissingTeamLookupEntry(item map[string]any) bool {
	message, _ := stringValue(item["message"])
	message = strings.ToLower(strings.TrimSpace(message))
	if !strings.Contains(message, "team") || !strings.Contains(message, "not found") {
		return false
	}

	path, ok := item["path"].([]any)
	if !ok {
		return true
	}
	for _, part := range path {
		value, _ := stringValue(part)
		if value == "team" {
			return true
		}
	}
	return false
}

func (c *Client) appendTeamProjectPages(ctx context.Context, projects map[string]ProjectSummary, teamID string, after any) error {
	const query = `
query TeamProjectPage($teamID: String!, $after: String) {
  team(id: $teamID) {
    projects(first: 50, after: $after, includeSubTeams: true) {
      pageInfo {
        hasNextPage
        endCursor
      }
      nodes {
        name
        slugId
        teams(first: 10) {
          nodes {
            name
          }
        }
      }
    }
  }
}
`

	for {
		resp, err := c.doQuery(ctx, query, map[string]any{"teamID": teamID, "after": after})
		if err != nil {
			return err
		}
		teamNode, ok := nestedMap(resp, "data", "team")
		if !ok {
			return ErrUnknownPayload
		}
		projectNodes, ok := nestedSlice(teamNode, "projects", "nodes")
		if !ok {
			return ErrUnknownPayload
		}
		for _, projectNode := range projectNodes {
			appendProjectSummary(projects, projectNode)
		}

		hasNext, _ := nestedBool(teamNode, "projects", "pageInfo", "hasNextPage")
		if !hasNext {
			break
		}
		endCursor, _ := nestedString(teamNode, "projects", "pageInfo", "endCursor")
		if strings.TrimSpace(endCursor) == "" {
			return ErrMissingEndCursor
		}
		after = endCursor
	}
	return nil
}

func appendProjectSummary(projects map[string]ProjectSummary, node map[string]any) {
	name, _ := stringValue(node["name"])
	slug, _ := stringValue(node["slugId"])
	name = strings.TrimSpace(name)
	slug = strings.TrimSpace(slug)
	if name == "" || slug == "" {
		return
	}

	project := ProjectSummary{
		Name: name,
		Slug: slug,
	}
	if teamNodes, ok := nestedSlice(node, "teams", "nodes"); ok {
		for _, teamNode := range teamNodes {
			teamName, _ := stringValue(teamNode["name"])
			teamName = strings.TrimSpace(teamName)
			if teamName != "" {
				project.TeamNames = append(project.TeamNames, teamName)
			}
		}
	}

	if existing, ok := projects[slug]; ok {
		project.TeamNames = appendMissingStrings(existing.TeamNames, project.TeamNames)
	}
	projects[slug] = project
}

func appendMissingStrings(values []string, candidates []string) []string {
	out := slices.Clone(values)
	for _, candidate := range candidates {
		if !slices.Contains(out, candidate) {
			out = append(out, candidate)
		}
	}
	return out
}

func appendTrimmedNonEmpty(values []string, candidate string) []string {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" || slices.Contains(values, candidate) {
		return values
	}
	return append(values, candidate)
}

func newConfiguredAPIClient(cfg domain.ServiceConfig) (*Client, error) {
	endpoint := strings.TrimSpace(cfg.Tracker.Endpoint)
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	auth, err := newAuthorizationProvider(cfg)
	if err != nil {
		return nil, err
	}
	return &Client{
		endpoint:     endpoint,
		apiKey:       strings.TrimSpace(cfg.Tracker.APIKey),
		auth:         auth,
		labelIDs:     map[string]string{},
		projectsByID: map[string]string{},
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}, nil
}

func newAPIClient(endpoint string, apiKey string) *Client {
	return newStaticAPIClient(endpoint, apiKey)
}

func newStaticAPIClient(endpoint string, apiKey string) *Client {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	apiKey = strings.TrimSpace(apiKey)
	return &Client{
		endpoint: endpoint,
		apiKey:   apiKey,
		auth: &staticAuthorizationProvider{
			value: apiKey,
		},
		labelIDs:     map[string]string{},
		projectsByID: map[string]string{},
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// SetUIBaseURLResolver configures a late-bound metadata URL resolver.
func (c *Client) SetUIBaseURLResolver(resolver func(context.Context) string) {
	if c == nil {
		return
	}
	c.uiBaseURLResolver = resolver
}

// ActorIdentity resolves the current Linear actor represented by the configured API key.
func (c *Client) ActorIdentity(ctx context.Context) (linearActorIdentity, error) {
	if c == nil {
		return linearActorIdentity{}, errors.New("nil linear client")
	}

	c.actorMu.RLock()
	if c.actorIdentity != nil {
		identity := *c.actorIdentity
		c.actorMu.RUnlock()
		return identity, nil
	}
	c.actorMu.RUnlock()

	const query = `
query ViewerIdentity {
  viewer {
    id
    name
    displayName
    app
    supportsAgentSessions
  }
}
`
	resp, err := c.doQuery(ctx, query, nil)
	if err != nil {
		return linearActorIdentity{}, err
	}
	viewer, ok := nestedMap(resp, "data", "viewer")
	if !ok {
		return linearActorIdentity{}, ErrUnknownPayload
	}

	identity := linearActorIdentity{}
	identity.ID, _ = stringValue(viewer["id"])
	displayName, _ := stringValue(viewer["displayName"])
	identity.Name = strings.TrimSpace(displayName)
	if identity.Name == "" {
		identity.Name, _ = stringValue(viewer["name"])
	}
	identity.IsApp, _ = viewer["app"].(bool)
	identity.SupportsAgentSessions, _ = viewer["supportsAgentSessions"].(bool)
	if strings.TrimSpace(identity.ID) == "" || strings.TrimSpace(identity.Name) == "" {
		return linearActorIdentity{}, ErrUnknownPayload
	}

	c.actorMu.Lock()
	c.actorIdentity = &identity
	c.actorMu.Unlock()
	return identity, nil
}

func (c *Client) actorIdentityID() string {
	if c == nil {
		return ""
	}
	c.actorMu.RLock()
	defer c.actorMu.RUnlock()
	if c.actorIdentity == nil {
		return ""
	}
	return strings.TrimSpace(c.actorIdentity.ID)
}

func (c *Client) clearActorIdentity() {
	if c == nil {
		return
	}
	c.actorMu.Lock()
	defer c.actorMu.Unlock()
	c.actorIdentity = nil
}

// WatchedProjectID returns the stable Linear project ID resolved from the configured project slug.
func (c *Client) WatchedProjectID() string {
	ids := c.WatchedProjectIDs()
	if len(ids) == 0 {
		return ""
	}
	return ids[0]
}

// WatchedProjectIDs returns the stable Linear project IDs resolved from the configured project slugs.
func (c *Client) WatchedProjectIDs() []string {
	if c == nil {
		return nil
	}
	return slices.Clone(c.watchedProjectIDs)
}

// FetchCandidateIssueSnapshots returns lightweight scheduling snapshots for active issues.
func (c *Client) FetchCandidateIssueSnapshots(ctx context.Context) ([]domain.Issue, error) {
	return c.fetchIssueSnapshots(ctx, c.active)
}

// FetchIssueSnapshotsByStates returns lightweight issue snapshots for the provided states.
func (c *Client) FetchIssueSnapshotsByStates(ctx context.Context, stateNames []string) ([]domain.Issue, error) {
	if len(stateNames) == 0 {
		return nil, nil
	}
	return c.fetchIssueSnapshots(ctx, stateNames)
}

// FetchIssueSchedulingMetadataByIDs returns persisted Colin metadata for the supplied issues.
func (c *Client) FetchIssueSchedulingMetadataByIDs(ctx context.Context, issueIDs []string) (map[string]domain.ColinMetadata, error) {
	if len(issueIDs) == 0 {
		return map[string]domain.ColinMetadata{}, nil
	}
	const query = `
query IssueSchedulingMetadata($ids: [ID!]!) {
  issues(filter: { id: { in: $ids } }, first: 250) {
    nodes {
      id
      attachments(first: 50) {
        nodes {
          id
          title
          url
          createdAt
          updatedAt
          metadata
        }
      }
    }
  }
}
`
	metadataByIssueID := make(map[string]domain.ColinMetadata, len(issueIDs))
	for start := 0; start < len(issueIDs); start += linearIssueBatchSize {
		end := start + linearIssueBatchSize
		if end > len(issueIDs) {
			end = len(issueIDs)
		}
		resp, err := c.doQuery(ctx, query, map[string]any{"ids": issueIDs[start:end]})
		if err != nil {
			return nil, err
		}
		nodes, ok := nestedSlice(resp, "data", "issues", "nodes")
		if !ok {
			return nil, ErrUnknownPayload
		}
		for _, node := range nodes {
			issueID, _ := stringValue(node["id"])
			if strings.TrimSpace(issueID) == "" {
				continue
			}
			attachments, ok := nestedSlice(node, "attachments", "nodes")
			if !ok {
				continue
			}
			metadata, ok := extractColinMetadataFromAttachments(attachments)
			if !ok {
				continue
			}
			metadataByIssueID[issueID] = metadata
		}
	}
	return metadataByIssueID, nil
}

// FetchIssueStatesByIDs refreshes the minimal state snapshot for the supplied Linear issue IDs.
func (c *Client) FetchIssueStatesByIDs(ctx context.Context, issueIDs []string) ([]domain.Issue, error) {
	if len(issueIDs) == 0 {
		return nil, nil
	}
	const query = `
query IssueStates($ids: [ID!]!) {
  issues(filter: { id: { in: $ids } }, first: 250) {
    nodes {
      id
      identifier
      title
      project {
        id
        slugId
      }
      team {
        id
      }
      delegate {
        id
      }
      state { name }
      updatedAt
    }
  }
}
`
	resp, err := c.doQuery(ctx, query, map[string]any{"ids": issueIDs})
	if err != nil {
		return nil, err
	}
	nodes, ok := nestedSlice(resp, "data", "issues", "nodes")
	if !ok {
		return nil, ErrUnknownPayload
	}
	issues := make([]domain.Issue, 0, len(nodes))
	for _, node := range nodes {
		issue, err := c.normalizeIssueSnapshot(node)
		if err != nil {
			return nil, err
		}
		issues = append(issues, issue)
	}
	return issues, nil
}

// FetchIssueByID returns the current issue snapshot for a single Linear issue.
func (c *Client) FetchIssueByID(ctx context.Context, issueID string) (domain.Issue, error) {
	const query = `
query IssueByID($id: String!) {
  issue(id: $id) {
    id
    identifier
    title
    description
    priority
    project {
      id
      slugId
    }
    team {
      id
    }
    branchName
    url
    createdAt
    updatedAt
    state { name }
    delegate {
      id
    }
    labels { nodes { name } }
    inverseRelations {
      nodes {
        type
        issue {
          id
          identifier
          state { name }
        }
      }
    }
	    attachments(first: 50) {
	      nodes {
	        id
	        title
	        url
	        createdAt
	        updatedAt
	        metadata
	      }
	    }
    comments(first: 50) {
      nodes {
        id
        body
        createdAt
        parentId
        user {
          id
          app
        }
        children(first: 50) {
          nodes {
            id
            body
            createdAt
            parentId
            user {
              id
              app
            }
          }
        }
      }
    }
    history(first: 100) {
      nodes {
        createdAt
        fromState { name }
        toState { name }
      }
    }
  }
}
`
	resp, err := c.doQuery(ctx, query, map[string]any{"id": issueID})
	if err != nil {
		return domain.Issue{}, err
	}
	node, ok := nestedMap(resp, "data", "issue")
	if !ok {
		return domain.Issue{}, ErrUnknownPayload
	}
	return c.normalizeIssue(node)
}

// FindIssueByCodexThreadID returns the watched issue whose Colin metadata stores the supplied Codex thread id.
func (c *Client) FindIssueByCodexThreadID(ctx context.Context, threadID string) (domain.Issue, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return domain.Issue{}, fmt.Errorf("%w: thread id is required", ErrCodexThreadNotFound)
	}

	return c.findWatchedIssue(ctx, func(issue domain.Issue) bool {
		return issue.ColinMetadata != nil && strings.TrimSpace(issue.ColinMetadata.CodexThreadID) == threadID
	}, func() error {
		return fmt.Errorf("%w: %s", ErrCodexThreadNotFound, threadID)
	}, func(duplicates []string) error {
		return &AmbiguousCodexThreadError{
			ThreadID:         threadID,
			IssueIdentifiers: duplicates,
		}
	})
}

// FindIssueByIdentifier returns the watched issue whose Linear identifier matches the supplied identifier.
func (c *Client) FindIssueByIdentifier(ctx context.Context, identifier string) (domain.Issue, error) {
	identifier = strings.TrimSpace(identifier)
	if identifier == "" {
		return domain.Issue{}, fmt.Errorf("%w: issue identifier is required", ErrIssueIdentifierNotFound)
	}

	return c.findWatchedIssue(ctx, func(issue domain.Issue) bool {
		return strings.EqualFold(strings.TrimSpace(issue.Identifier), identifier)
	}, func() error {
		return fmt.Errorf("%w: %s", ErrIssueIdentifierNotFound, identifier)
	}, nil)
}

func (c *Client) findWatchedIssue(
	ctx context.Context,
	matchIssue func(domain.Issue) bool,
	notFound func() error,
	ambiguous func([]string) error,
) (domain.Issue, error) {
	var (
		after      *string
		match      *domain.Issue
		duplicates []string
	)
	for {
		resp, err := c.doQuery(ctx, watchedIssuesQuery, map[string]any{
			"projectIDs": c.watchedProjectIDs,
			"after":      after,
		})
		if err != nil {
			return domain.Issue{}, err
		}
		nodes, ok := nestedSlice(resp, "data", "issues", "nodes")
		if !ok {
			return domain.Issue{}, ErrUnknownPayload
		}
		for _, node := range nodes {
			issue, err := c.normalizeIssue(node)
			if err != nil {
				return domain.Issue{}, err
			}
			if !matchIssue(issue) {
				continue
			}
			if match == nil {
				candidate := issue
				match = &candidate
				duplicates = append(duplicates, issue.Identifier)
				continue
			}
			duplicates = append(duplicates, issue.Identifier)
			if ambiguous == nil {
				return domain.Issue{}, ErrUnknownPayload
			}
			return domain.Issue{}, ambiguous(duplicates)
		}
		hasNextPage, _ := nestedBool(resp, "data", "issues", "pageInfo", "hasNextPage")
		if !hasNextPage {
			break
		}
		cursor, ok := nestedString(resp, "data", "issues", "pageInfo", "endCursor")
		if !ok || strings.TrimSpace(cursor) == "" {
			return domain.Issue{}, ErrMissingEndCursor
		}
		after = &cursor
	}
	if match == nil {
		return domain.Issue{}, notFound()
	}
	return *match, nil
}

// UpdateIssueState moves an issue to the named workflow state within the issue's team.
func (c *Client) UpdateIssueState(ctx context.Context, issueID string, stateName string) error {
	stateID, err := c.lookupStateID(ctx, issueID, stateName)
	if err != nil {
		return err
	}

	const query = `
mutation UpdateIssueState($id: String!, $stateId: String!) {
  issueUpdate(id: $id, input: { stateId: $stateId }) {
    success
    issue {
      id
      state { name }
    }
  }
}
`
	resp, err := c.doQuery(ctx, query, map[string]any{
		"id":      issueID,
		"stateId": stateID,
	})
	if err != nil {
		return err
	}
	success, _ := nestedBool(resp, "data", "issueUpdate", "success")
	if !success {
		return ErrUnknownPayload
	}
	return nil
}

// CreateIssue creates a Linear issue and optional attachments.
func (c *Client) CreateIssue(ctx context.Context, input domain.IssueCreateInput) (domain.CreatedIssue, error) {
	teamID := strings.TrimSpace(input.TeamID)
	title := strings.TrimSpace(input.Title)
	if teamID == "" {
		return domain.CreatedIssue{}, errors.New("missing issue team id")
	}
	if title == "" {
		return domain.CreatedIssue{}, errors.New("missing issue title")
	}

	labelIDs := make([]string, 0, len(input.LabelNames))
	seenLabels := map[string]struct{}{}
	for _, name := range input.LabelNames {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, ok := seenLabels[key]; ok {
			continue
		}
		seenLabels[key] = struct{}{}
		labelID, err := c.ensureIssueLabelID(ctx, name)
		if err != nil {
			return domain.CreatedIssue{}, fmt.Errorf("ensure issue label %q: %w", name, err)
		}
		if strings.TrimSpace(labelID) != "" {
			labelIDs = append(labelIDs, labelID)
		}
	}

	createInput := map[string]any{
		"teamId": teamID,
		"title":  title,
	}
	if description := strings.TrimSpace(input.Description); description != "" {
		createInput["description"] = description
	}
	if projectID := strings.TrimSpace(input.ProjectID); projectID != "" {
		createInput["projectId"] = projectID
	}
	if parentID := strings.TrimSpace(input.ParentIssueID); parentID != "" {
		createInput["parentId"] = parentID
	}
	if len(labelIDs) > 0 {
		createInput["labelIds"] = labelIDs
	}

	const query = `
mutation CreateIssue($input: IssueCreateInput!) {
  issueCreate(input: $input) {
    success
    issue {
      id
      identifier
      title
      url
    }
  }
}
`
	resp, err := c.doQuery(ctx, query, map[string]any{"input": createInput})
	if err != nil {
		return domain.CreatedIssue{}, err
	}
	created, err := parseCreatedIssue(resp)
	if err != nil {
		return domain.CreatedIssue{}, err
	}
	for _, attachment := range input.Attachments {
		if err := c.createIssueAttachment(ctx, created.ID, attachment); err != nil {
			return created, fmt.Errorf("create attachment for issue %s: %w", strings.TrimSpace(created.Identifier), err)
		}
	}
	return created, nil
}

func (c *Client) createIssueAttachment(ctx context.Context, issueID string, attachment domain.IssueAttachmentInput) error {
	issueID = strings.TrimSpace(issueID)
	title := strings.TrimSpace(attachment.Title)
	rawURL := strings.TrimSpace(attachment.URL)
	if issueID == "" || title == "" || rawURL == "" {
		return nil
	}
	const query = `
mutation CreateIssueAttachment($input: AttachmentCreateInput!) {
  attachmentCreate(input: $input) {
    success
    attachment { id }
  }
}
`
	input := map[string]any{
		"issueId": issueID,
		"title":   title,
		"url":     rawURL,
	}
	if len(attachment.Metadata) > 0 {
		input["metadata"] = attachment.Metadata
	}
	resp, err := c.doQuery(ctx, query, map[string]any{"input": input})
	if err != nil {
		return err
	}
	success, _ := nestedBool(resp, "data", "attachmentCreate", "success")
	if !success {
		return ErrUnknownPayload
	}
	return nil
}

// EnsureIssueLabel makes sure the named Linear issue label exists.
func (c *Client) EnsureIssueLabel(ctx context.Context, labelName string) error {
	_, err := c.ensureIssueLabelID(ctx, labelName)
	return err
}

// AddIssueLabel applies the named Linear issue label to the supplied issue.
func (c *Client) AddIssueLabel(ctx context.Context, issueID string, labelName string) error {
	labelID, err := c.ensureIssueLabelID(ctx, labelName)
	if err != nil {
		return err
	}

	const query = `
mutation AddIssueLabel($id: String!, $labelId: String!) {
  issueAddLabel(id: $id, labelId: $labelId) {
    success
    issue {
      id
    }
  }
}
`
	resp, err := c.doQuery(ctx, query, map[string]any{
		"id":      issueID,
		"labelId": labelID,
	})
	if err != nil {
		return err
	}
	success, _ := nestedBool(resp, "data", "issueAddLabel", "success")
	if !success {
		return ErrUnknownPayload
	}
	return nil
}

// RemoveIssueLabel removes the named Linear issue label from the supplied issue.
func (c *Client) RemoveIssueLabel(ctx context.Context, issueID string, labelName string) error {
	labelID, err := c.findIssueLabelID(ctx, labelName)
	if err != nil {
		return err
	}
	if strings.TrimSpace(labelID) == "" {
		return nil
	}

	const query = `
mutation RemoveIssueLabel($id: String!, $labelId: String!) {
  issueRemoveLabel(id: $id, labelId: $labelId) {
    success
    issue {
      id
    }
  }
}
`
	resp, err := c.doQuery(ctx, query, map[string]any{
		"id":      issueID,
		"labelId": labelID,
	})
	if err != nil {
		if isMissingIssueLabelRemovalError(err) {
			return nil
		}
		return err
	}
	success, _ := nestedBool(resp, "data", "issueRemoveLabel", "success")
	if !success {
		return ErrUnknownPayload
	}
	return nil
}

func isMissingIssueLabelRemovalError(err error) bool {
	if err == nil || !errors.Is(err, ErrGraphQLErrors) {
		return false
	}
	var response *graphQLErrorResponse
	if !errors.As(err, &response) || len(response.raw) == 0 {
		return false
	}
	for _, item := range response.raw {
		if !isMissingIssueLabelRemovalEntry(item) {
			return false
		}
	}
	return true
}

func isMissingIssueLabelRemovalEntry(item map[string]any) bool {
	message, _ := stringValue(item["message"])
	if !strings.EqualFold(strings.TrimSpace(message), "Label not on issue") {
		return false
	}

	extensions, _ := item["extensions"].(map[string]any)
	userMessage, _ := stringValue(extensions["userPresentableMessage"])
	userMessage = strings.ToLower(strings.TrimSpace(userMessage))
	if !strings.Contains(userMessage, "is not on issue") || !strings.Contains(userMessage, "cannot be removed") {
		return false
	}

	path, ok := item["path"].([]any)
	if !ok {
		return true
	}
	for _, part := range path {
		value, _ := stringValue(part)
		if value == "issueRemoveLabel" {
			return true
		}
	}
	return false
}

func parseGraphQLErrorResponse(errorsField any) (*graphQLErrorResponse, bool) {
	items, ok := errorsField.([]any)
	if !ok {
		return nil, false
	}
	response := &graphQLErrorResponse{raw: make([]map[string]any, 0, len(items))}
	for _, item := range items {
		raw, ok := item.(map[string]any)
		if !ok {
			return nil, false
		}
		response.raw = append(response.raw, raw)
	}
	return response, true
}

// ResolveGitAutomationState returns the team-configured Linear git automation state for the supplied event.
func (c *Client) ResolveGitAutomationState(ctx context.Context, issueID string, event string, targetBranch string) (string, bool, error) {
	const query = `
query GitAutomationState($id: String!) {
  issue(id: $id) {
    team {
      gitAutomationStates(first: 50) {
        nodes {
          event
          state { name }
          targetBranch {
            branchPattern
            isRegex
          }
        }
      }
    }
  }
}
`
	resp, err := c.doQuery(ctx, query, map[string]any{"id": issueID})
	if err != nil {
		return "", false, err
	}
	nodes, ok := nestedSlice(resp, "data", "issue", "team", "gitAutomationStates", "nodes")
	if !ok {
		return "", false, nil
	}

	var (
		bestState string
		bestScore int
	)
	for _, node := range nodes {
		candidateEvent, _ := stringValue(node["event"])
		if !strings.EqualFold(strings.TrimSpace(candidateEvent), strings.TrimSpace(event)) {
			continue
		}
		stateName, _ := nestedString(node, "state", "name")
		score := gitAutomationMatchScore(node, targetBranch)
		if strings.TrimSpace(stateName) == "" || score == 0 || score < bestScore {
			continue
		}
		bestState = stateName
		bestScore = score
	}
	if strings.TrimSpace(bestState) == "" {
		return "", false, nil
	}
	return bestState, true, nil
}

// CreateIssueComment creates a top-level comment on a Linear issue.
func (c *Client) CreateIssueComment(ctx context.Context, issueID string, body string) (string, error) {
	const query = `
mutation CreateIssueComment($input: CommentCreateInput!) {
  commentCreate(input: $input) {
    success
    comment { id }
  }
}
`
	resp, err := c.doQuery(ctx, query, map[string]any{
		"input": map[string]any{
			"issueId": issueID,
			"body":    body,
		},
	})
	if err != nil {
		return "", err
	}
	return parseCreatedCommentID(resp)
}

// CreateCommentReply creates a reply comment under an existing issue comment.
func (c *Client) CreateCommentReply(ctx context.Context, issueID string, parentCommentID string, body string) (string, error) {
	const query = `
mutation CreateCommentReply($input: CommentCreateInput!) {
  commentCreate(input: $input) {
    success
    comment { id }
  }
}
`
	resp, err := c.doQuery(ctx, query, map[string]any{
		"input": map[string]any{
			"issueId":  issueID,
			"parentId": parentCommentID,
			"body":     body,
		},
	})
	if err != nil {
		return "", err
	}
	return parseCreatedCommentID(resp)
}

// CreateAgentActivityThought records an immediate acknowledgement activity on a Linear agent session.
func (c *Client) CreateAgentActivityThought(ctx context.Context, sessionID string, body string) error {
	const query = `
mutation CreateAgentActivityThought($input: AgentActivityCreateInput!) {
  agentActivityCreate(input: $input) {
    success
  }
}
`
	resp, err := c.doQuery(ctx, query, map[string]any{
		"input": map[string]any{
			"agentSessionId": sessionID,
			"content": map[string]any{
				"type": "thought",
				"body": body,
			},
		},
	})
	if err != nil {
		return err
	}
	success, _ := nestedBool(resp, "data", "agentActivityCreate", "success")
	if !success {
		return ErrUnknownPayload
	}
	return nil
}

// UpsertIssueMetadata stores Colin-specific metadata on the Linear issue via a dedicated attachment.
func (c *Client) UpsertIssueMetadata(ctx context.Context, issueID string, metadata domain.ColinMetadata) (domain.ColinMetadata, error) {
	existingMetadata, err := c.fetchIssueMetadataAttachments(ctx, issueID)
	if err != nil {
		return domain.ColinMetadata{}, err
	}
	if attachment, ok := selectCanonicalMetadataAttachment(existingMetadata); ok {
		return c.updateIssueMetadata(ctx, attachment.metadata.AttachmentID, metadata)
	}

	const query = `
mutation UpsertIssueMetadata($input: AttachmentCreateInput!) {
  attachmentCreate(input: $input) {
    success
    attachment {
      id
      title
      url
      metadata
    }
  }
}
`
	resp, err := c.doQuery(ctx, query, map[string]any{
		"input": map[string]any{
			"issueId":  issueID,
			"title":    colinMetadataAttachmentTitle,
			"url":      c.metadataAttachmentURL(ctx, issueID),
			"metadata": colinMetadataValue(metadata),
		},
	})
	if err != nil {
		if isDuplicateMetadataAttachmentCreateError(err) {
			existingMetadata, refetchErr := c.fetchIssueMetadataAttachments(ctx, issueID)
			if refetchErr == nil {
				if attachment, ok := selectCanonicalMetadataAttachment(existingMetadata); ok {
					return c.updateIssueMetadata(ctx, attachment.metadata.AttachmentID, metadata)
				}
			}
		}
		return domain.ColinMetadata{}, err
	}
	success, _ := nestedBool(resp, "data", "attachmentCreate", "success")
	if !success {
		return domain.ColinMetadata{}, ErrUnknownPayload
	}
	attachment, ok := nestedMap(resp, "data", "attachmentCreate", "attachment")
	if !ok {
		return domain.ColinMetadata{}, ErrUnknownPayload
	}
	return parseColinMetadataAttachment(attachment)
}

func (c *Client) updateIssueMetadata(ctx context.Context, attachmentID string, metadata domain.ColinMetadata) (domain.ColinMetadata, error) {
	const query = `
mutation UpdateIssueMetadata($id: String!, $input: AttachmentUpdateInput!) {
  attachmentUpdate(id: $id, input: $input) {
    success
    attachment {
      id
      title
      url
      metadata
    }
  }
}
`
	resp, err := c.doQuery(ctx, query, map[string]any{
		"id": attachmentID,
		"input": map[string]any{
			"title":    colinMetadataAttachmentTitle,
			"metadata": colinMetadataValue(metadata),
		},
	})
	if err != nil {
		return domain.ColinMetadata{}, err
	}
	success, _ := nestedBool(resp, "data", "attachmentUpdate", "success")
	if !success {
		return domain.ColinMetadata{}, ErrUnknownPayload
	}
	attachment, ok := nestedMap(resp, "data", "attachmentUpdate", "attachment")
	if !ok {
		return domain.ColinMetadata{}, ErrUnknownPayload
	}
	return parseColinMetadataAttachment(attachment)
}

// UpsertIssueExecPlan stores the current issue ExecPlan on the Linear issue via a dedicated attachment.
func (c *Client) UpsertIssueExecPlan(ctx context.Context, issueID string, plan domain.ExecPlan) (domain.ExecPlan, error) {
	existingPlans, err := c.fetchIssueExecPlans(ctx, issueID)
	if err != nil {
		return domain.ExecPlan{}, err
	}
	switch len(existingPlans) {
	case 0:
	case 1:
		return c.updateIssueExecPlan(ctx, existingPlans[0].AttachmentID, plan)
	default:
		return domain.ExecPlan{}, fmt.Errorf("%w: issue %s has %d Colin ExecPlan attachments", tracker.ErrDuplicateExecPlans, strings.TrimSpace(issueID), len(existingPlans))
	}

	const query = `
mutation UpsertIssueExecPlan($input: AttachmentCreateInput!) {
  attachmentCreate(input: $input) {
    success
    attachment {
      id
      title
      url
      metadata
    }
  }
}
`
	resp, err := c.doQuery(ctx, query, map[string]any{
		"input": map[string]any{
			"issueId":  issueID,
			"title":    colinExecPlanAttachmentTitle,
			"url":      c.execPlanAttachmentURL(ctx, issueID),
			"metadata": colinExecPlanValue(plan),
		},
	})
	if err != nil {
		return domain.ExecPlan{}, err
	}
	success, _ := nestedBool(resp, "data", "attachmentCreate", "success")
	if !success {
		return domain.ExecPlan{}, ErrUnknownPayload
	}
	attachment, ok := nestedMap(resp, "data", "attachmentCreate", "attachment")
	if !ok {
		return domain.ExecPlan{}, ErrUnknownPayload
	}
	return parseColinExecPlanAttachment(attachment)
}

func (c *Client) updateIssueExecPlan(ctx context.Context, attachmentID string, plan domain.ExecPlan) (domain.ExecPlan, error) {
	const query = `
mutation UpdateIssueExecPlan($id: String!, $input: AttachmentUpdateInput!) {
  attachmentUpdate(id: $id, input: $input) {
    success
    attachment {
      id
      title
      url
      metadata
    }
  }
}
`
	resp, err := c.doQuery(ctx, query, map[string]any{
		"id": attachmentID,
		"input": map[string]any{
			"title":    colinExecPlanAttachmentTitle,
			"metadata": colinExecPlanValue(plan),
		},
	})
	if err != nil {
		return domain.ExecPlan{}, err
	}
	success, _ := nestedBool(resp, "data", "attachmentUpdate", "success")
	if !success {
		return domain.ExecPlan{}, ErrUnknownPayload
	}
	attachment, ok := nestedMap(resp, "data", "attachmentUpdate", "attachment")
	if !ok {
		return domain.ExecPlan{}, ErrUnknownPayload
	}
	return parseColinExecPlanAttachment(attachment)
}

// CurrentRateLimits returns the latest Linear request budget observed from HTTP response headers.
func (c *Client) CurrentRateLimits() domain.RateLimitSnapshot {
	c.rateMu.RLock()
	defer c.rateMu.RUnlock()
	return cloneRateLimits(c.rateInfo)
}

func (c *Client) fetchIssueSnapshots(ctx context.Context, states []string) ([]domain.Issue, error) {
	const query = `
query CandidateIssueSnapshots($projectIDs: [ID!], $states: [String!], $after: String) {
  issues(
    first: 50
    after: $after
    filter: {
      project: { id: { in: $projectIDs } }
      state: { name: { in: $states } }
    }
  ) {
    pageInfo { hasNextPage endCursor }
    nodes {
      id
      identifier
      title
      priority
      project {
        id
        slugId
      }
      team {
        id
      }
      branchName
      url
      createdAt
      updatedAt
      state { name }
      delegate {
        id
      }
      labels { nodes { name } }
      inverseRelations {
        nodes {
          type
          issue {
            id
            identifier
            state { name }
          }
      }
    }
    }
  }
}
`
	var after *string
	var out []domain.Issue
	for {
		variables := map[string]any{
			"projectIDs": c.watchedProjectIDs,
			"states":     states,
			"after":      after,
		}
		resp, err := c.doQuery(ctx, query, variables)
		if err != nil {
			return nil, err
		}
		nodes, ok := nestedSlice(resp, "data", "issues", "nodes")
		if !ok {
			return nil, ErrUnknownPayload
		}
		for _, node := range nodes {
			issue, err := c.normalizeIssueSnapshot(node)
			if err != nil {
				return nil, err
			}
			out = append(out, issue)
		}
		hasNextPage, _ := nestedBool(resp, "data", "issues", "pageInfo", "hasNextPage")
		if !hasNextPage {
			break
		}
		cursor, ok := nestedString(resp, "data", "issues", "pageInfo", "endCursor")
		if !ok || strings.TrimSpace(cursor) == "" {
			return nil, ErrMissingEndCursor
		}
		after = &cursor
	}
	return out, nil
}

func (c *Client) lookupStateID(ctx context.Context, issueID string, stateName string) (string, error) {
	const query = `
query IssueTeamStates($id: String!) {
  issue(id: $id) {
    team {
      states {
        nodes {
          id
          name
        }
      }
    }
  }
}
`
	resp, err := c.doQuery(ctx, query, map[string]any{"id": issueID})
	if err != nil {
		return "", err
	}
	nodes, ok := nestedSlice(resp, "data", "issue", "team", "states", "nodes")
	if !ok {
		return "", ErrUnknownPayload
	}
	target := strings.TrimSpace(stateName)
	for _, node := range nodes {
		name, _ := stringValue(node["name"])
		if !strings.EqualFold(strings.TrimSpace(name), target) {
			continue
		}
		stateID, _ := stringValue(node["id"])
		if strings.TrimSpace(stateID) == "" {
			return "", ErrUnknownPayload
		}
		return stateID, nil
	}
	return "", fmt.Errorf("%w: %s", ErrUnknownState, stateName)
}

func (c *Client) validateWorkflowStates(ctx context.Context, cfg domain.ServiceConfig) ([]string, map[string]string, error) {
	requiredStates := requiredWorkflowStates(cfg)

	const query = `
query ProjectTeamStates($slug: String!) {
  projects(first: 1, filter: { slugId: { eq: $slug } }) {
    nodes {
      id
      teams {
        nodes {
          id
          name
          states {
            nodes {
              name
            }
          }
        }
      }
    }
  }
}
`
	targets := cfg.Targets
	if len(targets) == 0 && strings.TrimSpace(cfg.Tracker.ProjectSlug) != "" {
		targets = []domain.TargetConfig{{ProjectSlug: cfg.Tracker.ProjectSlug}}
	}
	projectIDs := make([]string, 0, len(targets))
	projectsByID := make(map[string]string, len(targets))
	for _, target := range targets {
		projectSlug := strings.TrimSpace(target.ProjectSlug)
		resp, err := c.doQuery(ctx, query, map[string]any{"slug": projectSlug})
		if err != nil {
			return nil, nil, err
		}
		projects, ok := nestedSlice(resp, "data", "projects", "nodes")
		if !ok || len(projects) == 0 {
			return nil, nil, fmt.Errorf("%w: project %q not found", ErrUnknownPayload, projectSlug)
		}
		projectID, _ := stringValue(projects[0]["id"])
		if strings.TrimSpace(projectID) == "" {
			return nil, nil, fmt.Errorf("%w: project %q id missing", ErrUnknownPayload, projectSlug)
		}
		projectID = strings.TrimSpace(projectID)
		projectIDs = append(projectIDs, projectID)
		projectsByID[projectID] = projectSlug
		teamNodes, ok := nestedSlice(projects[0], "teams", "nodes")
		if !ok || len(teamNodes) == 0 {
			return nil, nil, fmt.Errorf("%w: project %q has no teams", ErrUnknownPayload, projectSlug)
		}

		var missing []string
		for _, team := range teamNodes {
			available := make(map[string]struct{})
			if stateNodes, ok := nestedSlice(team, "states", "nodes"); ok {
				for _, stateNode := range stateNodes {
					name, _ := stringValue(stateNode["name"])
					key := config.StateKey(name)
					if key == "" {
						continue
					}
					available[key] = struct{}{}
				}
			}

			var missingForTeam []string
			for _, requirement := range requiredStates {
				if requirement.satisfiedBy(available) {
					continue
				}
				missingForTeam = append(missingForTeam, requirement.Description)
			}
			if len(missingForTeam) == 0 {
				continue
			}

			teamName, _ := stringValue(team["name"])
			if strings.TrimSpace(teamName) == "" {
				teamName, _ = stringValue(team["id"])
			}
			if strings.TrimSpace(teamName) == "" {
				teamName = "unknown"
			}
			missing = append(missing, fmt.Sprintf("team %q missing [%s]", teamName, strings.Join(missingForTeam, ", ")))
		}
		if len(missing) > 0 {
			return nil, nil, fmt.Errorf("%w: project %q %s", ErrMissingWorkflowState, projectSlug, strings.Join(missing, "; "))
		}
	}
	return projectIDs, projectsByID, nil
}

type workflowStateRequirement struct {
	Description  string
	Alternatives []string
}

func (r workflowStateRequirement) satisfiedBy(available map[string]struct{}) bool {
	for _, state := range r.Alternatives {
		if _, ok := available[config.StateKey(state)]; ok {
			return true
		}
	}
	return false
}

func requiredWorkflowStates(cfg domain.ServiceConfig) []workflowStateRequirement {
	seen := map[string]struct{}{}
	var out []workflowStateRequirement

	appendState := func(state string) {
		trimmed := strings.TrimSpace(state)
		if trimmed == "" {
			return
		}
		key := config.StateKey(trimmed)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, workflowStateRequirement{
			Description:  trimmed,
			Alternatives: []string{trimmed},
		})
	}
	appendStates := func(states []string) {
		for _, state := range states {
			appendState(state)
		}
	}
	appendAlternativeStates := func(states []string) {
		var alternatives []string
		for _, state := range states {
			trimmed := strings.TrimSpace(state)
			if trimmed == "" {
				continue
			}
			key := config.StateKey(trimmed)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			alternatives = append(alternatives, trimmed)
		}
		if len(alternatives) == 0 {
			return
		}
		out = append(out, workflowStateRequirement{
			Description:  fmt.Sprintf("one of [%s]", strings.Join(alternatives, ", ")),
			Alternatives: alternatives,
		})
	}
	appendStates(cfg.Tracker.ActiveStates)
	appendStates(cfg.Repo.PublishStates)
	appendStates(cfg.Repo.MergeStates)
	appendAlternativeStates(cfg.Tracker.TerminalStates)
	appendState(refineStateName)
	return out
}

func (c *Client) doQuery(ctx context.Context, query string, variables map[string]any) (map[string]any, error) {
	body := map[string]any{
		"query":     query,
		"variables": variables,
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	payload, err := c.doQueryPayload(ctx, data, false)
	if err != nil {
		return nil, err
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnknownPayload, err)
	}
	if errorsField, ok := decoded["errors"]; ok && errorsField != nil {
		if response, ok := parseGraphQLErrorResponse(errorsField); ok {
			return nil, response
		}
		return nil, fmt.Errorf("%w: %v", ErrGraphQLErrors, errorsField)
	}
	return decoded, nil
}

func (c *Client) doQueryPayload(ctx context.Context, data []byte, retried bool) ([]byte, error) {
	if c == nil {
		return nil, errors.New("nil linear client")
	}
	if c.auth == nil {
		if strings.TrimSpace(c.apiKey) == "" {
			return nil, config.ErrMissingTrackerAPIKey
		}
		c.auth = &staticAuthorizationProvider{value: strings.TrimSpace(c.apiKey)}
	}
	authValue, err := c.auth.Authorization(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAPIRequest, err)
	}
	req.Header.Set("Authorization", authValue)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAPIRequest, err)
	}
	defer resp.Body.Close()
	c.captureRateLimitHeaders(resp.Header, time.Now().UTC())
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAPIRequest, err)
	}
	if resp.StatusCode == http.StatusUnauthorized && !retried {
		if err := c.auth.Refresh(ctx); err == nil {
			c.clearActorIdentity()
			return c.doQueryPayload(ctx, data, true)
		}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status=%d body=%s", ErrAPIStatus, resp.StatusCode, string(payload))
	}
	return payload, nil
}

func (c *Client) captureRateLimitHeaders(header http.Header, observedAt time.Time) {
	limit, ok := parseHeaderInt(header.Get("X-RateLimit-Requests-Limit"))
	if !ok {
		return
	}
	remaining, ok := parseHeaderInt(header.Get("X-RateLimit-Requests-Remaining"))
	if !ok {
		return
	}
	resetAt, ok := parseHeaderTime(header.Get("X-RateLimit-Requests-Reset"))
	if !ok {
		return
	}

	nextAllowedAt := nextAllowedAt(observedAt, resetAt, remaining)
	info := domain.RateLimitSnapshot{
		"linear_requests": {
			Limit:         int64Ptr(limit),
			Remaining:     int64Ptr(remaining),
			ResetsAt:      timePtr(resetAt),
			NextAllowedAt: timePtr(nextAllowedAt),
		},
	}
	if limit > 0 {
		usedPercent := ((limit - remaining) * 100) / limit
		window := info["linear_requests"]
		window.UsedPercent = int64Ptr(usedPercent)
		info["linear_requests"] = window
	}

	c.rateMu.Lock()
	c.rateInfo = info
	c.rateMu.Unlock()
}

func nextAllowedAt(observedAt, resetAt time.Time, remaining int64) time.Time {
	if !resetAt.After(observedAt) {
		return observedAt
	}
	if remaining <= 0 {
		return resetAt
	}
	window := resetAt.Sub(observedAt)
	step := window / time.Duration(remaining+1)
	if step <= 0 {
		return observedAt
	}
	return observedAt.Add(step)
}

func (c *Client) normalizeIssueSnapshot(node map[string]any) (domain.Issue, error) {
	id, _ := stringValue(node["id"])
	identifier, _ := stringValue(node["identifier"])
	title, _ := stringValue(node["title"])
	state, _ := nestedString(node, "state", "name")

	issue := domain.Issue{
		ID:          id,
		Identifier:  identifier,
		Title:       title,
		TeamID:      strings.TrimSpace(teamID(node)),
		ProjectID:   strings.TrimSpace(projectID(node)),
		ProjectSlug: strings.TrimSpace(projectSlug(node)),
		State:       state,
	}
	if delegateID, ok := nestedString(node, "delegate", "id"); ok && strings.TrimSpace(delegateID) != "" {
		issue.DelegatedToColin = strings.EqualFold(strings.TrimSpace(delegateID), c.actorIdentityID())
	}
	if value, ok := intValue(node["priority"]); ok {
		issue.Priority = &value
	}
	if value, ok := stringValue(node["branchName"]); ok {
		issue.BranchName = &value
	}
	if value, ok := stringValue(node["url"]); ok {
		issue.URL = &value
	}
	if value, ok := stringValue(node["createdAt"]); ok {
		if ts, err := time.Parse(time.RFC3339, value); err == nil {
			issue.CreatedAt = &ts
		}
	}
	if value, ok := stringValue(node["updatedAt"]); ok {
		if ts, err := time.Parse(time.RFC3339, value); err == nil {
			issue.UpdatedAt = &ts
		}
	}
	if labelNodes, ok := nestedSlice(node, "labels", "nodes"); ok {
		for _, label := range labelNodes {
			if name, ok := stringValue(label["name"]); ok {
				issue.Labels = append(issue.Labels, strings.ToLower(name))
			}
		}
	}
	if relationNodes, ok := nestedSlice(node, "inverseRelations", "nodes"); ok {
		for _, relation := range relationNodes {
			relationType, _ := stringValue(relation["type"])
			if strings.ToLower(relationType) != "blocks" {
				continue
			}
			related, ok := relation["issue"].(map[string]any)
			if !ok {
				continue
			}
			blocker := domain.BlockerRef{}
			if value, ok := stringValue(related["id"]); ok {
				blocker.ID = &value
			}
			if value, ok := stringValue(related["identifier"]); ok {
				blocker.Identifier = &value
			}
			if value, ok := nestedString(related, "state", "name"); ok {
				blocker.State = &value
			}
			issue.BlockedBy = append(issue.BlockedBy, blocker)
		}
	}
	return issue, nil
}

func (c *Client) normalizeIssue(node map[string]any) (domain.Issue, error) {
	issue, err := c.normalizeIssueSnapshot(node)
	if err != nil {
		return domain.Issue{}, err
	}
	if value, ok := stringValue(node["description"]); ok {
		issue.Description = &value
	}
	issue.ColinMetadata = extractColinMetadata(node)
	issue.ExecPlan, issue.ExecPlanCount = extractExecPlan(node)
	issue.AttachedPullRequests = c.extractAttachedPullRequests(node)
	if start, end, ok := latestReviewCycleWindow(node); ok && strings.EqualFold(strings.TrimSpace(issue.State), "Todo") {
		issue.ReviewCycle = &domain.ReviewCycle{
			EnteredReviewAt:  start,
			ReturnedToTodoAt: end,
		}
	}
	issue.ReviewFeedback = c.extractReviewFeedback(issue.State, node)
	return issue, nil
}

func projectID(node map[string]any) string {
	value, _ := nestedString(node, "project", "id")
	return value
}

func teamID(node map[string]any) string {
	value, _ := nestedString(node, "team", "id")
	return value
}

func projectSlug(node map[string]any) string {
	value, _ := nestedString(node, "project", "slugId")
	return value
}

type linearComment struct {
	ID        string
	Body      string
	CreatedAt time.Time
	ParentID  *string
	UserID    string
	UserIsApp bool
}

type linearStateChange struct {
	CreatedAt time.Time
	FromState string
	ToState   string
}

type colinMetadataAttachment struct {
	metadata  domain.ColinMetadata
	createdAt *time.Time
	updatedAt *time.Time
}

func (c *Client) extractReviewFeedback(state string, node map[string]any) []domain.ReviewFeedback {
	if !strings.EqualFold(strings.TrimSpace(state), "Todo") {
		return nil
	}

	start, end, ok := latestReviewCycleWindow(node)
	if !ok {
		return nil
	}

	comments := flattenComments(node)
	colinCommentIDs := colinCommentIDSet(node)
	feedback := make([]domain.ReviewFeedback, 0, len(comments))
	for _, comment := range comments {
		if comment.CreatedAt.Before(start) || comment.CreatedAt.After(end) {
			continue
		}
		if _, ok := colinCommentIDs[comment.ID]; ok {
			continue
		}
		body := strings.TrimSpace(comment.Body)
		if body == "" || c.isColinComment(comment) {
			continue
		}
		feedback = append(feedback, domain.ReviewFeedback{
			Body:      body,
			CreatedAt: comment.CreatedAt,
			ParentID:  comment.ParentID,
		})
	}
	return feedback
}

func latestReviewCycleWindow(node map[string]any) (time.Time, time.Time, bool) {
	changes := flattenStateChanges(node)
	if len(changes) == 0 {
		return time.Time{}, time.Time{}, false
	}

	for exitIdx := len(changes) - 1; exitIdx >= 0; exitIdx-- {
		change := changes[exitIdx]
		if !strings.EqualFold(strings.TrimSpace(change.FromState), "Review") || !strings.EqualFold(strings.TrimSpace(change.ToState), "Todo") {
			continue
		}
		for enterIdx := exitIdx - 1; enterIdx >= 0; enterIdx-- {
			enter := changes[enterIdx]
			if strings.EqualFold(strings.TrimSpace(enter.ToState), "Review") {
				return enter.CreatedAt, change.CreatedAt, true
			}
		}
		break
	}

	return time.Time{}, time.Time{}, false
}

func flattenComments(node map[string]any) []linearComment {
	nodes, ok := nestedSlice(node, "comments", "nodes")
	if !ok {
		return nil
	}

	seen := make(map[string]struct{}, len(nodes))
	out := make([]linearComment, 0, len(nodes))
	for _, commentNode := range nodes {
		if comment, ok := parseLinearComment(commentNode); ok {
			if _, exists := seen[comment.ID]; !exists {
				out = append(out, comment)
				seen[comment.ID] = struct{}{}
			}
		}
		if children, ok := nestedSlice(commentNode, "children", "nodes"); ok {
			for _, childNode := range children {
				if comment, ok := parseLinearComment(childNode); ok {
					if _, exists := seen[comment.ID]; !exists {
						out = append(out, comment)
						seen[comment.ID] = struct{}{}
					}
				}
			}
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			leftParent := derefStringValue(out[i].ParentID)
			rightParent := derefStringValue(out[j].ParentID)
			if leftParent == rightParent {
				return out[i].Body < out[j].Body
			}
			return leftParent < rightParent
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})

	return out
}

func colinCommentIDSet(node map[string]any) map[string]struct{} {
	metadata := extractColinMetadata(node)
	if metadata == nil || len(metadata.ColinCommentIDs) == 0 {
		return nil
	}
	ids := make(map[string]struct{}, len(metadata.ColinCommentIDs))
	for _, id := range metadata.ColinCommentIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		ids[id] = struct{}{}
	}
	return ids
}

func flattenStateChanges(node map[string]any) []linearStateChange {
	nodes, ok := nestedSlice(node, "history", "nodes")
	if !ok {
		return nil
	}

	out := make([]linearStateChange, 0, len(nodes))
	for _, historyNode := range nodes {
		createdAtRaw, ok := stringValue(historyNode["createdAt"])
		if !ok {
			continue
		}
		createdAt, err := time.Parse(time.RFC3339, createdAtRaw)
		if err != nil {
			continue
		}
		fromState, _ := nestedString(historyNode, "fromState", "name")
		toState, _ := nestedString(historyNode, "toState", "name")
		if strings.TrimSpace(fromState) == "" && strings.TrimSpace(toState) == "" {
			continue
		}
		out = append(out, linearStateChange{
			CreatedAt: createdAt,
			FromState: fromState,
			ToState:   toState,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

func parseLinearComment(node map[string]any) (linearComment, bool) {
	id, ok := stringValue(node["id"])
	if !ok || strings.TrimSpace(id) == "" {
		return linearComment{}, false
	}
	body, ok := stringValue(node["body"])
	if !ok {
		return linearComment{}, false
	}
	createdAtRaw, ok := stringValue(node["createdAt"])
	if !ok {
		return linearComment{}, false
	}
	createdAt, err := time.Parse(time.RFC3339, createdAtRaw)
	if err != nil {
		return linearComment{}, false
	}
	comment := linearComment{
		ID:        id,
		Body:      body,
		CreatedAt: createdAt,
	}
	if parentID, ok := stringValue(node["parentId"]); ok && strings.TrimSpace(parentID) != "" {
		comment.ParentID = &parentID
	}
	if userNode, ok := nestedMap(node, "user"); ok {
		comment.UserID, _ = stringValue(userNode["id"])
		comment.UserIsApp, _ = userNode["app"].(bool)
	}
	return comment, true
}

func (c *Client) isColinComment(comment linearComment) bool {
	if c != nil && c.appMode && comment.UserIsApp && strings.EqualFold(strings.TrimSpace(comment.UserID), c.actorIdentityID()) {
		return true
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(comment.Body)), "[colin]")
}

func extractColinMetadata(node map[string]any) *domain.ColinMetadata {
	attachments, ok := nestedSlice(node, "attachments", "nodes")
	if !ok {
		return nil
	}
	metadata, ok := extractColinMetadataFromAttachments(attachments)
	if !ok {
		return nil
	}
	return &metadata
}

func extractColinMetadataFromAttachments(attachments []map[string]any) (domain.ColinMetadata, bool) {
	metadataAttachments := make([]colinMetadataAttachment, 0, len(attachments))
	for _, attachment := range attachments {
		metadata, err := parseColinMetadataAttachmentNode(attachment)
		if err != nil {
			continue
		}
		metadataAttachments = append(metadataAttachments, metadata)
	}
	selected, ok := selectCanonicalMetadataAttachment(metadataAttachments)
	if !ok {
		return domain.ColinMetadata{}, false
	}
	merged := selected.metadata
	mergeSlackMetadataFields(&merged, metadataAttachments)
	return merged, true
}

func extractExecPlan(node map[string]any) (*domain.ExecPlan, int) {
	attachments, ok := nestedSlice(node, "attachments", "nodes")
	if !ok {
		return nil, 0
	}
	return extractExecPlanFromAttachments(attachments)
}

func extractExecPlanFromAttachments(attachments []map[string]any) (*domain.ExecPlan, int) {
	var plans []domain.ExecPlan
	for _, attachment := range attachments {
		plan, err := parseColinExecPlanAttachment(attachment)
		if err != nil {
			continue
		}
		plans = append(plans, plan)
	}
	if len(plans) != 1 {
		return nil, len(plans)
	}
	return &plans[0], 1
}

func (c *Client) fetchIssueExecPlans(ctx context.Context, issueID string) ([]domain.ExecPlan, error) {
	const query = `
query IssueExecPlans($id: String!) {
  issue(id: $id) {
    attachments(first: 50) {
      nodes {
        id
        title
        url
        metadata
      }
    }
  }
}
`
	resp, err := c.doQuery(ctx, query, map[string]any{"id": issueID})
	if err != nil {
		return nil, err
	}
	attachments, ok := nestedSlice(resp, "data", "issue", "attachments", "nodes")
	if !ok {
		return nil, ErrUnknownPayload
	}
	plans := make([]domain.ExecPlan, 0, len(attachments))
	for _, attachment := range attachments {
		plan, err := parseColinExecPlanAttachment(attachment)
		if err != nil {
			continue
		}
		plans = append(plans, plan)
	}
	return plans, nil
}

func (c *Client) fetchIssueMetadataAttachments(ctx context.Context, issueID string) ([]colinMetadataAttachment, error) {
	const query = `
query IssueMetadataAttachments($id: String!) {
  issue(id: $id) {
    attachments(first: 50) {
      nodes {
        id
        title
        url
        createdAt
        updatedAt
        metadata
      }
    }
  }
}
`
	resp, err := c.doQuery(ctx, query, map[string]any{"id": issueID})
	if err != nil {
		return nil, err
	}
	attachments, ok := nestedSlice(resp, "data", "issue", "attachments", "nodes")
	if !ok {
		return nil, ErrUnknownPayload
	}
	metadataAttachments := make([]colinMetadataAttachment, 0, len(attachments))
	for _, attachment := range attachments {
		metadata, err := parseColinMetadataAttachmentNode(attachment)
		if err != nil {
			continue
		}
		metadataAttachments = append(metadataAttachments, metadata)
	}
	return metadataAttachments, nil
}

func parseColinMetadataAttachment(node map[string]any) (domain.ColinMetadata, error) {
	attachment, err := parseColinMetadataAttachmentNode(node)
	if err != nil {
		return domain.ColinMetadata{}, err
	}
	return attachment.metadata, nil
}

func parseColinMetadataAttachmentNode(node map[string]any) (colinMetadataAttachment, error) {
	url, _ := stringValue(node["url"])
	if !isColinMetadataURL(url) {
		return colinMetadataAttachment{}, errors.New("not a Colin metadata attachment")
	}

	metadataMap, _ := node["metadata"].(map[string]any)
	metadata := domain.ColinMetadata{}
	metadata.AttachmentID, _ = stringValue(node["id"])
	metadata.URL = strings.TrimSpace(url)
	metadata.CodexThreadID, _ = stringValue(metadataMap["codex_thread_id"])
	metadata.ProgressRootCommentID, _ = stringValue(metadataMap["progress_root_comment_id"])
	metadata.DelegationAckKind, _ = stringValue(metadataMap["delegation_ack_kind"])
	metadata.DelegationAckState, _ = stringValue(metadataMap["delegation_ack_state"])
	metadata.DelegationAckSessionID, _ = stringValue(metadataMap["delegation_ack_session_id"])
	metadata.ActualBranchName, _ = stringValue(metadataMap["actual_branch_name"])
	metadata.PendingReviewCommentID, _ = stringValue(metadataMap["pending_review_comment_id"])
	metadata.PendingReviewThreadID, _ = stringValue(metadataMap["pending_review_thread_id"])
	metadata.PendingReviewReactionID, _ = stringValue(metadataMap["pending_review_reaction_id"])
	metadata.PendingReviewReactor, _ = stringValue(metadataMap["pending_review_reactor"])
	metadata.QueuedReviewFollowUps = pendingReviewFollowUpsValue(metadataMap["queued_review_follow_ups"])
	metadata.PendingCheckFailure = pendingCheckFailureValue(metadataMap["pending_check_failure"])
	metadata.ReviewReactionWatermarks = stringMapValue(metadataMap["review_reaction_watermarks"])
	metadata.ReviewFeedbackIssueLinks = reviewFeedbackIssueLinksValue(metadataMap["review_feedback_issue_links"])
	if value, ok := stringValue(metadataMap["exec_plan_decision"]); ok {
		metadata.ExecPlanDecision = domain.ExecPlanDecision(value)
	}
	if value, ok := stringValue(metadataMap["review_publish_directive"]); ok {
		metadata.ReviewPublishDirective = domain.ReviewPublishDirective(value)
	}
	if value, ok := stringValue(metadataMap["last_run_type"]); ok {
		metadata.LastRunType = domain.RunType(value)
	}
	if value, ok := stringValue(metadataMap["last_outcome"]); ok {
		metadata.LastOutcome = domain.RunOutcome(value)
	}
	metadata.LastSummary, _ = stringValue(metadataMap["last_summary"])
	metadata.LastSummaryCommentID, _ = stringValue(metadataMap["last_summary_comment_id"])
	metadata.ColinCommentIDs = stringSliceValue(metadataMap["colin_comment_ids"])
	if value, ok := intValue(metadataMap["pull_request_number"]); ok {
		metadata.PullRequestNumber = value
	}
	metadata.PullRequestURL, _ = stringValue(metadataMap["pull_request_url"])
	metadata.PullRequestState, _ = stringValue(metadataMap["pull_request_state"])
	metadata.PullRequestHeadRef, _ = stringValue(metadataMap["pull_request_head_ref"])
	metadata.PullRequestBaseRef, _ = stringValue(metadataMap["pull_request_base_ref"])
	metadata.PullRequestBackend, _ = stringValue(metadataMap["pull_request_backend"])
	metadata.PullRequestRepoOwner, _ = stringValue(metadataMap["pull_request_repo_owner"])
	metadata.PullRequestRepoName, _ = stringValue(metadataMap["pull_request_repo_name"])
	metadata.LastCheckHeadSHA, _ = stringValue(metadataMap["last_check_head_sha"])
	metadata.LastCheckState, _ = stringValue(metadataMap["last_check_state"])
	metadata.LoopFailureFingerprint, _ = stringValue(metadataMap["loop_failure_fingerprint"])
	if value, ok := intValue(metadataMap["loop_failure_count"]); ok {
		metadata.LoopFailureCount = value
	}
	metadata.PausedRunType, _ = stringValue(metadataMap["paused_run_type"])
	metadata.PausedState, _ = stringValue(metadataMap["paused_state"])
	metadata.PausedReason, _ = stringValue(metadataMap["paused_reason"])
	metadata.SlackChannelID, _ = stringValue(metadataMap["slack_channel_id"])
	metadata.SlackMessageTS, _ = stringValue(metadataMap["slack_message_ts"])
	metadata.SlackPermalink, _ = stringValue(metadataMap["slack_permalink"])
	metadata.SlackSummaryFingerprint, _ = stringValue(metadataMap["slack_summary_fingerprint"])
	if value, _ := stringValue(metadataMap["paused_at"]); strings.TrimSpace(value) != "" {
		if parsed, err := time.Parse(time.RFC3339, value); err == nil {
			metadata.PausedAt = &parsed
		}
	}
	if value, _ := stringValue(metadataMap["pending_review_requested_at"]); strings.TrimSpace(value) != "" {
		if parsed, err := time.Parse(time.RFC3339, value); err == nil {
			metadata.PendingReviewRequestedAt = &parsed
		}
	}
	if value, _ := stringValue(metadataMap["updated_at"]); strings.TrimSpace(value) != "" {
		if parsed, err := time.Parse(time.RFC3339, value); err == nil {
			metadata.UpdatedAt = &parsed
		}
	}
	if nodes, ok := metadataMap["codex_output"].([]any); ok {
		output := make([]domain.OutputLog, 0, len(nodes))
		for _, raw := range nodes {
			node, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			timestampValue, ok := stringValue(node["timestamp"])
			if !ok || strings.TrimSpace(timestampValue) == "" {
				continue
			}
			timestamp, err := time.Parse(time.RFC3339, timestampValue)
			if err != nil {
				continue
			}
			event, _ := stringValue(node["event"])
			message, _ := stringValue(node["message"])
			output = append(output, domain.OutputLog{
				Timestamp: timestamp,
				Event:     event,
				Message:   message,
			})
		}
		metadata.CodexOutput = output
	}
	return colinMetadataAttachment{
		metadata:  metadata,
		createdAt: parseAttachmentTimestamp(node["createdAt"]),
		updatedAt: parseAttachmentTimestamp(node["updatedAt"]),
	}, nil
}

func isDuplicateMetadataAttachmentCreateError(err error) bool {
	if err == nil || !errors.Is(err, ErrGraphQLErrors) {
		return false
	}
	var response *graphQLErrorResponse
	if !errors.As(err, &response) || len(response.raw) == 0 {
		return false
	}
	for _, item := range response.raw {
		message, _ := stringValue(item["message"])
		message = strings.ToLower(strings.TrimSpace(message))
		if !strings.Contains(message, "duplicate url") {
			return false
		}
		path, ok := item["path"].([]any)
		if !ok {
			continue
		}
		matched := false
		for _, part := range path {
			value, _ := stringValue(part)
			if value == "attachmentCreate" {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func parseColinExecPlanAttachment(node map[string]any) (domain.ExecPlan, error) {
	title, _ := stringValue(node["title"])
	url, _ := stringValue(node["url"])
	if strings.TrimSpace(title) != colinExecPlanAttachmentTitle || !isColinExecPlanURL(url) {
		return domain.ExecPlan{}, errors.New("not a Colin ExecPlan attachment")
	}

	metadataMap, _ := node["metadata"].(map[string]any)
	plan := domain.ExecPlan{}
	plan.AttachmentID, _ = stringValue(node["id"])
	plan.URL = strings.TrimSpace(url)
	plan.Body, _ = stringValue(metadataMap["body"])
	if value, _ := stringValue(metadataMap["updated_at"]); strings.TrimSpace(value) != "" {
		if parsed, err := time.Parse(time.RFC3339, value); err == nil {
			plan.UpdatedAt = &parsed
		}
	}
	return plan, nil
}

func colinMetadataValue(metadata domain.ColinMetadata) map[string]any {
	value := map[string]any{
		"codex_thread_id":             strings.TrimSpace(metadata.CodexThreadID),
		"progress_root_comment_id":    strings.TrimSpace(metadata.ProgressRootCommentID),
		"delegation_ack_kind":         strings.TrimSpace(metadata.DelegationAckKind),
		"delegation_ack_state":        strings.TrimSpace(metadata.DelegationAckState),
		"delegation_ack_session_id":   strings.TrimSpace(metadata.DelegationAckSessionID),
		"actual_branch_name":          strings.TrimSpace(metadata.ActualBranchName),
		"pending_review_comment_id":   strings.TrimSpace(metadata.PendingReviewCommentID),
		"pending_review_thread_id":    strings.TrimSpace(metadata.PendingReviewThreadID),
		"pending_review_reaction_id":  strings.TrimSpace(metadata.PendingReviewReactionID),
		"pending_review_reactor":      strings.TrimSpace(metadata.PendingReviewReactor),
		"queued_review_follow_ups":    pendingReviewFollowUpsAny(metadata.QueuedReviewFollowUps),
		"pending_check_failure":       pendingCheckFailureAny(metadata.PendingCheckFailure),
		"review_reaction_watermarks":  stringMapAny(metadata.ReviewReactionWatermarks),
		"review_feedback_issue_links": reviewFeedbackIssueLinksAny(metadata.ReviewFeedbackIssueLinks),
		"exec_plan_decision":          strings.TrimSpace(string(metadata.ExecPlanDecision)),
		"review_publish_directive":    strings.TrimSpace(string(metadata.ReviewPublishDirective)),
		"last_run_type":               strings.TrimSpace(string(metadata.LastRunType)),
		"last_outcome":                strings.TrimSpace(string(metadata.LastOutcome)),
		"last_summary":                strings.TrimSpace(metadata.LastSummary),
		"last_summary_comment_id":     strings.TrimSpace(metadata.LastSummaryCommentID),
		"colin_comment_ids":           stringSliceAny(metadata.ColinCommentIDs),
		"pull_request_number":         metadata.PullRequestNumber,
		"pull_request_url":            strings.TrimSpace(metadata.PullRequestURL),
		"pull_request_state":          strings.TrimSpace(metadata.PullRequestState),
		"pull_request_head_ref":       strings.TrimSpace(metadata.PullRequestHeadRef),
		"pull_request_base_ref":       strings.TrimSpace(metadata.PullRequestBaseRef),
		"pull_request_backend":        strings.TrimSpace(metadata.PullRequestBackend),
		"pull_request_repo_owner":     strings.TrimSpace(metadata.PullRequestRepoOwner),
		"pull_request_repo_name":      strings.TrimSpace(metadata.PullRequestRepoName),
		"last_check_head_sha":         strings.TrimSpace(metadata.LastCheckHeadSHA),
		"last_check_state":            strings.TrimSpace(metadata.LastCheckState),
		"loop_failure_fingerprint":    strings.TrimSpace(metadata.LoopFailureFingerprint),
		"loop_failure_count":          metadata.LoopFailureCount,
		"paused_run_type":             strings.TrimSpace(metadata.PausedRunType),
		"paused_state":                strings.TrimSpace(metadata.PausedState),
		"paused_reason":               strings.TrimSpace(metadata.PausedReason),
		"slack_channel_id":            strings.TrimSpace(metadata.SlackChannelID),
		"slack_message_ts":            strings.TrimSpace(metadata.SlackMessageTS),
		"slack_permalink":             strings.TrimSpace(metadata.SlackPermalink),
		"slack_summary_fingerprint":   strings.TrimSpace(metadata.SlackSummaryFingerprint),
	}
	if metadata.PausedAt != nil {
		value["paused_at"] = metadata.PausedAt.UTC().Format(time.RFC3339)
	}
	if metadata.PendingReviewRequestedAt != nil {
		value["pending_review_requested_at"] = metadata.PendingReviewRequestedAt.UTC().Format(time.RFC3339)
	}
	if metadata.UpdatedAt != nil {
		value["updated_at"] = metadata.UpdatedAt.UTC().Format(time.RFC3339)
	}
	if len(metadata.CodexOutput) > 0 {
		output := make([]map[string]any, 0, len(metadata.CodexOutput))
		for _, item := range metadata.CodexOutput {
			output = append(output, map[string]any{
				"timestamp": item.Timestamp.UTC().Format(time.RFC3339),
				"event":     strings.TrimSpace(item.Event),
				"message":   strings.TrimSpace(item.Message),
			})
		}
		value["codex_output"] = output
	}
	return value
}

func selectCanonicalMetadataAttachment(attachments []colinMetadataAttachment) (colinMetadataAttachment, bool) {
	if len(attachments) == 0 {
		return colinMetadataAttachment{}, false
	}
	best := attachments[0]
	for _, candidate := range attachments[1:] {
		if compareMetadataAttachment(candidate, best) > 0 {
			best = candidate
		}
	}
	return best, true
}

func mergeSlackMetadataFields(target *domain.ColinMetadata, attachments []colinMetadataAttachment) {
	if target == nil || len(attachments) == 0 {
		return
	}

	sorted := append([]colinMetadataAttachment(nil), attachments...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return compareMetadataAttachment(sorted[i], sorted[j]) > 0
	})
	for _, attachment := range sorted {
		if strings.TrimSpace(target.SlackChannelID) == "" {
			target.SlackChannelID = strings.TrimSpace(attachment.metadata.SlackChannelID)
		}
		if strings.TrimSpace(target.SlackMessageTS) == "" {
			target.SlackMessageTS = strings.TrimSpace(attachment.metadata.SlackMessageTS)
		}
		if strings.TrimSpace(target.SlackPermalink) == "" {
			target.SlackPermalink = strings.TrimSpace(attachment.metadata.SlackPermalink)
		}
		if strings.TrimSpace(target.SlackSummaryFingerprint) == "" {
			target.SlackSummaryFingerprint = strings.TrimSpace(attachment.metadata.SlackSummaryFingerprint)
		}
		if strings.TrimSpace(target.SlackChannelID) != "" &&
			strings.TrimSpace(target.SlackMessageTS) != "" &&
			strings.TrimSpace(target.SlackPermalink) != "" &&
			strings.TrimSpace(target.SlackSummaryFingerprint) != "" {
			return
		}
	}
}

func compareMetadataAttachment(left, right colinMetadataAttachment) int {
	if cmp := compareOptionalTime(left.updatedAt, right.updatedAt); cmp != 0 {
		return cmp
	}
	if cmp := compareOptionalTime(left.createdAt, right.createdAt); cmp != 0 {
		return cmp
	}
	return strings.Compare(strings.TrimSpace(left.metadata.AttachmentID), strings.TrimSpace(right.metadata.AttachmentID))
}

func compareOptionalTime(left, right *time.Time) int {
	switch {
	case left == nil && right == nil:
		return 0
	case left == nil:
		return -1
	case right == nil:
		return 1
	case left.Before(*right):
		return -1
	case left.After(*right):
		return 1
	default:
		return 0
	}
}

func parseAttachmentTimestamp(value any) *time.Time {
	raw, ok := stringValue(value)
	if !ok || strings.TrimSpace(raw) == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil
	}
	return &parsed
}

func colinExecPlanValue(plan domain.ExecPlan) map[string]any {
	value := map[string]any{
		"body": strings.TrimSpace(plan.Body),
	}
	if plan.UpdatedAt != nil {
		value["updated_at"] = plan.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return value
}

func colinMetadataAttachmentURL(issueID string) string {
	return domain.ColinMetadataPath(issueID)
}

func (c *Client) metadataAttachmentURL(ctx context.Context, issueID string) string {
	return strings.TrimRight(c.resolvedUIBaseURL(ctx), "/") + colinMetadataAttachmentURL(issueID)
}

func (c *Client) execPlanAttachmentURL(ctx context.Context, issueID string) string {
	return strings.TrimRight(c.resolvedUIBaseURL(ctx), "/") + domain.ColinExecPlanPath(issueID)
}

func (c *Client) resolvedUIBaseURL(ctx context.Context) string {
	baseURL := strings.TrimSpace(c.uiBaseURL)
	if c.uiBaseURLResolver != nil {
		if resolved := strings.TrimSpace(c.uiBaseURLResolver(ctx)); resolved != "" {
			baseURL = resolved
		}
	}
	if baseURL == "" {
		baseURL = "http://127.0.0.1"
	}
	return baseURL
}

func isColinMetadataURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return false
	}
	_, ok := domain.ParseColinMetadataPath(parsed.EscapedPath())
	return ok
}

func (c *Client) extractAttachedPullRequests(node map[string]any) []domain.PullRequestRef {
	attachments, ok := nestedSlice(node, "attachments", "nodes")
	if !ok {
		return nil
	}
	return c.extractAttachedPullRequestsFromAttachments(attachments)
}

func (c *Client) extractAttachedPullRequestsFromAttachments(attachments []map[string]any) []domain.PullRequestRef {
	seen := make(map[string]struct{}, len(attachments))
	prs := make([]domain.PullRequestRef, 0, len(attachments))
	for _, attachment := range attachments {
		urlValue, _ := stringValue(attachment["url"])
		pr, ok := c.parsePullRequestAttachment(urlValue)
		if !ok {
			continue
		}
		key := pullRequestAttachmentKey(pr)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		prs = append(prs, pr)
	}
	return prs
}

func (c *Client) parsePullRequestAttachment(rawURL string) (domain.PullRequestRef, bool) {
	if c == nil || c.repoAdapter == nil {
		return domain.PullRequestRef{}, false
	}
	rawURL = strings.TrimSpace(rawURL)
	owner, repo, number, ok := c.repoAdapter.ParsePullRequestURL(rawURL)
	if !ok || strings.TrimSpace(owner) == "" || strings.TrimSpace(repo) == "" || number <= 0 {
		return domain.PullRequestRef{}, false
	}

	return domain.PullRequestRef{
		Number:          number,
		URL:             rawURL,
		Backend:         string(c.repoAdapter.Kind()),
		RepositoryOwner: strings.TrimSpace(owner),
		RepositoryName:  strings.TrimSpace(repo),
	}, true
}

func pullRequestAttachmentKey(pr domain.PullRequestRef) string {
	return strings.ToLower(strings.TrimSpace(pr.Backend)) + "|" +
		strings.ToLower(strings.TrimSpace(pr.RepositoryOwner)) + "|" +
		strings.ToLower(strings.TrimSpace(pr.RepositoryName)) + "|" +
		strconv.Itoa(pr.Number)
}

func pendingReviewFollowUpsValue(value any) []domain.PendingReviewFollowUp {
	nodes, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]domain.PendingReviewFollowUp, 0, len(nodes))
	for _, node := range nodes {
		item, ok := node.(map[string]any)
		if !ok {
			continue
		}
		followUp := domain.PendingReviewFollowUp{}
		followUp.ThreadID, _ = stringValue(item["thread_id"])
		followUp.CommentID, _ = stringValue(item["comment_id"])
		followUp.ReactionID, _ = stringValue(item["reaction_id"])
		followUp.Reactor, _ = stringValue(item["reactor"])
		if value, _ := stringValue(item["requested_at"]); strings.TrimSpace(value) != "" {
			if parsed, err := time.Parse(time.RFC3339, value); err == nil {
				followUp.RequestedAt = &parsed
			}
		}
		out = append(out, followUp)
	}
	return out
}

func pendingReviewFollowUpsAny(items []domain.PendingReviewFollowUp) []any {
	if len(items) == 0 {
		return nil
	}
	out := make([]any, 0, len(items))
	for _, item := range items {
		entry := map[string]any{
			"thread_id":   strings.TrimSpace(item.ThreadID),
			"comment_id":  strings.TrimSpace(item.CommentID),
			"reaction_id": strings.TrimSpace(item.ReactionID),
			"reactor":     strings.TrimSpace(item.Reactor),
		}
		if item.RequestedAt != nil {
			entry["requested_at"] = item.RequestedAt.UTC().Format(time.RFC3339)
		}
		out = append(out, entry)
	}
	return out
}

func reviewFeedbackIssueLinksValue(value any) []domain.ReviewFeedbackIssueLink {
	nodes, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]domain.ReviewFeedbackIssueLink, 0, len(nodes))
	for _, node := range nodes {
		item, ok := node.(map[string]any)
		if !ok {
			continue
		}
		link := domain.ReviewFeedbackIssueLink{}
		link.ThreadID, _ = stringValue(item["thread_id"])
		link.CommentID, _ = stringValue(item["comment_id"])
		link.ReactionID, _ = stringValue(item["reaction_id"])
		link.Reactor, _ = stringValue(item["reactor"])
		link.IssueID, _ = stringValue(item["issue_id"])
		link.IssueIdentifier, _ = stringValue(item["issue_identifier"])
		link.IssueURL, _ = stringValue(item["issue_url"])
		if value, _ := stringValue(item["requested_at"]); strings.TrimSpace(value) != "" {
			if parsed, err := time.Parse(time.RFC3339, value); err == nil {
				link.RequestedAt = &parsed
			}
		}
		if value, _ := stringValue(item["created_at"]); strings.TrimSpace(value) != "" {
			if parsed, err := time.Parse(time.RFC3339, value); err == nil {
				link.CreatedAt = &parsed
			}
		}
		out = append(out, link)
	}
	return out
}

func reviewFeedbackIssueLinksAny(items []domain.ReviewFeedbackIssueLink) []any {
	if len(items) == 0 {
		return nil
	}
	out := make([]any, 0, len(items))
	for _, item := range items {
		entry := map[string]any{
			"thread_id":        strings.TrimSpace(item.ThreadID),
			"comment_id":       strings.TrimSpace(item.CommentID),
			"reaction_id":      strings.TrimSpace(item.ReactionID),
			"reactor":          strings.TrimSpace(item.Reactor),
			"issue_id":         strings.TrimSpace(item.IssueID),
			"issue_identifier": strings.TrimSpace(item.IssueIdentifier),
			"issue_url":        strings.TrimSpace(item.IssueURL),
		}
		if item.RequestedAt != nil {
			entry["requested_at"] = item.RequestedAt.UTC().Format(time.RFC3339)
		}
		if item.CreatedAt != nil {
			entry["created_at"] = item.CreatedAt.UTC().Format(time.RFC3339)
		}
		out = append(out, entry)
	}
	return out
}

func pendingCheckFailureValue(value any) *domain.PendingPullRequestCheckFailure {
	node, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	failure := &domain.PendingPullRequestCheckFailure{}
	failure.Name, _ = stringValue(node["name"])
	failure.FailureKind, _ = stringValue(node["failure_kind"])
	failure.Status, _ = stringValue(node["status"])
	failure.Conclusion, _ = stringValue(node["conclusion"])
	failure.DetailsURL, _ = stringValue(node["details_url"])
	failure.Summary, _ = stringValue(node["summary"])
	failure.HeadSHA, _ = stringValue(node["head_sha"])
	if value, ok := intValue(node["pr_number"]); ok {
		failure.PRNumber = value
	}
	failure.PRURL, _ = stringValue(node["pr_url"])
	if value, _ := stringValue(node["observed_at"]); strings.TrimSpace(value) != "" {
		if parsed, err := time.Parse(time.RFC3339, value); err == nil {
			failure.ObservedAt = &parsed
		}
	}
	if strings.TrimSpace(failure.Name) == "" &&
		strings.TrimSpace(failure.FailureKind) == "" &&
		strings.TrimSpace(failure.HeadSHA) == "" &&
		failure.PRNumber == 0 {
		return nil
	}
	return failure
}

func pendingCheckFailureAny(failure *domain.PendingPullRequestCheckFailure) map[string]any {
	if failure == nil {
		return nil
	}
	out := map[string]any{
		"name":         strings.TrimSpace(failure.Name),
		"failure_kind": strings.TrimSpace(failure.FailureKind),
		"status":       strings.TrimSpace(failure.Status),
		"conclusion":   strings.TrimSpace(failure.Conclusion),
		"details_url":  strings.TrimSpace(failure.DetailsURL),
		"summary":      strings.TrimSpace(failure.Summary),
		"head_sha":     strings.TrimSpace(failure.HeadSHA),
		"pr_number":    failure.PRNumber,
		"pr_url":       strings.TrimSpace(failure.PRURL),
	}
	if failure.ObservedAt != nil {
		out["observed_at"] = failure.ObservedAt.UTC().Format(time.RFC3339)
	}
	return out
}

func stringMapValue(value any) map[string]string {
	node, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(node))
	for key, raw := range node {
		text, ok := stringValue(raw)
		if !ok {
			continue
		}
		out[key] = text
	}
	return out
}

func stringMapAny(values map[string]string) map[string]any {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func stringSliceValue(value any) []string {
	nodes, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(nodes))
	for _, node := range nodes {
		text, ok := stringValue(node)
		if !ok {
			continue
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		out = append(out, text)
	}
	return out
}

func stringSliceAny(values []string) []any {
	if len(values) == 0 {
		return nil
	}
	out := make([]any, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		out = append(out, value)
	}
	return out
}

func uiBaseURL(cfg domain.ServerConfig) string {
	if value := strings.TrimSpace(cfg.UIURL); value != "" {
		return value
	}
	if cfg.Port != nil {
		return fmt.Sprintf("http://127.0.0.1:%d", *cfg.Port)
	}
	return "http://127.0.0.1"
}

func colinExecPlanAttachmentURL(issueID string) string {
	return domain.ColinExecPlanPath(issueID)
}

func isColinExecPlanURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return false
	}
	_, ok := domain.ParseColinExecPlanPath(parsed.EscapedPath())
	return ok
}

func (c *Client) ensureIssueLabelID(ctx context.Context, labelName string) (string, error) {
	name := strings.TrimSpace(labelName)
	if name == "" {
		return "", errors.New("missing issue label name")
	}
	cacheKey := strings.ToLower(name)

	c.labelMu.RLock()
	if labelID := strings.TrimSpace(c.labelIDs[cacheKey]); labelID != "" {
		c.labelMu.RUnlock()
		return labelID, nil
	}
	c.labelMu.RUnlock()

	labelID, err := c.findIssueLabelID(ctx, name)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(labelID) == "" {
		labelID, err = c.createIssueLabel(ctx, name)
		if err != nil {
			return "", err
		}
	}

	c.labelMu.Lock()
	c.labelIDs[cacheKey] = labelID
	c.labelMu.Unlock()
	return labelID, nil
}

func (c *Client) findIssueLabelID(ctx context.Context, labelName string) (string, error) {
	const query = `
query IssueLabelsByName($name: String!) {
  issueLabels(first: 10, filter: { name: { eq: $name } }) {
    nodes {
      id
      name
    }
  }
}
`
	resp, err := c.doQuery(ctx, query, map[string]any{"name": labelName})
	if err != nil {
		return "", err
	}
	nodes, ok := nestedSlice(resp, "data", "issueLabels", "nodes")
	if !ok {
		return "", ErrUnknownPayload
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

func (c *Client) createIssueLabel(ctx context.Context, labelName string) (string, error) {
	const query = `
mutation CreateIssueLabel($input: IssueLabelCreateInput!) {
  issueLabelCreate(input: $input) {
    success
    issueLabel {
      id
      name
    }
  }
}
`
	resp, err := c.doQuery(ctx, query, map[string]any{
		"input": map[string]any{
			"name": labelName,
		},
	})
	if err != nil {
		return "", err
	}
	success, _ := nestedBool(resp, "data", "issueLabelCreate", "success")
	if !success {
		return "", ErrUnknownPayload
	}
	labelID, _ := nestedString(resp, "data", "issueLabelCreate", "issueLabel", "id")
	if strings.TrimSpace(labelID) == "" {
		return "", ErrUnknownPayload
	}
	return labelID, nil
}

func derefStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func gitAutomationMatchScore(node map[string]any, targetBranch string) int {
	targetBranch = strings.TrimSpace(targetBranch)
	targetNode, ok := node["targetBranch"].(map[string]any)
	if !ok || targetNode == nil {
		return 1
	}

	pattern, _ := stringValue(targetNode["branchPattern"])
	isRegex, _ := targetNode["isRegex"].(bool)
	if targetBranch == "" || strings.TrimSpace(pattern) == "" {
		return 0
	}
	if isRegex {
		matched, err := regexp.MatchString(pattern, targetBranch)
		if err != nil || !matched {
			return 0
		}
		return 2
	}
	if pattern == targetBranch {
		return 3
	}
	return 0
}

func parseCreatedCommentID(resp map[string]any) (string, error) {
	success, _ := nestedBool(resp, "data", "commentCreate", "success")
	if !success {
		return "", ErrUnknownPayload
	}
	commentID, ok := nestedString(resp, "data", "commentCreate", "comment", "id")
	if !ok || strings.TrimSpace(commentID) == "" {
		return "", ErrUnknownPayload
	}
	return commentID, nil
}

func parseCreatedIssue(resp map[string]any) (domain.CreatedIssue, error) {
	success, _ := nestedBool(resp, "data", "issueCreate", "success")
	if !success {
		return domain.CreatedIssue{}, ErrUnknownPayload
	}
	node, ok := nestedMap(resp, "data", "issueCreate", "issue")
	if !ok {
		return domain.CreatedIssue{}, ErrUnknownPayload
	}
	issue := domain.CreatedIssue{}
	issue.ID, _ = stringValue(node["id"])
	issue.Identifier, _ = stringValue(node["identifier"])
	issue.Title, _ = stringValue(node["title"])
	issue.URL, _ = stringValue(node["url"])
	if strings.TrimSpace(issue.ID) == "" || strings.TrimSpace(issue.Identifier) == "" {
		return domain.CreatedIssue{}, ErrUnknownPayload
	}
	return issue, nil
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

func nestedString(root map[string]any, keys ...string) (string, bool) {
	value, ok := nestedValue(root, keys...)
	if !ok {
		return "", false
	}
	return stringValue(value)
}

func nestedMap(root map[string]any, keys ...string) (map[string]any, bool) {
	value, ok := nestedValue(root, keys...)
	if !ok || value == nil {
		return nil, false
	}
	asMap, ok := value.(map[string]any)
	return asMap, ok
}

func nestedBool(root map[string]any, keys ...string) (bool, bool) {
	value, ok := nestedValue(root, keys...)
	if !ok {
		return false, false
	}
	v, ok := value.(bool)
	return v, ok
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
	switch v := value.(type) {
	case string:
		return v, true
	default:
		return "", false
	}
}

func intValue(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	default:
		return 0, false
	}
}

func parseHeaderInt(value string) (int64, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

func parseHeaderTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	if unix, err := strconv.ParseInt(value, 10, 64); err == nil {
		if unix > 1_000_000_000_000 {
			return time.UnixMilli(unix).UTC(), true
		}
		return time.Unix(unix, 0).UTC(), true
	}
	for _, layout := range []string{time.RFC3339, time.RFC1123, http.TimeFormat} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}

func cloneRateLimits(input domain.RateLimitSnapshot) domain.RateLimitSnapshot {
	if len(input) == 0 {
		return nil
	}
	out := make(domain.RateLimitSnapshot, len(input))
	for key, value := range input {
		clone := value
		if value.WindowDurationMinutes != nil {
			clone.WindowDurationMinutes = int64Ptr(*value.WindowDurationMinutes)
		}
		if value.Limit != nil {
			clone.Limit = int64Ptr(*value.Limit)
		}
		if value.Remaining != nil {
			clone.Remaining = int64Ptr(*value.Remaining)
		}
		if value.UsedPercent != nil {
			clone.UsedPercent = int64Ptr(*value.UsedPercent)
		}
		if value.ResetsAt != nil {
			clone.ResetsAt = timePtr(*value.ResetsAt)
		}
		if value.NextAllowedAt != nil {
			clone.NextAllowedAt = timePtr(*value.NextAllowedAt)
		}
		out[key] = clone
	}
	return out
}

func int64Ptr(value int64) *int64 {
	return &value
}

func timePtr(value time.Time) *time.Time {
	copy := value.UTC()
	return &copy
}
