package github

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/abix-/k3sc/internal/config"
	"github.com/abix-/k3sc/internal/types"
	gh "github.com/google/go-github/v68/github"
	"golang.org/x/oauth2"
)

func newClient(ctx context.Context) *gh.Client {
	token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
	if token == "" {
		token = strings.TrimSpace(os.Getenv("GH_TOKEN"))
	}
	if token == "" {
		if out, err := exec.CommandContext(ctx, "gh", "auth", "token").Output(); err == nil {
			token = strings.TrimSpace(string(out))
		}
	}

	var httpClient *gh.Client
	if token == "" {
		httpClient = gh.NewClient(nil)
	} else {
		ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
		tc := oauth2.NewClient(ctx, ts)
		httpClient = gh.NewClient(tc)
	}

	baseURL := strings.TrimRight(types.GitHubURL, "/")
	if baseURL != "" && baseURL != "https://github.com" {
		apiURL := baseURL + "/api/v3/"
		uploadURL := baseURL + "/api/uploads/"
		if ghe, err := httpClient.WithEnterpriseURLs(apiURL, uploadURL); err == nil {
			return ghe
		}
	}
	return httpClient
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
			issueNum := ParseBranchIssueNumber(branch)
			_, owner := parseIssueLabels(pr.Labels)
			waiting := false
			for _, l := range pr.Labels {
				if l.GetName() == "waiting" {
					waiting = true
					break
				}
			}
			if waiting {
				owner = "" // waiting overrides ownership
			}
			result = append(result, types.PullRequest{
				Number:  pr.GetNumber(),
				Title:   pr.GetTitle(),
				State:   pr.GetState(),
				Branch:  branch,
				Issue:   issueNum,
				Owner:   owner,
				Waiting: waiting,
				Repo:    repo,
			})
		}
	}
	return result, nil
}

func ParseBranchIssueNumber(branch string) int {
	issueNum := 0
	if strings.HasPrefix(branch, "issue-") {
		fmt.Sscanf(branch, "issue-%d", &issueNum)
	}
	return issueNum
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
				Author:    i.GetUser().GetLogin(),
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
	return SetIssueLabels(ctx, repo, issueNumber, []string{agentName}, "")
}

// UnclaimIssue removes owner label and sets a workflow state label.
func UnclaimIssue(ctx context.Context, repo types.Repo, issueNumber int, ownerLabel, returnLabel string) error {
	return SetIssueLabels(ctx, repo, issueNumber, []string{returnLabel}, "")
}

func setOwnerLabel(ctx context.Context, repo types.Repo, number int, ownerLabel string) error {
	client := newClient(ctx)

	issue, _, err := client.Issues.Get(ctx, repo.Owner, repo.Name, number)
	if err != nil {
		return fmt.Errorf("get issue: %w", err)
	}

	for _, l := range issue.Labels {
		name := l.GetName()
		if strings.HasPrefix(name, "claude-") || strings.HasPrefix(name, "codex-") {
			if _, err := client.Issues.RemoveLabelForIssue(ctx, repo.Owner, repo.Name, number, name); err != nil {
				return fmt.Errorf("remove owner label %q: %w", name, err)
			}
		}
	}

	if ownerLabel != "" {
		if _, _, err := client.Issues.AddLabelsToIssue(ctx, repo.Owner, repo.Name, number, []string{ownerLabel}); err != nil {
			return fmt.Errorf("add owner label: %w", err)
		}
	}
	return nil
}

func ClaimPullRequest(ctx context.Context, repo types.Repo, prNumber int, ownerLabel string) error {
	return setOwnerLabel(ctx, repo, prNumber, ownerLabel)
}

func UnclaimPullRequest(ctx context.Context, repo types.Repo, prNumber int) error {
	return setOwnerLabel(ctx, repo, prNumber, "")
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

// GetOpenPRNumber returns the PR number for an issue-N branch, or 0 if none.
func GetOpenPRNumber(ctx context.Context, repo types.Repo, issueNumber int) (int, error) {
	client := newClient(ctx)
	branch := fmt.Sprintf("issue-%d", issueNumber)
	prs, _, err := client.PullRequests.List(ctx, repo.Owner, repo.Name, &gh.PullRequestListOptions{
		State:       "open",
		Head:        fmt.Sprintf("%s:%s", repo.Owner, branch),
		ListOptions: gh.ListOptions{PerPage: 1},
	})
	if err != nil {
		return 0, err
	}
	if len(prs) == 0 {
		return 0, nil
	}
	return prs[0].GetNumber(), nil
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

func IsDispatchTrustedIssue(issue types.Issue) bool {
	return DispatchTrustReason(issue) == ""
}

func DispatchTrustReason(issue types.Issue) string {
	if !isAllowedRepo(issue.Repo) {
		return fmt.Sprintf("repo %s/%s is not allowlisted", issue.Repo.Owner, issue.Repo.Name)
	}
	if !isAllowedAuthor(issue.Author) {
		return fmt.Sprintf("author %q is not allowlisted", issue.Author)
	}
	return ""
}

func isAllowedRepo(repo types.Repo) bool {
	for _, allowed := range config.C.Repos {
		if strings.EqualFold(allowed.Owner, repo.Owner) && strings.EqualFold(allowed.Name, repo.Name) {
			return true
		}
	}
	return false
}

func isAllowedAuthor(author string) bool {
	for _, allowed := range config.C.AllowedAuthors {
		if strings.EqualFold(allowed, author) {
			return true
		}
	}
	return false
}

// GetReadyIssues returns issues with the "ready" label from configured repos.
// This is the only GitHub label the operator reads for dispatch decisions.
// After intake, all state is tracked in AgentJob CRDs.
func GetReadyIssues(ctx context.Context) ([]types.Issue, error) {
	all, err := GetAllOpenIssues(ctx)
	if err != nil {
		return nil, err
	}

	var ready []types.Issue
	for _, i := range all {
		if i.State == "ready" {
			ready = append(ready, i)
		}
	}
	return ready, nil
}

// GetNeedsReviewIssues returns issues with the "needs-review" label.
// Used by the scheduler to intake review jobs for agent-assisted review.
func GetNeedsReviewIssues(ctx context.Context) ([]types.Issue, error) {
	all, err := GetAllOpenIssues(ctx)
	if err != nil {
		return nil, err
	}

	var result []types.Issue
	for _, i := range all {
		if i.State == "needs-review" {
			result = append(result, i)
		}
	}
	return result, nil
}
