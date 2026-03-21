package operator

import (
	"context"
	"testing"
	"time"

	coretypes "github.com/abix-/k3sc/internal/types"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
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

func TestDesiredDispatchStatusResetsIdleOnWork(t *testing.T) {
	now := metav1.NewTime(time.Date(2026, time.March, 21, 3, 0, 0, 0, time.UTC))
	baseLastWork := metav1.NewTime(now.Add(-time.Hour))
	base := DispatchStateStatus{
		IdleScans:    4,
		LastWorkTime: &baseLastWork,
	}
	scan := scanResult{
		hadWork: true,
		familyStatuses: []DispatchFamilyStatus{{
			Family:    "claude",
			Available: true,
		}},
		reviewReservations: []DispatchReviewReservationStatus{{
			Repo:     "abix-/endless",
			RepoName: "endless",
			PRNumber: 194,
			WorkerID: "claude-a",
			Family:   "claude",
		}},
	}

	got := desiredDispatchStatus(base, 7, scan, nil, now)

	if got.IdleScans != 0 {
		t.Fatalf("IdleScans = %d, want 0", got.IdleScans)
	}
	if got.ObservedTriggerNonce != 7 {
		t.Fatalf("ObservedTriggerNonce = %d, want 7", got.ObservedTriggerNonce)
	}
	if got.LastScanTime == nil || !got.LastScanTime.Time.Equal(now.Time) {
		t.Fatalf("LastScanTime = %v, want %v", got.LastScanTime, now)
	}
	if got.LastWorkTime == nil || !got.LastWorkTime.Time.Equal(now.Time) {
		t.Fatalf("LastWorkTime = %v, want %v", got.LastWorkTime, now)
	}
	if len(got.FamilyStatuses) != 1 || got.FamilyStatuses[0].Family != "claude" {
		t.Fatalf("FamilyStatuses = %+v, want copied scan state", got.FamilyStatuses)
	}
	if len(got.ReviewReservations) != 1 || got.ReviewReservations[0].WorkerID != "claude-a" {
		t.Fatalf("ReviewReservations = %+v, want copied scan reservations", got.ReviewReservations)
	}
}

func TestSyncDispatchStatusUpdatesStatus(t *testing.T) {
	scheme := newOperatorTestScheme(t)
	state := &DispatchState{
		ObjectMeta: metav1.ObjectMeta{
			Name:      coretypes.DispatchStateName,
			Namespace: "claude-agents",
		},
		Spec: DispatchStateSpec{
			TriggerNonce: 9,
		},
		Status: DispatchStateStatus{
			IdleScans: 1,
		},
	}
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&DispatchState{}).
		WithObjects(state).
		Build()
	r := &DispatchReconciler{Client: c}
	now := metav1.NewTime(time.Date(2026, time.March, 21, 3, 15, 0, 0, time.UTC))

	got, err := r.syncDispatchStatus(context.Background(), client.ObjectKeyFromObject(state), scanResult{}, nil, now)
	if err != nil {
		t.Fatalf("syncDispatchStatus returned error: %v", err)
	}
	if got.IdleScans != 2 {
		t.Fatalf("IdleScans = %d, want 2", got.IdleScans)
	}
	if got.ObservedTriggerNonce != 9 {
		t.Fatalf("ObservedTriggerNonce = %d, want 9", got.ObservedTriggerNonce)
	}

	var updated DispatchState
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(state), &updated); err != nil {
		t.Fatalf("get updated dispatch state: %v", err)
	}
	if updated.Status.IdleScans != 2 {
		t.Fatalf("stored IdleScans = %d, want 2", updated.Status.IdleScans)
	}
	if updated.Status.LastScanTime == nil || !updated.Status.LastScanTime.Time.Equal(now.Time) {
		t.Fatalf("stored LastScanTime = %v, want %v", updated.Status.LastScanTime, now)
	}
}

func TestCreateAndRequeueTaskLifecycle(t *testing.T) {
	scheme := newOperatorTestScheme(t)
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&AgentJob{}).
		Build()
	r := &DispatchReconciler{
		Client:    c,
		Namespace: "claude-agents",
	}
	issue := coretypes.Issue{
		Number: 190,
		Repo:   coretypes.Repo{Owner: "abix-", Name: "endless"},
		State:  "ready",
	}

	name, err := r.createTask(context.Background(), issue, 1, "claude-a", "claude")
	if err != nil {
		t.Fatalf("createTask returned error: %v", err)
	}

	var created AgentJob
	key := client.ObjectKey{Name: name, Namespace: "claude-agents"}
	if err := c.Get(context.Background(), key, &created); err != nil {
		t.Fatalf("get created task: %v", err)
	}
	if created.Status.Phase != "" {
		t.Fatalf("created status phase = %q, want empty", created.Status.Phase)
	}

	if err := r.requeueTask(context.Background(), &created, issue, 2, "codex-b", "codex", 2); err != nil {
		t.Fatalf("requeueTask returned error: %v", err)
	}

	var requeued AgentJob
	if err := c.Get(context.Background(), key, &requeued); err != nil {
		t.Fatalf("get requeued task: %v", err)
	}
	if requeued.Spec.Slot != 2 || requeued.Spec.Agent != "codex-b" || requeued.Spec.Family != "codex" {
		t.Fatalf("requeued spec = %+v, want slot=2 agent=codex-b family=codex", requeued.Spec)
	}
	if requeued.Status.Phase != TaskPhasePending || requeued.Status.FailureCount != 2 {
		t.Fatalf("requeued status = %+v, want phase Pending failureCount 2", requeued.Status)
	}
}

func newOperatorTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	return scheme
}

func objectMetaAt(ts time.Time) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		CreationTimestamp: metav1.NewTime(ts),
	}
}
