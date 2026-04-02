package cmd

import (
	"fmt"

	"github.com/abix-/k3sc/internal/github"
	"github.com/abix-/k3sc/internal/k8s"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(dispatchCmd)
}

var dispatchCmd = &cobra.Command{
	Use:   "dispatch",
	Short: "Scan GitHub for ready/needs-review issues and create AgentJob CRDs",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()

		// load existing tasks to check for duplicates
		existing, err := k8s.GetAgentJobs(ctx)
		if err != nil {
			return fmt.Errorf("list tasks: %w", err)
		}
		activeIssues := map[string]bool{}
		for _, t := range existing {
			if t.Phase == "Running" || t.Phase == "Pending" || t.Phase == "" {
				key := fmt.Sprintf("%s/%s#%d", t.Repo.Owner, t.Repo.Name, t.Issue)
				activeIssues[key] = true
			}
		}

		created := 0

		ready, err := github.GetReadyIssues(ctx)
		if err != nil {
			return fmt.Errorf("get ready issues: %w", err)
		}
		for _, issue := range ready {
			if reason := github.DispatchTrustReason(issue); reason != "" {
				cmd.Printf("skip %s#%d: %s\n", issue.Repo.Name, issue.Number, reason)
				continue
			}
			key := fmt.Sprintf("%s/%s#%d", issue.Repo.Owner, issue.Repo.Name, issue.Number)
			if activeIssues[key] {
				continue
			}
			name, err := k8s.CreateAgentJob(ctx, issue)
			if err != nil {
				cmd.PrintErrf("create %s#%d: %v\n", issue.Repo.Name, issue.Number, err)
				continue
			}
			cmd.Printf("created %s (%s#%d, ready)\n", name, issue.Repo.Name, issue.Number)
			activeIssues[key] = true
			created++
		}

		needsReview, err := github.GetNeedsReviewIssues(ctx)
		if err != nil {
			return fmt.Errorf("get needs-review issues: %w", err)
		}
		for _, issue := range needsReview {
			if reason := github.DispatchTrustReason(issue); reason != "" {
				continue
			}
			key := fmt.Sprintf("%s/%s#%d", issue.Repo.Owner, issue.Repo.Name, issue.Number)
			if activeIssues[key] {
				continue
			}
			name, err := k8s.CreateAgentJob(ctx, issue)
			if err != nil {
				cmd.PrintErrf("create review %s#%d: %v\n", issue.Repo.Name, issue.Number, err)
				continue
			}
			cmd.Printf("created %s (%s#%d, needs-review)\n", name, issue.Repo.Name, issue.Number)
			activeIssues[key] = true
			created++
		}

		if created == 0 {
			cmd.Println("no new work to dispatch")
		} else {
			cmd.Printf("dispatched %d jobs\n", created)
		}
		return nil
	},
}
