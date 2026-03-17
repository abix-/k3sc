package k8s

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/abix-/k3s-claude/internal/types"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

func NewClient() (*kubernetes.Clientset, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, nil).ClientConfig()
	if err != nil {
		// try in-cluster
		config, err = clientcmd.BuildConfigFromFlags("", "")
		if err != nil {
			return nil, fmt.Errorf("k8s config: %w", err)
		}
	}
	// increase QPS to avoid client-side throttling with many log requests
	config.QPS = 50
	config.Burst = 100
	return kubernetes.NewForConfig(config)
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

	sort.Slice(result, func(i, j int) bool {
		oi, oj := result[i].Phase.Order(), result[j].Phase.Order()
		if oi != oj {
			return oi < oj
		}
		if result[i].Started == nil || result[j].Started == nil {
			return false
		}
		return result[i].Started.Before(*result[j].Started)
	})

	return result, nil
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

	_, err = io.Copy(io.Discard, stream) // TODO: write to stdout
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

func CreateJobFromTemplate(ctx context.Context, cs *kubernetes.Clientset, template string, issue, slot int, repoURL string) (string, error) {
	timestamp := time.Now().Unix()
	manifest := strings.ReplaceAll(template, "__ISSUE_NUMBER__", strconv.Itoa(issue))
	manifest = strings.ReplaceAll(manifest, "__AGENT_SLOT__", strconv.Itoa(slot))
	manifest = strings.ReplaceAll(manifest, "__REPO_URL__", repoURL)
	// extract repo name from clone URL (e.g. "https://github.com/abix-/endless.git" -> "endless")
	repoName := repoURL
	if idx := strings.LastIndex(repoName, "/"); idx >= 0 {
		repoName = repoName[idx+1:]
	}
	repoName = strings.TrimSuffix(repoName, ".git")
	manifest = strings.ReplaceAll(manifest, "__REPO_NAME__", repoName)
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

func GetDispatcherLog(ctx context.Context, cs *kubernetes.Clientset) (string, error) {
	pods, err := cs.CoreV1().Pods(types.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", err
	}

	// find most recent dispatcher pod
	var dispatchers []corev1.Pod
	for _, p := range pods.Items {
		if strings.Contains(p.Name, "dispatcher") {
			dispatchers = append(dispatchers, p)
		}
	}
	if len(dispatchers) == 0 {
		return "(no dispatcher runs found)", nil
	}

	sort.Slice(dispatchers, func(i, j int) bool {
		return dispatchers[i].CreationTimestamp.Before(&dispatchers[j].CreationTimestamp)
	})

	latest := dispatchers[len(dispatchers)-1]
	req := cs.CoreV1().Pods(types.Namespace).GetLogs(latest.Name, &corev1.PodLogOptions{})
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
