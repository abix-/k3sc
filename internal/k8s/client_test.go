package k8s

import (
	"strings"
	"testing"
	"time"

	"github.com/abix-/k3sc/internal/types"
)

func TestFindRecentUsageLimitPodFromLogsPrefersMostRecentFailure(t *testing.T) {
	now := time.Date(2026, time.March, 17, 12, 0, 0, 0, time.UTC)
	older := now.Add(-10 * time.Minute)
	recent := now.Add(-2 * time.Minute)

	pods := []types.AgentPod{
		{Name: "old", Issue: 8, Repo: types.Repo{Name: "endless"}, Phase: types.PhaseFailed, Finished: &older},
		{Name: "new", Issue: 11, Repo: types.Repo{Name: "k3sc"}, Phase: types.PhaseFailed, Finished: &recent},
	}
	logs := map[string]string{
		"old": UsageLimitMessage,
		"new": UsageLimitMessage,
	}

	pod := FindRecentUsageLimitPodFromLogs(now, 15*time.Minute, pods, logs)
	if pod == nil || pod.Name != "new" {
		t.Fatalf("FindRecentUsageLimitPodFromLogs() = %#v, want pod %q", pod, "new")
	}
}

func TestFindRecentUsageLimitPodFromLogsIgnoresOldAndHealthyPods(t *testing.T) {
	now := time.Date(2026, time.March, 17, 12, 0, 0, 0, time.UTC)
	old := now.Add(-20 * time.Minute)
	recent := now.Add(-3 * time.Minute)

	pods := []types.AgentPod{
		{Name: "old-failed", Issue: 8, Repo: types.Repo{Name: "k3sc"}, Phase: types.PhaseFailed, Finished: &old},
		{Name: "recent-success", Issue: 10, Repo: types.Repo{Name: "k3sc"}, Phase: types.PhaseSucceeded, Finished: &recent},
		{Name: "recent-failed", Issue: 11, Repo: types.Repo{Name: "k3sc"}, Phase: types.PhaseFailed, Finished: &recent},
	}
	logs := map[string]string{
		"old-failed":     UsageLimitMessage,
		"recent-success": UsageLimitMessage,
		"recent-failed":  "some other error",
	}

	pod := FindRecentUsageLimitPodFromLogs(now, 15*time.Minute, pods, logs)
	if pod != nil {
		t.Fatalf("FindRecentUsageLimitPodFromLogs() = %#v, want nil", pod)
	}
}

func TestParseUsageLimitResetTimeSameDay(t *testing.T) {
	now := time.Date(2026, time.March, 17, 16, 6, 0, 0, time.UTC)
	resetAt, ok := ParseUsageLimitResetTime(now, "You're out of extra usage -- resets 5pm (UTC)")
	if !ok {
		t.Fatal("ParseUsageLimitResetTime() = not ok, want ok")
	}
	want := time.Date(2026, time.March, 17, 17, 0, 0, 0, time.UTC)
	if !resetAt.Equal(want) {
		t.Fatalf("ParseUsageLimitResetTime() = %s, want %s", resetAt, want)
	}
}

func TestParseUsageLimitResetTimeRollsToNextDay(t *testing.T) {
	now := time.Date(2026, time.March, 17, 18, 0, 0, 0, time.UTC)
	resetAt, ok := ParseUsageLimitResetTime(now, "You're out of extra usage -- resets 5pm (UTC)")
	if !ok {
		t.Fatal("ParseUsageLimitResetTime() = not ok, want ok")
	}
	want := time.Date(2026, time.March, 18, 17, 0, 0, 0, time.UTC)
	if !resetAt.Equal(want) {
		t.Fatalf("ParseUsageLimitResetTime() = %s, want %s", resetAt, want)
	}
}

func TestCreateJobFromTemplateSlotLetter(t *testing.T) {
	template := `apiVersion: batch/v1
kind: Job
metadata:
  name: "claude-issue-__ISSUE_NUMBER__"
  namespace: claude-agents
spec:
  template:
    spec:
      containers:
        - name: claude-agent
          env:
            - name: AGENT_SLOT
              value: "__AGENT_SLOT__"
            - name: SLOT_LETTER
              value: "__SLOT_LETTER__"
            - name: REPO_URL
              value: "__REPO_URL__"
            - name: ISSUE_NUMBER
              value: "__ISSUE_NUMBER__"
`
	cases := []struct {
		slot   int
		letter string
	}{
		{1, "a"},
		{2, "b"},
		{3, "c"},
		{26, "z"},
	}

	for _, tc := range cases {
		result := applyTemplateSubstitutions(template, 42, tc.slot, "https://github.com/abix-/endless.git")
		want := `value: "` + tc.letter + `"`
		if !strings.Contains(result, want) {
			t.Errorf("slot %d: result does not contain %q\ngot:\n%s", tc.slot, want, result)
		}
		if strings.Contains(result, "__SLOT_LETTER__") {
			t.Errorf("slot %d: result still contains unreplaced __SLOT_LETTER__ placeholder", tc.slot)
		}
	}
}
