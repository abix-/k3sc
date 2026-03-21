package tui

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/abix-/k3sc/internal/format"
	"github.com/abix-/k3sc/internal/types"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type LiveLog struct {
	PodName string
	Issue   int
	Agent   string
	Lines   []string
	Tail    string // cached last meaningful line (O(1) for dashboard)
}

type Data struct {
	NodeName    string
	NodeVersion string
	Dispatch    types.DispatchStateInfo
	Pods        []types.AgentPod
	Tasks       []types.TaskInfo
	Issues      []types.Issue
	PRs         []types.PullRequest
	OperatorLog string
	LiveLogs    []LiveLog
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
	showOperator bool
	showErrors   bool
	logView      bool // full-screen live log view
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
		showOperator: true,
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
			m.showOperator = !m.showOperator
		case "e":
			m.showErrors = !m.showErrors
		case "l", "tab":
			m.logView = !m.logView
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
			d.OperatorLog = dispLog
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
	if m.logView {
		return m.renderLogView()
	}
	h := m.height
	if h < 20 {
		h = 50
	}

	// try full output, then reduce operator tasks until it fits
	for maxTasks := len(m.data.Tasks); maxTasks >= 0; maxTasks-- {
		result := m.renderView(maxTasks)
		lines := strings.Count(result, "\n") + 1
		if lines <= h {
			return result
		}
	}
	return m.renderView(0)
}

func (m Model) renderLogView() string {
	d := m.data
	w := m.width
	if w < 80 {
		w = 120
	}
	h := m.height
	if h < 20 {
		h = 50
	}

	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	titleFg := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15"))
	green := lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	yellow := lipgloss.NewStyle().Foreground(lipgloss.Color("11"))

	var sections []string

	// header
	running := 0
	for _, p := range d.Pods {
		if p.Phase == types.PhaseRunning || p.Phase == types.PhasePending {
			running++
		}
	}
	sections = append(sections, titleFg.Render(fmt.Sprintf(" Live Logs (%d running)", running)))

	if len(d.LiveLogs) == 0 {
		sections = append(sections, dim.Render("  (no running agents)"))
	} else {
		// divide available lines among running agents
		// reserve 2 lines for header + help
		available := h - 2
		linesPerAgent := available / len(d.LiveLogs)
		if linesPerAgent < 3 {
			linesPerAgent = 3
		}
		// subtract 1 for the divider/header per agent
		logLines := linesPerAgent - 1
		if logLines < 2 {
			logLines = 2
		}
		if logLines > 8 {
			logLines = 8
		}

		sep := dim.Render(strings.Repeat("-", w))
		for i, ll := range d.LiveLogs {
			if i > 0 {
				sections = append(sections, sep)
			}
			agent := yellow.Render(fmt.Sprintf(" %s #%d", ll.Agent, ll.Issue))
			sections = append(sections, agent)

			// take last N lines
			lines := ll.Lines
			if len(lines) > logLines {
				lines = lines[len(lines)-logLines:]
			}
			for _, line := range lines {
				sections = append(sections, green.Render("  "+format.Truncate(line, w-4)))
			}
			// pad if fewer lines than allocated
			for j := len(lines); j < logLines; j++ {
				sections = append(sections, "")
			}
		}
	}

	sections = append(sections, dim.Render(" l: dashboard  q: quit  r: refresh"))

	return strings.Join(sections, "\n")
}

func (m Model) renderView(maxVisibleTasks int) string {
	d := m.data
	w := m.width
	if w < 80 {
		w = 120
	}

	running, completed, failed := format.CountPhases(d.Pods)

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
	clusterContent := fmt.Sprintf(" Node: %s %s  |  Agents: %d running, %d completed, %d failed  |  Max slots: %d%s",
		d.NodeName, d.NodeVersion, running, completed, failed, m.maxSlots, pauseStr)
	sections = append(sections, titleFg.Render(" Cluster")+" "+clusterContent)

	var quotaLines []string
	if len(d.Dispatch.FamilyStatuses) == 0 {
		quotaLines = append(quotaLines, dim.Render("  (quota unknown)"))
	} else {
		for _, status := range orderedFamilyStatuses(d.Dispatch.FamilyStatuses) {
			reason := ""
			if status.Reason != "" {
				reason = "  " + format.Truncate(status.Reason, w-22)
			}
			line := fmt.Sprintf("  %-6s ", status.Family)
			switch {
			case !status.Available:
				quotaLines = append(quotaLines, red.Render(line+"BLOCKED"+reason))
			case status.Checked:
				quotaLines = append(quotaLines, green.Render(line+"OK"+reason))
			default:
				quotaLines = append(quotaLines, yellow.Render(line+"UNKNOWN"+reason))
			}
		}
	}
	sections = append(sections, sep)
	sections = append(sections, titleFg.Render(" Quota"))
	sections = append(sections, strings.Join(quotaLines, "\n"))

	// -- local review reservations --
	var reservationLines []string
	if len(d.Dispatch.ReviewReservations) == 0 {
		reservationLines = append(reservationLines, dim.Render("  (no local PR reservations)"))
	} else {
		reservationLines = append(reservationLines, titleFg.Render(fmt.Sprintf(" %-12s %-12s %-7s %-8s %-16s %-16s Branch", "Worker", "Repo", "PR", "Issue", "Reserved", "Expires")))
		for _, res := range d.Dispatch.ReviewReservations {
			issue := ""
			if res.Issue > 0 {
				issue = fmt.Sprintf("#%d", res.Issue)
			}
			line := fmt.Sprintf(" %-12s %-12s %-7s %-8s %-16s %-16s %s",
				res.WorkerID,
				res.Repo.Name,
				format.PRLink(res.Repo, res.PRNumber),
				issue,
				format.FmtTime(res.ReservedAt),
				format.FmtTime(res.ExpiresAt),
				format.Truncate(res.Branch, w-79))
			reservationLines = append(reservationLines, yellow.Render(line))
		}
	}
	sections = append(sections, sep)
	sections = append(sections, titleFg.Render(" Local Review"))
	sections = append(sections, strings.Join(reservationLines, "\n"))

	// -- dispatcher (toggle with d) --
	if m.showOperator {
		var dispLines []string
		if d.OperatorLog == "" {
			dispLines = append(dispLines, dim.Render("  (no operator logs)"))
		} else {
			// word wrap all lines, then take last 10 wrapped lines
			var allWrapped []string
			for _, line := range strings.Split(strings.TrimSpace(d.OperatorLog), "\n") {
				for _, wl := range wordWrap(line, w-4) {
					allWrapped = append(allWrapped, wl)
				}
			}
			if len(allWrapped) > 10 {
				allWrapped = allWrapped[len(allWrapped)-10:]
			}
			for _, wl := range allWrapped {
				dispLines = append(dispLines, dim.Render("  "+wl))
			}
		}
		sections = append(sections, sep)
		sections = append(sections, titleFg.Render(" Operator Log"))
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
			line := fmt.Sprintf(" %s %-12s %-14s %-10s %s", format.IssueLink(i.Repo, i.Number), i.Repo.Name, i.State, i.Owner, format.Truncate(i.Title, w-52))
			switch {
			case i.Owner != "" && i.State != "needs-human" && i.State != "needs-review":
				issueLines = append(issueLines, yellow.Render(line))
			case i.State == "needs-human":
				issueLines = append(issueLines, magenta.Render(line))
			case i.State == "needs-review":
				issueLines = append(issueLines, cyan.Render(line))
			case i.State == "ready":
				issueLines = append(issueLines, green.Render(line))
			default:
				issueLines = append(issueLines, line)
			}
		}
	}
	sections = append(sections, sep)
	sections = append(sections, titleFg.Render(" GitHub Issues"))
	sections = append(sections, strings.Join(issueLines, "\n"))

	// -- operator tasks --
	var taskLines []string
	tRunning, tDone, tFailed, tBlocked := 0, 0, 0, 0
	for _, t := range d.Tasks {
		switch t.Phase {
		case "Running", "Pending":
			tRunning++
		case "Succeeded":
			tDone++
		case "Failed":
			tFailed++
		case "Blocked":
			tBlocked++
		}
	}
	if len(d.Tasks) == 0 {
		taskLines = append(taskLines, dim.Render("  (no operator tasks)"))
	} else {
		taskLines = append(taskLines, titleFg.Render(fmt.Sprintf(" %-7s %-10s %-10s %-10s %-10s %-16s %-10s %-13s Last Output", "Issue", "Repo", "Agent", "Task", "Runtime", "Started", "Duration", "Next")))
		visibleTasks := d.Tasks
		if len(visibleTasks) > maxVisibleTasks {
			visibleTasks = visibleTasks[:maxVisibleTasks]
		}
		for _, t := range visibleTasks {
			started := format.FmtTime(t.Started)
			duration := format.FmtDuration(t.Started, t.Finished)
			runtime := "-"
			if t.RuntimePhase != "" {
				runtime = t.RuntimePhase.Display()
			}
			tail := format.Truncate(t.LogTail, w-96)
			line := fmt.Sprintf(" %-7s %-10s %-10s %-10s %-10s %-16s %-10s %-13s %s",
				fmt.Sprintf("#%d", t.Issue), t.Repo.Name, t.Agent, t.Phase, runtime, started, duration, t.NextAction, tail)
			switch t.Phase {
			case "Running", "Pending":
				taskLines = append(taskLines, green.Render(line))
			case "Failed":
				taskLines = append(taskLines, red.Render(line))
			case "Blocked":
				taskLines = append(taskLines, magenta.Render(line))
			default:
				taskLines = append(taskLines, dim.Render(line))
			}
		}
	}
	taskTitle := fmt.Sprintf(" Operator Tasks (%d running, %d done, %d failed, %d blocked)", tRunning, tDone, tFailed, tBlocked)
	sections = append(sections, sep)
	sections = append(sections, titleFg.Render(taskTitle))
	sections = append(sections, strings.Join(taskLines, "\n"))

	// -- pull requests --
	var prLines []string
	if len(d.PRs) == 0 {
		prLines = append(prLines, dim.Render("  (no open pull requests)"))
	} else {
		prLines = append(prLines, titleFg.Render(fmt.Sprintf(" %-3s %-7s %-12s %-12s %-7s %-20s Title", "#", "PR", "Repo", "Owner", "Issue", "Branch")))
		maxPRs := min(len(d.PRs), 6)
		for idx, pr := range d.PRs[:maxPRs] {
			pl := format.PRLink(pr.Repo, pr.Number)
			issueRef := "       "
			if pr.Issue > 0 {
				issueRef = format.IssueLink(pr.Repo, pr.Issue)
			}
			line := fmt.Sprintf(" %-3d %s %-12s %-12s %-7s %-20s %s", idx+1, pl, pr.Repo.Name, pr.Owner, issueRef, format.Truncate(pr.Branch, 20), format.Truncate(pr.Title, w-74))
			if pr.Owner != "" {
				prLines = append(prLines, yellow.Render(line))
			} else {
				prLines = append(prLines, cyan.Render(line))
			}
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
				errSection = append(errSection, red.Render("  "+format.Truncate(line, w-4)))
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
	sections = append(sections, dim.Render(" q: quit  n: dispatch  p: pause  d: operator  e: errors  l: logs  r: refresh  +/-: agents  1-6: copy /review"))

	return strings.Join(sections, "\n")
}

func copyToClipboard(s string) {
	c := exec.Command("clip")
	c.Stdin = strings.NewReader(s)
	c.Run()
}

func wordWrap(s string, width int) []string {
	if width <= 0 || len(s) <= width {
		return []string{s}
	}
	var lines []string
	for len(s) > width {
		// find last space before width
		cut := width
		for cut > 0 && s[cut] != ' ' {
			cut--
		}
		if cut == 0 {
			cut = width // no space found, hard break
		}
		lines = append(lines, s[:cut])
		s = s[cut:]
		if len(s) > 0 && s[0] == ' ' {
			s = s[1:]
		}
	}
	if len(s) > 0 {
		lines = append(lines, s)
	}
	return lines
}

func orderedFamilyStatuses(statuses []types.DispatchFamilyStatus) []types.DispatchFamilyStatus {
	byFamily := map[types.AgentFamily]types.DispatchFamilyStatus{}
	for _, status := range statuses {
		byFamily[status.Family] = status
	}

	var ordered []types.DispatchFamilyStatus
	for _, family := range []types.AgentFamily{types.FamilyClaude, types.FamilyCodex} {
		if status, ok := byFamily[family]; ok {
			ordered = append(ordered, status)
		}
	}
	for _, status := range statuses {
		if status.Family != types.FamilyClaude && status.Family != types.FamilyCodex {
			ordered = append(ordered, status)
		}
	}
	return ordered
}
