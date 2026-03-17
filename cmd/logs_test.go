package cmd

import (
	"strings"
	"testing"

	"github.com/abix-/k3sc/internal/types"
)

func TestParseLogsTarget(t *testing.T) {
	repo, issue, err := parseLogsTarget(nil)
	if err != nil {
		t.Fatalf("parseLogsTarget(nil) error = %v", err)
	}
	if repo != "" || issue != 0 {
		t.Fatalf("parseLogsTarget(nil) = (%q, %d), want empty summary target", repo, issue)
	}

	repo, issue, err = parseLogsTarget([]string{"k3sc", "11"})
	if err != nil {
		t.Fatalf("parseLogsTarget explicit repo error = %v", err)
	}
	if repo != "k3sc" || issue != 11 {
		t.Fatalf("parseLogsTarget explicit repo = (%q, %d), want (%q, %d)", repo, issue, "k3sc", 11)
	}
}

func TestParseLogsTargetRejectsUnknownRepo(t *testing.T) {
	_, _, err := parseLogsTarget([]string{"nope", "11"})
	if err == nil || !strings.Contains(err.Error(), "unknown repo") {
		t.Fatalf("parseLogsTarget unknown repo error = %v, want unknown repo", err)
	}
}

func TestSelectLogPodRequiresRepoWhenIssueIsAmbiguous(t *testing.T) {
	pods := []types.AgentPod{
		{Name: "endless-pod", Issue: 11, Repo: types.Repo{Name: "endless"}},
		{Name: "k3sc-pod", Issue: 11, Repo: types.Repo{Name: "k3sc"}},
	}

	_, err := selectLogPod(pods, "", 11)
	if err == nil || !strings.Contains(err.Error(), "multiple repos") {
		t.Fatalf("selectLogPod ambiguity error = %v, want multiple repos", err)
	}
}

func TestSelectLogPodUsesExplicitRepo(t *testing.T) {
	pods := []types.AgentPod{
		{Name: "older", Issue: 11, Repo: types.Repo{Name: "k3sc"}},
		{Name: "newer", Issue: 11, Repo: types.Repo{Name: "k3sc"}},
	}

	pod, err := selectLogPod(pods, "k3sc", 11)
	if err != nil {
		t.Fatalf("selectLogPod explicit repo error = %v", err)
	}
	if pod == nil || pod.Name != "newer" {
		t.Fatalf("selectLogPod explicit repo = %#v, want newer pod", pod)
	}
}
