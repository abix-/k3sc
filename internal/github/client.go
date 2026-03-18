package github

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/abix-/k3sc/internal/types"
	gh "github.com/google/go-github/v68/github"
	"golang.org/x/oauth2"
)

func newClient(ctx context.Context) *gh.Client {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		token = os.Getenv("GH_TOKEN")
	}
	// fallback: read from token file (shared between Windows and k8s pods)
	if token == "" {
		for _, path := range []string{
			"/home/claude/.gh-token",                // k8s pod (hostPath mount)
			os.Getenv("USERPROFILE") + "/.gh-token", // Windows
			os.Getenv("HOME") + "/.gh-token",        // Linux
		} {
			if b, err := os.ReadFile(path); err == nil {
				token = strings.TrimSpace(string(b))
				break
			}
		}
	}
	if token == "" {
		return gh.NewClient(nil)
	}
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	tc := oauth2.NewClient(ctx, ts)
	return gh.NewClient(tc)
}

// parseIssueLabels extracts workflow state and owner from GitHub labels.
// State is derived from workflow labels (needs-human, needs-review, ready)
// or inferred from the presence of an owner label (owner label present = working).
func parseIssueLabels(labels []*gh.Label) (state, owner string) {
	for _, l := range labels {
		name := l.GetName()
		switch name {
		case "needs-human", "needs-review", "ready":
			if state == "" {
				state = name
			}
		}
		if owner == "" && (strings.HasPrefix(name, "claude-") || strings.HasPrefix(name, "codex-")) {
			owner = name
		}
	}
	// owner label without explicit state = being worked (owner IS the state)
	if state == "" && owner != "" {
		state = owner
	}
	return
}

// GetIssueOwner returns the owner label (e.g. "claude-a") for an issue, or "" if unclaimed.
func GetIssueOwner(ctx context.Context, repo types.Repo, issueNumber int) (string, error) {
	client := newClient(ctx)
	issue, _, err := client.Issues.Get(ctx, repo.Owner, repo.Name, issueNumber)
	if err != nil {
		return "", fmt.Errorf("get issue %d: %w", issueNumber, err)
	}
	_, owner := parseIssueLabels(issue.Labels)
	return owner, nil
}

func GetOpenPRs(ctx context.Context) ([]types.PullRequest, error) {
	client := newClient(ctx)
	var result []types.PullRequest

	for _, repo := range types.Repos {
		opts := &gh.PullRequestListOptions{
			State:       "open",
			Sort:        "created",
			Direction:   "asc",
			ListOptions: gh.ListOptions{PerPage: 50},
		}

		prs, _, err := client.PullRequests.List(ctx, repo.Owner, repo.Name, opts)
		if err != nil {
			return nil, fmt.Errorf("%s/%s PRs: %w", repo.Owner, repo.Name, err)
		}

		for _, pr := range prs {
			branch := pr.GetHead().GetRef()
			issueNum := 0
			if strings.HasPrefix(branch, "issue-") {
				fmt.Sscanf(branch, "issue-%d", &issueNum)
			}
			result = append(result, types.PullRequest{
				Number: pr.GetNumber(),
				Title:  pr.GetTitle(),
				State:  pr.GetState(),
				Branch: branch,
				Issue:  issueNum,
				Repo:   repo,
			})
		}
	}
	return result, nil
}

// GetAllOpenIssues fetches open issues with workflow labels from all repos.
func GetAllOpenIssues(ctx context.Context) ([]types.Issue, error) {
	client := newClient(ctx)

	workflowLabels := map[string]bool{
		"needs-human": true, "needs-review": true, "ready": true,
	}

	var result []types.Issue
	for _, repo := range types.Repos {
		opts := &gh.IssueListByRepoOptions{
			State:       "open",
			ListOptions: gh.ListOptions{PerPage: 100},
		}

		ghIssues, _, err := client.Issues.ListByRepo(ctx, repo.Owner, repo.Name, opts)
		if err != nil {
			return nil, fmt.Errorf("%s/%s issues: %w", repo.Owner, repo.Name, err)
		}

		for _, i := range ghIssues {
			if i.IsPullRequest() {
				continue
			}

			hasWorkflow := false
			for _, l := range i.Labels {
				name := l.GetName()
				if workflowLabels[name] || strings.HasPrefix(name, "claude-") || strings.HasPrefix(name, "codex-") {
					hasWorkflow = true
					break
				}
			}
			if !hasWorkflow {
				continue
			}

			state, owner := parseIssueLabels(i.Labels)
			result = append(result, types.Issue{
				Number:    i.GetNumber(),
				Title:     i.GetTitle(),
				State:     state,
				Owner:     owner,
				Repo:      repo,
				CreatedAt: i.GetCreatedAt().Time,
			})
		}
	}

	// oldest first by creation date
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
	return result, nil
}

// allWorkflowLabels are the labels the operator manages. On every transition,
// ALL of these are removed and exactly the target labels are added.
var allWorkflowLabels = []string{"ready", "needs-review", "needs-human", "waiting"}

// SetIssueLabels removes all workflow + owner labels, then adds exactly the given labels.
// This is the ONLY function that should modify workflow labels on issues.
func SetIssueLabels(ctx context.Context, repo types.Repo, issueNumber int, addLabels []string, comment string) error {
	client := newClient(ctx)

	// get current labels to find owner labels to remove
	issue, _, err := client.Issues.Get(ctx, repo.Owner, repo.Name, issueNumber)
	if err != nil {
		return fmt.Errorf("get issue: %w", err)
	}

	// remove all workflow labels + any owner labels
	for _, l := range issue.Labels {
		name := l.GetName()
		isWorkflow := false
		for _, wl := range allWorkflowLabels {
			if name == wl {
				isWorkflow = true
				break
			}
		}
		isOwner := strings.HasPrefix(name, "claude-") || strings.HasPrefix(name, "codex-")
		if isWorkflow || isOwner {
			client.Issues.RemoveLabelForIssue(ctx, repo.Owner, repo.Name, issueNumber, name)
		}
	}

	// add target labels
	if len(addLabels) > 0 {
		_, _, err = client.Issues.AddLabelsToIssue(ctx, repo.Owner, repo.Name, issueNumber, addLabels)
		if err != nil {
			return fmt.Errorf("add labels: %w", err)
		}
	}

	// post comment
	if comment != "" {
		_, _, err = client.Issues.CreateComment(ctx, repo.Owner, repo.Name, issueNumber, &gh.IssueComment{Body: &comment})
		if err != nil {
			return fmt.Errorf("post comment: %w", err)
		}
	}

	return nil
}

// ClaimIssue sets the owner label on an issue (removes all other workflow/owner labels).
func ClaimIssue(ctx context.Context, repo types.Repo, issueNumber int, agentName string) error {
	return SetIssueLabels(ctx, repo, issueNumber, []string{agentName},
		fmt.Sprintf("## k3sc operator\n- Claimed by %s", agentName))
}

// UnclaimIssue removes owner label and sets a workflow state label.
func UnclaimIssue(ctx context.Context, repo types.Repo, issueNumber int, ownerLabel, returnLabel string) error {
	return SetIssueLabels(ctx, repo, issueNumber, []string{returnLabel},
		fmt.Sprintf("## k3sc operator\n- Released by %s -> %s", ownerLabel, returnLabel))
}

// isK3sAgent returns true if the owner label is a k3s letter-based agent (claude-a through claude-z).
// Windows agents use numbers (claude-1, claude-2) and should not be touched by the dispatcher.
func isK3sAgent(owner string) bool {
	if !strings.HasPrefix(owner, "claude-") {
		return false
	}
	suffix := strings.TrimPrefix(owner, "claude-")
	return len(suffix) == 1 && suffix[0] >= 'a' && suffix[0] <= 'z'
}

// GetOwnedIssues returns open issues owned by k3s agents (letter-based) that are not needs-human.
func GetOwnedIssues(ctx context.Context) ([]types.Issue, error) {
	all, err := GetAllOpenIssues(ctx)
	if err != nil {
		return nil, err
	}
	var result []types.Issue
	for _, i := range all {
		if i.Owner != "" && i.State != "needs-human" && isK3sAgent(i.Owner) {
			result = append(result, i)
		}
	}
	return result, nil
}


// HasOpenPR checks if there's an open PR for a given issue-N branch.
func HasOpenPR(ctx context.Context, repo types.Repo, issueNumber int) (bool, error) {
	client := newClient(ctx)
	branch := fmt.Sprintf("issue-%d", issueNumber)
	prs, _, err := client.PullRequests.List(ctx, repo.Owner, repo.Name, &gh.PullRequestListOptions{
		State:       "open",
		Head:        fmt.Sprintf("%s:%s", repo.Owner, branch),
		ListOptions: gh.ListOptions{PerPage: 1},
	})
	if err != nil {
		return false, err
	}
	return len(prs) > 0, nil
}

// PostComment posts a comment on a GitHub issue.
func PostComment(ctx context.Context, repo types.Repo, issueNumber int, body string) error {
	client := newClient(ctx)
	_, _, err := client.Issues.CreateComment(ctx, repo.Owner, repo.Name, issueNumber, &gh.IssueComment{Body: &body})
	return err
}

func GetWorkflowIssues(ctx context.Context) ([]types.Issue, error) {
	return GetAllOpenIssues(ctx)
}

func GetEligibleIssues(ctx context.Context) ([]types.Issue, error) {
	all, err := GetAllOpenIssues(ctx)
	if err != nil {
		return nil, err
	}

	// needs-review first (sorted ascending), then ready
	var review, ready []types.Issue
	for _, i := range all {
		switch i.State {
		case "needs-review":
			review = append(review, i)
		case "ready":
			ready = append(ready, i)
		}
	}
	return append(review, ready...), nil
}
