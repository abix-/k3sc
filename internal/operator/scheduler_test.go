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

func TestAttemptTaskNameFormat(t *testing.T) {
	got := attemptTaskName("Abix-/My_Repo", 42)
	if len(got) == 0 {
		t.Fatal("attemptTaskName() returned empty string")
	}
	prefix := "my-repo-42-abix-"
	if got[:len(prefix)] != prefix {
		t.Fatalf("attemptTaskName() = %q, want prefix %q", got, prefix)
	}
}

func TestCreateTaskCreatesNewCRD(t *testing.T) {
	scheme := newOperatorTestScheme(t)
	coretypes.Namespace = "claude-agents"
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&AgentJob{}).
		Build()
	issue := coretypes.Issue{
		Number: 190,
		Repo:   coretypes.Repo{Owner: "abix-", Name: "endless"},
		State:  "ready",
	}

	name1, err := CreateTask(context.Background(), c, issue, 0)
	if err != nil {
		t.Fatalf("CreateTask returned error: %v", err)
	}

	var created AgentJob
	key := client.ObjectKey{Name: name1, Namespace: "claude-agents"}
	if err := c.Get(context.Background(), key, &created); err != nil {
		t.Fatalf("get created task: %v", err)
	}
	if created.Status.FailureCount != 0 {
		t.Fatalf("created failureCount = %d, want 0", created.Status.FailureCount)
	}

	// second attempt creates a new CRD
	name2, err := CreateTask(context.Background(), c, issue, 1)
	if err != nil {
		t.Fatalf("second CreateTask returned error: %v", err)
	}
	if name1 == name2 {
		t.Fatal("second attempt should have a different CRD name")
	}

	var second AgentJob
	key2 := client.ObjectKey{Name: name2, Namespace: "claude-agents"}
	if err := c.Get(context.Background(), key2, &second); err != nil {
		t.Fatalf("get second task: %v", err)
	}
	if second.Status.FailureCount != 1 {
		t.Fatalf("second failureCount = %d, want 1", second.Status.FailureCount)
	}
}

func TestDisabledFamiliesOverrideAvailability(t *testing.T) {
	states := map[coretypes.AgentFamily]familyDispatchState{
		coretypes.FamilyClaude: {Available: true, Reason: "ok"},
		coretypes.FamilyCodex:  {Available: true, Reason: "ok"},
	}

	disabled := []string{"codex"}
	for _, d := range disabled {
		family := coretypes.AgentFamily(d)
		if state, ok := states[family]; ok {
			state.Available = false
			state.Reason = "disabled via k3sc disable"
			states[family] = state
		}
	}

	if states[coretypes.FamilyCodex].Available {
		t.Fatal("codex should be blocked after disable")
	}
	if !states[coretypes.FamilyClaude].Available {
		t.Fatal("claude should remain available")
	}
}

func TestDeepCopyDispatchStatePreservesDisabledFamilies(t *testing.T) {
	state := &DispatchState{
		Spec: DispatchStateSpec{
			TriggerNonce:     5,
			DisabledFamilies: []string{"codex"},
		},
	}
	copied := state.DeepCopyObject().(*DispatchState)

	if len(copied.Spec.DisabledFamilies) != 1 || copied.Spec.DisabledFamilies[0] != "codex" {
		t.Fatalf("copied DisabledFamilies = %v, want [codex]", copied.Spec.DisabledFamilies)
	}

	copied.Spec.DisabledFamilies[0] = "claude"
	if state.Spec.DisabledFamilies[0] != "codex" {
		t.Fatal("mutating copy should not affect original")
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
