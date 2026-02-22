package workflow

import (
	"fmt"
	"strings"
	"time"
)

const (
	MetaLeaseOwner                 = "colin.lease_owner"
	MetaLeaseExecutionID           = "colin.execution_id"
	MetaLeaseExpiresAtUTC          = "colin.lease_expires_at"
	MetaReason                     = "colin.reason"
	MetaNeedsRefine                = "colin.needs_refine"
	MetaReadyForHumanReview        = "colin.ready_for_human_review"
	MetaMergeReady                 = "colin.merge_ready"
	MetaSpecReady                  = "colin.spec_ready"
	MetaLastHeartbeatUTC           = "colin.last_heartbeat"
	MetaInProgressOutcome          = "colin.in_progress_outcome"
	MetaInProgressCommentID        = "colin.in_progress_comment_id"
	MetaInProgressContextCommentID = "colin.in_progress_context_comment_id"
	MetaDoneRecoveryCommentID      = "colin.done_recovery_comment_id"
	MetaWorktreePath               = "colin.worktree_path"
	MetaBranchName                 = "colin.branch_name"
	MetaThreadID                   = "colin.thread_id"
)

// Lease coordinates exclusive work ownership on a Linear issue.
type Lease struct {
	Owner        string
	ExecutionID  string
	ExpiresAtUTC time.Time
}

// IsLeaseActive returns true when the lease has an owner and has not expired.
func IsLeaseActive(lease Lease, now time.Time) bool {
	if strings.TrimSpace(lease.Owner) == "" {
		return false
	}
	return now.Before(lease.ExpiresAtUTC)
}

// BuildLease creates a new lease window from now+ttl.
func BuildLease(owner string, executionID string, now time.Time, ttl time.Duration) Lease {
	return Lease{
		Owner:        owner,
		ExecutionID:  executionID,
		ExpiresAtUTC: now.UTC().Add(ttl),
	}
}

// LeaseFromMetadata parses lease fields from metadata.
func LeaseFromMetadata(meta map[string]string) (Lease, error) {
	if meta == nil {
		return Lease{}, nil
	}

	out := Lease{
		Owner:       strings.TrimSpace(meta[MetaLeaseOwner]),
		ExecutionID: strings.TrimSpace(meta[MetaLeaseExecutionID]),
	}

	expires := strings.TrimSpace(meta[MetaLeaseExpiresAtUTC])
	if expires == "" {
		return out, nil
	}

	t, err := time.Parse(time.RFC3339, expires)
	if err != nil {
		return Lease{}, fmt.Errorf("parse %s: %w", MetaLeaseExpiresAtUTC, err)
	}
	out.ExpiresAtUTC = t.UTC()
	return out, nil
}

// LeaseMetadataMap converts a lease into metadata fields.
func LeaseMetadataMap(lease Lease) map[string]string {
	return map[string]string{
		MetaLeaseOwner:        lease.Owner,
		MetaLeaseExecutionID:  lease.ExecutionID,
		MetaLeaseExpiresAtUTC: lease.ExpiresAtUTC.UTC().Format(time.RFC3339),
	}
}
