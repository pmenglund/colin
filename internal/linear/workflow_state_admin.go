package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/pmenglund/colin/internal/workflow"
)

const (
	workflowStateTypeUnstarted = "unstarted"
	workflowStateTypeStarted   = "started"
	workflowStateTypeCompleted = "completed"
)

var workflowStateCanonicalOrder = []string{"todo", "in_progress", "refine", "review", "merge", "merged", "done"}

// WorkflowStateMapping describes one resolved canonical workflow state.
type WorkflowStateMapping struct {
	CanonicalKey   string
	ConfiguredName string
	ActualName     string
	StateID        string
	StateType      string
	Created        bool
}

// WorkflowStateResolution contains resolved workflow states for one team.
type WorkflowStateResolution struct {
	TeamKey  string
	TeamID   string
	Mappings map[string]WorkflowStateMapping
}

// RuntimeStates returns resolved workflow state names for runtime transitions.
func (r WorkflowStateResolution) RuntimeStates() workflow.States {
	read := func(key, fallback string) string {
		mapping, ok := r.Mappings[key]
		if !ok {
			return fallback
		}
		name := strings.TrimSpace(mapping.ActualName)
		if name == "" {
			return fallback
		}
		return name
	}

	return workflow.States{
		Todo:       read("todo", workflow.StateTodo),
		InProgress: read("in_progress", workflow.StateInProgress),
		Refine:     read("refine", workflow.StateRefine),
		Review:     read("review", workflow.StateReview),
		Merge:      read("merge", workflow.StateMerge),
		Merged:     read("merged", workflow.StateMerged),
		Done:       read("done", workflow.StateDone),
	}
}

// StateIDByName returns a map of resolved state name to workflow state ID.
func (r WorkflowStateResolution) StateIDByName() map[string]string {
	out := make(map[string]string, len(r.Mappings))
	for _, mapping := range r.Mappings {
		if strings.TrimSpace(mapping.ActualName) == "" || strings.TrimSpace(mapping.StateID) == "" {
			continue
		}
		out[mapping.ActualName] = mapping.StateID
	}
	return out
}

// WorkflowStateAdmin provides setup/startup workflow-state operations against Linear.
type WorkflowStateAdmin struct {
	endpoint string
	token    string
	teamKey  string
	http     *http.Client
}

// NewWorkflowStateAdmin creates an admin helper for workflow state setup and resolution.
func NewWorkflowStateAdmin(endpoint, token, teamKey string, httpClient *http.Client) *WorkflowStateAdmin {
	if strings.TrimSpace(endpoint) == "" {
		endpoint = "https://api.linear.app/graphql"
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	return &WorkflowStateAdmin{
		endpoint: strings.TrimSpace(endpoint),
		token:    strings.TrimSpace(token),
		teamKey:  strings.TrimSpace(teamKey),
		http:     httpClient,
	}
}

// ResolveWorkflowStates validates and resolves configured workflow names to actual states.
func (a *WorkflowStateAdmin) ResolveWorkflowStates(ctx context.Context, configured workflow.States) (WorkflowStateResolution, error) {
	configured = configured.WithDefaults()
	if err := configured.Validate(); err != nil {
		return WorkflowStateResolution{}, err
	}

	teamID, err := a.resolveTeamIDByKey(ctx)
	if err != nil {
		return WorkflowStateResolution{}, err
	}
	availableStates, err := a.listWorkflowStates(ctx)
	if err != nil {
		return WorkflowStateResolution{}, err
	}

	mappings := make(map[string]WorkflowStateMapping, len(workflowStateCanonicalOrder))
	missing := make([]WorkflowStateMapping, 0)
	for _, canonical := range workflowStateCanonicalOrder {
		configuredName := configuredStateName(configured, canonical)
		expectedType := requiredWorkflowStateType(canonical)
		state, ok := findWorkflowStateByName(availableStates, configuredName)
		if !ok {
			missing = append(missing, WorkflowStateMapping{CanonicalKey: canonical, ConfiguredName: configuredName})
			continue
		}
		if !stateTypeMatches(state.Type, expectedType) {
			return WorkflowStateResolution{}, fmt.Errorf("workflow state %q mapped to %q has type %q, expected %q", canonical, configuredName, state.Type, expectedType)
		}
		mappings[canonical] = WorkflowStateMapping{
			CanonicalKey:   canonical,
			ConfiguredName: configuredName,
			ActualName:     state.Name,
			StateID:        state.ID,
			StateType:      state.Type,
		}
	}

	if len(missing) > 0 {
		parts := make([]string, 0, len(missing))
		for _, item := range missing {
			parts = append(parts, fmt.Sprintf("%s=%q", item.CanonicalKey, item.ConfiguredName))
		}
		sort.Strings(parts)
		return WorkflowStateResolution{}, fmt.Errorf("required workflow states not found: %s", strings.Join(parts, ", "))
	}

	return WorkflowStateResolution{
		TeamKey:  a.teamKey,
		TeamID:   teamID,
		Mappings: mappings,
	}, nil
}

// EnsureWorkflowStates creates missing mapped states and validates existing state types.
func (a *WorkflowStateAdmin) EnsureWorkflowStates(ctx context.Context, configured workflow.States) (WorkflowStateResolution, error) {
	configured = configured.WithDefaults()
	if err := configured.Validate(); err != nil {
		return WorkflowStateResolution{}, err
	}

	teamID, err := a.resolveTeamIDByKey(ctx)
	if err != nil {
		return WorkflowStateResolution{}, err
	}
	availableStates, err := a.listWorkflowStates(ctx)
	if err != nil {
		return WorkflowStateResolution{}, err
	}

	mappings := make(map[string]WorkflowStateMapping, len(workflowStateCanonicalOrder))
	for _, canonical := range workflowStateCanonicalOrder {
		configuredName := configuredStateName(configured, canonical)
		expectedType := requiredWorkflowStateType(canonical)
		state, ok := findWorkflowStateByName(availableStates, configuredName)
		if !ok {
			created, createErr := a.createWorkflowState(ctx, teamID, configuredName, expectedType)
			if createErr != nil {
				return WorkflowStateResolution{}, fmt.Errorf("create workflow state %q (%s): %w", configuredName, canonical, createErr)
			}
			mappings[canonical] = WorkflowStateMapping{
				CanonicalKey:   canonical,
				ConfiguredName: configuredName,
				ActualName:     created.Name,
				StateID:        created.ID,
				StateType:      created.Type,
				Created:        true,
			}
			availableStates = append(availableStates, created)
			continue
		}
		if !stateTypeMatches(state.Type, expectedType) {
			return WorkflowStateResolution{}, fmt.Errorf("workflow state %q mapped to %q has type %q, expected %q", canonical, configuredName, state.Type, expectedType)
		}
		mappings[canonical] = WorkflowStateMapping{
			CanonicalKey:   canonical,
			ConfiguredName: configuredName,
			ActualName:     state.Name,
			StateID:        state.ID,
			StateType:      state.Type,
			Created:        false,
		}
	}

	return WorkflowStateResolution{
		TeamKey:  a.teamKey,
		TeamID:   teamID,
		Mappings: mappings,
	}, nil
}

type workflowStateNode struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

func (a *WorkflowStateAdmin) resolveTeamIDByKey(ctx context.Context) (string, error) {
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
	if err := a.graphQL(ctx, query, map[string]any{"teamKey": a.teamKey}, &resp); err != nil {
		return "", err
	}
	if len(resp.Teams.Nodes) == 0 || strings.TrimSpace(resp.Teams.Nodes[0].ID) == "" {
		return "", fmt.Errorf("team with key %q not found", a.teamKey)
	}
	return strings.TrimSpace(resp.Teams.Nodes[0].ID), nil
}

func (a *WorkflowStateAdmin) listWorkflowStates(ctx context.Context) ([]workflowStateNode, error) {
	query := `query WorkflowStates($teamKey: String!) {
  workflowStates(filter: { team: { key: { eq: $teamKey } } }) {
    nodes {
      id
      name
      type
    }
  }
}`

	var resp struct {
		WorkflowStates struct {
			Nodes []workflowStateNode `json:"nodes"`
		} `json:"workflowStates"`
	}
	if err := a.graphQL(ctx, query, map[string]any{"teamKey": a.teamKey}, &resp); err != nil {
		return nil, err
	}
	out := make([]workflowStateNode, 0, len(resp.WorkflowStates.Nodes))
	for _, node := range resp.WorkflowStates.Nodes {
		trimmedName := strings.TrimSpace(node.Name)
		trimmedID := strings.TrimSpace(node.ID)
		if trimmedName == "" || trimmedID == "" {
			continue
		}
		out = append(out, workflowStateNode{
			ID:   trimmedID,
			Name: trimmedName,
			Type: strings.TrimSpace(node.Type),
		})
	}
	return out, nil
}

func (a *WorkflowStateAdmin) createWorkflowState(ctx context.Context, teamID string, name string, stateType string) (workflowStateNode, error) {
	mutation := `mutation WorkflowStateCreate($input: WorkflowStateCreateInput!) {
  workflowStateCreate(input: $input) {
    success
    workflowState {
      id
      name
      type
    }
  }
}`

	variables := map[string]any{
		"input": map[string]any{
			"teamId": teamID,
			"name":   name,
			"type":   stateType,
		},
	}

	var resp struct {
		WorkflowStateCreate struct {
			Success       bool              `json:"success"`
			WorkflowState workflowStateNode `json:"workflowState"`
		} `json:"workflowStateCreate"`
	}
	if err := a.graphQL(ctx, mutation, variables, &resp); err != nil {
		return workflowStateNode{}, err
	}
	if !resp.WorkflowStateCreate.Success || strings.TrimSpace(resp.WorkflowStateCreate.WorkflowState.ID) == "" {
		return workflowStateNode{}, errors.New("workflowStateCreate returned unsuccessful response")
	}
	created := resp.WorkflowStateCreate.WorkflowState
	created.ID = strings.TrimSpace(created.ID)
	created.Name = strings.TrimSpace(created.Name)
	created.Type = strings.TrimSpace(created.Type)
	return created, nil
}

func configuredStateName(states workflow.States, canonical string) string {
	states = states.WithDefaults()
	switch canonical {
	case "todo":
		return states.Todo
	case "in_progress":
		return states.InProgress
	case "refine":
		return states.Refine
	case "review":
		return states.Review
	case "merge":
		return states.Merge
	case "merged":
		return states.Merged
	case "done":
		return states.Done
	default:
		return ""
	}
}

func requiredWorkflowStateType(canonical string) string {
	switch canonical {
	case "todo":
		return workflowStateTypeUnstarted
	case "done":
		return workflowStateTypeCompleted
	default:
		return workflowStateTypeStarted
	}
}

func findWorkflowStateByName(states []workflowStateNode, configuredName string) (workflowStateNode, bool) {
	normalizedTarget := normalizeWorkflowStateName(configuredName)
	for _, state := range states {
		if normalizeWorkflowStateName(state.Name) == normalizedTarget {
			return state, true
		}
	}
	return workflowStateNode{}, false
}

func stateTypeMatches(actual string, expected string) bool {
	return normalizeWorkflowStateName(actual) == normalizeWorkflowStateName(expected)
}

func normalizeWorkflowStateName(name string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(name)), " "))
}

func (a *WorkflowStateAdmin) graphQL(ctx context.Context, query string, variables map[string]any, out any) error {
	if out == nil {
		return errors.New("graphql output target is required")
	}
	if strings.TrimSpace(query) == "" {
		return errors.New("graphql query is required")
	}
	if variables == nil {
		variables = map[string]any{}
	}

	body, err := json.Marshal(map[string]any{
		"query":     query,
		"variables": variables,
	})
	if err != nil {
		return fmt.Errorf("marshal GraphQL request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", a.token)

	resp, err := a.http.Do(req)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return newGraphQLStatusError(resp.StatusCode, resp.Header, respBody)
	}

	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return fmt.Errorf("decode response envelope: %w", err)
	}
	if len(envelope.Errors) > 0 {
		return newGraphQLMessageError(envelope.Errors[0].Message, resp.Header, respBody)
	}
	if len(envelope.Data) == 0 {
		return errors.New("graphql response missing data")
	}

	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return fmt.Errorf("decode response payload: %w", err)
	}
	return nil
}
