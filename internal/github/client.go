package github

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/abix-/k3sc/internal/types"
	gh "github.com/google/go-github/v68/github"
)

// newClientFn is the factory used by all exported functions. Override in tests.
var newClientFn func(token string) *gh.Client = defaultNewClient

func defaultNewClient(token string) *gh.Client {
	if token == "" {
		return gh.NewClient(nil)
	}
	return gh.NewClient(nil).WithAuthToken(token)
}

func resolveToken() string {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		token = os.Getenv("GH_TOKEN")
	}
	if token == "" {
		for _, path := range []string{
			"/home/claude/.gh-token",                // k8s pod (hostPath mount)
			os.Getenv("USERPROFILE") + "/.gh-token", // Windows
			os.Getenv("HOME") + "/.gh-token",        // Linux
		} {
			if b, err := os.ReadFile(path); err == nil {
				return strings.TrimSpace(string(b))
			}
		}
	}
	return token
}

func newClient(_ context.Context) *gh.Client {
	return newClientFn(resolveToken())
}

// parseIssueLabels extracts workflow state and owner from GitHub labels.
func parseIssueLabels(labels []*gh.Label) (state, owner string) {
	for _, s := range []string{"claimed", "needs-human", "needs-review", "ready"} {
		for _, l := range labels {
			if l.GetName() == s {
				state = s
				break
			}
		}
		if state != "" {
			break
		}
	}
	for _, l := range labels {
		name := l.GetName()
		if strings.HasPrefix(name, "owner:") {
			owner = strings.TrimPrefix(name, "owner:")
			break
		}
	}
	return
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
		"claimed": true, "needs-human": true, "needs-review": true, "ready": true,
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
				if workflowLabels[l.GetName()] {
					hasWorkflow = true
					break
				}
			}
			if !hasWorkflow {
				continue
			}

			state, owner := parseIssueLabels(i.Labels)
			result = append(result, types.Issue{
				Number: i.GetNumber(),
				Title:  i.GetTitle(),
				State:  state,
				Owner:  owner,
				Repo:   repo,
			})
		}
	}

	sort.Slice(result, func(i, j int) bool { return result[i].Number < result[j].Number })
	return result, nil
}

// ClaimIssue transitions an issue from ready/needs-review to claimed with an owner label.
// It removes ready and needs-review labels, adds claimed + owner label, and posts a claim comment.
func ClaimIssue(ctx context.Context, repo types.Repo, issueNumber int, agentName string) error {
	client := newClient(ctx)

	// remove ready and needs-review, add claimed
	for _, label := range []string{"ready", "needs-review"} {
		client.Issues.RemoveLabelForIssue(ctx, repo.Owner, repo.Name, issueNumber, label)
	}
	_, _, err := client.Issues.AddLabelsToIssue(ctx, repo.Owner, repo.Name, issueNumber, []string{"claimed", agentName})
	if err != nil {
		return fmt.Errorf("add labels: %w", err)
	}

	// post claim comment
	body := fmt.Sprintf("## Claude\n- State: ready -> claimed\n- Owner: %s\n- Intent: dispatched by k3sc dispatcher", agentName)
	_, _, err = client.Issues.CreateComment(ctx, repo.Owner, repo.Name, issueNumber, &gh.IssueComment{Body: &body})
	if err != nil {
		return fmt.Errorf("post comment: %w", err)
	}

	return nil
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
