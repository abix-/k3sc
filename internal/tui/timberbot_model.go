package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/abix-/k3sc/internal/format"
	"github.com/abix-/k3sc/internal/types"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type TimberbotRun struct {
	Name     string
	Agent    string
	Phase    string // Succeeded, Failed, Running, Pending
	Started  *time.Time
	Finished *time.Time
}

type TimberbotData struct {
	Info     types.TimberbotInfo
	Runs     []TimberbotRun
	LiveLogs []LiveLog
}

type TimberbotGatherFunc func() (*TimberbotData, error)
type TimberbotK8sGatherFunc func() (*TimberbotData, error)

type tbTickMsg time.Time
type tbData *TimberbotData

type TimberbotModel struct {
	data      *TimberbotData
	gatherFn  TimberbotGatherFunc
	k8sGather TimberbotK8sGatherFunc
	width     int
	height    int
	quitting  bool
}

func NewTimberbotModel(gatherFn TimberbotGatherFunc, k8sGather TimberbotK8sGatherFunc) TimberbotModel {
	return TimberbotModel{
		gatherFn:  gatherFn,
		k8sGather: k8sGather,
	}
}

func tbTickCmd() tea.Cmd {
	return tea.Tick(3*time.Second, func(t time.Time) tea.Msg { return tbTickMsg(t) })
}

func (m TimberbotModel) Init() tea.Cmd {
	return tea.Batch(
		func() tea.Msg { d, _ := m.gatherFn(); return tbData(d) },
		tbTickCmd(),
	)
}

func (m TimberbotModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "r":
			return m, func() tea.Msg { d, _ := m.gatherFn(); return tbData(d) }
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tbTickMsg:
		return m, tea.Batch(
			func() tea.Msg {
				if m.k8sGather != nil {
					d, _ := m.k8sGather()
					return tbData(d)
				}
				return nil
			},
			tbTickCmd(),
		)
	case tbData:
		if msg != nil {
			m.data = (*TimberbotData)(msg)
		}
	}
	return m, nil
}

func (m TimberbotModel) View() string {
	if m.quitting || m.data == nil {
		return ""
	}

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
	red := lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	yellow := lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	sep := dim.Render(strings.Repeat("-", w))

	var sections []string

	// -- header --
	info := m.data.Info
	if !info.Enabled {
		sections = append(sections, red.Render(" Timberbot  STOPPED"))
	} else {
		// find active run
		var active *TimberbotRun
		for i := range m.data.Runs {
			if m.data.Runs[i].Phase == "Running" || m.data.Runs[i].Phase == "Pending" {
				active = &m.data.Runs[i]
				break
			}
		}
		if active != nil {
			sections = append(sections, green.Render(" Timberbot  RUNNING"))
		} else {
			sections = append(sections, yellow.Render(" Timberbot  WAITING"))
		}
	}

	sections = append(sections, fmt.Sprintf(" goal: %s", info.Goal))
	ok, fail := countTimberbotRuns(m.data.Runs)
	runNum := ok + fail + 1
	for _, r := range m.data.Runs {
		if r.Phase == "Running" || r.Phase == "Pending" {
			break
		}
	}
	sections = append(sections, fmt.Sprintf(" rounds: %d  |  current run: #%d  |  total: %d ok, %d failed", info.Rounds, runNum, ok, fail))

	// active agent info
	var active *TimberbotRun
	for i := range m.data.Runs {
		if m.data.Runs[i].Phase == "Running" || m.data.Runs[i].Phase == "Pending" {
			active = &m.data.Runs[i]
			break
		}
	}
	if active != nil {
		dur := format.FmtDuration(active.Started, nil)
		sections = append(sections, fmt.Sprintf(" agent: %s  |  pod: %s  |  uptime: %s", active.Agent, active.Name, dur))
	}

	// -- recent runs --
	sections = append(sections, sep)
	sections = append(sections, titleFg.Render(" Recent Runs"))
	completed := completedRuns(m.data.Runs)
	if len(completed) == 0 {
		sections = append(sections, dim.Render("  (no completed runs)"))
	} else {
		maxRuns := min(len(completed), 10)
		for i, r := range completed[:maxRuns] {
			dur := format.FmtDuration(r.Started, r.Finished)
			started := format.FmtTime(r.Started)
			line := fmt.Sprintf("  #%-3d %-12s %-10s %-8s %s", len(completed)-i, r.Agent, r.Phase, dur, started)
			if r.Phase == "Succeeded" {
				sections = append(sections, green.Render(line))
			} else {
				sections = append(sections, red.Render(line))
			}
		}
	}

	// -- live log --
	sections = append(sections, sep)
	sections = append(sections, titleFg.Render(" Live Log"))
	if len(m.data.LiveLogs) == 0 {
		sections = append(sections, dim.Render("  (no active agent)"))
	} else {
		// use all remaining vertical space for logs
		usedLines := len(sections) + 2 // +2 for help line + buffer
		logSpace := h - usedLines
		if logSpace < 4 {
			logSpace = 4
		}

		for _, ll := range m.data.LiveLogs {
			lines := ll.Lines
			if len(lines) > logSpace {
				lines = lines[len(lines)-logSpace:]
			}
			for _, line := range lines {
				sections = append(sections, green.Render("  "+format.Truncate(line, w-4)))
			}
		}
	}

	// -- help --
	sections = append(sections, dim.Render(" q: quit  r: refresh"))

	return strings.Join(sections, "\n")
}

func countTimberbotRuns(runs []TimberbotRun) (ok, fail int) {
	for _, r := range runs {
		switch r.Phase {
		case "Succeeded":
			ok++
		case "Failed":
			fail++
		}
	}
	return
}

func completedRuns(runs []TimberbotRun) []TimberbotRun {
	var result []TimberbotRun
	for _, r := range runs {
		if r.Phase == "Succeeded" || r.Phase == "Failed" {
			result = append(result, r)
		}
	}
	// newest first
	sort.Slice(result, func(i, j int) bool {
		ti := runTime(result[i])
		tj := runTime(result[j])
		return ti.After(tj)
	})
	return result
}

func runTime(r TimberbotRun) time.Time {
	if r.Finished != nil {
		return *r.Finished
	}
	if r.Started != nil {
		return *r.Started
	}
	return time.Time{}
}
