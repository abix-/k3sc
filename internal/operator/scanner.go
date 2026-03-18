package operator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/abix-/k3sc/internal/dispatch"
	"github.com/abix-/k3sc/internal/github"
	"github.com/abix-/k3sc/internal/types"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	ScanIntervalMin = 2 * time.Minute
	ScanIntervalMax = 1 * time.Hour
	TaskTTL         = 24 * time.Hour
	MaxFailures     = 3
)

func Scanner(ctx context.Context, c client.Client, namespace string) {
	logger := log.FromContext(ctx).WithName("scanner")
	interval := ScanIntervalMin
	logger.Info("starting github scanner", "interval", interval)

	hadWork := scan(ctx, c, namespace)
	if hadWork {
		interval = ScanIntervalMin
	} else {
		interval = nextBackoff(interval)
	}

	timer := time.NewTimer(interval)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			hadWork = scan(ctx, c, namespace)
			if hadWork {
				interval = ScanIntervalMin
			} else {
				interval = nextBackoff(interval)
			}
			fmt.Printf("[scanner] next scan in %s\n", interval)
			timer.Reset(interval)
		}
	}
}

func nextBackoff(current time.Duration) time.Duration {
	next := current * 2
	if next > ScanIntervalMax {
		next = ScanIntervalMax
	}
	return next
}

func scan(ctx context.Context, c client.Client, namespace string) bool {
	eligible, err := github.GetEligibleIssues(ctx)
	if err != nil {
		fmt.Printf("[scanner] github error: %v\n", err)
		return false
	}

	var existing ClaudeTaskList
	if err := c.List(ctx, &existing, client.InNamespace(namespace)); err != nil {
		fmt.Printf("[scanner] list tasks error: %v\n", err)
		return false
	}

	// build state from existing tasks
	activeIssues := map[string]bool{}
	failCounts := map[string]int{}
	usedSlots := []int{}
	for _, t := range existing.Items {
		key := fmt.Sprintf("%s-%d", t.Spec.RepoName, t.Spec.IssueNumber)
		if !IsTerminal(t.Status.Phase) && t.Status.Phase != "" {
			activeIssues[key] = true
			usedSlots = append(usedSlots, t.Spec.Slot)
		}
		if t.Status.Phase == TaskPhaseFailed {
			failCounts[key]++
		}
	}

	maxSlots := dispatch.MaxSlots()

	// create tasks one at a time, updating usedSlots after each
	for _, issue := range eligible {
		key := fmt.Sprintf("%s-%d", issue.Repo.Name, issue.Number)
		if activeIssues[key] {
			continue
		}
		if failCounts[key] >= MaxFailures {
			fmt.Printf("[scanner] %s blocked after %d failures\n", key, failCounts[key])
			continue
		}

		slot := dispatch.FindFreeSlotFromList(usedSlots, maxSlots)
		if slot == -1 {
			fmt.Printf("[scanner] no free slots\n")
			break
		}

		agent := types.AgentName(slot)
		ts := time.Now().Unix()
		name := fmt.Sprintf("%s-%d-%d", strings.ReplaceAll(issue.Repo.Name, "/", "-"), issue.Number, ts)

		task := &ClaudeTask{
			TypeMeta: metav1.TypeMeta{
				APIVersion: GroupVersion.String(),
				Kind:       "ClaudeTask",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
			},
			Spec: ClaudeTaskSpec{
				Repo:        fmt.Sprintf("%s/%s", issue.Repo.Owner, issue.Repo.Name),
				RepoName:    issue.Repo.Name,
				IssueNumber: issue.Number,
				RepoURL:     issue.Repo.CloneURL(),
				Slot:        slot,
				Agent:       agent,
				OriginState: issue.State,
			},
			Status: ClaudeTaskStatus{
				Phase: TaskPhasePending,
			},
		}

		if err := c.Create(ctx, task); err != nil {
			fmt.Printf("[scanner] create %s: %v\n", name, err)
			continue
		}
		fmt.Printf("[scanner] created %s (slot %d, %s)\n", name, slot, agent)

		// update in-memory state so next iteration sees this slot as used
		usedSlots = append(usedSlots, slot)
		activeIssues[key] = true
	}

	// TTL cleanup
	for i := range existing.Items {
		t := &existing.Items[i]
		if !IsTerminal(t.Status.Phase) {
			continue
		}
		if time.Since(t.CreationTimestamp.Time) > TaskTTL {
			if err := c.Delete(ctx, t); err != nil {
				fmt.Printf("[scanner] cleanup %s: %v\n", t.Name, err)
			} else {
				fmt.Printf("[scanner] cleaned up %s\n", t.Name)
			}
		}
	}

	return len(eligible) > 0
}
