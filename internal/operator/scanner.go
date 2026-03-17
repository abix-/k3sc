package operator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/abix-/k3sc/internal/github"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	ScanInterval = 60 * time.Second
	TaskTTL      = 24 * time.Hour
)

// Scanner polls GitHub for eligible issues and creates ClaudeTask CRs.
func Scanner(ctx context.Context, c client.Client, namespace string) {
	logger := log.FromContext(ctx).WithName("scanner")
	logger.Info("starting github scanner", "interval", ScanInterval)

	scan(ctx, c, namespace)
	ticker := time.NewTicker(ScanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			scan(ctx, c, namespace)
		}
	}
}

func scan(ctx context.Context, c client.Client, namespace string) {
	eligible, err := github.GetEligibleIssues(ctx)
	if err != nil {
		fmt.Printf("[scanner] github error: %v\n", err)
		return
	}

	var existing ClaudeTaskList
	if err := c.List(ctx, &existing, client.InNamespace(namespace)); err != nil {
		fmt.Printf("[scanner] list tasks error: %v\n", err)
		return
	}

	// only block issues that have an active (non-terminal) task
	activeIssues := map[string]bool{}
	for _, t := range existing.Items {
		if !IsTerminal(t.Status.Phase) && t.Status.Phase != "" {
			key := fmt.Sprintf("%s-%d", t.Spec.RepoName, t.Spec.IssueNumber)
			activeIssues[key] = true
		}
	}

	for _, issue := range eligible {
		key := fmt.Sprintf("%s-%d", issue.Repo.Name, issue.Number)
		if activeIssues[key] {
			continue
		}

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
			},
			Status: ClaudeTaskStatus{
				Phase: TaskPhasePending,
			},
		}

		if err := c.Create(ctx, task); err != nil {
			fmt.Printf("[scanner] create task %s: %v\n", name, err)
		} else {
			fmt.Printf("[scanner] created task %s\n", name)
		}
	}

	// TTL cleanup: delete terminal tasks older than 24h
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
}
