package tui

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/abix-/k3sc/internal/types"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var loc, _ = time.LoadLocation("America/New_York")

type LiveLog struct {
	Issue int
	Agent string
	Lines []string
}

type Data struct {
	NodeName      string
	NodeVersion   string
	Pods          []types.AgentPod
	Issues        []types.Issue
	PRs           []types.PullRequest
	DispatcherLog string
	LiveLogs      []LiveLog
}

type GatherFunc func() (*Data, error)
type K8sGatherFunc func(current *Data) (*Data, error)
type DispatchFunc func() (string, error)
type SetMaxSlotsFunc func(n int)

type k8sTickMsg time.Time
type ghTickMsg time.Time
type k8sData *Data
type dispatchDone string

type ErrorLinesFunc func() []string

type Model struct {
	data         *Data
	gatherFn     GatherFunc
	k8sGatherFn  K8sGatherFunc
	dispatchFn   DispatchFunc
	setMaxSlots  SetMaxSlotsFunc
	errorLines   ErrorLinesFunc
	statusMsg    string
	maxSlots     int
	paused       bool
	showDispatch bool
	showLive     bool
	showErrors   bool
	width        int
	height       int
	quitting     bool
}

func NewModel(gatherFn GatherFunc, k8sGatherFn K8sGatherFunc, dispatchFn DispatchFunc, maxSlots int, setMaxSlots SetMaxSlotsFunc, errorLines ErrorLinesFunc) Model {
	return Model{
		gatherFn:     gatherFn,
		k8sGatherFn:  k8sGatherFn,
		dispatchFn:   dispatchFn,
		setMaxSlots:  setMaxSlots,
		errorLines:   errorLines,
		maxSlots:     maxSlots,
		showDispatch: true,
		showLive:     true,
	}
}

func k8sTickCmd() tea.Cmd {
	return tea.Tick(3*time.Second, func(t time.Time) tea.Msg { return k8sTickMsg(t) })
}

func ghTickCmd() tea.Cmd {
	return tea.Tick(30*time.Second, func(t time.Time) tea.Msg { return ghTickMsg(t) })
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		func() tea.Msg { d, _ := m.gatherFn(); return d },
		k8sTickCmd(),
		ghTickCmd(),
	)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "n":
			if m.paused {
				m.statusMsg = "dispatcher is paused (press p to resume)"
				return m, nil
			}
			m.statusMsg = "dispatching..."
			return m, func() tea.Msg {
				log, err := m.dispatchFn()
				if err != nil {
					return fmt.Sprintf("dispatch error: %v", err)
				}
				return dispatchDone(log)
			}
		case "p":
			m.paused = !m.paused
			if m.paused {
				m.statusMsg = "dispatcher PAUSED"
			} else {
				m.statusMsg = "dispatcher resumed"
			}
		case "d":
			m.showDispatch = !m.showDispatch
		case "e":
			m.showErrors = !m.showErrors
		case "l":
			m.showLive = !m.showLive
		case "r":
			m.statusMsg = "refreshing..."
			return m, func() tea.Msg { d, _ := m.gatherFn(); return d }
		case "+", "=":
			if m.maxSlots < 5 {
				m.maxSlots++
				if m.setMaxSlots != nil {
					m.setMaxSlots(m.maxSlots)
				}
				m.statusMsg = fmt.Sprintf("max agents: %d", m.maxSlots)
			}
		case "-":
			if m.maxSlots > 1 {
				m.maxSlots--
				if m.setMaxSlots != nil {
					m.setMaxSlots(m.maxSlots)
				}
				m.statusMsg = fmt.Sprintf("max agents: %d", m.maxSlots)
			}
		case "1", "2", "3", "4", "5", "6":
			idx, _ := fmt.Sscanf(msg.String(), "%d", new(int))
			if idx == 1 && m.data != nil {
				n := int(msg.String()[0] - '0')
				if n >= 1 && n <= len(m.data.PRs) {
					pr := m.data.PRs[n-1]
					clip := fmt.Sprintf("/review %s %d", pr.Repo.Name, pr.Number)
					copyToClipboard(clip)
					m.statusMsg = fmt.Sprintf("copied: %s", clip)
				}
			}
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case k8sTickMsg:
		// fast local refresh: k8s pods + live logs only
		return m, tea.Batch(
			func() tea.Msg {
				if m.k8sGatherFn != nil && m.data != nil {
					d, _ := m.k8sGatherFn(m.data)
					return k8sData(d)
				}
				return nil
			},
			k8sTickCmd(),
		)
	case ghTickMsg:
		// slow remote refresh: github issues + PRs
		return m, tea.Batch(
			func() tea.Msg { d, _ := m.gatherFn(); return d },
			ghTickCmd(),
		)
	case k8sData:
		if msg != nil {
			m.data = (*Data)(msg)
		}
	case *Data:
		m.data = msg
		if m.statusMsg == "refreshing..." {
			m.statusMsg = ""
		}
	case dispatchDone:
		dispLog := string(msg)
		d, _ := m.gatherFn()
		if d != nil {
			d.DispatcherLog = dispLog
			m.data = d
		}
		m.statusMsg = "dispatch complete"
	case string:
		m.statusMsg = msg
	}
	return m, nil
}

func (m Model) View() string {
	if m.quitting || m.data == nil {
		return ""
	}
	h := m.height
	if h < 20 {
		h = 50
	}

	// try full output, then reduce agents until it fits
	for maxPods := len(m.data.Pods); maxPods >= 0; maxPods-- {
		result := m.renderView(maxPods)
		lines := strings.Count(result, "\n") + 1
		if lines <= h {
			return result
		}
	}
	return m.renderView(0)
}

func (m Model) renderView(maxVisiblePods int) string {
	d := m.data
	w := m.width
	if w < 80 {
		w = 120
	}

	running, completed, failed := countPhases(d.Pods)
	maxLivePerAgent := 6

	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	titleFg := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15"))
	sep := dim.Render(strings.Repeat("-", w))
	green := lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	red := lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	yellow := lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	magenta := lipgloss.NewStyle().Foreground(lipgloss.Color("13"))
	cyan := lipgloss.NewStyle().Foreground(lipgloss.Color("14"))

	var sections []string

	// -- cluster --
	pauseStr := ""
	if m.paused {
		pauseStr = "  |  PAUSED"
	}
	clusterContent := fmt.Sprintf(" Node: %s %s  |  Agents: %d running, %d completed  |  Max slots: %d%s",
		d.NodeName, d.NodeVersion, running, completed, m.maxSlots, pauseStr)
	sections = append(sections, titleFg.Render(" Cluster")+" "+clusterContent)

	// -- dispatcher (toggle with d) --
	if m.showDispatch {
		var dispLines []string
		if d.DispatcherLog == "" {
			dispLines = append(dispLines, dim.Render("  (no dispatcher runs found)"))
		} else {
			for _, line := range strings.Split(strings.TrimSpace(d.DispatcherLog), "\n") {
				dispLines = append(dispLines, dim.Render("  "+line))
			}
		}
		sections = append(sections, sep)
		sections = append(sections, titleFg.Render(" Dispatcher (last run)"))
		sections = append(sections, strings.Join(dispLines, "\n"))
	}

	// -- issues --
	var issueLines []string
	if len(d.Issues) == 0 {
		issueLines = append(issueLines, dim.Render("  (no issues with workflow labels)"))
	} else {
		issueLines = append(issueLines, titleFg.Render(fmt.Sprintf(" %-7s %-12s %-14s %-10s Title", "Issue", "Repo", "State", "Owner")))
		maxIssues := min(len(d.Issues), 10)
		for _, i := range d.Issues[:maxIssues] {
			line := fmt.Sprintf(" %s %-12s %-14s %-10s %s", issueLink(i.Repo, i.Number), i.Repo.Name, i.State, i.Owner, truncate(i.Title, w-52))
			switch i.State {
			case "claimed":
				issueLines = append(issueLines, yellow.Render(line))
			case "needs-human":
				issueLines = append(issueLines, magenta.Render(line))
			case "needs-review":
				issueLines = append(issueLines, cyan.Render(line))
			case "ready":
				issueLines = append(issueLines, green.Render(line))
			default:
				issueLines = append(issueLines, line)
			}
		}
	}
	sections = append(sections, sep)
	sections = append(sections, titleFg.Render(" GitHub Issues"))
	sections = append(sections, strings.Join(issueLines, "\n"))

	// -- agents --
	var agentLines []string
	if len(d.Pods) == 0 {
		agentLines = append(agentLines, dim.Render("  (no agent pods)"))
	} else {
		agentLines = append(agentLines, titleFg.Render(fmt.Sprintf(" %-7s %-10s %-11s %-16s %-10s Last Output", "Issue", "Agent", "Status", "Started", "Duration")))
		visiblePods := d.Pods
		if len(visiblePods) > maxVisiblePods {
			var runPods, donePods []types.AgentPod
			for _, p := range visiblePods {
				if p.Phase == types.PhaseRunning || p.Phase == types.PhasePending {
					runPods = append(runPods, p)
				} else {
					donePods = append(donePods, p)
				}
			}
			keep := maxVisiblePods - len(runPods)
			if keep < 0 {
				keep = 0
			}
			if len(donePods) > keep {
				donePods = donePods[len(donePods)-keep:]
			}
			visiblePods = append(runPods, donePods...)
		}
		for _, pod := range visiblePods {
			agent := types.AgentName(pod.Slot)
			started := fmtTime(pod.Started)
			duration := fmtDuration(pod.Started, pod.Finished)
			tail := truncate(pod.LogTail, w-65)
			line := fmt.Sprintf(" %s %-10s %-11s %-16s %-10s %s",
				issueLink(pod.Repo, pod.Issue), agent, pod.Phase.Display(), started, duration, tail)
			switch pod.Phase {
			case types.PhaseRunning, types.PhasePending:
				agentLines = append(agentLines, green.Render(line))
			case types.PhaseFailed:
				agentLines = append(agentLines, red.Render(line))
			default:
				agentLines = append(agentLines, dim.Render(line))
			}
		}
	}
	agentTitle := fmt.Sprintf(" Agents (%d running, %d completed, %d failed)", running, completed, failed)
	sections = append(sections, sep)
	sections = append(sections, titleFg.Render(agentTitle))
	sections = append(sections, strings.Join(agentLines, "\n"))

	// -- live output (only if agents are running, capped to budget) --
	if m.showLive && len(d.LiveLogs) > 0 {
		var liveLines []string
		for _, ll := range d.LiveLogs {
			liveLines = append(liveLines, titleFg.Render(fmt.Sprintf(" -- %s (issue #%d) --", ll.Agent, ll.Issue)))
			visibleLogLines := ll.Lines
			if len(visibleLogLines) > maxLivePerAgent {
				visibleLogLines = visibleLogLines[len(visibleLogLines)-maxLivePerAgent:]
			}
			for _, line := range visibleLogLines {
				liveLines = append(liveLines, green.Render("  "+truncate(line, w-6)))
			}
		}
		sections = append(sections, sep)
		sections = append(sections, titleFg.Render(" Live Output"))
		sections = append(sections, strings.Join(liveLines, "\n"))
	}

	// -- pull requests --
	var prLines []string
	if len(d.PRs) == 0 {
		prLines = append(prLines, dim.Render("  (no open pull requests)"))
	} else {
		prLines = append(prLines, titleFg.Render(fmt.Sprintf(" %-3s %-7s %-12s %-7s %-20s Title", "#", "PR", "Repo", "Issue", "Branch")))
		maxPRs := min(len(d.PRs), 6)
		for idx, pr := range d.PRs[:maxPRs] {
			pl := prLink(pr.Repo, pr.Number)
			issueRef := "       "
			if pr.Issue > 0 {
				issueRef = issueLink(pr.Repo, pr.Issue)
			}
			line := fmt.Sprintf(" %-3d %s %-12s %-7s %-20s %s", idx+1, pl, pr.Repo.Name, issueRef, truncate(pr.Branch, 20), truncate(pr.Title, w-61))
			prLines = append(prLines, cyan.Render(line))
		}
	}
	sections = append(sections, sep)
	sections = append(sections, titleFg.Render(" Pull Requests"))
	sections = append(sections, strings.Join(prLines, "\n"))

	// -- errors (toggle with e) --
	if m.showErrors && m.errorLines != nil {
		errLines := m.errorLines()
		var errSection []string
		if len(errLines) == 0 {
			errSection = append(errSection, dim.Render("  (no errors)"))
		} else {
			for _, line := range errLines {
				errSection = append(errSection, red.Render("  "+truncate(line, w-4)))
			}
		}
		sections = append(sections, sep)
		sections = append(sections, titleFg.Render(fmt.Sprintf(" Errors (%d)", len(errLines))))
		sections = append(sections, strings.Join(errSection, "\n"))
	}

	// -- status + help --
	if m.statusMsg != "" {
		sections = append(sections, yellow.Render(" "+m.statusMsg))
	}
	sections = append(sections, dim.Render(" q: quit  n: dispatch  p: pause  d: dispatcher  e: errors  l: live  r: refresh  +/-: agents  1-6: copy /review"))

	return strings.Join(sections, "\n")
}

func fmtTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.In(loc).Format("3:04 PM MST")
}

func fmtDuration(start, end *time.Time) string {
	if start == nil {
		return ""
	}
	e := time.Now()
	if end != nil {
		e = *end
	}
	d := e.Sub(*start)
	return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
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

func issueLink(repo types.Repo, number int) string {
	url := fmt.Sprintf("https://github.com/%s/%s/issues/%d", repo.Owner, repo.Name, number)
	text := fmt.Sprintf("#%d", number)
	link := fmt.Sprintf("\033]8;;%s\033\\%s\033]8;;\033\\", url, text)
	if len(text) < 7 {
		link += strings.Repeat(" ", 7-len(text))
	}
	return link
}

func prLink(repo types.Repo, number int) string {
	url := fmt.Sprintf("https://github.com/%s/%s/pull/%d", repo.Owner, repo.Name, number)
	text := fmt.Sprintf("#%d", number)
	link := fmt.Sprintf("\033]8;;%s\033\\%s\033]8;;\033\\", url, text)
	if len(text) < 7 {
		link += strings.Repeat(" ", 7-len(text))
	}
	return link
}

func copyToClipboard(s string) {
	c := exec.Command("clip")
	c.Stdin = strings.NewReader(s)
	c.Run()
}

func truncate(s string, max int) string {
	if max < 4 {
		max = 4
	}
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}
