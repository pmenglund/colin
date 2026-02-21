//go:build livee2e

package e2e_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const liveLinearEndpoint = "https://api.linear.app/graphql"
const (
	liveMetadataAttachmentTitle    = "Colin metadata"
	liveMetadataAttachmentSubtitle = "Managed by Colin"
	liveMetadataAttachmentURLValue = "https://github.com/pmenglund/colin/blob/main/docs/metadata.md"
)

type liveLinearAdmin struct {
	endpoint   string
	token      string
	teamID     string
	team       string
	httpClient *http.Client
}

type liveLinearProject struct {
	ID   string
	Name string
	URL  string
}

type liveLinearIssue struct {
	ID          string
	Identifier  string
	Title       string
	URL         string
	StateName   string
	Description string
	Metadata    map[string]string
	Comments    []string
}

type liveLinearIssueInput struct {
	Title       string
	Description string
	ProjectID   string
	StateID     string
}

func newLiveLinearAdmin(token string, team string) *liveLinearAdmin {
	return &liveLinearAdmin{
		endpoint: liveLinearEndpoint,
		token:    strings.TrimSpace(token),
		team:     strings.TrimSpace(team),
		httpClient: &http.Client{
			Timeout: 20 * time.Second,
		},
	}
}

func (a *liveLinearAdmin) resolveTeamIDByKey(ctx context.Context) (string, error) {
	query := `query ResolveTeamByKey($teamKey: String!) {
  teams(filter: { key: { eq: $teamKey } }, first: 1) {
    nodes {
      id
      key
    }
  }
}`

	var resp struct {
		Teams struct {
			Nodes []struct {
				ID  string `json:"id"`
				Key string `json:"key"`
			} `json:"nodes"`
		} `json:"teams"`
	}
	if err := a.graphQL(ctx, query, map[string]any{"teamKey": a.team}, &resp); err != nil {
		return "", err
	}
	if len(resp.Teams.Nodes) == 0 || strings.TrimSpace(resp.Teams.Nodes[0].ID) == "" {
		return "", fmt.Errorf("team with key %q not found", a.team)
	}
	return strings.TrimSpace(resp.Teams.Nodes[0].ID), nil
}

func (a *liveLinearAdmin) stateIDByName(ctx context.Context) (map[string]string, error) {
	query := `query WorkflowStates($teamKey: String!) {
  workflowStates(filter: { team: { key: { eq: $teamKey } } }) {
    nodes {
      id
      name
    }
  }
}`

	var resp struct {
		WorkflowStates struct {
			Nodes []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"nodes"`
		} `json:"workflowStates"`
	}
	if err := a.graphQL(ctx, query, map[string]any{"teamKey": a.team}, &resp); err != nil {
		return nil, err
	}

	out := make(map[string]string, len(resp.WorkflowStates.Nodes))
	for _, node := range resp.WorkflowStates.Nodes {
		out[node.Name] = node.ID
	}
	return out, nil
}

func (a *liveLinearAdmin) createProject(ctx context.Context, name string, description string) (liveLinearProject, error) {
	if strings.TrimSpace(a.teamID) == "" {
		return liveLinearProject{}, fmt.Errorf("team id is required")
	}

	mutation := `mutation CreateProject($input: ProjectCreateInput!) {
  projectCreate(input: $input) {
    success
    project {
      id
      name
      url
    }
  }
}`

	variables := map[string]any{
		"input": map[string]any{
			"name":        strings.TrimSpace(name),
			"description": strings.TrimSpace(description),
			"teamIds":     []string{a.teamID},
		},
	}

	var resp struct {
		ProjectCreate struct {
			Success bool `json:"success"`
			Project struct {
				ID   string `json:"id"`
				Name string `json:"name"`
				URL  string `json:"url"`
			} `json:"project"`
		} `json:"projectCreate"`
	}
	if err := a.graphQL(ctx, mutation, variables, &resp); err != nil {
		return liveLinearProject{}, err
	}
	if !resp.ProjectCreate.Success || strings.TrimSpace(resp.ProjectCreate.Project.ID) == "" {
		return liveLinearProject{}, fmt.Errorf("projectCreate returned unsuccessful response")
	}
	return liveLinearProject{
		ID:   resp.ProjectCreate.Project.ID,
		Name: resp.ProjectCreate.Project.Name,
		URL:  resp.ProjectCreate.Project.URL,
	}, nil
}

func (a *liveLinearAdmin) createIssue(ctx context.Context, input liveLinearIssueInput) (liveLinearIssue, error) {
	if strings.TrimSpace(a.teamID) == "" {
		return liveLinearIssue{}, fmt.Errorf("team id is required")
	}

	mutation := `mutation CreateIssue($input: IssueCreateInput!) {
  issueCreate(input: $input) {
    success
    issue {
      id
      identifier
      title
      url
      description
      state {
        name
      }
    }
  }
}`

	issueInput := map[string]any{
		"teamId":      a.teamID,
		"projectId":   strings.TrimSpace(input.ProjectID),
		"title":       strings.TrimSpace(input.Title),
		"description": strings.TrimSpace(input.Description),
	}
	if strings.TrimSpace(input.StateID) != "" {
		issueInput["stateId"] = strings.TrimSpace(input.StateID)
	}

	variables := map[string]any{"input": issueInput}

	var resp struct {
		IssueCreate struct {
			Success bool `json:"success"`
			Issue   struct {
				ID          string `json:"id"`
				Identifier  string `json:"identifier"`
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
				State       struct {
					Name string `json:"name"`
				} `json:"state"`
			} `json:"issue"`
		} `json:"issueCreate"`
	}
	if err := a.graphQL(ctx, mutation, variables, &resp); err != nil {
		return liveLinearIssue{}, err
	}
	if !resp.IssueCreate.Success || strings.TrimSpace(resp.IssueCreate.Issue.ID) == "" {
		return liveLinearIssue{}, fmt.Errorf("issueCreate returned unsuccessful response")
	}
	return toLiveIssue(
		resp.IssueCreate.Issue.ID,
		resp.IssueCreate.Issue.Identifier,
		resp.IssueCreate.Issue.Title,
		resp.IssueCreate.Issue.URL,
		resp.IssueCreate.Issue.State.Name,
		resp.IssueCreate.Issue.Description,
		map[string]string{},
		nil,
	), nil
}

func (a *liveLinearAdmin) updateIssueDescription(ctx context.Context, issueID string, description string) error {
	mutation := `mutation UpdateIssue($id: String!, $input: IssueUpdateInput!) {
  issueUpdate(id: $id, input: $input) {
    success
  }
}`

	variables := map[string]any{
		"id": strings.TrimSpace(issueID),
		"input": map[string]any{
			"description": description,
		},
	}

	var resp struct {
		IssueUpdate struct {
			Success bool `json:"success"`
		} `json:"issueUpdate"`
	}
	if err := a.graphQL(ctx, mutation, variables, &resp); err != nil {
		return err
	}
	if !resp.IssueUpdate.Success {
		return fmt.Errorf("issueUpdate returned unsuccessful response")
	}
	return nil
}

func (a *liveLinearAdmin) getIssue(ctx context.Context, issueID string) (liveLinearIssue, error) {
	query := `query GetIssue($issueId: String!) {
  issue(id: $issueId) {
    id
    identifier
    title
    url
    description
    state {
      name
    }
    comments(first: 50) {
      nodes {
        body
      }
    }
  }
}`

	var resp struct {
		Issue *struct {
			ID          string `json:"id"`
			Identifier  string `json:"identifier"`
			Title       string `json:"title"`
			URL         string `json:"url"`
			Description string `json:"description"`
			State       struct {
				Name string `json:"name"`
			} `json:"state"`
			Comments struct {
				Nodes []struct {
					Body string `json:"body"`
				} `json:"nodes"`
			} `json:"comments"`
		} `json:"issue"`
	}
	if err := a.graphQL(ctx, query, map[string]any{"issueId": issueID}, &resp); err != nil {
		return liveLinearIssue{}, err
	}
	if resp.Issue == nil {
		return liveLinearIssue{}, fmt.Errorf("issue %q not found", issueID)
	}

	comments := make([]string, 0, len(resp.Issue.Comments.Nodes))
	for _, node := range resp.Issue.Comments.Nodes {
		comments = append(comments, strings.TrimSpace(node.Body))
	}

	metadata, err := a.getIssueMetadata(ctx, strings.TrimSpace(issueID))
	if err != nil {
		return liveLinearIssue{}, err
	}

	return toLiveIssue(
		resp.Issue.ID,
		resp.Issue.Identifier,
		resp.Issue.Title,
		resp.Issue.URL,
		resp.Issue.State.Name,
		resp.Issue.Description,
		metadata,
		comments,
	), nil
}

func (a *liveLinearAdmin) archiveIssue(ctx context.Context, issueID string) error {
	mutation := `mutation ArchiveIssue($id: String!) {
  issueArchive(id: $id) {
    success
  }
}`

	var resp struct {
		IssueArchive struct {
			Success bool `json:"success"`
		} `json:"issueArchive"`
	}
	if err := a.graphQL(ctx, mutation, map[string]any{"id": issueID}, &resp); err != nil {
		return err
	}
	if !resp.IssueArchive.Success {
		return fmt.Errorf("issueArchive returned unsuccessful response")
	}
	return nil
}

func (a *liveLinearAdmin) deleteProject(ctx context.Context, projectID string) error {
	mutation := `mutation DeleteProject($id: String!) {
  projectDelete(id: $id) {
    success
  }
}`

	var resp struct {
		ProjectDelete struct {
			Success bool `json:"success"`
		} `json:"projectDelete"`
	}
	if err := a.graphQL(ctx, mutation, map[string]any{"id": projectID}, &resp); err != nil {
		return err
	}
	if !resp.ProjectDelete.Success {
		return fmt.Errorf("projectDelete returned unsuccessful response")
	}
	return nil
}

func (a *liveLinearAdmin) graphQL(ctx context.Context, query string, variables map[string]any, out any) error {
	if out == nil {
		return fmt.Errorf("graphql output target is required")
	}
	if strings.TrimSpace(query) == "" {
		return fmt.Errorf("graphql query is required")
	}
	if variables == nil {
		variables = map[string]any{}
	}

	body, err := json.Marshal(map[string]any{
		"query":     query,
		"variables": variables,
	})
	if err != nil {
		return fmt.Errorf("marshal graphql request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create graphql request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", a.token)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("execute graphql request: %w", err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read graphql response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("graphql status %d: %s", resp.StatusCode, strings.TrimSpace(string(payload)))
	}

	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return fmt.Errorf("decode graphql envelope: %w", err)
	}
	if len(envelope.Errors) > 0 {
		messages := make([]string, 0, len(envelope.Errors))
		for _, entry := range envelope.Errors {
			if strings.TrimSpace(entry.Message) != "" {
				messages = append(messages, strings.TrimSpace(entry.Message))
			}
		}
		if len(messages) == 0 {
			messages = append(messages, "unknown graphql error")
		}
		return fmt.Errorf("graphql error: %s", strings.Join(messages, "; "))
	}
	if len(envelope.Data) == 0 {
		return fmt.Errorf("graphql response did not include data")
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return fmt.Errorf("decode graphql data: %w", err)
	}
	return nil
}

func toLiveIssue(
	id string,
	identifier string,
	title string,
	url string,
	stateName string,
	description string,
	metadata map[string]string,
	comments []string,
) liveLinearIssue {
	return liveLinearIssue{
		ID:          strings.TrimSpace(id),
		Identifier:  strings.TrimSpace(identifier),
		Title:       strings.TrimSpace(title),
		URL:         strings.TrimSpace(url),
		StateName:   strings.TrimSpace(stateName),
		Description: description,
		Metadata:    cloneLiveMetadata(metadata),
		Comments:    append([]string(nil), comments...),
	}
}

func (a *liveLinearAdmin) getIssueMetadata(ctx context.Context, issueID string) (map[string]string, error) {
	query := `query AttachmentsForURL($url: String!) {
  attachmentsForURL(url: $url) {
    nodes {
      metadata
      issue {
        id
      }
    }
  }
}`

	var resp struct {
		AttachmentsForURL struct {
			Nodes []struct {
				Metadata any `json:"metadata"`
				Issue    *struct {
					ID string `json:"id"`
				} `json:"issue"`
			} `json:"nodes"`
		} `json:"attachmentsForURL"`
	}
	if err := a.graphQL(ctx, query, map[string]any{
		"url": liveMetadataAttachmentURL(issueID),
	}, &resp); err != nil {
		return nil, err
	}

	trimmedIssueID := strings.TrimSpace(issueID)
	for _, node := range resp.AttachmentsForURL.Nodes {
		if node.Issue == nil || strings.TrimSpace(node.Issue.ID) != trimmedIssueID {
			continue
		}
		return liveMetadataObjectToStringMap(node.Metadata), nil
	}
	return map[string]string{}, nil
}

func (a *liveLinearAdmin) upsertIssueMetadata(ctx context.Context, issueID string, set map[string]string) error {
	current, err := a.getIssueMetadata(ctx, issueID)
	if err != nil {
		return err
	}
	for key, value := range set {
		trimmedKey := strings.TrimSpace(key)
		if trimmedKey == "" {
			continue
		}
		current[trimmedKey] = value
	}

	mutation := `mutation UpsertIssueMetadataAttachment($input: AttachmentCreateInput!) {
  attachmentCreate(input: $input) {
    success
  }
}`
	var resp struct {
		AttachmentCreate struct {
			Success bool `json:"success"`
		} `json:"attachmentCreate"`
	}
	if err := a.graphQL(ctx, mutation, map[string]any{
		"input": map[string]any{
			"issueId":  strings.TrimSpace(issueID),
			"url":      liveMetadataAttachmentURL(issueID),
			"title":    liveMetadataAttachmentTitle,
			"subtitle": liveMetadataAttachmentSubtitle,
			"metadata": liveMetadataStringMapToObject(current),
		},
	}, &resp); err != nil {
		return err
	}
	if !resp.AttachmentCreate.Success {
		return fmt.Errorf("attachmentCreate returned unsuccessful response")
	}
	return nil
}

func liveMetadataAttachmentURL(_ string) string {
	return liveMetadataAttachmentURLValue
}

func liveMetadataObjectToStringMap(raw any) map[string]string {
	object, ok := raw.(map[string]any)
	if !ok || len(object) == 0 {
		return map[string]string{}
	}

	out := make(map[string]string, len(object))
	for key, value := range object {
		trimmedKey := strings.TrimSpace(key)
		if trimmedKey == "" || value == nil {
			continue
		}
		out[trimmedKey] = fmt.Sprint(value)
	}
	return out
}

func liveMetadataStringMapToObject(metadata map[string]string) map[string]any {
	out := make(map[string]any, len(metadata))
	for key, value := range metadata {
		trimmedKey := strings.TrimSpace(key)
		if trimmedKey == "" {
			continue
		}
		out[trimmedKey] = value
	}
	return out
}

func cloneLiveMetadata(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
