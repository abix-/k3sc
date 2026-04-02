package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/abix-/k3sc/internal/claude"
	"github.com/abix-/k3sc/internal/dispatch"
	"github.com/abix-/k3sc/internal/github"
	"github.com/abix-/k3sc/internal/k8s"
	"github.com/abix-/k3sc/internal/types"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const RequeueDelay = 10 * time.Second

type Reconciler struct {
	client.Client
	K8s      *kubernetes.Clientset
	Template string
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var task AgentJob
	if err := r.Get(ctx, req.NamespacedName, &task); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	switch task.Status.Phase {
	case "", TaskPhasePending:
		return r.handlePending(ctx, &task)
	case TaskPhaseAssigned:
		return r.handleAssigned(ctx, &task)
	case TaskPhaseRunning:
		return r.handleRunning(ctx, &task)
	case TaskPhaseSucceeded:
		if !task.Status.Reported {
			return r.handleCompleted(ctx, &task)
		}
	case TaskPhaseFailed:
		if !task.Status.Reported {
			return r.handleCompleted(ctx, &task)
		}
	case TaskPhaseBlocked:
		// terminal
	}

	return ctrl.Result{}, nil
}

func (r *Reconciler) logf(ctx context.Context, task *AgentJob, format string, args ...any) {
	if Verbose {
		logger := log.FromContext(ctx)
		logger.Info(fmt.Sprintf(format, args...), "issue", task.Spec.IssueNumber, "agent", task.Status.Agent)
	} else {
		olog("operator", "#%d %s", task.Spec.IssueNumber, fmt.Sprintf(format, args...))
	}
}

func (r *Reconciler) errf(ctx context.Context, err error, task *AgentJob, format string, args ...any) {
	if Verbose {
		logger := log.FromContext(ctx)
		logger.Error(err, fmt.Sprintf(format, args...), "issue", task.Spec.IssueNumber)
	} else {
		olog("operator", "#%d %s: %v", task.Spec.IssueNumber, fmt.Sprintf(format, args...), err)
	}
}

func taskAgent(task *AgentJob) string {
	if task.Status.Agent != "" {
		return task.Status.Agent
	}
	return task.Spec.Agent
}

func taskSlot(task *AgentJob) int {
	if task.Status.Slot > 0 {
		return task.Status.Slot
	}
	return task.Spec.Slot
}

func taskFamily(task *AgentJob) string {
	if task.Status.Family != "" {
		return task.Status.Family
	}
	if task.Spec.Family != "" {
		return task.Spec.Family
	}
	if agent := taskAgent(task); len(agent) >= len("codex-") && agent[:len("codex-")] == "codex-" {
		return string(types.FamilyCodex)
	}
	return string(types.FamilyClaude)
}

func (r *Reconciler) handlePending(ctx context.Context, task *AgentJob) (ctrl.Result, error) {
	slot := taskSlot(task)
	agent := taskAgent(task)
	family := taskFamily(task)

	if slot == 0 || agent == "" {
		var err error
		slot, err = dispatch.FindFreeSlot(ctx, r.K8s, dispatch.MaxSlots())
		if err != nil {
			return ctrl.Result{RequeueAfter: RequeueDelay}, err
		}
		if slot == -1 {
			return ctrl.Result{RequeueAfter: RequeueDelay}, nil
		}
		f := pickFamily()
		family = string(f)
		agent = types.AgentName(f, slot)
	}

	task.Status.Phase = TaskPhaseAssigned
	task.Status.Agent = agent
	task.Status.Slot = slot
	task.Status.Family = family

	r.logf(ctx, task, "assigned %s slot %d", agent, slot)
	return ctrl.Result{Requeue: true}, r.Status().Update(ctx, task)
}

func (r *Reconciler) handleAssigned(ctx context.Context, task *AgentJob) (ctrl.Result, error) {
	repo := dispatch.RepoFromString(task.Spec.Repo)
	task.Status.Agent = taskAgent(task)
	task.Status.Slot = taskSlot(task)
	task.Status.Family = taskFamily(task)

	if err := github.ClaimIssue(ctx, repo, task.Spec.IssueNumber, task.Status.Agent); err != nil {
		r.errf(ctx, err, task, "claim failed")
		return ctrl.Result{RequeueAfter: RequeueDelay}, nil
	}

	jobKind := "issue"
	if task.Spec.OriginState == "needs-review" {
		jobKind = "review"
	}
	jobName, err := k8s.CreateJobFromTemplate(ctx, r.K8s, r.Template, task.Spec.IssueNumber, task.Status.Slot, task.Spec.RepoURL, taskFamily(task), jobKind, task.Spec.PRNumber)
	if err != nil {
		r.errf(ctx, err, task, "create job failed")
		return ctrl.Result{RequeueAfter: RequeueDelay}, err
	}

	now := metav1.Now()
	task.Status.Phase = TaskPhaseRunning
	task.Status.JobName = jobName
	task.Status.StartedAt = &now

	r.logf(ctx, task, "dispatched %s -> %s", task.Status.Agent, jobName)
	return ctrl.Result{}, r.Status().Update(ctx, task)
}

func (r *Reconciler) handleRunning(ctx context.Context, task *AgentJob) (ctrl.Result, error) {
	jobs, err := r.K8s.BatchV1().Jobs(types.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app=claude-agent,issue-number=%d", task.Spec.IssueNumber),
	})
	if err != nil {
		return ctrl.Result{RequeueAfter: RequeueDelay}, err
	}

	if len(jobs.Items) == 0 {
		now := metav1.Now()
		task.Status.Phase = TaskPhaseFailed
		task.Status.FinishedAt = &now
		task.Status.LastError = "job disappeared"
		task.Status.FailureCount++
		r.logf(ctx, task, "job disappeared")
		return ctrl.Result{}, r.Status().Update(ctx, task)
	}

	var latest *batchv1.Job
	for i := range jobs.Items {
		if latest == nil || jobs.Items[i].CreationTimestamp.After(latest.CreationTimestamp.Time) {
			latest = &jobs.Items[i]
		}
	}

	if latest.Status.Succeeded > 0 {
		now := metav1.Now()
		task.Status.Phase = TaskPhaseSucceeded
		task.Status.FinishedAt = &now
		lines := r.getPodLogLines(ctx, task)
		task.Status.Usage = parseUsageFromLines(lines)
		task.Status.SecurityEvents = parseSecurityEvents(lines)
		r.recordCost(task, 0)
		r.logSecurityEvents(ctx, task)
		return ctrl.Result{}, r.Status().Update(ctx, task)
	}

	if latest.Status.Failed > 0 || isJobDead(latest) {
		now := metav1.Now()
		task.Status.Phase = TaskPhaseFailed
		task.Status.FinishedAt = &now
		task.Status.LastError = "pod failed"
		task.Status.FailureCount++
		lines := r.getPodLogLines(ctx, task)
		task.Status.Usage = parseUsageFromLines(lines)
		task.Status.SecurityEvents = parseSecurityEvents(lines)
		r.recordCost(task, 1)
		r.logSecurityEvents(ctx, task)
		return ctrl.Result{}, r.Status().Update(ctx, task)
	}

	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

func (r *Reconciler) handleCompleted(ctx context.Context, task *AgentJob) (ctrl.Result, error) {
	repo := dispatch.RepoFromString(task.Spec.Repo)
	succeeded := task.Status.Phase == TaskPhaseSucceeded

	status := "succeeded"
	if !succeeded {
		status = "failed"
	}
	duration := ""
	if task.Status.StartedAt != nil && task.Status.FinishedAt != nil {
		d := task.Status.FinishedAt.Sub(task.Status.StartedAt.Time)
		duration = fmt.Sprintf(" (%dm %02ds)", int(d.Minutes()), int(d.Seconds())%60)
	}

	if succeeded {
		nextAction := "needs-review"
		if task.Spec.OriginState == "needs-review" {
			nextAction = "needs-human"
		}
		task.Status.NextAction = nextAction
		github.UnclaimIssue(ctx, repo, task.Spec.IssueNumber, task.Status.Agent, nextAction)
	} else {
		returnTo := task.Spec.OriginState
		if returnTo == "" {
			returnTo = "ready"
		}
		// failed reviews escalate to human -- don't loop back to needs-review
		if returnTo == "needs-review" {
			returnTo = "needs-human"
		}
		task.Status.NextAction = returnTo
		github.UnclaimIssue(ctx, repo, task.Spec.IssueNumber, task.Status.Agent, returnTo)
	}

	task.Status.Reported = true
	r.logf(ctx, task, "%s (origin=%s) -> %s%s", status, task.Spec.OriginState, task.Status.NextAction, duration)
	if !succeeded && task.Status.FailureCount >= MaxFailures {
		r.logf(ctx, task, "blocked after %d failures", task.Status.FailureCount)
	}

	return ctrl.Result{}, r.Status().Update(ctx, task)
}

// getPodLogLines reads the tail of pod logs for a completed job.
func (r *Reconciler) getPodLogLines(ctx context.Context, task *AgentJob) []string {
	podName := ""
	pods, err := r.K8s.CoreV1().Pods(types.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app=claude-agent,issue-number=%d", task.Spec.IssueNumber),
	})
	if err != nil || len(pods.Items) == 0 {
		return nil
	}
	// find most recent pod
	for _, p := range pods.Items {
		if podName == "" || p.CreationTimestamp.After(pods.Items[0].CreationTimestamp.Time) {
			podName = p.Name
		}
	}

	lines, err := k8s.GetPodLogLines(ctx, r.K8s, podName, 200)
	if err != nil {
		return nil
	}
	return lines
}

const usageLinePrefix = "[usage] "

// usageLogLine matches the snake_case JSON emitted by entrypoint.sh collect_usage().
type usageLogLine struct {
	InputTokens         int64    `json:"input_tokens"`
	OutputTokens        int64    `json:"output_tokens"`
	CacheCreationTokens int64    `json:"cache_creation_tokens"`
	CacheReadTokens     int64    `json:"cache_read_tokens"`
	TotalTokens         int64    `json:"total_tokens"`
	CacheHitRate        float64  `json:"cache_hit_rate"`
	OutputRatio         float64  `json:"output_ratio"`
	Models              []string `json:"models"`
	Entries             int      `json:"entries"`
}

func parseUsageFromLines(lines []string) *UsageStats {
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		idx := strings.Index(line, usageLinePrefix)
		if idx == -1 {
			continue
		}
		jsonStr := line[idx+len(usageLinePrefix):]
		var raw usageLogLine
		if err := json.Unmarshal([]byte(jsonStr), &raw); err != nil {
			continue
		}
		if raw.TotalTokens > 0 {
			return &UsageStats{
				InputTokens:         raw.InputTokens,
				OutputTokens:        raw.OutputTokens,
				CacheCreationTokens: raw.CacheCreationTokens,
				CacheReadTokens:     raw.CacheReadTokens,
				TotalTokens:         raw.TotalTokens,
				CacheHitRate:        raw.CacheHitRate,
				OutputRatio:         raw.OutputRatio,
				Models:              raw.Models,
				Entries:             raw.Entries,
			}
		}
	}
	return nil
}

const securityLinePrefix = "[security] "

func parseSecurityEvents(lines []string) []string {
	var events []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		idx := strings.Index(line, securityLinePrefix)
		if idx == -1 {
			continue
		}
		event := line[idx+len(securityLinePrefix):]
		if event != "" {
			events = append(events, event)
		}
	}
	return events
}

func (r *Reconciler) logSecurityEvents(ctx context.Context, task *AgentJob) {
	for _, event := range task.Status.SecurityEvents {
		olog("SECURITY", "#%d %s: %s", task.Spec.IssueNumber, task.Status.Agent, event)
	}
}

func isJobDead(job *batchv1.Job) bool {
	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Status == "True" {
			return true
		}
	}
	return false
}

func (r *Reconciler) recordCost(task *AgentJob, exitCode int) {
	usage := task.Status.Usage
	if usage == nil || usage.TotalTokens == 0 {
		return
	}

	model := ""
	if len(usage.Models) > 0 {
		model = usage.Models[0]
	}

	durationSec := 0
	if task.Status.StartedAt != nil && task.Status.FinishedAt != nil {
		durationSec = int(task.Status.FinishedAt.Sub(task.Status.StartedAt.Time).Seconds())
	}

	cost := claude.CalculateCostForTokens(model, usage.InputTokens, usage.OutputTokens, usage.CacheCreationTokens, usage.CacheReadTokens)

	entry := CostEntry{
		Timestamp:   time.Now().UTC(),
		Repo:        task.Spec.Repo,
		Issue:       task.Spec.IssueNumber,
		Job:         task.Name,
		Agent:       task.Spec.Agent,
		Family:      task.Spec.Family,
		Model:       model,
		Input:       usage.InputTokens,
		Output:      usage.OutputTokens,
		CacheCreate: usage.CacheCreationTokens,
		CacheRead:   usage.CacheReadTokens,
		Total:       usage.TotalTokens,
		CostUSD:     cost,
		DurationSec: durationSec,
		ExitCode:    exitCode,
	}

	if err := AppendCostEntry(entry); err != nil {
		olog("costs", "ledger write failed: %v", err)
	}
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("agentjob").
		For(&AgentJob{}).
		WithOptions(controller.Options{MaxConcurrentReconciles: 1}).
		Complete(r)
}
