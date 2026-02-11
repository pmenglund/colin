package workflow

import (
	"testing"
	"time"
)

func TestIsLeaseActive(t *testing.T) {
	now := time.Date(2026, 2, 11, 0, 0, 0, 0, time.UTC)
	lease := Lease{Owner: "worker-1", ExpiresAtUTC: now.Add(time.Minute)}

	if !IsLeaseActive(lease, now) {
		t.Fatal("expected lease to be active")
	}
	if IsLeaseActive(lease, now.Add(2*time.Minute)) {
		t.Fatal("expected lease to be inactive after expiration")
	}
}

func TestBuildAndParseLease(t *testing.T) {
	now := time.Date(2026, 2, 11, 0, 0, 0, 0, time.UTC)
	lease := BuildLease("worker", "exec", now, 5*time.Minute)
	meta := LeaseMetadataMap(lease)

	parsed, err := LeaseFromMetadata(meta)
	if err != nil {
		t.Fatalf("LeaseFromMetadata() error = %v", err)
	}

	if parsed.Owner != "worker" {
		t.Fatalf("Owner = %q", parsed.Owner)
	}
	if parsed.ExecutionID != "exec" {
		t.Fatalf("ExecutionID = %q", parsed.ExecutionID)
	}
	if !parsed.ExpiresAtUTC.Equal(now.Add(5 * time.Minute)) {
		t.Fatalf("ExpiresAtUTC = %s", parsed.ExpiresAtUTC)
	}
}
