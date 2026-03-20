package operator

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/abix-/k3sc/internal/config"
	"github.com/abix-/k3sc/internal/dispatch"
	"github.com/abix-/k3sc/internal/github"
	"github.com/abix-/k3sc/internal/k8s"
	coretypes "github.com/abix-/k3sc/internal/types"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ktypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const MaxFailures = 3

// Verbose enables full structured logging from controller-runtime.
// Set via --verbose flag on the operator command.
var Verbose bool

var edt = time.FixedZone("EDT", -4*3600)

// olog prints a concise timestamped log line: "18:27:22 [prefix] message"
func olog(prefix, format string, args ...any) {
	t := time.Now().In(edt).Format("15:04:05")
	msg := fmt.Sprintf(format, args...)
	fmt.Printf("%s [%s] %s\n", t, prefix, msg)
}

const usageLimitLookback = 15 * time.Minute

var (
	familyMu   sync.Mutex
	nextFamily = coretypes.FamilyClaude
)

func pickFamily() coretypes.AgentFamily {
	familyMu.Lock()
	defer familyMu.Unlock()

	f := nextFamily
	if nextFamily == coretypes.FamilyClaude {
		nextFamily = coretypes.FamilyCodex
	} else {
		nextFamily = coretypes.FamilyClaude
	}
	return f
}

type DispatchReconciler struct {
	client.Client
	APIReader client.Reader
	K8s       *kubernetes.Clientset
	Namespace string
}

type issueTaskState struct {
	Current      *AgentJob
	Active       *AgentJob
	FailureCount int
	failedJobs   int
}

func EnsureDispatchState(ctx context.Context, c client.Client, namespace string) error {
	var state DispatchState
	key := ktypes.NamespacedName{Namespace: namespace, Name: coretypes.DispatchStateName}
	if err := c.Get(ctx, key, &state); err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		return c.Create(ctx, &DispatchState{
			TypeMeta: metav1.TypeMeta{
				APIVersion: GroupVersion.String(),
				Kind:       "DispatchState",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      coretypes.DispatchStateName,
				Namespace: namespace,
			},
		})
	}
	return nil
}

func (r *DispatchReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if req.Namespace != r.Namespace || req.Name != coretypes.DispatchStateName {
		return ctrl.Result{}, nil
	}

	var state DispatchState
	if err := r.Get(ctx, req.NamespacedName, &state); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	logger := log.FromContext(ctx).WithName("scheduler")
	hadWork, err := r.scan(ctx)
	if err != nil {
		logger.Error(err, "scan failed")
	}

	now := metav1.Now()
	desired := state.Status
	desired.LastScanTime = &now
	desired.ObservedTriggerNonce = state.Spec.TriggerNonce
	if hadWork {
		desired.IdleScans = 0
		desired.LastWorkTime = &now
	} else {
		desired.IdleScans++
	}
	if err != nil {
		desired.LastError = err.Error()
	} else {
		desired.LastError = ""
	}

	if !dispatchStatusEqual(state.Status, desired) {
		state.Status = desired
		if updateErr := r.Status().Update(ctx, &state); updateErr != nil {
			return ctrl.Result{}, updateErr
		}
	}

	if err != nil {
		return ctrl.Result{RequeueAfter: config.C.Scan.MinInterval.Duration}, nil
	}
	return ctrl.Result{
		RequeueAfter: nextDispatchInterval(
			config.C.Scan.MinInterval.Duration,
			config.C.Scan.MaxInterval.Duration,
			desired.IdleScans,
		),
	}, nil
}

func dispatchStatusEqual(a, b DispatchStateStatus) bool {
	return a.ObservedTriggerNonce == b.ObservedTriggerNonce &&
		a.IdleScans == b.IdleScans &&
		a.LastError == b.LastError &&
		sameTime(a.LastScanTime, b.LastScanTime) &&
		sameTime(a.LastWorkTime, b.LastWorkTime)
}

func sameTime(a, b *metav1.Time) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return a.Time.Equal(b.Time)
	}
}

func nextDispatchInterval(minInterval, maxInterval time.Duration, idleScans int) time.Duration {
	if idleScans <= 0 {
		return minInterval
	}
	interval := minInterval
	for i := 0; i < idleScans; i++ {
		interval *= 2
		if interval >= maxInterval {
			return maxInterval
		}
	}
	return interval
}

func (r *DispatchReconciler) scan(ctx context.Context) (bool, error) {
	tasks, groups, err := r.loadTaskState(ctx)
	if err != nil {
		return false, err
	}

	r.cleanupTerminalTasks(ctx, tasks)

	usagePod, _, err := k8s.FindRecentUsageLimitPod(ctx, r.K8s, usageLimitLookback)
	if err != nil {
		olog("scheduler", "usage limit check error: %v", err)
	}
	if usagePod != nil {
		olog("scheduler", "usage limit detected in pod %s (%s#%d), skipping dispatch", usagePod.Name, usagePod.Repo.Name, usagePod.Issue)
	}

	eligible, err := github.GetEligibleIssues(ctx)
	if err != nil {
		return false, err
	}

	maxSlots := dispatch.MaxSlots()
	usedSlots := usedSlotsFromGroups(groups)
	hadEligible := false

	for _, issue := range eligible {
		if reason := github.DispatchTrustReason(issue); reason != "" {
			olog("scheduler", "skip %s#%d: %s", issue.Repo.Name, issue.Number, reason)
			continue
		}
		hadEligible = true

		key := issueKey(fullRepo(issue.Repo), issue.Number)
		group := groups[key]
		if group != nil && group.Active != nil {
			continue
		}
		if group != nil && group.FailureCount >= MaxFailures {
			olog("scheduler", "%s blocked after %d failures", key, group.FailureCount)
			continue
		}
		if usagePod != nil {
			continue
		}

		slot := dispatch.FindFreeSlotFromList(usedSlots, maxSlots)
		if slot == -1 {
			olog("scheduler", "no free slots")
			break
		}

		family := pickFamily()
		agent := coretypes.AgentName(family, slot)
		if group != nil && group.Current != nil {
			if IsTerminal(group.Current.Status.Phase) && group.Current.Status.Reported {
				continue
			}
			if err := r.requeueTask(ctx, group.Current, issue, slot, agent, string(family), group.FailureCount); err != nil {
				olog("scheduler", "requeue %s: %v", group.Current.Name, err)
				continue
			}
			olog("scheduler", "requeued %s (slot %d, %s)", group.Current.Name, slot, agent)
		} else {
			name, err := r.createTask(ctx, issue, slot, agent, string(family))
			if err != nil {
				olog("scheduler", "create %s#%d: %v", issue.Repo.Name, issue.Number, err)
				continue
			}
			olog("scheduler", "created %s (slot %d, %s)", name, slot, agent)
		}

		usedSlots = append(usedSlots, slot)
		if group == nil {
			group = &issueTaskState{}
			groups[key] = group
		}
		group.Active = &AgentJob{
			Spec: AgentJobSpec{
				Slot:  slot,
				Agent: agent,
			},
		}
	}

	r.orphanCleanup(ctx, groups)

	return usagePod == nil && hadEligible, nil
}

func (r *DispatchReconciler) loadTaskState(ctx context.Context) ([]AgentJob, map[string]*issueTaskState, error) {
	var tasks AgentJobList
	if err := r.APIReader.List(ctx, &tasks, client.InNamespace(r.Namespace)); err != nil {
		return nil, nil, err
	}

	groups := make(map[string]*issueTaskState, len(tasks.Items))
	for i := range tasks.Items {
		task := &tasks.Items[i]
		key := issueKey(task.Spec.Repo, task.Spec.IssueNumber)
		group := groups[key]
		if group == nil {
			group = &issueTaskState{}
			groups[key] = group
		}
		if task.Status.Phase == TaskPhaseFailed {
			group.failedJobs++
		}
		if task.Status.FailureCount > group.FailureCount {
			group.FailureCount = task.Status.FailureCount
		}
		if group.failedJobs > group.FailureCount {
			group.FailureCount = group.failedJobs
		}
		if taskIsActive(task) && (group.Active == nil || task.CreationTimestamp.After(group.Active.CreationTimestamp.Time)) {
			group.Active = task
		}
		if preferTask(task, group.Current) {
			group.Current = task
		}
	}

	return tasks.Items, groups, nil
}

func usedSlotsFromGroups(groups map[string]*issueTaskState) []int {
	seen := map[int]bool{}
	var slots []int
	for _, group := range groups {
		if group == nil || group.Active == nil {
			continue
		}
		slot := taskAssignedSlot(group.Active)
		if slot == 0 || seen[slot] {
			continue
		}
		seen[slot] = true
		slots = append(slots, slot)
	}
	return slots
}

func taskIsActive(task *AgentJob) bool {
	if task == nil {
		return false
	}
	if task.Status.Phase != "" {
		return !IsTerminal(task.Status.Phase)
	}
	return task.Spec.Slot > 0 || task.Spec.Agent != ""
}

func taskAssignedSlot(task *AgentJob) int {
	if task.Status.Slot > 0 {
		return task.Status.Slot
	}
	return task.Spec.Slot
}

func preferTask(candidate, current *AgentJob) bool {
	if candidate == nil {
		return false
	}
	if current == nil {
		return true
	}
	candidateActive := taskIsActive(candidate)
	currentActive := taskIsActive(current)
	if candidateActive != currentActive {
		return candidateActive
	}

	candidateCanonical := isCanonicalTask(candidate)
	currentCanonical := isCanonicalTask(current)
	if candidateCanonical != currentCanonical {
		return candidateCanonical
	}

	return candidate.CreationTimestamp.After(current.CreationTimestamp.Time)
}

func isCanonicalTask(task *AgentJob) bool {
	return task != nil && task.Name == canonicalTaskName(task.Spec.Repo, task.Spec.IssueNumber)
}

func fullRepo(repo coretypes.Repo) string {
	return repo.Owner + "/" + repo.Name
}

func issueKey(repo string, issue int) string {
	return strings.ToLower(fmt.Sprintf("%s#%d", repo, issue))
}

func canonicalTaskName(repo string, issue int) string {
	parts := strings.SplitN(repo, "/", 2)
	owner := ""
	name := repo
	if len(parts) == 2 {
		owner = parts[0]
		name = parts[1]
	}
	return fmt.Sprintf("%s-%s-%d", sanitizeName(owner), sanitizeName(name), issue)
}

func sanitizeName(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		isAlphaNum := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if isAlphaNum {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func (r *DispatchReconciler) createTask(ctx context.Context, issue coretypes.Issue, slot int, agent, family string) (string, error) {
	task := &AgentJob{
		TypeMeta: metav1.TypeMeta{
			APIVersion: GroupVersion.String(),
			Kind:       "AgentJob",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      canonicalTaskName(fullRepo(issue.Repo), issue.Number),
			Namespace: r.Namespace,
		},
		Spec: AgentJobSpec{
			Repo:        fullRepo(issue.Repo),
			RepoName:    issue.Repo.Name,
			IssueNumber: issue.Number,
			RepoURL:     issue.Repo.CloneURL(),
			Slot:        slot,
			Agent:       agent,
			Family:      family,
			OriginState: issue.State,
		},
	}
	if err := r.Create(ctx, task); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return task.Name, nil
		}
		return "", err
	}
	if err := r.setTaskPending(ctx, task, 0); err != nil {
		return "", err
	}
	return task.Name, nil
}

func (r *DispatchReconciler) requeueTask(ctx context.Context, task *AgentJob, issue coretypes.Issue, slot int, agent, family string, failureCount int) error {
	updated := task.DeepCopyObject().(*AgentJob)
	updated.Spec.Repo = fullRepo(issue.Repo)
	updated.Spec.RepoName = issue.Repo.Name
	updated.Spec.IssueNumber = issue.Number
	updated.Spec.RepoURL = issue.Repo.CloneURL()
	updated.Spec.Slot = slot
	updated.Spec.Agent = agent
	updated.Spec.Family = family
	updated.Spec.OriginState = issue.State
	if err := r.Update(ctx, updated); err != nil {
		return err
	}
	return r.setTaskPending(ctx, updated, failureCount)
}

func (r *DispatchReconciler) setTaskPending(ctx context.Context, task *AgentJob, failureCount int) error {
	latest := &AgentJob{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(task), latest); err != nil {
		return err
	}
	latest.Status = AgentJobStatus{
		Phase:        TaskPhasePending,
		FailureCount: failureCount,
	}
	return r.Status().Update(ctx, latest)
}

func (r *DispatchReconciler) cleanupTerminalTasks(ctx context.Context, tasks []AgentJob) {
	ttl := config.C.Scan.TaskTTL.Duration
	for i := range tasks {
		task := &tasks[i]
		if !IsTerminal(task.Status.Phase) {
			continue
		}
		if time.Since(task.CreationTimestamp.Time) <= ttl {
			continue
		}
		if err := r.Delete(ctx, task); err != nil {
			olog("scheduler", "cleanup %s: %v", task.Name, err)
		} else {
			olog("scheduler", "cleaned up %s", task.Name)
		}
	}
}

// orphanCleanup finds issues with owner labels but no active pod or non-terminal AgentJob.
func (r *DispatchReconciler) orphanCleanup(ctx context.Context, groups map[string]*issueTaskState) {
	owned, err := github.GetOwnedIssues(ctx)
	if err != nil {
		olog("scheduler", "orphan check error: %v", err)
		return
	}
	if len(owned) == 0 {
		return
	}

	activeSlots, err := k8s.GetActiveSlots(ctx, r.K8s)
	if err != nil {
		olog("scheduler", "orphan slot check error: %v", err)
		return
	}
	activeAgents := map[string]bool{}
	for _, slot := range activeSlots {
		activeAgents[coretypes.AgentName(coretypes.FamilyClaude, slot)] = true
		activeAgents[coretypes.AgentName(coretypes.FamilyCodex, slot)] = true
	}

	for _, issue := range owned {
		if activeAgents[issue.Owner] {
			continue
		}
		key := issueKey(fullRepo(issue.Repo), issue.Number)
		if group := groups[key]; group != nil {
			if group.Active != nil {
				olog("scheduler", "%s#%d owned by %s: task active, deferring to controller", issue.Repo.Name, issue.Number, issue.Owner)
				continue
			}
			if group.Current != nil && IsTerminal(group.Current.Status.Phase) && group.Current.Status.Reported {
				olog("scheduler", "%s#%d owned by %s: task reported, skipping orphan cleanup", issue.Repo.Name, issue.Number, issue.Owner)
				continue
			}
		}

		returnLabel := "ready"
		hasPR, err := github.HasOpenPR(ctx, issue.Repo, issue.Number)
		if err == nil && hasPR {
			returnLabel = "needs-review"
		}
		olog("scheduler", "orphan: %s#%d owned by %s, returning to %s", issue.Repo.Name, issue.Number, issue.Owner, returnLabel)
		if err := github.UnclaimIssue(ctx, issue.Repo, issue.Number, issue.Owner, returnLabel); err != nil {
			olog("scheduler", "orphan unclaim %s#%d: %v", issue.Repo.Name, issue.Number, err)
		}
	}
}

func (r *DispatchReconciler) SetupWithManager(mgr ctrl.Manager) error {
	dispatchRequest := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		return []reconcile.Request{{
			NamespacedName: ktypes.NamespacedName{
				Namespace: r.Namespace,
				Name:      coretypes.DispatchStateName,
			},
		}}
	})

	return ctrl.NewControllerManagedBy(mgr).
		Named("dispatchstate").
		For(&DispatchState{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&AgentJob{}, dispatchRequest).
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		Complete(r)
}
