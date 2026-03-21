package cmd

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/abix-/k3sc/internal/github"
	"github.com/abix-/k3sc/internal/k8s"
	"github.com/abix-/k3sc/internal/types"
	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

var takeWorker string
var takeRepo string

func init() {
	takeCmd.Flags().StringVar(&takeWorker, "worker", "", "worker name to reserve for (for example claude-a or codex-a)")
	takeCmd.Flags().StringVar(&takeRepo, "repo", "all", "repo name (endless, k3sc, or all)")
	rootCmd.AddCommand(takeCmd)
}

var takeCmd = &cobra.Command{
	Use:   "take",
	Short: "Reserve the next eligible open PR for a local review worker",
	RunE:  runTake,
}

func runTake(cmd *cobra.Command, args []string) error {
	workerID := strings.ToLower(strings.TrimSpace(takeWorker))
	if workerID == "" {
		return fmt.Errorf("--worker is required")
	}
	family, ok := types.ParseWorkerFamily(workerID)
	if !ok {
		return fmt.Errorf("invalid worker %q: want claude-* or codex-*", takeWorker)
	}

	ctx := cmd.Context()
	prs, err := github.GetOpenPRs(ctx)
	if err != nil {
		return err
	}
	issues, err := github.GetWorkflowIssues(ctx)
	if err != nil {
		return err
	}
	leases, err := k8s.GetReviewLeases(ctx)
	if err != nil {
		return err
	}

	sortPRReviewCandidates(prs)
	issueMap := make(map[string]types.Issue, len(issues))
	for _, issue := range issues {
		issueMap[repoIssueKey(issue.Repo, issue.Number)] = issue
	}
	leaseMap := make(map[string]types.ReviewReservation, len(leases))
	for _, lease := range leases {
		leaseMap[repoPRKey(lease.Repo, lease.PRNumber)] = lease
	}

	now := time.Now().UTC()

	for _, lease := range leases {
		if strings.EqualFold(lease.WorkerID, workerID) {
			return fmt.Errorf("worker %s already has %s#%d reserved", workerID, lease.Repo.Name, lease.PRNumber)
		}
	}

	for _, pr := range prs {
		if !matchesRepoFilter(pr.Repo, takeRepo) {
			continue
		}
		if pr.Owner != "" || pr.Issue <= 0 {
			continue
		}
		if _, leased := leaseMap[repoPRKey(pr.Repo, pr.Number)]; leased {
			continue
		}

		linkedIssue, ok := issueMap[repoIssueKey(pr.Repo, pr.Issue)]
		if ok && linkedIssue.Owner != "" {
			continue
		}

		active, err := k8s.HasActiveAgentJobForRepoIssue(ctx, pr.Repo.Name, pr.Issue)
		if err != nil {
			return err
		}
		if active {
			continue
		}

		reservation := types.ReviewReservation{
			Repo:       pr.Repo,
			PRNumber:   pr.Number,
			PRURL:      fmt.Sprintf("https://github.com/%s/%s/pull/%d", pr.Repo.Owner, pr.Repo.Name, pr.Number),
			Branch:     pr.Branch,
			Issue:      pr.Issue,
			Family:     family,
			WorkerID:   workerID,
			WorkerKind: "windows",
			ReservedAt: &now,
		}
		expires := now.Add(types.LocalReviewLeaseTTL)
		reservation.ExpiresAt = &expires

		if err := k8s.CreateReviewLease(ctx, reservation); err != nil {
			if apierrors.IsAlreadyExists(err) {
				continue
			}
			return err
		}
		if err := github.ClaimPullRequest(ctx, pr.Repo, pr.Number, workerID); err != nil {
			_, _ = k8s.DeleteReviewLease(ctx, pr.Repo, pr.Number)
			return err
		}
		_, _ = k8s.TriggerDispatch(ctx)

		cmd.Printf("worker %s reserved %s#%d (%s)\n", workerID, pr.Repo.Name, pr.Number, family)
		cmd.Printf("issue: #%d\n", pr.Issue)
		cmd.Printf("branch: %s\n", pr.Branch)
		cmd.Printf("url: %s\n", reservation.PRURL)
		cmd.Printf("expires: %s\n", expires.Local().Format(time.RFC1123))
		cmd.Printf("next: start %s manually, then review %s\n", workerID, reservation.PRURL)
		return nil
	}

	cmd.Println("no eligible open PRs")
	return nil
}

func sortPRReviewCandidates(prs []types.PullRequest) {
	sort.SliceStable(prs, func(i, j int) bool {
		pi, pj := prPriority(prs[i].Title), prPriority(prs[j].Title)
		if pi != pj {
			return pi < pj
		}
		if prs[i].Repo.Name != prs[j].Repo.Name {
			return prs[i].Repo.Name < prs[j].Repo.Name
		}
		return prs[i].Number < prs[j].Number
	})
}

func prPriority(title string) int {
	title = strings.ToLower(strings.TrimSpace(title))
	switch {
	case strings.HasPrefix(title, "perf:"):
		return 0
	case strings.HasPrefix(title, "fix:"):
		return 1
	default:
		return 2
	}
}

func matchesRepoFilter(repo types.Repo, filter string) bool {
	filter = strings.ToLower(strings.TrimSpace(filter))
	return filter == "" || filter == "all" || repo.Name == filter
}

func repoPRKey(repo types.Repo, prNumber int) string {
	return strings.ToLower(fmt.Sprintf("%s/%s#%d", repo.Owner, repo.Name, prNumber))
}

func repoIssueKey(repo types.Repo, issueNumber int) string {
	return strings.ToLower(fmt.Sprintf("%s/%s#%d", repo.Owner, repo.Name, issueNumber))
}
