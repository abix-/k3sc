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

const RequeueDelay = 10 * time.Second

type Reconciler struct {
	client.Client
	K8s      *kubernetes.Clientset
	Template string
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

	// terminal states with Reported=true -- nothing to do
	logger.V(1).Info("task terminal", "issue", task.Spec.IssueNumber, "phase", task.Status.Phase)
	return ctrl.Result{}, nil
}

func (r *Reconciler) handlePending(ctx context.Context, task *ClaudeTask) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	maxSlots := dispatch.MaxSlots()
	slot, err := dispatch.FindFreeSlot(ctx, r.K8s, maxSlots)
	if err != nil {
		return ctrl.Result{RequeueAfter: RequeueDelay}, err
	}
	if slot == -1 {
		logger.Info("no free slots, requeueing", "issue", task.Spec.IssueNumber)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	task.Status.Phase = TaskPhaseAssigned
	task.Status.Agent = types.AgentName(slot)
	task.Status.Slot = slot

	logger.Info("assigned", "issue", task.Spec.IssueNumber, "agent", task.Status.Agent, "slot", slot)
	return ctrl.Result{Requeue: true}, r.Status().Update(ctx, task)
}

func (r *Reconciler) handleAssigned(ctx context.Context, task *ClaudeTask) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	repo := dispatch.RepoFromString(task.Spec.Repo)

	if err := github.ClaimIssue(ctx, repo, task.Spec.IssueNumber, task.Status.Agent); err != nil {
		logger.Error(err, "failed to claim issue", "issue", task.Spec.IssueNumber)
		return ctrl.Result{RequeueAfter: RequeueDelay}, nil
	}

	jobName, err := k8s.CreateJobFromTemplate(ctx, r.K8s, r.Template, task.Spec.IssueNumber, task.Status.Slot, task.Spec.RepoURL)
	if err != nil {
		logger.Error(err, "failed to create job", "issue", task.Spec.IssueNumber)
		return ctrl.Result{RequeueAfter: RequeueDelay}, err
	}

	now := metav1.Now()
	task.Status.Phase = TaskPhaseRunning
	task.Status.JobName = jobName
	task.Status.StartedAt = &now

	logger.Info("dispatched", "issue", task.Spec.IssueNumber, "agent", task.Status.Agent, "job", jobName)
	return ctrl.Result{}, r.Status().Update(ctx, task)
}

func (r *Reconciler) handleRunning(ctx context.Context, task *ClaudeTask) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

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
		logger.Info("job disappeared", "issue", task.Spec.IssueNumber)
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
		r.captureLogTail(ctx, task)
		logger.Info("task succeeded", "issue", task.Spec.IssueNumber)
		return ctrl.Result{}, r.Status().Update(ctx, task)
	}

	if latest.Status.Failed > 0 || isJobDead(latest) {
		now := metav1.Now()
		task.Status.Phase = TaskPhaseFailed
		task.Status.FinishedAt = &now
		task.Status.LastError = "pod failed"
		r.captureLogTail(ctx, task)
		logger.Info("task failed", "issue", task.Spec.IssueNumber)
		return ctrl.Result{}, r.Status().Update(ctx, task)
	}

	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

// handleCompleted runs once: posts result comment, syncs labels, marks reported.
func (r *Reconciler) handleCompleted(ctx context.Context, task *ClaudeTask) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	repo := dispatch.RepoFromString(task.Spec.Repo)
	succeeded := task.Status.Phase == TaskPhaseSucceeded

	// post result comment
	status := "succeeded"
	if !succeeded {
		status = "failed"
	}
	duration := ""
	if task.Status.StartedAt != nil && task.Status.FinishedAt != nil {
		d := task.Status.FinishedAt.Sub(task.Status.StartedAt.Time)
		duration = fmt.Sprintf(" (%dm %02ds)", int(d.Minutes()), int(d.Seconds())%60)
	}

	body := fmt.Sprintf("## k3sc operator\n- Agent: %s\n- Status: %s%s",
		task.Status.Agent, status, duration)
	if task.Status.LogTail != "" {
		body += fmt.Sprintf("\n\n```\n%s\n```", task.Status.LogTail)
	}
	if task.Status.LastError != "" {
		body += fmt.Sprintf("\n\nError: %s", task.Status.LastError)
	}
	github.PostComment(ctx, repo, task.Spec.IssueNumber, body)

	// sync labels
	if succeeded {
		nextAction := "needs-human"
		hasPR, _ := github.HasOpenPR(ctx, repo, task.Spec.IssueNumber)
		if hasPR {
			nextAction = "needs-review"
		}
		task.Status.NextAction = nextAction
		github.UnclaimIssue(ctx, repo, task.Spec.IssueNumber, task.Status.Agent, nextAction)
	} else {
		// failed -- put back to ready so scanner can re-dispatch
		task.Status.NextAction = "ready"
		github.UnclaimIssue(ctx, repo, task.Spec.IssueNumber, task.Status.Agent, "ready")
	}

	task.Status.Reported = true
	logger.Info("completed", "issue", task.Spec.IssueNumber, "status", status, "nextAction", task.Status.NextAction)
	return ctrl.Result{}, r.Status().Update(ctx, task)
}

func (r *Reconciler) captureLogTail(ctx context.Context, task *ClaudeTask) {
	podName, _ := k8s.FindPodForIssue(ctx, r.K8s, task.Spec.IssueNumber)
	if podName != "" {
		tail, _ := k8s.GetPodLogTail(ctx, r.K8s, podName, 20)
		task.Status.LogTail = tail
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

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&ClaudeTask{}).
		Complete(r)
}
