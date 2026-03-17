package github

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/abix-/k3s-claude/internal/types"
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
			"/home/claude/.gh-token",                       // k8s pod (hostPath mount)
			os.Getenv("USERPROFILE") + "/.gh-token",        // Windows
			os.Getenv("HOME") + "/.gh-token",               // Linux
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

func GetIssuesByLabel(ctx context.Context, label string) ([]types.Issue, error) {
	client := newClient(ctx)
	opts := &gh.IssueListByRepoOptions{
		State:  "open",
		Labels: []string{label},
		ListOptions: gh.ListOptions{PerPage: 50},
	}

	issues, _, err := client.Issues.ListByRepo(ctx, types.RepoOwner, types.RepoName, opts)
	if err != nil {
		return nil, err
	}

	var result []types.Issue
	for _, i := range issues {
		if i.IsPullRequest() {
			continue
		}
		var labels []string
		for _, l := range i.Labels {
			labels = append(labels, l.GetName())
		}

		state := ""
		for _, s := range []string{"claimed", "needs-human", "needs-review", "ready"} {
			for _, l := range labels {
				if l == s {
					state = s
					break
				}
			}
			if state != "" {
				break
			}
		}

		owner := ""
		for _, l := range labels {
			if len(l) > 6 && (l[:7] == "claude-" || l[:6] == "codex-") {
				owner = l
				break
			}
		}

		title := i.GetTitle()
		result = append(result, types.Issue{
			Number: i.GetNumber(),
			Title:  title,
			State:  state,
			Owner:  owner,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Number < result[j].Number
	})
	return result, nil
}

func GetOpenPRs(ctx context.Context) ([]types.PullRequest, error) {
	client := newClient(ctx)
	opts := &gh.PullRequestListOptions{
		State:       "open",
		Sort:        "created",
		Direction:   "asc",
		ListOptions: gh.ListOptions{PerPage: 50},
	}

	prs, _, err := client.PullRequests.List(ctx, types.RepoOwner, types.RepoName, opts)
	if err != nil {
		return nil, err
	}

	var result []types.PullRequest
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
		})
	}
	return result, nil
}

// GetAllOpenIssues fetches all open issues in one API call and filters client-side.
// This uses 1 API call instead of 4-5 separate label queries.
func GetAllOpenIssues(ctx context.Context) ([]types.Issue, error) {
	client := newClient(ctx)
	opts := &gh.IssueListByRepoOptions{
		State:       "open",
		ListOptions: gh.ListOptions{PerPage: 100},
	}

	ghIssues, _, err := client.Issues.ListByRepo(ctx, types.RepoOwner, types.RepoName, opts)
	if err != nil {
		return nil, err
	}

	workflowLabels := map[string]bool{
		"claimed": true, "needs-human": true, "needs-review": true, "ready": true,
	}

	var result []types.Issue
	for _, i := range ghIssues {
		if i.IsPullRequest() {
			continue
		}
		var labels []string
		for _, l := range i.Labels {
			labels = append(labels, l.GetName())
		}

		state := ""
		for _, s := range []string{"claimed", "needs-human", "needs-review", "ready"} {
			for _, l := range labels {
				if l == s {
					state = s
					break
				}
			}
			if state != "" {
				break
			}
		}

		// skip issues without workflow labels
		hasWorkflow := false
		for _, l := range labels {
			if workflowLabels[l] {
				hasWorkflow = true
				break
			}
		}
		if !hasWorkflow {
			continue
		}

		owner := ""
		for _, l := range labels {
			if len(l) > 6 && (l[:7] == "claude-" || l[:6] == "codex-") {
				owner = l
				break
			}
		}

		result = append(result, types.Issue{
			Number: i.GetNumber(),
			Title:  i.GetTitle(),
			State:  state,
			Owner:  owner,
		})
	}

	sort.Slice(result, func(i, j int) bool { return result[i].Number < result[j].Number })
	return result, nil
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
