package operator

import (
	"context"
	"sort"
	"time"

	"github.com/abix-/k3sc/internal/dispatch"
	"github.com/abix-/k3sc/internal/github"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *DispatchReconciler) loadReviewReservations(ctx context.Context) ([]DispatchReviewReservationStatus, map[string]ReviewLease, error) {
	var leases ReviewLeaseList
	if err := r.APIReader.List(ctx, &leases, client.InNamespace(r.Namespace)); err != nil {
		return nil, nil, err
	}

	now := time.Now().UTC()
	var reservations []DispatchReviewReservationStatus
	byIssue := map[string]ReviewLease{}

	for i := range leases.Items {
		lease := &leases.Items[i]
		if reviewLeaseExpired(lease, now) {
			repo := dispatch.RepoFromString(lease.Spec.Repo)
			if err := github.UnclaimPullRequest(ctx, repo, lease.Spec.PRNumber); err != nil {
				olog("scheduler", "unclaim expired review lease %s: %v", lease.Name, err)
			}
			if err := r.Delete(ctx, lease); err != nil {
				olog("scheduler", "delete expired review lease %s: %v", lease.Name, err)
			} else {
				olog("scheduler", "expired review lease %s", lease.Name)
			}
			continue
		}

		reservations = append(reservations, DispatchReviewReservationStatus{
			Repo:        lease.Spec.Repo,
			RepoName:    lease.Spec.RepoName,
			PRNumber:    lease.Spec.PRNumber,
			PRURL:       lease.Spec.PRURL,
			Branch:      lease.Spec.Branch,
			IssueNumber: lease.Spec.IssueNumber,
			Family:      lease.Spec.Family,
			WorkerID:    lease.Spec.WorkerID,
			WorkerKind:  lease.Spec.WorkerKind,
			ReservedAt:  lease.Spec.ReservedAt,
			ExpiresAt:   lease.Spec.ExpiresAt,
		})
		if lease.Spec.IssueNumber > 0 {
			byIssue[issueKey(lease.Spec.Repo, lease.Spec.IssueNumber)] = *lease
		}
	}

	sort.Slice(reservations, func(i, j int) bool {
		if reservations[i].WorkerID != reservations[j].WorkerID {
			return reservations[i].WorkerID < reservations[j].WorkerID
		}
		if reservations[i].RepoName != reservations[j].RepoName {
			return reservations[i].RepoName < reservations[j].RepoName
		}
		return reservations[i].PRNumber < reservations[j].PRNumber
	})

	return reservations, byIssue, nil
}

func reviewLeaseExpired(lease *ReviewLease, now time.Time) bool {
	return lease != nil && lease.Spec.ExpiresAt != nil && !lease.Spec.ExpiresAt.Time.After(now)
}

func sameReviewReservations(a, b []DispatchReviewReservationStatus) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Repo != b[i].Repo ||
			a[i].RepoName != b[i].RepoName ||
			a[i].PRNumber != b[i].PRNumber ||
			a[i].PRURL != b[i].PRURL ||
			a[i].Branch != b[i].Branch ||
			a[i].IssueNumber != b[i].IssueNumber ||
			a[i].Family != b[i].Family ||
			a[i].WorkerID != b[i].WorkerID ||
			a[i].WorkerKind != b[i].WorkerKind ||
			!sameTime(a[i].ReservedAt, b[i].ReservedAt) ||
			!sameTime(a[i].ExpiresAt, b[i].ExpiresAt) {
			return false
		}
	}
	return true
}
