package operator

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestNextDispatchIntervalBackoffCapsAtMax(t *testing.T) {
	min := 2 * time.Minute
	max := 1 * time.Hour

	cases := []struct {
		idle int
		want time.Duration
	}{
		{idle: 0, want: 2 * time.Minute},
		{idle: 1, want: 4 * time.Minute},
		{idle: 2, want: 8 * time.Minute},
		{idle: 5, want: 1 * time.Hour},
		{idle: 99, want: 1 * time.Hour},
	}

	for _, tc := range cases {
		if got := nextDispatchInterval(min, max, tc.idle); got != tc.want {
			t.Fatalf("idle=%d interval=%s want %s", tc.idle, got, tc.want)
		}
	}
}

func TestCanonicalTaskNameSanitizesRepo(t *testing.T) {
	got := canonicalTaskName("Abix-/My_Repo", 42)
	want := "abix-my-repo-42"
	if got != want {
		t.Fatalf("canonicalTaskName() = %q, want %q", got, want)
	}
}

func TestPreferTaskPrefersActiveThenCanonicalThenNewest(t *testing.T) {
	current := &AgentJob{
		ObjectMeta: objectMetaAt(time.Now().Add(-time.Minute)),
		Spec: AgentJobSpec{
			Repo:        "abix-/k3sc",
			IssueNumber: 7,
		},
		Status: AgentJobStatus{Phase: TaskPhaseSucceeded},
	}
	candidate := &AgentJob{
		ObjectMeta: objectMetaAt(time.Now()),
		Spec: AgentJobSpec{
			Repo:        "abix-/k3sc",
			IssueNumber: 7,
		},
		Status: AgentJobStatus{Phase: TaskPhaseRunning},
	}
	if !preferTask(candidate, current) {
		t.Fatal("active task should win over terminal task")
	}

	current = &AgentJob{
		ObjectMeta: objectMetaAt(time.Now()),
		Spec: AgentJobSpec{
			Repo:        "abix-/k3sc",
			IssueNumber: 7,
		},
	}
	current.Name = "legacy-7-123"
	candidate = &AgentJob{
		ObjectMeta: objectMetaAt(time.Now().Add(-time.Minute)),
		Spec: AgentJobSpec{
			Repo:        "abix-/k3sc",
			IssueNumber: 7,
		},
	}
	candidate.Name = canonicalTaskName("abix-/k3sc", 7)
	if !preferTask(candidate, current) {
		t.Fatal("canonical task should win over legacy name")
	}
}

func objectMetaAt(ts time.Time) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		CreationTimestamp: metav1.NewTime(ts),
	}
}
