package workflow

const (
	StateTodo        = "Todo"
	StateRefine      = "Refine"
	StateInProgress  = "In Progress"
	StateHumanReview = "Human Review"
	StateMerge       = "Merge"
	StateDone        = "Done"
	StateCancelled   = "Cancelled"
)

var allowedTransitions = map[string]map[string]struct{}{
	StateTodo: {
		StateInProgress: {},
		StateRefine:     {},
	},
	StateInProgress: {
		StateHumanReview: {},
		StateRefine:      {},
	},
	StateHumanReview: {
		StateTodo:  {},
		StateMerge: {},
	},
	StateMerge: {
		StateDone: {},
	},
}

// CanTransition reports whether from->to is an allowed workflow transition.
func CanTransition(from, to string) bool {
	next, ok := allowedTransitions[from]
	if !ok {
		return false
	}
	_, ok = next[to]
	return ok
}
