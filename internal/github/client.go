package github

import (
	"context"
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

func GetWorkflowIssues(ctx context.Context) ([]types.Issue, error) {
	seen := make(map[int]bool)
	var all []types.Issue
	for _, label := range []string{"needs-review", "needs-human", "claimed", "ready"} {
		issues, err := GetIssuesByLabel(ctx, label)
		if err != nil {
			continue
		}
		for _, i := range issues {
			if !seen[i.Number] {
				seen[i.Number] = true
				all = append(all, i)
			}
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Number < all[j].Number })
	return all, nil
}

func GetEligibleIssues(ctx context.Context) ([]types.Issue, error) {
	review, _ := GetIssuesByLabel(ctx, "needs-review")
	ready, _ := GetIssuesByLabel(ctx, "ready")

	seen := make(map[int]bool)
	var result []types.Issue
	for _, i := range review {
		seen[i.Number] = true
		result = append(result, i)
	}
	for _, i := range ready {
		if !seen[i.Number] {
			result = append(result, i)
		}
	}
	return result, nil
}
