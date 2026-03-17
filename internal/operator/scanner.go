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
)

// Scanner polls GitHub for eligible issues and creates ClaudeTask CRs.
func Scanner(ctx context.Context, c client.Client, namespace string) {
	logger := log.FromContext(ctx).WithName("scanner")
	logger.Info("starting github scanner", "interval", ScanInterval)

	// run immediately, then on interval
	scan(ctx, c, namespace, logger)
	ticker := time.NewTicker(ScanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			scan(ctx, c, namespace, logger)
		}
	}
}

func scan(ctx context.Context, c client.Client, namespace string, logger interface{ Info(string, ...interface{}) }) {
	eligible, err := github.GetEligibleIssues(ctx)
	if err != nil {
		fmt.Printf("[scanner] github error: %v\n", err)
		return
	}

	// list existing tasks to avoid duplicates
	var existing ClaudeTaskList
	if err := c.List(ctx, &existing, client.InNamespace(namespace)); err != nil {
		fmt.Printf("[scanner] list tasks error: %v\n", err)
		return
	}
	existingSet := map[string]bool{}
	for _, t := range existing.Items {
		key := fmt.Sprintf("%s-%d", t.Spec.RepoName, t.Spec.IssueNumber)
		existingSet[key] = true
	}

	for _, issue := range eligible {
		key := fmt.Sprintf("%s-%d", issue.Repo.Name, issue.Number)
		if existingSet[key] {
			continue
		}

		task := &ClaudeTask{
			TypeMeta: metav1.TypeMeta{
				APIVersion: GroupVersion.String(),
				Kind:       "ClaudeTask",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("%s-%d", strings.ReplaceAll(issue.Repo.Name, "/", "-"), issue.Number),
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
			fmt.Printf("[scanner] create task %s: %v\n", key, err)
		} else {
			fmt.Printf("[scanner] created task %s\n", key)
		}
	}
}
