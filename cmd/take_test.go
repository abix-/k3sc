package cmd

import (
	"testing"

	"github.com/abix-/k3sc/internal/types"
)

func TestSortPRReviewCandidatesPrioritizesPerfThenFixThenOldest(t *testing.T) {
	prs := []types.PullRequest{
		{Number: 14, Title: "feat: ui cleanup", Repo: types.Repo{Name: "endless"}},
		{Number: 12, Title: "fix: broken claim reset", Repo: types.Repo{Name: "endless"}},
		{Number: 11, Title: "perf: reduce scan cost", Repo: types.Repo{Name: "endless"}},
		{Number: 10, Title: "perf: older perf fix", Repo: types.Repo{Name: "endless"}},
		{Number: 9, Title: "fix: older fix", Repo: types.Repo{Name: "endless"}},
	}

	sortPRReviewCandidates(prs)

	got := []int{prs[0].Number, prs[1].Number, prs[2].Number, prs[3].Number, prs[4].Number}
	want := []int{10, 11, 9, 12, 14}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sorted numbers = %v, want %v", got, want)
		}
	}
}

func TestMatchesRepoFilter(t *testing.T) {
	repo := types.Repo{Name: "k3sc"}
	if !matchesRepoFilter(repo, "all") {
		t.Fatal("all filter should match any repo")
	}
	if !matchesRepoFilter(repo, "k3sc") {
		t.Fatal("exact repo name should match")
	}
	if matchesRepoFilter(repo, "endless") {
		t.Fatal("different repo should not match")
	}
}

func TestParseWorkerFamily(t *testing.T) {
	tests := []struct {
		worker string
		family types.AgentFamily
		ok     bool
	}{
		{worker: "claude-a", family: types.FamilyClaude, ok: true},
		{worker: "codex-b", family: types.FamilyCodex, ok: true},
		{worker: "claude-", ok: false},
		{worker: "human-a", ok: false},
	}

	for _, tc := range tests {
		got, ok := types.ParseWorkerFamily(tc.worker)
		if ok != tc.ok || got != tc.family {
			t.Fatalf("ParseWorkerFamily(%q) = (%q, %v), want (%q, %v)", tc.worker, got, ok, tc.family, tc.ok)
		}
	}
}
