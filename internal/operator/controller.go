package operator

import (
	"context"
	"fmt"
	"time"

	"github.com/abix-/k3sc/internal/dispatch"
	"github.com/abix-/k3sc/internal/github"
	"github.com/abix-/k3sc/internal/k8s"
	"github.com/abix-/k3sc/internal/types"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	MaxRetries   = 3
	RequeueDelay = 10 * time.Second
)

type Reconciler struct {
	client.Client
	K8s      *kubernetes.Clientset
	Template string // job template YAML
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var task ClaudeTask
	if err := r.Get(ctx, req.NamespacedName, &task); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	switch task.Status.Phase {
	case "", TaskPhasePending:
		return r.handlePending(ctx, &task)
	case TaskPhaseRunning:
		return r.handleRunning(ctx, &task)
	case TaskPhaseSucceeded:
		return r.handleCompleted(ctx, &task, true)
	case TaskPhaseFailed:
		return r.handleCompleted(ctx, &task, false)
	case TaskPhaseBlocked:
		// nothing to do
		logger.Info("task blocked", "issue", task.Spec.IssueNumber, "attempts", task.Status.Attempts)
		return ctrl.Result{}, nil
	}

	return ctrl.Result{}, nil
}

func (r *Reconciler) handlePending(ctx context.Context, task *ClaudeTask) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// find a free slot
	maxSlots := dispatch.MaxSlots()
	slot, err := dispatch.FindFreeSlot(ctx, r.K8s, maxSlots)
	if err != nil {
		return ctrl.Result{RequeueAfter: RequeueDelay}, err
	}
	if slot == -1 {
		logger.Info("no free slots, requeueing", "issue", task.Spec.IssueNumber)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	agentName := types.AgentName(slot)

	// claim on github
	if !task.Status.Claimed {
		repo := dispatch.RepoFromString(task.Spec.Repo)
		if err := github.ClaimIssue(ctx, repo, task.Spec.IssueNumber, agentName); err != nil {
			logger.Error(err, "failed to claim issue", "issue", task.Spec.IssueNumber)
			return ctrl.Result{RequeueAfter: RequeueDelay}, nil
		}
		task.Status.Claimed = true
	}

	// create k8s job
	jobName, err := k8s.CreateJobFromTemplate(ctx, r.K8s, r.Template, task.Spec.IssueNumber, slot, task.Spec.RepoURL)
	if err != nil {
		logger.Error(err, "failed to create job", "issue", task.Spec.IssueNumber)
		return ctrl.Result{RequeueAfter: RequeueDelay}, err
	}

	now := metav1.Now()
	task.Status.Phase = TaskPhaseRunning
	task.Status.Agent = agentName
	task.Status.Slot = slot
	task.Status.JobName = jobName
	task.Status.Attempts++
	task.Status.StartedAt = &now
	task.Status.FinishedAt = nil
	task.Status.MaxRetries = MaxRetries

	logger.Info("dispatched", "issue", task.Spec.IssueNumber, "agent", agentName, "slot", slot, "job", jobName)
	return ctrl.Result{}, r.Status().Update(ctx, task)
}

func (r *Reconciler) handleRunning(ctx context.Context, task *ClaudeTask) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// check if the job still exists and its status
	jobs, err := r.K8s.BatchV1().Jobs(types.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app=claude-agent,issue-number=%d", task.Spec.IssueNumber),
	})
	if err != nil {
		return ctrl.Result{RequeueAfter: RequeueDelay}, err
	}

	if len(jobs.Items) == 0 {
		// job gone -- orphaned
		logger.Info("job disappeared, marking failed", "issue", task.Spec.IssueNumber)
		now := metav1.Now()
		task.Status.Phase = TaskPhaseFailed
		task.Status.FinishedAt = &now
		task.Status.LastError = "job disappeared"
		return ctrl.Result{}, r.Status().Update(ctx, task)
	}

	// find the most recent job matching this task
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
		logger.Info("task succeeded", "issue", task.Spec.IssueNumber)
		return ctrl.Result{}, r.Status().Update(ctx, task)
	}

	if latest.Status.Failed > 0 || isJobDead(latest) {
		now := metav1.Now()
		task.Status.Phase = TaskPhaseFailed
		task.Status.FinishedAt = &now
		task.Status.LastError = "pod failed"
		logger.Info("task failed", "issue", task.Spec.IssueNumber, "attempts", task.Status.Attempts)
		return ctrl.Result{}, r.Status().Update(ctx, task)
	}

	// still running, recheck later
	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

func (r *Reconciler) handleCompleted(ctx context.Context, task *ClaudeTask, succeeded bool) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// post result comment if not yet reported
	if !task.Status.Reported {
		repo := dispatch.RepoFromString(task.Spec.Repo)
		podName, _ := k8s.FindPodForIssue(ctx, r.K8s, task.Spec.IssueNumber)
		logTail := ""
		if podName != "" {
			logTail, _ = k8s.GetPodLogTail(ctx, r.K8s, podName, 20)
		}

		status := "succeeded"
		if !succeeded {
			status = "failed"
		}
		duration := ""
		if task.Status.StartedAt != nil && task.Status.FinishedAt != nil {
			d := task.Status.FinishedAt.Sub(task.Status.StartedAt.Time)
			duration = fmt.Sprintf(" (%dm %02ds)", int(d.Minutes()), int(d.Seconds())%60)
		}

		body := fmt.Sprintf("## k3sc operator\n- Agent: %s\n- Status: %s%s\n- Attempts: %d",
			task.Status.Agent, status, duration, task.Status.Attempts)
		if logTail != "" {
			body += fmt.Sprintf("\n\n```\n%s\n```", logTail)
		}
		if task.Status.LastError != "" {
			body += fmt.Sprintf("\n\nError: %s", task.Status.LastError)
		}

		github.PostComment(ctx, repo, task.Spec.IssueNumber, body)
		task.Status.Reported = true
		logger.Info("posted result comment", "issue", task.Spec.IssueNumber, "status", status)
	}

	// if failed and retries remaining, reset to pending
	if !succeeded && task.Status.Attempts < MaxRetries {
		// unclaim on github so it can be re-dispatched
		repo := dispatch.RepoFromString(task.Spec.Repo)
		returnLabel := "ready"
		hasPR, _ := github.HasOpenPR(ctx, repo, task.Spec.IssueNumber)
		if hasPR {
			returnLabel = "needs-review"
		}
		github.UnclaimIssue(ctx, repo, task.Spec.IssueNumber, task.Status.Agent, returnLabel)

		task.Status.Phase = TaskPhasePending
		task.Status.Claimed = false
		task.Status.Reported = false
		logger.Info("retrying", "issue", task.Spec.IssueNumber, "attempt", task.Status.Attempts+1)
		return ctrl.Result{RequeueAfter: time.Duration(task.Status.Attempts) * 30 * time.Second}, r.Status().Update(ctx, task)
	}

	// if failed and no retries left, mark blocked
	if !succeeded {
		repo := dispatch.RepoFromString(task.Spec.Repo)
		github.UnclaimIssue(ctx, repo, task.Spec.IssueNumber, task.Status.Agent, "needs-human")
		task.Status.Phase = TaskPhaseBlocked
		logger.Info("task blocked after max retries", "issue", task.Spec.IssueNumber)
		return ctrl.Result{}, r.Status().Update(ctx, task)
	}

	// succeeded -- transition labels: remove owner, add needs-review (if PR) or needs-human
	repo := dispatch.RepoFromString(task.Spec.Repo)
	returnLabel := "needs-human"
	hasPR, _ := github.HasOpenPR(ctx, repo, task.Spec.IssueNumber)
	if hasPR {
		returnLabel = "needs-review"
	}
	github.UnclaimIssue(ctx, repo, task.Spec.IssueNumber, task.Status.Agent, returnLabel)
	logger.Info("success cleanup", "issue", task.Spec.IssueNumber, "label", returnLabel)
	return ctrl.Result{}, r.Status().Update(ctx, task)
}

func isJobDead(job *batchv1.Job) bool {
	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Status == "True" {
			return true
		}
	}
	return false
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&ClaudeTask{}).
		Complete(r)
}
