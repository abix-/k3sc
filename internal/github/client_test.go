package github

import (
	"testing"

	"github.com/abix-/k3sc/internal/config"
	"github.com/abix-/k3sc/internal/types"
	gh "github.com/google/go-github/v68/github"
)

func label(name string) *gh.Label {
	return &gh.Label{Name: &name}
}

func TestParseIssueLabels(t *testing.T) {
	tests := []struct {
		name      string
		labels    []*gh.Label
		wantState string
		wantOwner string
	}{
		{"owner only = owner is state", []*gh.Label{label("claude-a")}, "claude-a", "claude-a"},
		{"ready no owner", []*gh.Label{label("ready")}, "ready", ""},
		{"needs-review no owner", []*gh.Label{label("needs-review")}, "needs-review", ""},
		{"needs-human with owner", []*gh.Label{label("needs-human"), label("claude-b")}, "needs-human", "claude-b"},
		{"codex owner", []*gh.Label{label("codex-1")}, "codex-1", "codex-1"},
		{"ready with owner", []*gh.Label{label("ready"), label("claude-c")}, "ready", "claude-c"},
		{"no workflow labels", []*gh.Label{label("bug"), label("enhancement")}, "", ""},
		{"owner: prefix not detected", []*gh.Label{label("owner:claude-a")}, "", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotState, gotOwner := parseIssueLabels(tc.labels)
			if gotState != tc.wantState {
				t.Errorf("state: got %q, want %q", gotState, tc.wantState)
			}
			if gotOwner != tc.wantOwner {
				t.Errorf("owner: got %q, want %q", gotOwner, tc.wantOwner)
			}
		})
	}
}

func TestDispatchTrustReason(t *testing.T) {
	prev := config.C
	config.C = config.Config{
		Repos: []config.RepoConfig{
			{Owner: "abix-", Name: "endless"},
			{Owner: "abix-", Name: "k3sc"},
		},
		AllowedAuthors: []string{"abix-"},
	}
	t.Cleanup(func() { config.C = prev })

	tests := []struct {
		name    string
		issue   types.Issue
		trusted bool
	}{
		{
			name: "allowed repo and allowed author",
			issue: types.Issue{
				Repo:   types.Repo{Owner: "abix-", Name: "endless"},
				Author: "abix-",
			},
			trusted: true,
		},
		{
			name: "allowed repo but untrusted author",
			issue: types.Issue{
				Repo:   types.Repo{Owner: "abix-", Name: "endless"},
				Author: "random-user",
			},
			trusted: false,
		},
		{
			name: "untrusted repo",
			issue: types.Issue{
				Repo:   types.Repo{Owner: "someone", Name: "else"},
				Author: "abix-",
			},
			trusted: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := IsDispatchTrustedIssue(tc.issue)
			if got != tc.trusted {
				t.Fatalf("IsDispatchTrustedIssue() = %v, want %v (reason=%q)", got, tc.trusted, DispatchTrustReason(tc.issue))
			}
		})
	}
}

func TestParseBranchIssueNumber(t *testing.T) {
	tests := []struct {
		branch string
		want   int
	}{
		{branch: "issue-194", want: 194},
		{branch: "feature/foo", want: 0},
		{branch: "issue-not-a-number", want: 0},
	}

	for _, tc := range tests {
		if got := ParseBranchIssueNumber(tc.branch); got != tc.want {
			t.Fatalf("ParseBranchIssueNumber(%q) = %d, want %d", tc.branch, got, tc.want)
		}
	}
}
