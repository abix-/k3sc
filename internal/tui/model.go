package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/abix-/k3s-claude/internal/types"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var loc, _ = time.LoadLocation("America/New_York")

type Data struct {
	NodeName      string
	NodeVersion   string
	Pods          []types.AgentPod
	Issues        []types.Issue
	DispatcherLog string
}

type GatherFunc func() (*Data, error)
type DispatchFunc func() (string, error)

type tickMsg time.Time
type dispatchDone string

type Model struct {
	data       *Data
	gatherFn   GatherFunc
	dispatchFn DispatchFunc
	statusMsg  string
	width      int
	height     int
	quitting   bool
}

func NewModel(gatherFn GatherFunc, dispatchFn DispatchFunc) Model {
	return Model{gatherFn: gatherFn, dispatchFn: dispatchFn}
}

func tickCmd() tea.Cmd {
	return tea.Tick(5*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(
		func() tea.Msg { d, _ := m.gatherFn(); return d },
		tickCmd(),
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
			m.statusMsg = "dispatching..."
			return m, func() tea.Msg {
				log, err := m.dispatchFn()
				if err != nil {
					return fmt.Sprintf("dispatch error: %v", err)
				}
				return dispatchDone(log)
			}
		case "r":
			m.statusMsg = "refreshing..."
			return m, func() tea.Msg { d, _ := m.gatherFn(); return d }
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tickMsg:
		return m, tea.Batch(
			func() tea.Msg { d, _ := m.gatherFn(); return d },
			tickCmd(),
		)
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

	d := m.data
	w := m.width
	if w < 80 {
		w = 120
	}

	running, completed, failed := countPhases(d.Pods)

	// styles
	border := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("8")).
		Width(w - 2)

	titleFg := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15"))
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	green := lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	red := lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	yellow := lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	magenta := lipgloss.NewStyle().Foreground(lipgloss.Color("13"))
	cyan := lipgloss.NewStyle().Foreground(lipgloss.Color("14"))

	var sections []string

	// -- cluster --
	clusterContent := fmt.Sprintf(" Node: %s %s  |  Agents: %d running, %d completed",
		d.NodeName, d.NodeVersion, running, completed)
	clusterBox := border.Copy().BorderTop(true).Render(
		titleFg.Render(" Cluster") + "\n" + clusterContent)
	sections = append(sections, clusterBox)

	// -- issues --
	var issueLines []string
	if len(d.Issues) == 0 {
		issueLines = append(issueLines, dim.Render("  (no issues with workflow labels)"))
	} else {
		issueLines = append(issueLines, titleFg.Render(fmt.Sprintf(" %-7s %-14s %-10s Title", "Issue", "State", "Owner")))
		for _, i := range d.Issues {
			line := fmt.Sprintf(" #%-6d %-14s %-10s %s", i.Number, i.State, i.Owner, truncate(i.Title, w-40))
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
	issueBox := border.Render(titleFg.Render(" GitHub Issues") + "\n" + strings.Join(issueLines, "\n"))
	sections = append(sections, issueBox)

	// -- dispatcher --
	var dispLines []string
	if d.DispatcherLog == "" {
		dispLines = append(dispLines, dim.Render("  (no dispatcher runs found)"))
	} else {
		for _, line := range strings.Split(strings.TrimSpace(d.DispatcherLog), "\n") {
			dispLines = append(dispLines, dim.Render("  "+line))
		}
	}
	dispBox := border.Render(titleFg.Render(" Dispatcher (last run)") + "\n" + strings.Join(dispLines, "\n"))
	sections = append(sections, dispBox)

	// -- agents --
	var agentLines []string
	if len(d.Pods) == 0 {
		agentLines = append(agentLines, dim.Render("  (no agent pods)"))
	} else {
		agentLines = append(agentLines, titleFg.Render(fmt.Sprintf(" %-7s %-10s %-11s %-16s %-10s Last Output", "Issue", "Agent", "Status", "Started", "Duration")))
		for _, pod := range d.Pods {
			agent := fmt.Sprintf("claude-%d", pod.Slot+types.SlotOffset)
			started := fmtTime(pod.Started)
			duration := fmtDuration(pod.Started, pod.Finished)
			tail := truncate(pod.LogTail, w-65)
			line := fmt.Sprintf(" #%-6d %-10s %-11s %-16s %-10s %s",
				pod.Issue, agent, pod.Phase.Display(), started, duration, tail)
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
	agentBox := border.Render(titleFg.Render(agentTitle) + "\n" + strings.Join(agentLines, "\n"))
	sections = append(sections, agentBox)

	// -- status + help bar --
	if m.statusMsg != "" {
		sections = append(sections, yellow.Render(" "+m.statusMsg))
	}
	sections = append(sections, dim.Render(" q: quit  |  n: dispatch now  |  r: refresh  |  refreshes every 5s"))

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

func truncate(s string, max int) string {
	if max < 4 {
		max = 4
	}
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}
