package cmd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/abix-/k3sc/internal/format"
	"github.com/abix-/k3sc/internal/github"
	"github.com/abix-/k3sc/internal/k8s"
	"github.com/abix-/k3sc/internal/tui"
	"github.com/abix-/k3sc/internal/types"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

// ringBuffer captures the last N lines written to it.
type ringBuffer struct {
	mu    sync.Mutex
	lines []string
	max   int
	buf   bytes.Buffer
}

func (r *ringBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf.Write(p)
	for {
		line, err := r.buf.ReadString('\n')
		if err != nil {
			// incomplete line, put it back
			r.buf.WriteString(line)
			break
		}
		line = strings.TrimRight(line, "\r\n")
		if line != "" {
			r.lines = append(r.lines, line)
			if len(r.lines) > r.max {
				r.lines = r.lines[1:]
			}
		}
	}
	return len(p), nil
}

func (r *ringBuffer) Lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.lines))
	copy(out, r.lines)
	return out
}

var once bool

func init() {
	topCmd.Flags().BoolVar(&once, "once", false, "Print once and exit (no TUI)")
	rootCmd.AddCommand(topCmd)
}

var topCmd = &cobra.Command{
	Use:   "top",
	Short: "Dashboard of agent pods, GitHub issues, and cluster health",
	RunE:  runTop,
}

type dashboard struct {
	nodeName    string
	nodeVersion string
	dispatch    types.DispatchStateInfo
	pods        []types.AgentPod
	tasks       []types.TaskInfo
	issues      []types.Issue
	prs         []types.PullRequest
	operatorLog string
	timberbot   types.TimberbotInfo
}

func gather(cs *kubernetes.Clientset) (*dashboard, error) {
	ctx := context.Background()

	var (
		nodeName, nodeVersion string
		dispatchState         types.DispatchStateInfo
		pods                  []types.AgentPod
		tasks                 []types.TaskInfo
		issues                []types.Issue
		prs                   []types.PullRequest
		dispLog               string
		timberbot             types.TimberbotInfo
		mu                    sync.Mutex
		wg                    sync.WaitGroup
		errs                  []error
	)

	wg.Add(8)
	go func() {
		defer wg.Done()
		n, v, e := k8s.GetNodeInfo(ctx, cs)
		mu.Lock()
		nodeName, nodeVersion = n, v
		if e != nil {
			errs = append(errs, e)
		}
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		s, e := k8s.GetDispatchState(ctx)
		mu.Lock()
		dispatchState = s
		if e != nil {
			errs = append(errs, e)
		}
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		p, e := k8s.GetAgentPods(ctx, cs)
		mu.Lock()
		pods = p
		if e != nil {
			errs = append(errs, e)
		}
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		t, e := k8s.GetAgentJobs(ctx)
		mu.Lock()
		tasks = t
		if e != nil {
			errs = append(errs, e)
		}
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		i, e := github.GetWorkflowIssues(ctx)
		mu.Lock()
		issues = i
		if e != nil {
			errs = append(errs, e)
		}
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		p, e := github.GetOpenPRs(ctx)
		mu.Lock()
		prs = p
		if e != nil {
			errs = append(errs, e)
		}
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		d, e := k8s.GetOperatorLog(ctx, cs)
		mu.Lock()
		dispLog = d
		if e != nil {
			errs = append(errs, e)
		}
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		t, e := k8s.GetTimberbotSpec(ctx)
		mu.Lock()
		timberbot = t
		if e != nil {
			errs = append(errs, e)
		}
		mu.Unlock()
	}()
	wg.Wait()

	return &dashboard{
		nodeName:    nodeName,
		nodeVersion: nodeVersion,
		dispatch:    dispatchState,
		pods:        pods,
		tasks:       tasks,
		issues:      issues,
		prs:         prs,
		operatorLog: dispLog,
		timberbot:   timberbot,
	}, nil
}

func hydratePodLogTails(ctx context.Context, cs *kubernetes.Clientset, pods []types.AgentPod) {
	var wg sync.WaitGroup
	for i := range pods {
		if pods[i].Phase != types.PhaseRunning && pods[i].Phase != types.PhasePending {
			continue
		}
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			tail, _ := k8s.GetPodLogTail(ctx, cs, pods[idx].Name, 20)
			pods[idx].LogTail = tail
		}(i)
	}
	wg.Wait()
}

func applyLiveLogTails(pods []types.AgentPod, liveLogs []tui.LiveLog) {
	tailsByPod := make(map[string]string, len(liveLogs))
	for _, log := range liveLogs {
		tailsByPod[log.PodName] = log.Tail
	}
	for i := range pods {
		if tail, ok := tailsByPod[pods[i].Name]; ok {
			pods[i].LogTail = tail
		} else if pods[i].Phase == types.PhaseRunning {
			pods[i].LogTail = ""
		}
	}
}

func mergeTaskRuntime(tasks []types.TaskInfo, pods []types.AgentPod) []types.TaskInfo {
	if len(tasks) == 0 {
		return nil
	}

	latestByKey := make(map[string]types.AgentPod, len(pods))
	for _, pod := range pods {
		key := taskPodKey(pod.Repo, pod.Issue, types.AgentName(pod.Family, pod.Slot))
		if current, ok := latestByKey[key]; !ok || newerPod(pod, current) {
			latestByKey[key] = pod
		}
	}

	merged := make([]types.TaskInfo, len(tasks))
	copy(merged, tasks)
	for i := range merged {
		if pod, ok := latestByKey[taskPodKey(merged[i].Repo, merged[i].Issue, merged[i].Agent)]; ok {
			merged[i].RuntimePhase = pod.Phase
			merged[i].LogTail = pod.LogTail
		}
	}

	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].PhaseOrder() != merged[j].PhaseOrder() {
			return merged[i].PhaseOrder() < merged[j].PhaseOrder()
		}
		ti, tj := taskEventTime(merged[i]), taskEventTime(merged[j])
		switch {
		case ti.Equal(tj):
			return merged[i].Issue > merged[j].Issue
		case ti.IsZero():
			return false
		case tj.IsZero():
			return true
		default:
			return ti.After(tj)
		}
	})

	return merged
}

func taskPodKey(repo types.Repo, issue int, agent string) string {
	return strings.ToLower(fmt.Sprintf("%s/%s#%d:%s", repo.Owner, repo.Name, issue, agent))
}

func newerPod(a, b types.AgentPod) bool {
	ta, tb := podEventTime(a), podEventTime(b)
	switch {
	case ta.Equal(tb):
		return a.Name > b.Name
	case ta.IsZero():
		return false
	case tb.IsZero():
		return true
	default:
		return ta.After(tb)
	}
}

func podEventTime(p types.AgentPod) time.Time {
	if p.Finished != nil {
		return *p.Finished
	}
	if p.Started != nil {
		return *p.Started
	}
	return time.Time{}
}

func taskEventTime(t types.TaskInfo) time.Time {
	if t.Started != nil {
		return *t.Started
	}
	if t.Finished != nil {
		return *t.Finished
	}
	return time.Time{}
}

func countTaskPhases(tasks []types.TaskInfo) (running, done, failed, blocked int) {
	for _, t := range tasks {
		switch t.Phase {
		case "Running", "Pending":
			running++
		case "Succeeded":
			done++
		case "Failed":
			failed++
		case "Blocked":
			blocked++
		}
	}
	return running, done, failed, blocked
}

func runtimeLabel(phase types.PodPhase) string {
	if phase == "" {
		return "-"
	}
	return phase.Display()
}

func printDashboard(d *dashboard) {
	fmt.Println("=== CLUSTER ===")
	fmt.Printf("Node: %s Ready %s\n\n", d.nodeName, d.nodeVersion)

	// 1. Operator
	fmt.Println("=== QUOTA ===")
	if len(d.dispatch.FamilyStatuses) == 0 {
		fmt.Println("  (quota unknown)")
	} else {
		for _, status := range d.dispatch.FamilyStatuses {
			label := "unknown"
			if status.Checked && status.Available {
				label = "ok"
			} else if !status.Available {
				label = "blocked"
			}
			reason := status.Reason
			if reason != "" {
				fmt.Printf("  %-6s %-8s %s\n", status.Family, label, reason)
			} else {
				fmt.Printf("  %-6s %s\n", status.Family, label)
			}
		}
	}
	fmt.Println()

	// timberbot
	if d.timberbot.Enabled {
		fmt.Println("=== TIMBERBOT ===")
		fmt.Printf("  status: enabled\n")
		fmt.Printf("  goal: %s\n", d.timberbot.Goal)
		fmt.Printf("  rounds: %d\n", d.timberbot.Rounds)
		var tbPod *types.AgentPod
		for i := range d.pods {
			if d.pods[i].JobKind == "timberbot" && (d.pods[i].Phase == types.PhaseRunning || d.pods[i].Phase == types.PhasePending) {
				tbPod = &d.pods[i]
				break
			}
		}
		if tbPod != nil {
			agent := types.AgentName(tbPod.Family, tbPod.Slot)
			fmt.Printf("  agent: %s  pod: %s  uptime: %s\n", agent, tbPod.Name, format.FmtDuration(tbPod.Started, nil))
		} else {
			fmt.Printf("  agent: (waiting for dispatch)\n")
		}
		fmt.Println()
	}

	// 2. Local review
	fmt.Println("=== LOCAL REVIEW ===")
	if len(d.dispatch.ReviewReservations) == 0 {
		fmt.Println("  (no local PR reservations)")
	} else {
		fmt.Printf("%-12s %-12s %-7s %-8s %-16s %-16s Branch\n", "Worker", "Repo", "PR", "Issue", "Reserved", "Expires")
		for _, res := range d.dispatch.ReviewReservations {
			issue := ""
			if res.Issue > 0 {
				issue = fmt.Sprintf("#%d", res.Issue)
			}
			fmt.Printf("%-12s %-12s %-7s %-8s %-16s %-16s %s\n",
				res.WorkerID,
				res.Repo.Name,
				format.PRLink(res.Repo, res.PRNumber),
				issue,
				format.FmtTime(res.ReservedAt),
				format.FmtTime(res.ExpiresAt),
				res.Branch)
		}
	}
	fmt.Println()

	// 3. Operator
	fmt.Println("=== OPERATOR ===")
	if d.operatorLog == "" {
		fmt.Println("  (no operator logs)")
	} else {
		for _, line := range strings.Split(strings.TrimSpace(d.operatorLog), "\n") {
			if line != "" {
				fmt.Printf("  %s\n", line)
			}
		}
	}
	fmt.Println()

	// 4. Issues
	fmt.Println("=== GITHUB ISSUES ===")
	if len(d.issues) == 0 {
		fmt.Println("  (no issues with workflow labels)")
	} else {
		fmt.Printf("%-7s %-12s %-14s %-10s Title\n", "Issue", "Repo", "State", "Owner")
		for _, i := range d.issues {
			fmt.Printf("%s %-12s %-14s %-10s %s\n", format.IssueLink(i.Repo, i.Number), i.Repo.Name, i.State, i.Owner, i.Title)
		}
	}
	fmt.Println()

	// 5. Operator Tasks
	running, done, failed, blocked := countTaskPhases(d.tasks)
	fmt.Printf("=== OPERATOR TASKS (%d running, %d done, %d failed, %d blocked) ===\n", running, done, failed, blocked)
	if len(d.tasks) == 0 {
		fmt.Println("  (no operator tasks)")
	} else {
		fmt.Printf("%-7s %-10s %-10s %-10s %-10s %-16s %-10s %-13s %s\n", "Issue", "Repo", "Agent", "Task", "Runtime", "Started", "Duration", "Next", "Last Output")
		for _, task := range d.tasks {
			fmt.Printf("%s %-10s %-10s %-10s %-10s %-16s %-10s %-13s %s\n",
				format.IssueLink(task.Repo, task.Issue),
				task.Repo.Name,
				task.Agent,
				task.Phase,
				runtimeLabel(task.RuntimePhase),
				format.FmtTime(task.Started),
				format.FmtDuration(task.Started, task.Finished),
				task.NextAction,
				task.LogTail)
		}
	}
	fmt.Println()

	fmt.Println("=== PULL REQUESTS ===")
	if len(d.prs) == 0 {
		fmt.Println("  (no open pull requests)")
	} else {
		fmt.Printf("%-7s %-12s %-12s %-20s Title\n", "PR", "Repo", "Owner", "Branch")
		for _, pr := range d.prs {
			fmt.Printf("%s %-12s %-12s %-20s %s\n", format.PRLink(pr.Repo, pr.Number), pr.Repo.Name, pr.Owner, pr.Branch, pr.Title)
		}
	}
	fmt.Println()
}

func runTop(cmd *cobra.Command, args []string) error {
	cs, err := k8s.NewClient()
	if err != nil {
		return err
	}

	if once {
		d, err := gather(cs)
		if err != nil {
			return err
		}
		hydratePodLogTails(context.Background(), cs, d.pods)
		d.tasks = mergeTaskRuntime(d.tasks, d.pods)
		printDashboard(d)
		return nil
	}

	// capture klog output to ring buffer instead of stderr
	logBuf := &ringBuffer{max: 50}
	klog.SetOutput(logBuf)
	klog.LogToStderr(false)

	// TUI mode
	streamer := tui.NewLogStreamer(cs, types.Namespace)
	defer streamer.Stop()

	gatherFn := func() (*tui.Data, error) {
		d, err := gather(cs)
		if err != nil {
			return nil, err
		}
		streamer.Sync(d.pods)
		liveLogs := streamer.Snapshot()
		applyLiveLogTails(d.pods, liveLogs)
		tasks := mergeTaskRuntime(d.tasks, d.pods)
		return &tui.Data{
			NodeName:    d.nodeName,
			NodeVersion: d.nodeVersion,
			Dispatch:    d.dispatch,
			Pods:        d.pods,
			Tasks:       tasks,
			Issues:      d.issues,
			PRs:         d.prs,
			OperatorLog: d.operatorLog,
			LiveLogs:    liveLogs,
			Timberbot:   d.timberbot,
		}, nil
	}

	k8sGatherFn := func(current *tui.Data) (*tui.Data, error) {
		ctx := context.Background()

		var (
			dispatch  types.DispatchStateInfo
			pods      []types.AgentPod
			dispLog   string
			tbInfo    types.TimberbotInfo
			wg2       sync.WaitGroup
			mu2       sync.Mutex
		)
		wg2.Add(4)
		go func() {
			defer wg2.Done()
			s, _ := k8s.GetDispatchState(ctx)
			mu2.Lock()
			dispatch = s
			mu2.Unlock()
		}()
		go func() {
			defer wg2.Done()
			p, _ := k8s.GetAgentPods(ctx, cs)
			mu2.Lock()
			pods = p
			mu2.Unlock()
		}()
		go func() {
			defer wg2.Done()
			d, _ := k8s.GetOperatorLog(ctx, cs)
			mu2.Lock()
			dispLog = d
			mu2.Unlock()
		}()
		go func() {
			defer wg2.Done()
			t, _ := k8s.GetTimberbotSpec(ctx)
			mu2.Lock()
			tbInfo = t
			mu2.Unlock()
		}()
		wg2.Wait()

		// sync streaming logs
		streamer.Sync(pods)
		liveLogs := streamer.Snapshot()
		applyLiveLogTails(pods, liveLogs)
		tasks, _ := k8s.GetAgentJobs(ctx)
		tasks = mergeTaskRuntime(tasks, pods)

		return &tui.Data{
			NodeName:    current.NodeName,
			NodeVersion: current.NodeVersion,
			Dispatch:    dispatch,
			Pods:        pods,
			Tasks:       tasks,
			Issues:      current.Issues,
			PRs:         current.PRs,
			OperatorLog: dispLog,
			LiveLogs:    liveLogs,
			Timberbot:   tbInfo,
		}, nil
	}

	dispatchFn := func() (string, error) {
		return k8s.TriggerDispatch(context.Background())
	}

	maxSlots := 5
	setMaxSlots := func(n int) {
		maxSlots = n
		os.Setenv("MAX_SLOTS", fmt.Sprintf("%d", n))
	}

	m := tui.NewModel(gatherFn, k8sGatherFn, dispatchFn, maxSlots, setMaxSlots, logBuf.Lines)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err = p.Run()
	return err
}
