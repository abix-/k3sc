package cmd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

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
	nodeName      string
	nodeVersion   string
	pods          []types.AgentPod
	tasks         []types.TaskInfo
	issues        []types.Issue
	prs           []types.PullRequest
	operatorLog string
}

func gather(cs *kubernetes.Clientset) (*dashboard, error) {
	ctx := context.Background()

	var (
		nodeName, nodeVersion string
		pods                  []types.AgentPod
		tasks                 []types.TaskInfo
		issues                []types.Issue
		prs                   []types.PullRequest
		dispLog               string
		mu                    sync.Mutex
		wg                    sync.WaitGroup
		errs                  []error
	)

	wg.Add(6)
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
	wg.Wait()

	// fetch log tails only for running/pending pods
	var lwg sync.WaitGroup
	for i := range pods {
		if pods[i].Phase != types.PhaseRunning && pods[i].Phase != types.PhasePending {
			continue
		}
		lwg.Add(1)
		go func(idx int) {
			defer lwg.Done()
			tail, _ := k8s.GetPodLogTail(ctx, cs, pods[idx].Name, 20)
			pods[idx].LogTail = tail
		}(i)
	}
	lwg.Wait()

	return &dashboard{
		nodeName:      nodeName,
		nodeVersion:   nodeVersion,
		pods:          pods,
		tasks:         tasks,
		issues:        issues,
		prs:           prs,
		operatorLog: dispLog,
	}, nil
}

func printDashboard(d *dashboard) {
	fmt.Println("=== CLUSTER ===")
	fmt.Printf("Node: %s Ready %s\n\n", d.nodeName, d.nodeVersion)

	// 1. Operator
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

	// 2. Issues
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

	// 3. Agents
	running, completed, failed := format.CountPhases(d.pods)
	fmt.Printf("=== AGENTS (%d running, %d completed, %d failed) ===\n", running, completed, failed)
	if len(d.pods) == 0 {
		fmt.Println("  (no agent pods)")
	} else {
		fmt.Printf("%-7s %-10s %-11s %-16s %-10s Last Output\n", "Issue", "Agent", "Status", "Started", "Duration")
		for _, pod := range d.pods {
			agent := types.AgentName(pod.Family, pod.Slot)
			fmt.Printf("%s %-10s %-11s %-16s %-10s %s\n",
				format.IssueLink(pod.Repo, pod.Issue), agent, pod.Phase.Display(),
				format.FmtTime(pod.Started), format.FmtDuration(pod.Started, pod.Finished),
				pod.LogTail)
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
		return &tui.Data{
			NodeName:    d.nodeName,
			NodeVersion: d.nodeVersion,
			Pods:        d.pods,
			Tasks:       d.tasks,
			Issues:      d.issues,
			PRs:         d.prs,
			OperatorLog: d.operatorLog,
			LiveLogs:    streamer.Snapshot(),
		}, nil
	}

	k8sGatherFn := func(current *tui.Data) (*tui.Data, error) {
		ctx := context.Background()

		var (
			pods    []types.AgentPod
			dispLog string
			wg2     sync.WaitGroup
			mu2     sync.Mutex
		)
		wg2.Add(2)
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
		wg2.Wait()

		// fetch log tails for dashboard display
		var lwg sync.WaitGroup
		for i := range pods {
			if pods[i].Phase != types.PhaseRunning && pods[i].Phase != types.PhasePending {
				continue
			}
			lwg.Add(1)
			go func(idx int) {
				defer lwg.Done()
				tail, _ := k8s.GetPodLogTail(ctx, cs, pods[idx].Name, 20)
				pods[idx].LogTail = tail
			}(i)
		}
		lwg.Wait()

		// sync streaming logs
		streamer.Sync(pods)
		tasks, _ := k8s.GetAgentJobs(ctx)

		return &tui.Data{
			NodeName:    current.NodeName,
			NodeVersion: current.NodeVersion,
			Pods:        pods,
			Tasks:       tasks,
			Issues:      current.Issues,
			PRs:         current.PRs,
			OperatorLog: dispLog,
			LiveLogs:    streamer.Snapshot(),
		}, nil
	}

	dispatchFn := func() (string, error) {
		return RunDispatch()
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
