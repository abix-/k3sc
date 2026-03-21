package cmd

import (
	"testing"
	"time"

	"github.com/abix-/k3sc/internal/types"
)

func TestMergeTaskRuntimeMatchesLatestPod(t *testing.T) {
	now := time.Now()
	earlier := now.Add(-time.Minute)
	tasks := []types.TaskInfo{{
		Repo:  types.Repo{Owner: "abix-", Name: "endless"},
		Issue: 190,
		Agent: "claude-a",
		Phase: "Running",
	}}
	pods := []types.AgentPod{
		{
			Name:     "older",
			Repo:     types.Repo{Owner: "abix-", Name: "endless"},
			Issue:    190,
			Slot:     1,
			Family:   types.FamilyClaude,
			Phase:    types.PhasePending,
			Started:  &earlier,
			Finished: &earlier,
			LogTail:  "old",
		},
		{
			Name:    "newer",
			Repo:    types.Repo{Owner: "abix-", Name: "endless"},
			Issue:   190,
			Slot:    1,
			Family:  types.FamilyClaude,
			Phase:   types.PhaseRunning,
			Started: &now,
			LogTail: "new",
		},
	}

	merged := mergeTaskRuntime(tasks, pods)
	if len(merged) != 1 {
		t.Fatalf("merged len = %d, want 1", len(merged))
	}
	if merged[0].RuntimePhase != types.PhaseRunning || merged[0].LogTail != "new" {
		t.Fatalf("merged runtime = (%q, %q), want (Running, new)", merged[0].RuntimePhase, merged[0].LogTail)
	}
}

func TestMergeTaskRuntimeIgnoresMismatchedPods(t *testing.T) {
	tasks := []types.TaskInfo{{
		Repo:  types.Repo{Owner: "abix-", Name: "endless"},
		Issue: 190,
		Agent: "claude-a",
		Phase: "Pending",
	}}
	pods := []types.AgentPod{{
		Name:    "codex",
		Repo:    types.Repo{Owner: "abix-", Name: "endless"},
		Issue:   190,
		Slot:    1,
		Family:  types.FamilyCodex,
		Phase:   types.PhaseRunning,
		LogTail: "ignore-me",
	}}

	merged := mergeTaskRuntime(tasks, pods)
	if len(merged) != 1 {
		t.Fatalf("merged len = %d, want 1", len(merged))
	}
	if merged[0].RuntimePhase != "" || merged[0].LogTail != "" {
		t.Fatalf("unexpected runtime match: %+v", merged[0])
	}
}
