package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/abix-/k3s-claude/internal/github"
	"github.com/abix-/k3s-claude/internal/k8s"
	"github.com/abix-/k3s-claude/internal/tui"
	"github.com/abix-/k3s-claude/internal/types"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
)

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

var loc *time.Location

func init() {
	loc, _ = time.LoadLocation("America/New_York")
}

func fmtTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.In(loc).Format("3:04 PM MST")
}

func fmtDuration(start *time.Time, end *time.Time) string {
	if start == nil {
		return ""
	}
	e := time.Now()
	if end != nil {
		e = *end
	}
	d := e.Sub(*start)
	mins := int(d.Minutes())
	secs := int(d.Seconds()) % 60
	return fmt.Sprintf("%dm %02ds", mins, secs)
}

type dashboard struct {
	nodeName      string
	nodeVersion   string
	pods          []types.AgentPod
	issues        []types.Issue
	prs           []types.PullRequest
	dispatcherLog string
}

func gather() (*dashboard, error) {
	ctx := context.Background()
	cs, err := k8s.NewClient()
	if err != nil {
		return nil, err
	}

	var (
		nodeName, nodeVersion string
		pods                  []types.AgentPod
		issues                []types.Issue
		prs                   []types.PullRequest
		dispLog               string
		mu                    sync.Mutex
		wg                    sync.WaitGroup
		errs                  []error
	)

	wg.Add(5)
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
		d, e := k8s.GetDispatcherLog(ctx, cs)
		mu.Lock()
		dispLog = d
		if e != nil {
			errs = append(errs, e)
		}
		mu.Unlock()
	}()
	wg.Wait()

	// fetch log tails in parallel
	var lwg sync.WaitGroup
	for i := range pods {
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
		issues:        issues,
		prs:           prs,
		dispatcherLog: dispLog,
	}, nil
}

func printDashboard(d *dashboard) {
	fmt.Println("=== CLUSTER ===")
	fmt.Printf("Node: %s Ready %s\n\n", d.nodeName, d.nodeVersion)

	// 1. Dispatcher
	fmt.Println("=== DISPATCHER ===")
	if d.dispatcherLog == "" {
		fmt.Println("  (no dispatcher runs found)")
	} else {
		for _, line := range splitLines(d.dispatcherLog) {
			fmt.Printf("  %s\n", line)
		}
	}
	fmt.Println()

	// 2. Issues
	fmt.Println("=== GITHUB ISSUES ===")
	if len(d.issues) == 0 {
		fmt.Println("  (no issues with workflow labels)")
	} else {
		fmt.Printf("%-7s %-14s %-10s Title\n", "Issue", "State", "Owner")
		for _, i := range d.issues {
			fmt.Printf("%s %-14s %-10s %s\n", issueLink(i.Number), i.State, i.Owner, i.Title)
		}
	}
	fmt.Println()

	// 3. Agents
	running, completed, failed := countPhases(d.pods)
	fmt.Printf("=== AGENTS (%d running, %d completed, %d failed) ===\n", running, completed, failed)
	if len(d.pods) == 0 {
		fmt.Println("  (no agent pods)")
	} else {
		fmt.Printf("%-7s %-10s %-11s %-16s %-10s Last Output\n", "Issue", "Agent", "Status", "Started", "Duration")
		for _, pod := range d.pods {
			agent := fmt.Sprintf("claude-%d", pod.Slot+types.SlotOffset)
			fmt.Printf("%s %-10s %-11s %-16s %-10s %s\n",
				issueLink(pod.Issue), agent, pod.Phase.Display(),
				fmtTime(pod.Started), fmtDuration(pod.Started, pod.Finished),
				pod.LogTail)
		}
	}
	fmt.Println()
}

func issueLink(number int) string {
	url := fmt.Sprintf("https://github.com/%s/%s/issues/%d", types.RepoOwner, types.RepoName, number)
	text := fmt.Sprintf("#%d", number)
	link := fmt.Sprintf("\033]8;;%s\033\\%s\033]8;;\033\\", url, text)
	if len(text) < 7 {
		link += strings.Repeat(" ", 7-len(text))
	}
	return link
}

func countPhases(pods []types.AgentPod) (running, completed, failed int) {
	for _, p := range pods {
		switch p.Phase {
		case types.PhaseRunning, types.PhasePending:
			running++
		case types.PhaseSucceeded:
			completed++
		case types.PhaseFailed:
			failed++
		}
	}
	return
}

func splitLines(s string) []string {
	var lines []string
	for _, l := range split(s, '\n') {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

func split(s string, sep byte) []string {
	var result []string
	for len(s) > 0 {
		idx := -1
		for i := 0; i < len(s); i++ {
			if s[i] == sep {
				idx = i
				break
			}
		}
		if idx == -1 {
			result = append(result, s)
			break
		}
		result = append(result, s[:idx])
		s = s[idx+1:]
	}
	return result
}

func runTop(cmd *cobra.Command, args []string) error {
	if once {
		d, err := gather()
		if err != nil {
			return err
		}
		printDashboard(d)
		return nil
	}

	// TUI mode
	gatherFn := func() (*tui.Data, error) {
		d, err := gather()
		if err != nil {
			return nil, err
		}
		return &tui.Data{
			NodeName:      d.nodeName,
			NodeVersion:   d.nodeVersion,
			Pods:          d.pods,
			Issues:        d.issues,
			PRs:           d.prs,
			DispatcherLog: d.dispatcherLog,
		}, nil
	}

	dispatchFn := func() (string, error) {
		return RunDispatch()
	}

	maxSlots := 3
	setMaxSlots := func(n int) {
		maxSlots = n
		// update env so dispatch picks it up
		os.Setenv("MAX_SLOTS", fmt.Sprintf("%d", n))
	}

	m := tui.NewModel(gatherFn, dispatchFn, maxSlots, setMaxSlots)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}
