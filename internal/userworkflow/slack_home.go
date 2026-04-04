package userworkflow

import (
	"sort"
	"strings"

	"github.com/pmenglund/colin/internal/domain"
)

// SlackHomeView is the grouped watched-issue list rendered in the Slack App Home tab.
type SlackHomeView struct {
	TotalIssues int
	Groups      []SlackHomeStateGroup
}

// SlackHomeStateGroup is one ordered state section in the Slack App Home view.
type SlackHomeStateGroup struct {
	State  string
	Issues []SlackHomeIssue
}

// SlackHomeIssue is the minimal issue payload rendered in the Slack App Home view.
type SlackHomeIssue struct {
	ID         string
	Identifier string
	Title      string
	URL        string
}

// SlackHomeStateNames returns the watched issue states rendered in the Slack App Home tab.
func SlackHomeStateNames(cfg domain.ServiceConfig) []string {
	seen := map[string]struct{}{}
	var states []string
	appendState := func(state string) {
		state = strings.TrimSpace(state)
		if state == "" {
			return
		}
		if strings.EqualFold(state, "Backlog") {
			return
		}
		for _, terminal := range cfg.Tracker.TerminalStates {
			if strings.EqualFold(state, strings.TrimSpace(terminal)) {
				return
			}
		}
		key := strings.ToLower(state)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		states = append(states, state)
	}

	for _, state := range cfg.Tracker.ActiveStates {
		appendState(state)
	}
	appendState("Refine")
	for _, state := range cfg.Repo.PublishStates {
		appendState(state)
	}
	for _, state := range cfg.Repo.MergeStates {
		appendState(state)
	}
	return states
}

// SlackHomeIssueView builds the ordered grouped Slack App Home issue list for the watched project.
func SlackHomeIssueView(cfg domain.ServiceConfig, issues []domain.Issue) SlackHomeView {
	stateOrder := SlackHomeStateNames(cfg)
	indexByState := make(map[string]int, len(stateOrder))
	for i, state := range stateOrder {
		indexByState[strings.ToLower(strings.TrimSpace(state))] = i
	}

	grouped := make([][]SlackHomeIssue, len(stateOrder))
	total := 0
	for _, issue := range issues {
		stateKey := strings.ToLower(strings.TrimSpace(issue.State))
		idx, ok := indexByState[stateKey]
		if !ok {
			continue
		}
		total++
		item := SlackHomeIssue{
			ID:         strings.TrimSpace(issue.ID),
			Identifier: strings.TrimSpace(issue.Identifier),
			Title:      strings.TrimSpace(issue.Title),
		}
		if issue.URL != nil {
			item.URL = strings.TrimSpace(*issue.URL)
		}
		grouped[idx] = append(grouped[idx], item)
	}

	view := SlackHomeView{TotalIssues: total}
	for idx, items := range grouped {
		if len(items) == 0 {
			continue
		}
		sort.Slice(items, func(i, j int) bool {
			if items[i].Identifier != items[j].Identifier {
				return items[i].Identifier < items[j].Identifier
			}
			if items[i].Title != items[j].Title {
				return items[i].Title < items[j].Title
			}
			return items[i].ID < items[j].ID
		})
		view.Groups = append(view.Groups, SlackHomeStateGroup{
			State:  stateOrder[idx],
			Issues: items,
		})
	}
	return view
}
