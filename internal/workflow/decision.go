package workflow

import (
	"strings"
	"time"
)

// ActionType describes the next worker action for an issue.
type ActionType string

const (
	ActionNoop               ActionType = "noop"
	ActionClaimAndTransition ActionType = "claim_and_transition"
	ActionTransition         ActionType = "transition"
)

// IssueSnapshot is normalized issue data consumed by Decide.
type IssueSnapshot struct {
	IssueID     string
	Identifier  string
	State       string
	Description string
	Metadata    map[string]string
	WorkerID    string
	ExecutionID string
	LeaseTTL    time.Duration
}

// Decision is a deterministic workflow decision.
type Decision struct {
	Action        ActionType
	ToState       string
	Reason        string
	LeasePatch    *Lease
	MetadataPatch map[string]string
}

// Decide evaluates one issue snapshot and emits the next action.
func Decide(snapshot IssueSnapshot, now time.Time) Decision {
	return DecideWithStates(snapshot, now, DefaultStates())
}

// DecideWithStates evaluates one issue snapshot using runtime state names.
func DecideWithStates(snapshot IssueSnapshot, now time.Time, states States) Decision {
	states = states.WithDefaults()

	lease, err := LeaseFromMetadata(snapshot.Metadata)
	leaseMetadataInvalid := err != nil

	activeLeaseByOther := !leaseMetadataInvalid && IsLeaseActive(lease, now) && lease.Owner != snapshot.WorkerID
	specReady := hasRequiredSpec(snapshot)

	switch snapshot.State {
	case states.Todo:
		if !specReady {
			return Decision{
				Action:  ActionTransition,
				ToState: states.Refine,
				Reason:  "missing required specification",
				MetadataPatch: map[string]string{
					MetaReason:      "missing required specification",
					MetaNeedsRefine: "true",
				},
			}
		}
		if activeLeaseByOther {
			return Decision{Action: ActionNoop, Reason: "active lease owned by another worker"}
		}

		newLease := BuildLease(snapshot.WorkerID, snapshot.ExecutionID, now, snapshot.LeaseTTL)
		reason := "claimed todo issue"
		if leaseMetadataInvalid {
			reason = "claimed todo issue after invalid lease metadata recovery"
		}
		return Decision{
			Action:     ActionClaimAndTransition,
			ToState:    states.InProgress,
			Reason:     reason,
			LeasePatch: &newLease,
			MetadataPatch: map[string]string{
				MetaReason:      "",
				MetaNeedsRefine: "false",
			},
		}

	case states.InProgress:
		if activeLeaseByOther {
			return Decision{Action: ActionNoop, Reason: "active lease owned by another worker"}
		}
		if !specReady || parseBool(snapshot.Metadata[MetaNeedsRefine]) {
			return Decision{
				Action:  ActionTransition,
				ToState: states.Refine,
				Reason:  "specification requires refinement",
				MetadataPatch: map[string]string{
					MetaReason:              "specification requires refinement",
					MetaNeedsRefine:         "true",
					MetaLeaseOwner:          "",
					MetaLeaseExecutionID:    "",
					MetaLeaseExpiresAtUTC:   "",
					MetaReadyForHumanReview: "false",
				},
			}
		}
		if parseBool(snapshot.Metadata[MetaReadyForHumanReview]) {
			return Decision{
				Action:  ActionTransition,
				ToState: states.Review,
				Reason:  "issue ready for human review",
				MetadataPatch: map[string]string{
					MetaReason:            "",
					MetaLeaseOwner:        "",
					MetaLeaseExecutionID:  "",
					MetaLeaseExpiresAtUTC: "",
				},
			}
		}
		return Decision{Action: ActionNoop, Reason: "no state change required"}

	case states.Merge:
		return Decision{
			Action:  ActionTransition,
			ToState: states.Done,
			Reason:  "issue queued in merge state",
			MetadataPatch: map[string]string{
				MetaReason:     "",
				MetaMergeReady: "false",
			},
		}
	}

	return Decision{Action: ActionNoop, Reason: "state not automated in milestone 1"}
}

func hasRequiredSpec(snapshot IssueSnapshot) bool {
	if parseBool(snapshot.Metadata[MetaSpecReady]) {
		return true
	}
	return strings.TrimSpace(snapshot.Description) != ""
}

func parseBool(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}
