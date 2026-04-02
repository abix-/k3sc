package operator

import (
	"time"
)

func reviewLeaseExpired(lease *ReviewLease, now time.Time) bool {
	return lease != nil && lease.Spec.ExpiresAt != nil && !lease.Spec.ExpiresAt.Time.After(now)
}
