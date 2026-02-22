package workflow

import (
	"fmt"
	"strings"
)

const (
	StateTodo       = "Todo"
	StateRefine     = "Refine"
	StateInProgress = "In Progress"
	StateReview     = "Review"
	StateMerge      = "Merge"
	StateDone       = "Done"
)

// States defines concrete runtime workflow state names.
type States struct {
	Todo       string
	Refine     string
	InProgress string
	Review     string
	Merge      string
	Done       string
}

// DefaultStates returns Colin's canonical workflow state names.
func DefaultStates() States {
	return States{
		Todo:       StateTodo,
		Refine:     StateRefine,
		InProgress: StateInProgress,
		Review:     StateReview,
		Merge:      StateMerge,
		Done:       StateDone,
	}
}

// WithDefaults fills blank state names with canonical defaults.
func (s States) WithDefaults() States {
	defaults := DefaultStates()

	if strings.TrimSpace(s.Todo) == "" {
		s.Todo = defaults.Todo
	}
	if strings.TrimSpace(s.Refine) == "" {
		s.Refine = defaults.Refine
	}
	if strings.TrimSpace(s.InProgress) == "" {
		s.InProgress = defaults.InProgress
	}
	if strings.TrimSpace(s.Review) == "" {
		s.Review = defaults.Review
	}
	if strings.TrimSpace(s.Merge) == "" {
		s.Merge = defaults.Merge
	}
	if strings.TrimSpace(s.Done) == "" {
		s.Done = defaults.Done
	}

	return s
}

// Validate reports whether runtime workflow state names are configured.
func (s States) Validate() error {
	s = s.WithDefaults()

	entries := map[string]string{
		"todo":        s.Todo,
		"refine":      s.Refine,
		"in_progress": s.InProgress,
		"review":      s.Review,
		"merge":       s.Merge,
		"done":        s.Done,
	}

	seen := map[string]string{}
	for key, value := range entries {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return fmt.Errorf("workflow state %q must not be empty", key)
		}
		normalized := normalizeStateName(trimmed)
		if prev, ok := seen[normalized]; ok {
			return fmt.Errorf("workflow states %q and %q must map to distinct names", prev, key)
		}
		seen[normalized] = key
	}
	return nil
}

// IsCandidate reports whether the state is eligible for worker processing.
func (s States) IsCandidate(state string) bool {
	s = s.WithDefaults()
	switch strings.TrimSpace(state) {
	case s.Todo, s.InProgress, s.Merge, s.Done:
		return true
	default:
		return false
	}
}

// IsDone reports whether state represents completion.
func (s States) IsDone(state string) bool {
	s = s.WithDefaults()
	return strings.TrimSpace(state) == s.Done
}

// CanTransition reports whether from->to is an allowed workflow transition.
func (s States) CanTransition(from, to string) bool {
	s = s.WithDefaults()

	allowedTransitions := map[string]map[string]struct{}{
		s.Todo: {
			s.InProgress: {},
			s.Refine:     {},
		},
		s.InProgress: {
			s.Review: {},
			s.Refine: {},
		},
		s.Review: {
			s.Todo:  {},
			s.Merge: {},
		},
		s.Merge: {
			s.Done: {},
		},
		s.Done: {
			s.Merge: {},
		},
	}

	next, ok := allowedTransitions[strings.TrimSpace(from)]
	if !ok {
		return false
	}
	_, ok = next[strings.TrimSpace(to)]
	return ok
}

// CanTransition reports whether from->to is an allowed transition for canonical names.
func CanTransition(from, to string) bool {
	return DefaultStates().CanTransition(from, to)
}

func normalizeStateName(name string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(name)), " "))
}
