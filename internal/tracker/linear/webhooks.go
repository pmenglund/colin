package linear

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

const (
	linearWebhookResourceTypeIssue      = "Issue"
	linearWebhookResourceTypeIssueLabel = "IssueLabel"
)

// WebhookSetupResult describes the final managed webhook state after setup.
type WebhookSetupResult struct {
	Action    string
	WebhookID string
	Label     string
	URL       string
	TeamID    string
	TeamName  string
}

type projectTeam struct {
	ID   string
	Name string
}

type organizationWebhook struct {
	ID            string
	Label         string
	URL           string
	Enabled       bool
	ResourceTypes []string
	TeamID        string
	TeamName      string
}

// EnsureProjectIssueWebhook ensures the watched project has exactly one managed Issue and IssueLabel webhook.
func (c *Client) EnsureProjectIssueWebhook(ctx context.Context, webhookURL string, label string) (WebhookSetupResult, error) {
	webhookURL = strings.TrimSpace(webhookURL)
	if webhookURL == "" {
		return WebhookSetupResult{}, errors.New("missing webhook url")
	}
	label = strings.TrimSpace(label)
	if label == "" {
		return WebhookSetupResult{}, errors.New("missing webhook label")
	}

	team, err := c.lookupProjectTeam(ctx)
	if err != nil {
		return WebhookSetupResult{}, err
	}
	webhooks, err := c.listOrganizationWebhooks(ctx)
	if err != nil {
		return WebhookSetupResult{}, err
	}

	managed := managedTeamWebhooks(webhooks, team.ID, webhookURL, label)
	if len(managed) == 1 && webhookURLsEqual(managed[0].URL, webhookURL) && managed[0].Enabled && strings.EqualFold(strings.TrimSpace(managed[0].Label), label) && hasLinearWebhookResourceTypes(managed[0].ResourceTypes) {
		return WebhookSetupResult{
			Action:    "unchanged",
			WebhookID: managed[0].ID,
			Label:     managed[0].Label,
			URL:       webhookURL,
			TeamID:    team.ID,
			TeamName:  team.Name,
		}, nil
	}

	action := "created"
	if len(managed) > 0 {
		action = "replaced"
		for _, webhook := range managed {
			if err := c.deleteWebhook(ctx, webhook.ID); err != nil {
				return WebhookSetupResult{}, err
			}
		}
	}

	webhookID, err := c.createWebhook(ctx, team.ID, webhookURL, label, []string{linearWebhookResourceTypeIssue, linearWebhookResourceTypeIssueLabel})
	if err != nil {
		return WebhookSetupResult{}, err
	}
	return WebhookSetupResult{
		Action:    action,
		WebhookID: webhookID,
		Label:     label,
		URL:       webhookURL,
		TeamID:    team.ID,
		TeamName:  team.Name,
	}, nil
}

func (c *Client) lookupProjectTeam(ctx context.Context) (projectTeam, error) {
	const query = `
query ProjectTeamInfo($slug: String!) {
  projects(first: 1, filter: { slugId: { eq: $slug } }) {
    nodes {
      id
      teams {
        nodes {
          id
          name
        }
      }
    }
  }
}
`
	resp, err := c.doQuery(ctx, query, map[string]any{"slug": c.primaryProjectSlug})
	if err != nil {
		return projectTeam{}, err
	}
	nodes, ok := nestedSlice(resp, "data", "projects", "nodes")
	if !ok || len(nodes) == 0 {
		return projectTeam{}, fmt.Errorf("%w: project %q not found", ErrUnknownPayload, c.primaryProjectSlug)
	}
	teamNodes, ok := nestedSlice(nodes[0], "teams", "nodes")
	if !ok || len(teamNodes) == 0 {
		return projectTeam{}, fmt.Errorf("%w: project %q has no teams", ErrUnknownPayload, c.primaryProjectSlug)
	}
	if len(teamNodes) != 1 {
		return projectTeam{}, fmt.Errorf("project %q has %d teams; linear webhook setup requires exactly 1 team", c.primaryProjectSlug, len(teamNodes))
	}
	teamID, _ := stringValue(teamNodes[0]["id"])
	teamName, _ := stringValue(teamNodes[0]["name"])
	if strings.TrimSpace(teamID) == "" {
		return projectTeam{}, fmt.Errorf("%w: project %q team id missing", ErrUnknownPayload, c.primaryProjectSlug)
	}
	if strings.TrimSpace(teamName) == "" {
		teamName = teamID
	}
	return projectTeam{ID: teamID, Name: teamName}, nil
}

func (c *Client) listOrganizationWebhooks(ctx context.Context) ([]organizationWebhook, error) {
	const query = `
	query OrganizationWebhooks {
	webhooks(first: 250) {
    nodes {
      id
      label
      url
      enabled
      resourceTypes
      team {
        id
        name
      }
    }
  }
}
`
	resp, err := c.doQuery(ctx, query, nil)
	if err != nil {
		return nil, err
	}
	nodes, ok := nestedSlice(resp, "data", "webhooks", "nodes")
	if !ok {
		return nil, ErrUnknownPayload
	}
	out := make([]organizationWebhook, 0, len(nodes))
	for _, node := range nodes {
		id, _ := stringValue(node["id"])
		if strings.TrimSpace(id) == "" {
			continue
		}
		webhook := organizationWebhook{
			ID:      id,
			Label:   strings.TrimSpace(nestedStringValue(node, "label")),
			URL:     strings.TrimSpace(nestedStringValue(node, "url")),
			Enabled: nestedBoolValue(node, "enabled"),
		}
		if values, ok := node["resourceTypes"].([]any); ok {
			webhook.ResourceTypes = make([]string, 0, len(values))
			for _, value := range values {
				resourceType, _ := stringValue(value)
				resourceType = strings.TrimSpace(resourceType)
				if resourceType == "" {
					continue
				}
				webhook.ResourceTypes = append(webhook.ResourceTypes, resourceType)
			}
		}
		if teamNode, ok := nestedMap(node, "team"); ok {
			webhook.TeamID, _ = stringValue(teamNode["id"])
			webhook.TeamName, _ = stringValue(teamNode["name"])
		}
		out = append(out, webhook)
	}
	return out, nil
}

func (c *Client) createWebhook(ctx context.Context, teamID string, webhookURL string, label string, resourceTypes []string) (string, error) {
	const query = `
mutation CreateWebhook($input: WebhookCreateInput!) {
  webhookCreate(input: $input) {
    success
    webhook {
      id
      enabled
    }
  }
}
`
	resp, err := c.doQuery(ctx, query, map[string]any{
		"input": map[string]any{
			"url":           webhookURL,
			"label":         label,
			"teamId":        teamID,
			"resourceTypes": resourceTypes,
		},
	})
	if err != nil {
		return "", err
	}
	if ok, _ := nestedBool(resp, "data", "webhookCreate", "success"); !ok {
		return "", errors.New("webhookCreate returned success=false")
	}
	id, ok := nestedString(resp, "data", "webhookCreate", "webhook", "id")
	if !ok || strings.TrimSpace(id) == "" {
		return "", errors.New("webhookCreate webhook id missing")
	}
	return id, nil
}

func (c *Client) deleteWebhook(ctx context.Context, webhookID string) error {
	const query = `
mutation DeleteWebhook($id: String!) {
  webhookDelete(id: $id) {
    success
  }
}
`
	resp, err := c.doQuery(ctx, query, map[string]any{"id": webhookID})
	if err != nil {
		return err
	}
	if ok, _ := nestedBool(resp, "data", "webhookDelete", "success"); !ok {
		return errors.New("webhookDelete returned success=false")
	}
	return nil
}

func managedTeamWebhooks(webhooks []organizationWebhook, teamID string, targetURL string, label string) []organizationWebhook {
	out := make([]organizationWebhook, 0, len(webhooks))
	for _, webhook := range webhooks {
		if !strings.EqualFold(strings.TrimSpace(webhook.TeamID), strings.TrimSpace(teamID)) {
			continue
		}
		if !isManagedLinearWebhookURL(webhook.URL, targetURL) && !strings.EqualFold(strings.TrimSpace(webhook.Label), strings.TrimSpace(label)) {
			continue
		}
		out = append(out, webhook)
	}
	return out
}

func isManagedLinearWebhookURL(candidate string, target string) bool {
	if webhookURLsEqual(candidate, target) {
		return true
	}
	candidatePath, ok := normalizedURLPath(candidate)
	if !ok {
		return false
	}
	targetPath, ok := normalizedURLPath(target)
	if !ok {
		return false
	}
	return candidatePath == targetPath
}

func hasLinearWebhookResourceTypes(resourceTypes []string) bool {
	hasIssue := false
	hasIssueLabel := false
	for _, resourceType := range resourceTypes {
		switch strings.ToLower(strings.TrimSpace(resourceType)) {
		case strings.ToLower(linearWebhookResourceTypeIssue):
			hasIssue = true
		case strings.ToLower(linearWebhookResourceTypeIssueLabel):
			hasIssueLabel = true
		}
	}
	return hasIssue && hasIssueLabel
}

func webhookURLsEqual(left string, right string) bool {
	return normalizeWebhookURL(left) == normalizeWebhookURL(right)
}

func normalizeWebhookURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return strings.TrimRight(strings.TrimSpace(raw), "/")
	}
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/")
}

func normalizedURLPath(raw string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", false
	}
	path := strings.TrimRight(strings.TrimSpace(parsed.EscapedPath()), "/")
	if path == "" {
		path = "/"
	}
	return path, true
}

func nestedStringValue(root map[string]any, keys ...string) string {
	value, _ := nestedString(root, keys...)
	return strings.TrimSpace(value)
}

func nestedBoolValue(root map[string]any, keys ...string) bool {
	value, _ := nestedBool(root, keys...)
	return value
}
