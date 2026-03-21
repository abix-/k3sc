package operator

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestReviewLeaseExpired(t *testing.T) {
	now := time.Date(2026, time.March, 20, 12, 0, 0, 0, time.UTC)
	past := metav1.NewTime(now.Add(-time.Minute))
	future := metav1.NewTime(now.Add(time.Minute))

	if !reviewLeaseExpired(&ReviewLease{Spec: ReviewLeaseSpec{ExpiresAt: &past}}, now) {
		t.Fatal("past lease should be expired")
	}
	if reviewLeaseExpired(&ReviewLease{Spec: ReviewLeaseSpec{ExpiresAt: &future}}, now) {
		t.Fatal("future lease should not be expired")
	}
}

func TestSameReviewReservations(t *testing.T) {
	now := metav1.Now()
	a := []DispatchReviewReservationStatus{{
		Repo:        "abix-/endless",
		RepoName:    "endless",
		PRNumber:    194,
		IssueNumber: 186,
		Family:      "claude",
		WorkerID:    "claude-a",
		ReservedAt:  &now,
	}}
	b := []DispatchReviewReservationStatus{{
		Repo:        "abix-/endless",
		RepoName:    "endless",
		PRNumber:    194,
		IssueNumber: 186,
		Family:      "claude",
		WorkerID:    "claude-a",
		ReservedAt:  &now,
	}}
	if !sameReviewReservations(a, b) {
		t.Fatal("identical reservations should compare equal")
	}

	b[0].WorkerID = "codex-a"
	if sameReviewReservations(a, b) {
		t.Fatal("different reservations should not compare equal")
	}
}
