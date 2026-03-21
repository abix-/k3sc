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
	coretypes "github.com/abix-/k3sc/internal/types"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ktypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
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

func pickAvailableFamily(claudeAvailable, codexAvailable bool) (coretypes.AgentFamily, bool) {
	familyMu.Lock()
	defer familyMu.Unlock()

	switch {
	case claudeAvailable && codexAvailable:
		f := nextFamily
		if nextFamily == coretypes.FamilyClaude {
			nextFamily = coretypes.FamilyCodex
		} else {
			nextFamily = coretypes.FamilyClaude
		}
		return f, true
	case claudeAvailable:
		nextFamily = coretypes.FamilyCodex
		return coretypes.FamilyClaude, true
	case codexAvailable:
		nextFamily = coretypes.FamilyClaude
		return coretypes.FamilyCodex, true
	default:
		return "", false
	}
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

type scanResult struct {
	hadWork            bool
	familyStatuses     []DispatchFamilyStatus
	reviewReservations []DispatchReviewReservationStatus
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
	scan, err := r.scan(ctx, state.Spec.DisabledFamilies)
	if err != nil {
		logger.Error(err, "scan failed")
	}

	now := metav1.Now()
	desired := desiredDispatchStatus(state.Status, state.Spec.TriggerNonce, scan, err, now)

	if !dispatchStatusEqual(state.Status, desired) {
		var updateErr error
		desired, updateErr = r.syncDispatchStatus(ctx, req.NamespacedName, scan, err, now)
		if updateErr != nil {
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

func desiredDispatchStatus(base DispatchStateStatus, triggerNonce int64, scan scanResult, scanErr error, now metav1.Time) DispatchStateStatus {
	desired := base
	desired.LastScanTime = &now
	desired.ObservedTriggerNonce = triggerNonce
	if scan.hadWork {
		desired.IdleScans = 0
		desired.LastWorkTime = &now
	} else {
		desired.IdleScans++
	}
	if len(scan.familyStatuses) > 0 {
		desired.FamilyStatuses = scan.familyStatuses
	}
	desired.ReviewReservations = scan.reviewReservations
	if scanErr != nil {
		desired.LastError = scanErr.Error()
	} else {
		desired.LastError = ""
	}
	return desired
}

func (r *DispatchReconciler) syncDispatchStatus(ctx context.Context, key ktypes.NamespacedName, scan scanResult, scanErr error, now metav1.Time) (DispatchStateStatus, error) {
	var final DispatchStateStatus
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latest DispatchState
		if err := r.Get(ctx, key, &latest); err != nil {
			return err
		}
		desired := desiredDispatchStatus(latest.Status, latest.Spec.TriggerNonce, scan, scanErr, now)
		final = desired
		if dispatchStatusEqual(latest.Status, desired) {
			return nil
		}
		latest.Status = desired
		return r.Status().Update(ctx, &latest)
	})
	return final, err
}

func dispatchStatusEqual(a, b DispatchStateStatus) bool {
	return a.ObservedTriggerNonce == b.ObservedTriggerNonce &&
		a.IdleScans == b.IdleScans &&
		a.LastError == b.LastError &&
		sameFamilyStatuses(a.FamilyStatuses, b.FamilyStatuses) &&
		sameReviewReservations(a.ReviewReservations, b.ReviewReservations) &&
		sameTime(a.LastScanTime, b.LastScanTime) &&
		sameTime(a.LastWorkTime, b.LastWorkTime)
}

func sameFamilyStatuses(a, b []DispatchFamilyStatus) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
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

func (r *DispatchReconciler) scan(ctx context.Context, disabledFamilies []string) (scanResult, error) {
	tasks, groups, err := r.loadTaskState(ctx)
	if err != nil {
		return scanResult{}, err
	}

	r.cleanupTerminalTasks(ctx, tasks)

	reviewReservations, reviewReservationsByIssue, err := r.loadReviewReservations(ctx)
	if err != nil {
		return scanResult{}, err
	}

	familyStates, warnings := probeFamilyDispatchStates(ctx, r.K8s, usageLimitLookback)

	// apply manual family disables from DispatchState spec
	for _, disabled := range disabledFamilies {
		family := coretypes.AgentFamily(disabled)
		if state, ok := familyStates[family]; ok {
			state.Available = false
			state.Reason = "disabled via k3sc disable"
			familyStates[family] = state
		}
	}

	result := scanResult{
		familyStatuses:     dispatchFamilyStatuses(familyStates),
		reviewReservations: reviewReservations,
	}
	for _, warning := range warnings {
		olog("scheduler", "%s", warning)
	}

	claudeAvailable := familyStates[coretypes.FamilyClaude].Available
	codexAvailable := familyStates[coretypes.FamilyCodex].Available
	for _, family := range []coretypes.AgentFamily{coretypes.FamilyClaude, coretypes.FamilyCodex} {
		if state := familyStates[family]; !state.Available {
			olog("scheduler", "%s dispatch blocked: %s", family, state.Reason)
		}
	}
	if !claudeAvailable && !codexAvailable {
		olog("scheduler", "all agent families blocked by quota, skipping dispatch")
		return result, nil
	}

	maxSlots := dispatch.MaxSlots()
	usedSlots := usedSlotsFromGroups(groups)

	// phase 1: re-dispatch from CRD state (needs-review and ready retries)
	for _, group := range groups {
		if group == nil || group.Current == nil || group.Active != nil {
			continue
		}
		if !IsTerminal(group.Current.Status.Phase) || !group.Current.Status.Reported {
			continue
		}
		if group.FailureCount >= MaxFailures {
			continue
		}
		nextAction := group.Current.Status.NextAction
		if nextAction != "needs-review" && nextAction != "ready" {
			continue
		}
		if nextAction == "needs-review" {
			if _, blocked := reviewReservationsByIssue[issueKey(group.Current.Spec.Repo, group.Current.Spec.IssueNumber)]; blocked {
				olog("scheduler", "skip %s redispatch: local PR review lease active", group.Current.Name)
				continue
			}
		}

		slot := dispatch.FindFreeSlotFromList(usedSlots, maxSlots)
		if slot == -1 {
			olog("scheduler", "no free slots")
			break
		}

		family, ok := pickAvailableFamily(claudeAvailable, codexAvailable)
		if !ok {
			olog("scheduler", "no dispatchable agent family")
			break
		}
		agent := coretypes.AgentName(family, slot)
		// build a synthetic issue for requeueTask
		repo := dispatch.RepoFromString(group.Current.Spec.Repo)
		issue := coretypes.Issue{
			Number: group.Current.Spec.IssueNumber,
			Repo:   repo,
			State:  nextAction,
		}
		if err := r.requeueTask(ctx, group.Current, issue, slot, agent, string(family), group.FailureCount); err != nil {
			olog("scheduler", "redispatch %s: %v", group.Current.Name, err)
			continue
		}
		olog("scheduler", "redispatch %s -> %s (slot %d, %s)", group.Current.Name, nextAction, slot, agent)
		usedSlots = append(usedSlots, slot)
		group.Active = &AgentJob{Spec: AgentJobSpec{Slot: slot, Agent: agent}}
		result.hadWork = true
	}

	// phase 2: intake new issues with "ready" label from GitHub
	ready, err := github.GetReadyIssues(ctx)
	if err != nil {
		return result, err
	}

	for _, issue := range ready {
		if reason := github.DispatchTrustReason(issue); reason != "" {
			olog("scheduler", "skip %s#%d: %s", issue.Repo.Name, issue.Number, reason)
			continue
		}

		key := issueKey(fullRepo(issue.Repo), issue.Number)
		group := groups[key]
		if group != nil && group.Active != nil {
			continue
		}
		if group != nil && group.FailureCount >= MaxFailures {
			continue
		}
		// skip if already has a reported terminal task (already processed)
		if group != nil && group.Current != nil && IsTerminal(group.Current.Status.Phase) && group.Current.Status.Reported {
			continue
		}

		slot := dispatch.FindFreeSlotFromList(usedSlots, maxSlots)
		if slot == -1 {
			olog("scheduler", "no free slots")
			break
		}

		family, ok := pickAvailableFamily(claudeAvailable, codexAvailable)
		if !ok {
			olog("scheduler", "no dispatchable agent family")
			break
		}
		agent := coretypes.AgentName(family, slot)
		if group != nil && group.Current != nil {
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
		group.Active = &AgentJob{Spec: AgentJobSpec{Slot: slot, Agent: agent}}
		result.hadWork = true
	}

	// phase 3: intake needs-review issues for agent-assisted review
	needsReview, err := github.GetNeedsReviewIssues(ctx)
	if err != nil {
		return result, err
	}

	for _, issue := range needsReview {
		if reason := github.DispatchTrustReason(issue); reason != "" {
			continue
		}

		key := issueKey(fullRepo(issue.Repo), issue.Number)
		group := groups[key]
		if group != nil && group.Active != nil {
			continue
		}
		if group != nil && group.FailureCount >= MaxFailures {
			continue
		}
		if group != nil && group.Current != nil && IsTerminal(group.Current.Status.Phase) && group.Current.Status.Reported {
			continue
		}

		slot := dispatch.FindFreeSlotFromList(usedSlots, maxSlots)
		if slot == -1 {
			break
		}

		family, ok := pickAvailableFamily(claudeAvailable, codexAvailable)
		if !ok {
			break
		}
		agent := coretypes.AgentName(family, slot)

		// avoid assigning the same agent that implemented the issue
		if group != nil && group.Current != nil && group.Current.Status.Agent == agent {
			olog("scheduler", "skip review %s#%d: same agent %s implemented it", issue.Repo.Name, issue.Number, agent)
			continue
		}

		if group != nil && group.Current != nil {
			if err := r.requeueTask(ctx, group.Current, issue, slot, agent, string(family), group.FailureCount); err != nil {
				olog("scheduler", "requeue review %s: %v", group.Current.Name, err)
				continue
			}
			olog("scheduler", "requeued review %s (slot %d, %s)", group.Current.Name, slot, agent)
		} else {
			name, err := r.createTask(ctx, issue, slot, agent, string(family))
			if err != nil {
				olog("scheduler", "create review %s#%d: %v", issue.Repo.Name, issue.Number, err)
				continue
			}
			olog("scheduler", "created review %s (slot %d, %s)", name, slot, agent)
		}

		usedSlots = append(usedSlots, slot)
		if group == nil {
			group = &issueTaskState{}
			groups[key] = group
		}
		group.Active = &AgentJob{Spec: AgentJobSpec{Slot: slot, Agent: agent}}
		result.hadWork = true
	}

	return result, nil
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
	// look up PR number for review jobs
	var prNumber int
	if issue.State == "needs-review" {
		if pr, err := github.GetOpenPRNumber(ctx, issue.Repo, issue.Number); err == nil {
			prNumber = pr
		}
	}

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
			PRNumber:    prNumber,
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
	return task.Name, nil
}

func (r *DispatchReconciler) requeueTask(ctx context.Context, task *AgentJob, issue coretypes.Issue, slot int, agent, family string, failureCount int) error {
	key := client.ObjectKeyFromObject(task)
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &AgentJob{}
		if err := r.Get(ctx, key, latest); err != nil {
			return err
		}
		latest.Spec.Repo = fullRepo(issue.Repo)
		latest.Spec.RepoName = issue.Repo.Name
		latest.Spec.IssueNumber = issue.Number
		latest.Spec.RepoURL = issue.Repo.CloneURL()
		latest.Spec.Slot = slot
		latest.Spec.Agent = agent
		latest.Spec.Family = family
		latest.Spec.OriginState = issue.State
		// look up PR number for review jobs
		if issue.State == "needs-review" {
			if pr, err := github.GetOpenPRNumber(ctx, issue.Repo, issue.Number); err == nil {
				latest.Spec.PRNumber = pr
			}
		}
		return r.Update(ctx, latest)
	}); err != nil {
		return err
	}
	return r.setTaskPending(ctx, key, failureCount)
}

func (r *DispatchReconciler) setTaskPending(ctx context.Context, key client.ObjectKey, failureCount int) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		latest := &AgentJob{}
		if err := r.Get(ctx, key, latest); err != nil {
			return err
		}
		desired := AgentJobStatus{
			Phase:        TaskPhasePending,
			FailureCount: failureCount,
		}
		if latest.Status == desired {
			return nil
		}
		latest.Status = desired
		return r.Status().Update(ctx, latest)
	})
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
