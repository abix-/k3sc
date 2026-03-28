package k8s

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/abix-/k3sc/internal/types"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	UsageLimitMessage = "You're out of extra usage"
)

var usageLimitResetPattern = regexp.MustCompile(`(?i)resets ([0-9]{1,2}(?::[0-9]{2})?\s*(?:am|pm)) \(([^)]+)\)`)

func getConfig() (*rest.Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, nil).ClientConfig()
	if err != nil {
		config, err = clientcmd.BuildConfigFromFlags("", "")
		if err != nil {
			return nil, fmt.Errorf("k8s config: %w", err)
		}
	}
	config.QPS = 50
	config.Burst = 100
	return config, nil
}

func NewClient() (*kubernetes.Clientset, error) {
	config, err := getConfig()
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(config)
}

var dispatchStateGVR = schema.GroupVersionResource{
	Group:    "k3sc.abix.dev",
	Version:  "v1",
	Resource: "dispatchstates",
}

var agentJobGVR = schema.GroupVersionResource{
	Group:    "k3sc.abix.dev",
	Version:  "v1",
	Resource: "agentjobs",
}

var reviewLeaseGVR = schema.GroupVersionResource{
	Group:    "k3sc.abix.dev",
	Version:  "v1",
	Resource: "reviewleases",
}

// GetAgentJobs fetches AgentJob CRs and returns TUI-friendly TaskInfo structs.
func GetAgentJobs(ctx context.Context) ([]types.TaskInfo, error) {
	cfg, err := getConfig()
	if err != nil {
		return nil, err
	}
	dc, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("dynamic client: %w", err)
	}

	list, err := dc.Resource(agentJobGVR).Namespace(types.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list agentjobs: %w", err)
	}

	var result []types.TaskInfo
	for _, item := range list.Items {
		specRepoName, _, _ := unstructured.NestedString(item.Object, "spec", "repoName")
		specIssue, _, _ := unstructured.NestedInt64(item.Object, "spec", "issueNumber")
		specAgent, _, _ := unstructured.NestedString(item.Object, "spec", "agent")
		specSlot, _, _ := unstructured.NestedInt64(item.Object, "spec", "slot")
		phase, _, _ := unstructured.NestedString(item.Object, "status", "phase")
		agent, _, _ := unstructured.NestedString(item.Object, "status", "agent")
		slot, _, _ := unstructured.NestedInt64(item.Object, "status", "slot")
		nextAction, _, _ := unstructured.NestedString(item.Object, "status", "nextAction")
		startedAtRaw, _, _ := unstructured.NestedString(item.Object, "status", "startedAt")
		finishedAtRaw, _, _ := unstructured.NestedString(item.Object, "status", "finishedAt")
		if agent == "" {
			agent = specAgent
		}
		if slot == 0 {
			slot = specSlot
		}

		t := types.TaskInfo{
			Name:       item.GetName(),
			Repo:       types.RepoByName(specRepoName),
			Issue:      int(specIssue),
			Phase:      phase,
			Agent:      agent,
			Slot:       int(slot),
			NextAction: nextAction,
		}
		if startedAtRaw != "" {
			if ts, err := time.Parse(time.RFC3339, startedAtRaw); err == nil {
				t.Started = &ts
			}
		}
		if finishedAtRaw != "" {
			if ts, err := time.Parse(time.RFC3339, finishedAtRaw); err == nil {
				t.Finished = &ts
			}
		}
		result = append(result, t)
	}

	// newest first, no phase ordering
	sort.Slice(result, func(i, j int) bool {
		ti, tj := result[i].Started, result[j].Started
		if ti == nil && tj == nil {
			return false
		}
		if ti == nil {
			return false
		}
		if tj == nil {
			return true
		}
		return tj.Before(*ti)
	})

	return result, nil
}

func GetAgentPods(ctx context.Context, cs *kubernetes.Clientset) ([]types.AgentPod, error) {
	pods, err := cs.CoreV1().Pods(types.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app=claude-agent",
	})
	if err != nil {
		return nil, err
	}

	var result []types.AgentPod
	for _, p := range pods.Items {
		issue, _ := strconv.Atoi(p.Labels["issue-number"])
		slot, _ := strconv.Atoi(p.Labels["agent-slot"])
		phase := types.PodPhase(p.Status.Phase)
		repo := types.RepoByName(p.Labels["repo"])

		var started, finished *time.Time
		if p.Status.StartTime != nil {
			t := p.Status.StartTime.Time
			started = &t
		}
		if len(p.Status.ContainerStatuses) > 0 {
			if term := p.Status.ContainerStatuses[0].State.Terminated; term != nil {
				t := term.FinishedAt.Time
				finished = &t
			}
		}

		family := types.AgentFamily(p.Labels["agent-family"])
		if family == "" {
			family = types.FamilyClaude
		}

		result = append(result, types.AgentPod{
			Name:     p.Name,
			Issue:    issue,
			Slot:     slot,
			Family:   family,
			Phase:    phase,
			Started:  started,
			Finished: finished,
			Repo:     repo,
		})
	}

	// newest first
	sort.Slice(result, func(i, j int) bool {
		ti, tj := result[i].Started, result[j].Started
		if ti == nil && tj == nil {
			return false
		}
		if ti == nil {
			return false
		}
		if tj == nil {
			return true
		}
		return tj.Before(*ti)
	})

	return result, nil
}

// HasAgentJobForIssue returns true if a non-terminal AgentJob CRD exists for the issue.
// Only checks the operator's own state, not raw k8s batch Jobs.
func HasAgentJobForIssue(ctx context.Context, issue int) (bool, error) {
	jobs, err := GetAgentJobs(ctx)
	if err != nil {
		return false, err
	}
	terminalPhases := map[string]bool{"Succeeded": true, "Failed": true, "Blocked": true}
	for _, j := range jobs {
		if j.Issue == issue && !terminalPhases[j.Phase] {
			return true, nil
		}
	}
	return false, nil
}

func HasActiveAgentJobForRepoIssue(ctx context.Context, repoName string, issue int) (bool, error) {
	jobs, err := GetAgentJobs(ctx)
	if err != nil {
		return false, err
	}
	terminalPhases := map[string]bool{"Succeeded": true, "Failed": true, "Blocked": true}
	for _, j := range jobs {
		if j.Repo.Name == repoName && j.Issue == issue && !terminalPhases[j.Phase] {
			return true, nil
		}
	}
	return false, nil
}

// DeleteAgentJobsForIssue deletes all terminal AgentJob CRDs for the given repo/issue pair.
// Returns the number of deleted jobs.
func DeleteAgentJobsForIssue(ctx context.Context, repoName string, issue int) (int, error) {
	cfg, err := getConfig()
	if err != nil {
		return 0, err
	}
	dc, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return 0, fmt.Errorf("dynamic client: %w", err)
	}

	list, err := dc.Resource(agentJobGVR).Namespace(types.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, fmt.Errorf("list agentjobs: %w", err)
	}
	deleted := 0
	terminal := map[string]bool{"Succeeded": true, "Failed": true, "Blocked": true}
	for _, item := range list.Items {
		specRepoName, _, _ := unstructured.NestedString(item.Object, "spec", "repoName")
		specIssue, _, _ := unstructured.NestedInt64(item.Object, "spec", "issueNumber")
		phase, _, _ := unstructured.NestedString(item.Object, "status", "phase")
		if specRepoName != repoName || int(specIssue) != issue || !terminal[phase] {
			continue
		}
		if err := dc.Resource(agentJobGVR).Namespace(types.Namespace).Delete(ctx, item.GetName(), metav1.DeleteOptions{}); err == nil {
			deleted++
		}
	}
	return deleted, nil
}

func TriggerDispatch(ctx context.Context) (string, error) {
	cfg, err := getConfig()
	if err != nil {
		return "", err
	}
	dc, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return "", fmt.Errorf("dynamic client: %w", err)
	}

	resource := dc.Resource(dispatchStateGVR).Namespace(types.Namespace)
	state, err := resource.Get(ctx, types.DispatchStateName, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return "", fmt.Errorf("get dispatch state: %w", err)
		}
		state = &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "k3sc.abix.dev/v1",
				"kind":       "DispatchState",
				"metadata": map[string]any{
					"name":      types.DispatchStateName,
					"namespace": types.Namespace,
				},
				"spec": map[string]any{
					"triggerNonce": int64(1),
				},
			},
		}
		if _, err := resource.Create(ctx, state, metav1.CreateOptions{}); err != nil {
			return "", fmt.Errorf("create dispatch state: %w", err)
		}
		return "dispatch state created and scan requested\n", nil
	}

	nonce, found, err := unstructured.NestedInt64(state.Object, "spec", "triggerNonce")
	if err != nil {
		return "", fmt.Errorf("read trigger nonce: %w", err)
	}
	if !found {
		nonce = 0
	}
	if err := unstructured.SetNestedField(state.Object, nonce+1, "spec", "triggerNonce"); err != nil {
		return "", fmt.Errorf("set trigger nonce: %w", err)
	}
	if _, err := resource.Update(ctx, state, metav1.UpdateOptions{}); err != nil {
		return "", fmt.Errorf("update dispatch state: %w", err)
	}
	return fmt.Sprintf("scan requested (trigger %d)\n", nonce+1), nil
}

func GetDispatchState(ctx context.Context) (types.DispatchStateInfo, error) {
	cfg, err := getConfig()
	if err != nil {
		return types.DispatchStateInfo{}, err
	}
	dc, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return types.DispatchStateInfo{}, fmt.Errorf("dynamic client: %w", err)
	}

	state, err := dc.Resource(dispatchStateGVR).Namespace(types.Namespace).Get(ctx, types.DispatchStateName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return types.DispatchStateInfo{}, nil
		}
		return types.DispatchStateInfo{}, fmt.Errorf("get dispatch state: %w", err)
	}

	items, found, err := unstructured.NestedSlice(state.Object, "status", "familyStatuses")
	if err != nil {
		return types.DispatchStateInfo{}, err
	}

	info := types.DispatchStateInfo{}

	// read disabled families from spec
	disabledRaw, _, _ := unstructured.NestedStringSlice(state.Object, "spec", "disabledFamilies")
	for _, f := range disabledRaw {
		info.DisabledFamilies = append(info.DisabledFamilies, types.AgentFamily(f))
	}

	if found {
		info.FamilyStatuses = make([]types.DispatchFamilyStatus, 0, len(items))
		for _, item := range items {
			entry, ok := item.(map[string]any)
			if !ok {
				continue
			}

			family, _, _ := unstructured.NestedString(entry, "family")
			available, _, _ := unstructured.NestedBool(entry, "available")
			checked, _, _ := unstructured.NestedBool(entry, "checked")
			reason, _, _ := unstructured.NestedString(entry, "reason")
			info.FamilyStatuses = append(info.FamilyStatuses, types.DispatchFamilyStatus{
				Family:    types.AgentFamily(family),
				Available: available,
				Checked:   checked,
				Reason:    reason,
			})
		}
	}

	reviewItems, found, err := unstructured.NestedSlice(state.Object, "status", "reviewReservations")
	if err != nil {
		return types.DispatchStateInfo{}, err
	}
	if found {
		info.ReviewReservations = make([]types.ReviewReservation, 0, len(reviewItems))
		for _, item := range reviewItems {
			entry, ok := item.(map[string]any)
			if !ok {
				continue
			}

			repoName, _, _ := unstructured.NestedString(entry, "repoName")
			prNumber, _, _ := unstructured.NestedInt64(entry, "prNumber")
			prURL, _, _ := unstructured.NestedString(entry, "prUrl")
			branch, _, _ := unstructured.NestedString(entry, "branch")
			issueNumber, _, _ := unstructured.NestedInt64(entry, "issueNumber")
			family, _, _ := unstructured.NestedString(entry, "family")
			workerID, _, _ := unstructured.NestedString(entry, "workerId")
			workerKind, _, _ := unstructured.NestedString(entry, "workerKind")
			reservedAt, _ := nestedTime(entry, "reservedAt")
			expiresAt, _ := nestedTime(entry, "expiresAt")

			info.ReviewReservations = append(info.ReviewReservations, types.ReviewReservation{
				Repo:       types.RepoByName(repoName),
				PRNumber:   int(prNumber),
				PRURL:      prURL,
				Branch:     branch,
				Issue:      int(issueNumber),
				Family:     types.AgentFamily(family),
				WorkerID:   workerID,
				WorkerKind: workerKind,
				ReservedAt: reservedAt,
				ExpiresAt:  expiresAt,
			})
		}
	}

	return info, nil
}

func SetDisabledFamilies(ctx context.Context, families []string) error {
	cfg, err := getConfig()
	if err != nil {
		return err
	}
	dc, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("dynamic client: %w", err)
	}

	resource := dc.Resource(dispatchStateGVR).Namespace(types.Namespace)
	state, err := resource.Get(ctx, types.DispatchStateName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get dispatch state: %w", err)
	}

	if len(families) == 0 {
		unstructured.RemoveNestedField(state.Object, "spec", "disabledFamilies")
	} else {
		vals := make([]any, len(families))
		for i, f := range families {
			vals[i] = f
		}
		if err := unstructured.SetNestedSlice(state.Object, vals, "spec", "disabledFamilies"); err != nil {
			return fmt.Errorf("set disabled families: %w", err)
		}
	}

	if _, err := resource.Update(ctx, state, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update dispatch state: %w", err)
	}
	return nil
}

func GetTimberbotSpec(ctx context.Context) (types.TimberbotInfo, error) {
	cfg, err := getConfig()
	if err != nil {
		return types.TimberbotInfo{}, err
	}
	dc, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return types.TimberbotInfo{}, fmt.Errorf("dynamic client: %w", err)
	}

	state, err := dc.Resource(dispatchStateGVR).Namespace(types.Namespace).Get(ctx, types.DispatchStateName, metav1.GetOptions{})
	if err != nil {
		return types.TimberbotInfo{}, fmt.Errorf("get dispatch state: %w", err)
	}

	enabled, _, _ := unstructured.NestedBool(state.Object, "spec", "timberbot", "enabled")
	goal, _, _ := unstructured.NestedString(state.Object, "spec", "timberbot", "goal")
	rounds, _, _ := unstructured.NestedInt64(state.Object, "spec", "timberbot", "rounds")
	return types.TimberbotInfo{
		Enabled: enabled,
		Goal:    goal,
		Rounds:  int(rounds),
	}, nil
}

func SetTimberbotSpec(ctx context.Context, info types.TimberbotInfo) error {
	cfg, err := getConfig()
	if err != nil {
		return err
	}
	dc, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("dynamic client: %w", err)
	}

	resource := dc.Resource(dispatchStateGVR).Namespace(types.Namespace)
	state, err := resource.Get(ctx, types.DispatchStateName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get dispatch state: %w", err)
	}

	tb := map[string]any{
		"enabled": info.Enabled,
		"goal":    info.Goal,
		"rounds":  int64(info.Rounds),
	}
	if err := unstructured.SetNestedField(state.Object, tb, "spec", "timberbot"); err != nil {
		return fmt.Errorf("set timberbot spec: %w", err)
	}

	if _, err := resource.Update(ctx, state, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update dispatch state: %w", err)
	}
	return nil
}

func nestedTime(entry map[string]any, key string) (*time.Time, bool) {
	raw, found, err := unstructured.NestedString(entry, key)
	if err != nil || !found || raw == "" {
		return nil, false
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, false
	}
	return &parsed, true
}

func GetReviewLeases(ctx context.Context) ([]types.ReviewReservation, error) {
	cfg, err := getConfig()
	if err != nil {
		return nil, err
	}
	dc, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("dynamic client: %w", err)
	}

	list, err := dc.Resource(reviewLeaseGVR).Namespace(types.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list review leases: %w", err)
	}

	var result []types.ReviewReservation
	for _, item := range list.Items {
		spec := item.Object["spec"]
		specMap, ok := spec.(map[string]any)
		if !ok {
			continue
		}

		repoName, _, _ := unstructured.NestedString(specMap, "repoName")
		prNumber, _, _ := unstructured.NestedInt64(specMap, "prNumber")
		prURL, _, _ := unstructured.NestedString(specMap, "prUrl")
		branch, _, _ := unstructured.NestedString(specMap, "branch")
		issueNumber, _, _ := unstructured.NestedInt64(specMap, "issueNumber")
		family, _, _ := unstructured.NestedString(specMap, "family")
		workerID, _, _ := unstructured.NestedString(specMap, "workerId")
		workerKind, _, _ := unstructured.NestedString(specMap, "workerKind")
		reservedAt, _ := nestedTime(specMap, "reservedAt")
		expiresAt, _ := nestedTime(specMap, "expiresAt")

		result = append(result, types.ReviewReservation{
			Name:       item.GetName(),
			Repo:       types.RepoByName(repoName),
			PRNumber:   int(prNumber),
			PRURL:      prURL,
			Branch:     branch,
			Issue:      int(issueNumber),
			Family:     types.AgentFamily(family),
			WorkerID:   workerID,
			WorkerKind: workerKind,
			ReservedAt: reservedAt,
			ExpiresAt:  expiresAt,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		ti, tj := result[i].ReservedAt, result[j].ReservedAt
		if ti == nil && tj == nil {
			return result[i].PRNumber < result[j].PRNumber
		}
		if ti == nil {
			return false
		}
		if tj == nil {
			return true
		}
		return ti.Before(*tj)
	})
	return result, nil
}

func CreateReviewLease(ctx context.Context, lease types.ReviewReservation) error {
	cfg, err := getConfig()
	if err != nil {
		return err
	}
	dc, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("dynamic client: %w", err)
	}

	spec := map[string]any{
		"repo":       lease.Repo.Owner + "/" + lease.Repo.Name,
		"repoName":   lease.Repo.Name,
		"prNumber":   int64(lease.PRNumber),
		"prUrl":      lease.PRURL,
		"branch":     lease.Branch,
		"family":     string(lease.Family),
		"workerId":   lease.WorkerID,
		"workerKind": lease.WorkerKind,
	}
	if lease.Issue > 0 {
		spec["issueNumber"] = int64(lease.Issue)
	}
	if lease.ReservedAt != nil {
		spec["reservedAt"] = lease.ReservedAt.UTC().Format(time.RFC3339)
	}
	if lease.ExpiresAt != nil {
		spec["expiresAt"] = lease.ExpiresAt.UTC().Format(time.RFC3339)
	}

	obj := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "k3sc.abix.dev/v1",
			"kind":       "ReviewLease",
			"metadata": map[string]any{
				"name":      types.ReviewLeaseName(lease.Repo, lease.PRNumber),
				"namespace": types.Namespace,
			},
			"spec": spec,
			"status": map[string]any{
				"phase": "Reserved",
			},
		},
	}

	_, err = dc.Resource(reviewLeaseGVR).Namespace(types.Namespace).Create(ctx, obj, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("create review lease: %w", err)
	}
	return nil
}

func DeleteReviewLease(ctx context.Context, repo types.Repo, prNumber int) (bool, error) {
	cfg, err := getConfig()
	if err != nil {
		return false, err
	}
	dc, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return false, fmt.Errorf("dynamic client: %w", err)
	}

	name := types.ReviewLeaseName(repo, prNumber)
	err = dc.Resource(reviewLeaseGVR).Namespace(types.Namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("delete review lease: %w", err)
	}
	return true, nil
}

func GetActiveSlots(ctx context.Context, cs *kubernetes.Clientset) ([]int, error) {
	jobs, err := cs.BatchV1().Jobs(types.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app=claude-agent",
	})
	if err != nil {
		return nil, err
	}

	var slots []int
	for _, j := range jobs.Items {
		if j.Status.Active > 0 {
			if s, err := strconv.Atoi(j.Labels["agent-slot"]); err == nil {
				slots = append(slots, s)
			}
		}
	}
	return slots, nil
}

func podEventTime(pod types.AgentPod) time.Time {
	if pod.Finished != nil {
		return *pod.Finished
	}
	if pod.Started != nil {
		return *pod.Started
	}
	return time.Time{}
}

func podFailedWithinLookback(pod types.AgentPod, now time.Time, lookback time.Duration) bool {
	if pod.Phase != types.PhaseFailed {
		return false
	}
	eventTime := podEventTime(pod)
	return !eventTime.IsZero() && eventTime.After(now.Add(-lookback))
}

func ParseUsageLimitResetTime(now time.Time, log string) (time.Time, bool) {
	match := usageLimitResetPattern.FindStringSubmatch(log)
	if len(match) != 3 {
		return time.Time{}, false
	}

	timePart := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(match[1])), " ", "")
	zoneName := strings.ToUpper(strings.TrimSpace(match[2]))
	loc, err := time.LoadLocation(zoneName)
	if err != nil {
		if zoneName != "UTC" {
			return time.Time{}, false
		}
		loc = time.UTC
	}

	base := now.In(loc)
	candidateDate := base.Format("2006-01-02")
	candidate := fmt.Sprintf("%s %s", candidateDate, timePart)

	var parsed time.Time
	for _, layout := range []string{"2006-01-02 3pm", "2006-01-02 3:04pm"} {
		parsed, err = time.ParseInLocation(layout, candidate, loc)
		if err == nil {
			break
		}
	}
	if err != nil {
		return time.Time{}, false
	}
	if !parsed.After(base) {
		parsed = parsed.Add(24 * time.Hour)
	}
	return parsed.UTC(), true
}

func FindRecentUsageLimitPodFromLogs(now time.Time, lookback time.Duration, pods []types.AgentPod, logs map[string]string) *types.AgentPod {
	grouped := FindRecentUsageLimitPodsFromLogs(now, lookback, pods, logs)
	var matches []types.AgentPod
	for _, pod := range grouped {
		matches = append(matches, pod)
	}
	if len(matches) == 0 {
		return nil
	}

	sort.Slice(matches, func(i, j int) bool {
		return podEventTime(matches[i]).After(podEventTime(matches[j]))
	})
	pod := matches[0]
	return &pod
}

func FindRecentUsageLimitPodsFromLogs(now time.Time, lookback time.Duration, pods []types.AgentPod, logs map[string]string) map[types.AgentFamily]types.AgentPod {
	matches := map[types.AgentFamily]types.AgentPod{}
	for _, pod := range pods {
		if !podFailedWithinLookback(pod, now, lookback) {
			continue
		}
		if !strings.Contains(logs[pod.Name], UsageLimitMessage) {
			continue
		}

		family := pod.Family
		if family == "" {
			family = types.FamilyClaude
		}

		current, ok := matches[family]
		if !ok || podEventTime(pod).After(podEventTime(current)) {
			matches[family] = pod
		}
	}
	return matches
}

func FindRecentUsageLimitPod(ctx context.Context, cs *kubernetes.Clientset, lookback time.Duration) (*types.AgentPod, string, error) {
	grouped, logs, err := FindRecentUsageLimitPods(ctx, cs, lookback)
	if err != nil {
		return nil, "", err
	}

	var matches []types.AgentPod
	for _, pod := range grouped {
		matches = append(matches, pod)
	}
	if len(matches) == 0 {
		return nil, "", nil
	}

	sort.Slice(matches, func(i, j int) bool {
		return podEventTime(matches[i]).After(podEventTime(matches[j]))
	})
	pod := matches[0]
	return &pod, logs[pod.Name], nil
}

func FindRecentUsageLimitPods(ctx context.Context, cs *kubernetes.Clientset, lookback time.Duration) (map[types.AgentFamily]types.AgentPod, map[string]string, error) {
	pods, err := GetAgentPods(ctx, cs)
	if err != nil {
		return nil, nil, err
	}

	now := time.Now()
	logs := map[string]string{}
	for _, pod := range pods {
		if !podFailedWithinLookback(pod, now, lookback) {
			continue
		}
		lines, err := GetPodLogLines(ctx, cs, pod.Name, 40)
		if err != nil {
			return nil, nil, err
		}
		logs[pod.Name] = strings.Join(lines, "\n")
	}

	return FindRecentUsageLimitPodsFromLogs(now, lookback, pods, logs), logs, nil
}

func GetPodLogTail(ctx context.Context, cs *kubernetes.Clientset, podName string, lines int64) (string, error) {
	req := cs.CoreV1().Pods(types.Namespace).GetLogs(podName, &corev1.PodLogOptions{
		TailLines: &lines,
	})
	stream, err := req.Stream(ctx)
	if err != nil {
		return "", err
	}
	defer stream.Close()

	buf, err := io.ReadAll(stream)
	if err != nil {
		return "", err
	}

	// find last meaningful line
	var last string
	for _, line := range strings.Split(string(buf), "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "[entrypoint]") || strings.HasPrefix(t, "[tool]") || strings.HasPrefix(t, "[result]") || strings.HasSuffix(t, "/10") {
			continue
		}
		last = t
	}
	return last, nil
}

func GetPodLogLines(ctx context.Context, cs *kubernetes.Clientset, podName string, n int64) ([]string, error) {
	req := cs.CoreV1().Pods(types.Namespace).GetLogs(podName, &corev1.PodLogOptions{
		TailLines: &n,
	})
	stream, err := req.Stream(ctx)
	if err != nil {
		return nil, err
	}
	defer stream.Close()

	buf, err := io.ReadAll(stream)
	if err != nil {
		return nil, err
	}

	var lines []string
	for _, line := range strings.Split(string(buf), "\n") {
		t := strings.TrimSpace(line)
		if t != "" {
			lines = append(lines, t)
		}
	}
	return lines, nil
}

func GetFullLog(ctx context.Context, cs *kubernetes.Clientset, podName string) (string, error) {
	req := cs.CoreV1().Pods(types.Namespace).GetLogs(podName, &corev1.PodLogOptions{})
	stream, err := req.Stream(ctx)
	if err != nil {
		return "", err
	}
	defer stream.Close()

	buf, err := io.ReadAll(stream)
	return string(buf), err
}

func FollowLog(ctx context.Context, cs *kubernetes.Clientset, podName string) error {
	req := cs.CoreV1().Pods(types.Namespace).GetLogs(podName, &corev1.PodLogOptions{
		Follow: true,
	})
	stream, err := req.Stream(ctx)
	if err != nil {
		return err
	}
	defer stream.Close()

	_, err = io.Copy(os.Stdout, stream)
	return err
}

func FindPodForIssue(ctx context.Context, cs *kubernetes.Clientset, issue int) (string, error) {
	pods, err := cs.CoreV1().Pods(types.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("issue-number=%d", issue),
	})
	if err != nil {
		return "", err
	}
	if len(pods.Items) == 0 {
		return "", nil
	}

	// most recent pod
	sort.Slice(pods.Items, func(i, j int) bool {
		return pods.Items[i].CreationTimestamp.Before(&pods.Items[j].CreationTimestamp)
	})
	return pods.Items[len(pods.Items)-1].Name, nil
}

// applyTemplateSubstitutions replaces all __PLACEHOLDER__ tokens in the template.
// Extracted as a standalone function so it can be unit-tested without a k8s client.
func applyTemplateSubstitutions(tmpl string, issue, slot int, repoURL, family, jobKind string, prNumber int) string {
	repoName := repoURL
	if idx := strings.LastIndex(repoName, "/"); idx >= 0 {
		repoName = repoName[idx+1:]
	}
	repoName = strings.TrimSuffix(repoName, ".git")

	if jobKind == "" {
		jobKind = "issue"
	}

	m := strings.ReplaceAll(tmpl, "__ISSUE_NUMBER__", strconv.Itoa(issue))
	m = strings.ReplaceAll(m, "__AGENT_SLOT__", strconv.Itoa(slot))
	m = strings.ReplaceAll(m, "__SLOT_LETTER__", types.SlotLetter(slot))
	m = strings.ReplaceAll(m, "__REPO_URL__", repoURL)
	m = strings.ReplaceAll(m, "__REPO_NAME__", repoName)
	m = strings.ReplaceAll(m, "__AGENT_FAMILY__", family)
	m = strings.ReplaceAll(m, "__JOB_KIND__", jobKind)
	m = strings.ReplaceAll(m, "__PR_NUMBER__", strconv.Itoa(prNumber))
	return m
}

func CreateJobFromTemplate(ctx context.Context, cs *kubernetes.Clientset, template string, issue, slot int, repoURL, family, jobKind string, prNumber int) (string, error) {
	timestamp := time.Now().Unix()
	manifest := applyTemplateSubstitutions(template, issue, slot, repoURL, family, jobKind, prNumber)
	manifest = strings.Replace(manifest,
		fmt.Sprintf(`name: "%s-issue-%d"`, family, issue),
		fmt.Sprintf(`name: "%s-issue-%d-%d"`, family, issue, timestamp),
		1,
	)

	var job batchv1.Job
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader([]byte(manifest)), 4096)
	if err := decoder.Decode(&job); err != nil {
		return "", fmt.Errorf("decode job manifest: %w", err)
	}

	created, err := cs.BatchV1().Jobs(types.Namespace).Create(ctx, &job, metav1.CreateOptions{})
	if err != nil {
		return "", err
	}
	return created.Name, nil
}

func CreateTimberbotJob(ctx context.Context, cs *kubernetes.Clientset, template string, slot int, family, goal string, rounds int) (string, error) {
	timestamp := time.Now().Unix()

	m := strings.ReplaceAll(template, "__AGENT_SLOT__", strconv.Itoa(slot))
	m = strings.ReplaceAll(m, "__SLOT_LETTER__", types.SlotLetter(slot))
	m = strings.ReplaceAll(m, "__AGENT_FAMILY__", family)
	m = strings.ReplaceAll(m, "__TIMBERBOT_GOAL__", goal)
	m = strings.ReplaceAll(m, "__TIMBERBOT_ROUNDS__", strconv.Itoa(rounds))

	// add timestamp to job name for uniqueness
	m = strings.Replace(m,
		fmt.Sprintf(`name: "timberbot-%s"`, types.SlotLetter(slot)),
		fmt.Sprintf(`name: "timberbot-%s-%d"`, types.SlotLetter(slot), timestamp),
		1,
	)

	var job batchv1.Job
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader([]byte(m)), 4096)
	if err := decoder.Decode(&job); err != nil {
		return "", fmt.Errorf("decode timberbot job manifest: %w", err)
	}

	created, err := cs.BatchV1().Jobs(types.Namespace).Create(ctx, &job, metav1.CreateOptions{})
	if err != nil {
		return "", err
	}
	return created.Name, nil
}

// GetActiveTimberbotJobs returns active (non-terminal) timberbot jobs.
func GetActiveTimberbotJobs(ctx context.Context, cs *kubernetes.Clientset) ([]batchv1.Job, error) {
	jobs, err := cs.BatchV1().Jobs(types.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "job-kind=timberbot",
	})
	if err != nil {
		return nil, err
	}

	var active []batchv1.Job
	for _, j := range jobs.Items {
		if j.Status.Succeeded > 0 || j.Status.Failed > 0 {
			continue
		}
		dead := false
		for _, c := range j.Status.Conditions {
			if c.Type == batchv1.JobFailed && c.Status == "True" {
				dead = true
				break
			}
		}
		if dead {
			continue
		}
		active = append(active, j)
	}
	return active, nil
}

func GetOperatorLog(ctx context.Context, cs *kubernetes.Clientset) (string, error) {
	pods, err := cs.CoreV1().Pods(types.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app=k3sc-operator",
	})
	if err != nil {
		return "", err
	}
	if len(pods.Items) == 0 {
		return "(no operator pod found)", nil
	}

	// get the running operator pod
	var latest *corev1.Pod
	for i := range pods.Items {
		if pods.Items[i].Status.Phase == corev1.PodRunning {
			latest = &pods.Items[i]
		}
	}
	if latest == nil {
		latest = &pods.Items[0]
	}

	var lines int64 = 30
	req := cs.CoreV1().Pods(types.Namespace).GetLogs(latest.Name, &corev1.PodLogOptions{
		TailLines: &lines,
	})
	stream, err := req.Stream(ctx)
	if err != nil {
		return "", err
	}
	defer stream.Close()

	buf, err := io.ReadAll(stream)
	return string(buf), err
}

func GetNodeInfo(ctx context.Context, cs *kubernetes.Clientset) (name, version string, err error) {
	nodes, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", "", err
	}
	if len(nodes.Items) == 0 {
		return "unknown", "unknown", nil
	}
	n := nodes.Items[0]
	return n.Name, n.Status.NodeInfo.KubeletVersion, nil
}
