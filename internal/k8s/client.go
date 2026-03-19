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

	"encoding/json"

	"github.com/abix-/k3sc/internal/types"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	DispatcherCronJobName              = "claude-dispatcher"
	DispatcherDefaultSchedule          = "*/3 * * * *"
	DispatcherHourlySchedule           = "0 * * * *"
	DispatcherNormalScheduleAnnotation = "k3sc.abix.dev/normal-schedule"
	DispatcherUsageResetAtAnnotation   = "k3sc.abix.dev/usage-reset-at"
	UsageLimitMessage                  = "You're out of extra usage"
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

// agentJobList is used for JSON deserialization of AgentJob CRs.
type agentJobList struct {
	Items []struct {
		Metadata struct {
			Name              string `json:"name"`
			CreationTimestamp string `json:"creationTimestamp"`
		} `json:"metadata"`
		Spec struct {
			Repo        string `json:"repo"`
			RepoName    string `json:"repoName"`
			IssueNumber int    `json:"issueNumber"`
		} `json:"spec"`
		Status struct {
			Phase      string `json:"phase"`
			Agent      string `json:"agent"`
			Slot       int    `json:"slot"`
			NextAction string `json:"nextAction"`
			StartedAt  string `json:"startedAt"`
			FinishedAt string `json:"finishedAt"`
		} `json:"status"`
	} `json:"items"`
}

// GetAgentJobs fetches AgentJob CRs and returns TUI-friendly TaskInfo structs.
func GetAgentJobs(ctx context.Context) ([]types.TaskInfo, error) {
	config, err := getConfig()
	if err != nil {
		return nil, err
	}
	config.APIPath = "/apis"
	config.GroupVersion = &schema.GroupVersion{Group: "k3sc.abix.dev", Version: "v1"}
	config.NegotiatedSerializer = nil

	rc, err := rest.UnversionedRESTClientFor(config)
	if err != nil {
		return nil, fmt.Errorf("rest client: %w", err)
	}

	body, err := rc.Get().
		AbsPath("/apis/k3sc.abix.dev/v1/namespaces/" + types.Namespace + "/agentjobs").
		DoRaw(ctx)
	if err != nil {
		return nil, fmt.Errorf("get agentjobs: %w", err)
	}

	var list agentJobList
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("unmarshal agentjobs: %w", err)
	}

	var result []types.TaskInfo
	for _, item := range list.Items {
		t := types.TaskInfo{
			Name:     item.Metadata.Name,
			Repo:     types.RepoByName(item.Spec.RepoName),
			Issue:    item.Spec.IssueNumber,
			Phase:    item.Status.Phase,
			Agent:    item.Status.Agent,
			Slot:     item.Status.Slot,
			NextAction: item.Status.NextAction,
		}
		if item.Status.StartedAt != "" {
			if ts, err := time.Parse(time.RFC3339, item.Status.StartedAt); err == nil {
				t.Started = &ts
			}
		}
		if item.Status.FinishedAt != "" {
			if ts, err := time.Parse(time.RFC3339, item.Status.FinishedAt); err == nil {
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

		result = append(result, types.AgentPod{
			Name:     p.Name,
			Issue:    issue,
			Slot:     slot,
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

// HasJobForIssue returns true if any k8s Job exists for the given issue number,
// regardless of whether it's active, succeeded, or failed.
func HasJobForIssue(ctx context.Context, cs *kubernetes.Clientset, issue int) (bool, error) {
	jobs, err := cs.BatchV1().Jobs(types.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: fmt.Sprintf("app=claude-agent,issue-number=%d", issue),
	})
	if err != nil {
		return false, err
	}
	return len(jobs.Items) > 0, nil
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
	var matches []types.AgentPod
	for _, pod := range pods {
		if !podFailedWithinLookback(pod, now, lookback) {
			continue
		}
		if strings.Contains(logs[pod.Name], UsageLimitMessage) {
			matches = append(matches, pod)
		}
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

func FindRecentUsageLimitPod(ctx context.Context, cs *kubernetes.Clientset, lookback time.Duration) (*types.AgentPod, string, error) {
	pods, err := GetAgentPods(ctx, cs)
	if err != nil {
		return nil, "", err
	}

	now := time.Now()
	logs := map[string]string{}
	for _, pod := range pods {
		if !podFailedWithinLookback(pod, now, lookback) {
			continue
		}
		lines, err := GetPodLogLines(ctx, cs, pod.Name, 40)
		if err != nil {
			return nil, "", err
		}
		logs[pod.Name] = strings.Join(lines, "\n")
	}

	pod := FindRecentUsageLimitPodFromLogs(now, lookback, pods, logs)
	if pod == nil {
		return nil, "", nil
	}
	return pod, logs[pod.Name], nil
}

func CheckAndRestoreDispatcherBackoff(ctx context.Context, cs *kubernetes.Clientset, name string, now time.Time) (bool, bool, string, *time.Time, error) {
	cronjob, err := cs.BatchV1().CronJobs(types.Namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return false, false, "", nil, err
	}
	if cronjob.Annotations == nil {
		return false, false, "", nil, nil
	}

	resetAtRaw := cronjob.Annotations[DispatcherUsageResetAtAnnotation]
	if resetAtRaw == "" {
		return false, false, "", nil, nil
	}
	resetAt, err := time.Parse(time.RFC3339, resetAtRaw)
	if err != nil {
		return false, false, "", nil, err
	}
	if now.Before(resetAt) {
		return true, false, cronjob.Spec.Schedule, &resetAt, nil
	}

	normal := cronjob.Annotations[DispatcherNormalScheduleAnnotation]
	if normal == "" {
		normal = DispatcherDefaultSchedule
	}
	cronjob = cronjob.DeepCopy()
	cronjob.Spec.Schedule = normal
	delete(cronjob.Annotations, DispatcherNormalScheduleAnnotation)
	delete(cronjob.Annotations, DispatcherUsageResetAtAnnotation)
	if len(cronjob.Annotations) == 0 {
		cronjob.Annotations = nil
	}
	if _, err := cs.BatchV1().CronJobs(types.Namespace).Update(ctx, cronjob, metav1.UpdateOptions{}); err != nil {
		return false, false, "", nil, err
	}
	return false, true, normal, &resetAt, nil
}

func SetDispatcherBackoff(ctx context.Context, cs *kubernetes.Clientset, name string, resetAt time.Time) (bool, string, error) {
	cronjob, err := cs.BatchV1().CronJobs(types.Namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return false, "", err
	}
	previous := cronjob.Spec.Schedule
	cronjob = cronjob.DeepCopy()
	if cronjob.Annotations == nil {
		cronjob.Annotations = map[string]string{}
	}

	normal := cronjob.Annotations[DispatcherNormalScheduleAnnotation]
	if normal == "" {
		normal = previous
		if normal == DispatcherHourlySchedule {
			normal = DispatcherDefaultSchedule
		}
	}
	desiredResetAt := resetAt.UTC().Format(time.RFC3339)
	changed := false

	if cronjob.Spec.Schedule != DispatcherHourlySchedule {
		cronjob.Spec.Schedule = DispatcherHourlySchedule
		changed = true
	}
	if cronjob.Annotations[DispatcherNormalScheduleAnnotation] != normal {
		cronjob.Annotations[DispatcherNormalScheduleAnnotation] = normal
		changed = true
	}
	if cronjob.Annotations[DispatcherUsageResetAtAnnotation] != desiredResetAt {
		cronjob.Annotations[DispatcherUsageResetAtAnnotation] = desiredResetAt
		changed = true
	}
	if !changed {
		return false, previous, nil
	}

	if _, err := cs.BatchV1().CronJobs(types.Namespace).Update(ctx, cronjob, metav1.UpdateOptions{}); err != nil {
		return false, previous, err
	}
	return true, previous, nil
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
func applyTemplateSubstitutions(tmpl string, issue, slot int, repoURL string) string {
	repoName := repoURL
	if idx := strings.LastIndex(repoName, "/"); idx >= 0 {
		repoName = repoName[idx+1:]
	}
	repoName = strings.TrimSuffix(repoName, ".git")

	m := strings.ReplaceAll(tmpl, "__ISSUE_NUMBER__", strconv.Itoa(issue))
	m = strings.ReplaceAll(m, "__AGENT_SLOT__", strconv.Itoa(slot))
	m = strings.ReplaceAll(m, "__SLOT_LETTER__", types.SlotLetter(slot))
	m = strings.ReplaceAll(m, "__REPO_URL__", repoURL)
	m = strings.ReplaceAll(m, "__REPO_NAME__", repoName)
	return m
}

func CreateJobFromTemplate(ctx context.Context, cs *kubernetes.Clientset, template string, issue, slot int, repoURL string) (string, error) {
	timestamp := time.Now().Unix()
	manifest := applyTemplateSubstitutions(template, issue, slot, repoURL)
	manifest = strings.Replace(manifest,
		fmt.Sprintf(`name: "claude-issue-%d"`, issue),
		fmt.Sprintf(`name: "claude-issue-%d-%d"`, issue, timestamp),
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
