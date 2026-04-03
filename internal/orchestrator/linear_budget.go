package orchestrator

import (
	"time"

	"github.com/pmenglund/colin/internal/domain"
)

const (
	linearRequestHardReserve int64 = 100
	linearRequestSoftReserve int64 = 250
)

type linearRequestPriority string

const (
	linearRequestDispatch   linearRequestPriority = "dispatch"
	linearRequestRunning    linearRequestPriority = "running"
	linearRequestBackground linearRequestPriority = "background"
)

type linearBudgetDecision struct {
	Priority linearRequestPriority
	Allowed  bool
	Delay    time.Duration
	Reason   string
	Window   domain.RateLimitWindow
}

func (o *Orchestrator) linearBudgetDecision(now time.Time, priority linearRequestPriority) linearBudgetDecision {
	decision := linearBudgetDecision{
		Priority: priority,
		Allowed:  true,
		Reason:   "no_rate_limit_snapshot",
	}

	window, ok := o.currentLinearRequests()
	if !ok {
		return decision
	}
	decision.Window = window

	delay := linearNextAllowedDelay(window, now)
	if window.Remaining == nil {
		if delay <= 0 {
			decision.Reason = "next_allowed_at_ready"
			return decision
		}
		decision.Allowed = false
		decision.Delay = delay
		decision.Reason = "remaining_unknown_next_allowed_at"
		return decision
	}

	remaining := *window.Remaining
	switch {
	case remaining > linearRequestSoftReserve:
		decision.Reason = "above_soft_reserve"
		return decision
	case remaining > linearRequestHardReserve:
		switch priority {
		case linearRequestDispatch:
			decision.Reason = "soft_reserve_dispatch_priority"
			return decision
		case linearRequestRunning:
			if delay <= 0 {
				decision.Reason = "soft_reserve_running_ready"
				return decision
			}
			decision.Allowed = false
			decision.Delay = delay
			decision.Reason = "soft_reserve_running_spaced"
			return decision
		case linearRequestBackground:
			decision.Allowed = false
			decision.Reason = "soft_reserve_background_suppressed"
			return decision
		}
	default:
		switch priority {
		case linearRequestBackground:
			decision.Allowed = false
			decision.Reason = "hard_reserve_background_suppressed"
			return decision
		case linearRequestDispatch, linearRequestRunning:
			if delay <= 0 {
				decision.Reason = "hard_reserve_ready"
				return decision
			}
			decision.Allowed = false
			decision.Delay = delay
			decision.Reason = "hard_reserve_spaced"
			return decision
		}
	}

	return decision
}

func linearNextAllowedDelay(window domain.RateLimitWindow, now time.Time) time.Duration {
	if window.NextAllowedAt == nil || !window.NextAllowedAt.After(now) {
		return 0
	}
	return window.NextAllowedAt.Sub(now)
}

func (o *Orchestrator) linearBudgetLogArgs(decision linearBudgetDecision) []any {
	args := []any{
		"request_priority", string(decision.Priority),
		"budget_reason", decision.Reason,
		"idle_slots", o.idleGlobalSlots(),
		"linear_requests_soft_reserve", linearRequestSoftReserve,
		"linear_requests_hard_reserve", linearRequestHardReserve,
	}
	if decision.Delay > 0 {
		args = append(args, "delay", decision.Delay.String())
	}
	if decision.Window.Remaining != nil {
		args = append(args, "linear_requests_remaining", *decision.Window.Remaining)
	}
	if decision.Window.Limit != nil {
		args = append(args, "linear_requests_limit", *decision.Window.Limit)
	}
	if decision.Window.ResetsAt != nil {
		args = append(args, "linear_requests_reset_at", decision.Window.ResetsAt.Format(time.RFC3339))
	}
	if decision.Window.NextAllowedAt != nil {
		args = append(args, "linear_requests_next_allowed_at", decision.Window.NextAllowedAt.Format(time.RFC3339))
	}
	return args
}

func (o *Orchestrator) idleGlobalSlots() int {
	limit := o.runtime.Config.Agent.MaxConcurrentAgents
	if limit <= 0 {
		return 0
	}
	idle := limit - len(o.running)
	if idle < 0 {
		return 0
	}
	return idle
}
