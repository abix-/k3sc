package tui

import (
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"time"

	"github.com/abix-/k3sc/internal/claude"
	"github.com/abix-/k3sc/internal/format"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type SessionGatherFunc func() (*claude.Snapshot, error)

type sessionTickMsg time.Time
type sessionErrorMsg string

type SessionsModel struct {
	snapshot   *claude.Snapshot
	gatherFn   SessionGatherFunc
	statusMsg  string
	width      int
	height     int
	refreshing bool
	quitting   bool
}

func NewSessionsModel(initial *claude.Snapshot, gatherFn SessionGatherFunc) SessionsModel {
	return SessionsModel{
		snapshot: initial,
		gatherFn: gatherFn,
	}
}

func sessionTickCmd() tea.Cmd {
	return tea.Tick(30*time.Second, func(t time.Time) tea.Msg { return sessionTickMsg(t) })
}

func (m SessionsModel) Init() tea.Cmd {
	return sessionTickCmd()
}

func (m SessionsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "r":
			if m.refreshing {
				m.statusMsg = "refresh already in progress"
				return m, nil
			}
			m.refreshing = true
			m.statusMsg = "refreshing..."
			return m, m.refreshCmd()
		case "1", "2", "3", "4", "5", "6", "7", "8", "9":
			idx := int(msg.String()[0] - '1')
			if m.snapshot != nil && idx >= 0 && idx < len(m.snapshot.Sessions) {
				session := m.snapshot.Sessions[idx]
				if session.SessionID == "" {
					m.statusMsg = fmt.Sprintf("session %d has no session ID", idx+1)
				} else {
					copyToClipboard(session.SessionID)
					m.statusMsg = fmt.Sprintf("copied session ID: %s", session.SessionID)
				}
			}
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case sessionTickMsg:
		if m.refreshing {
			return m, sessionTickCmd()
		}
		m.refreshing = true
		return m, tea.Batch(m.refreshCmd(), sessionTickCmd())
	case *claude.Snapshot:
		m.refreshing = false
		m.snapshot = msg
		if m.statusMsg == "refreshing..." {
			m.statusMsg = ""
		}
	case sessionErrorMsg:
		m.refreshing = false
		m.statusMsg = string(msg)
	}
	return m, nil
}

func (m SessionsModel) refreshCmd() tea.Cmd {
	return func() tea.Msg {
		snapshot, err := m.gatherFn()
		if err != nil {
			return sessionErrorMsg(err.Error())
		}
		return snapshot
	}
}

func (m SessionsModel) View() string {
	if m.quitting {
		return ""
	}
	if m.snapshot == nil {
		if m.statusMsg == "" {
			return "loading Claude sessions..."
		}
		return m.statusMsg
	}

	h := m.height
	if h < 18 {
		h = 44
	}

	for maxVisible := len(m.snapshot.Sessions); maxVisible >= 0; maxVisible-- {
		result := m.renderView(maxVisible)
		lines := strings.Count(result, "\n") + 1
		if lines <= h {
			return result
		}
	}

	return m.renderView(0)
}

func (m SessionsModel) renderView(maxVisible int) string {
	s := m.snapshot
	w := m.width
	if w < 96 {
		w = 120
	}

	st := newSessionStyles()

	header := m.renderHeader(st, w)
	summary := m.renderSummaryRow(st, s, w)
	main := m.renderMainArea(st, s, w, maxVisible)

	var footerParts []string
	if m.statusMsg != "" {
		footerParts = append(footerParts, st.warning.Render(m.statusMsg))
	}
	footerParts = append(footerParts, st.help.Render("q quit  r refresh  1-9 copy session ID"))

	return strings.Join([]string{
		header,
		summary,
		main,
		strings.Join(footerParts, "\n"),
	}, "\n\n")
}

func (m SessionsModel) renderHeader(st sessionStyles, width int) string {
	title := st.title.Render("k3sc sessions")

	stateText := "LIVE"
	stateStyle := st.badgeOK
	if m.refreshing {
		stateText = "REFRESHING"
		stateStyle = st.badgeWarn
	}
	state := stateStyle.Render(stateText)

	meta := st.meta.Render(fmt.Sprintf(
		"updated %s  •  refresh 30s",
		m.snapshot.GeneratedAt.Local().Format("3:04:05 PM"),
	))

	left := lipgloss.JoinHorizontal(lipgloss.Center, title, " ", state)
	return padBetween(left, meta, width)
}

func (m SessionsModel) renderSummaryRow(st sessionStyles, s *claude.Snapshot, width int) string {
	cards := []string{
		renderStatCard(st, "Live Sessions", fmt.Sprintf("%d", s.SessionCount), fmt.Sprintf("%d with usage", s.UsageCount), st.blue, statCardWidth(width, 4)),
		renderStatCard(st, "Total Cost", formatUSD(s.TotalCostUSD), "local Claude usage", st.green, statCardWidth(width, 4)),
		renderStatCard(st, "Total Tokens", formatInt64(s.TotalTokens), fmt.Sprintf("%s cached", formatInt64(s.CachedTokens)), st.cyan, statCardWidth(width, 4)),
		renderStatCard(st, "Cache Share", percentLabel(percentOf(s.CachedTokens, s.TotalTokens)), fmt.Sprintf("%s output", formatInt64(s.OutputTokens)), st.amber, statCardWidth(width, 4)),
	}

	return lipgloss.JoinHorizontal(lipgloss.Top, joinWithGap(cards, 1)...)
}

func (m SessionsModel) renderMainArea(st sessionStyles, s *claude.Snapshot, width, maxVisible int) string {
	if width >= 160 {
		leftWidth := minInt(56, width/3+8)
		rightWidth := width - leftWidth - 1
		left := m.renderBlockPanel(st, s, leftWidth)
		right := m.renderSessionsPanel(st, s, rightWidth, maxVisible)
		return lipgloss.JoinHorizontal(lipgloss.Top, left, " ", right)
	}

	return lipgloss.JoinVertical(
		lipgloss.Left,
		m.renderBlockPanel(st, s, width),
		"",
		m.renderSessionsPanel(st, s, width, maxVisible),
	)
}

func (m SessionsModel) renderBlockPanel(st sessionStyles, s *claude.Snapshot, outerWidth int) string {
	var lines []string

	switch {
	case s.ActiveBlock != nil:
		block := s.ActiveBlock
		lines = append(lines, st.sectionTitle.Render("Active Block"))
		lines = append(lines, st.subtle.Render(fmt.Sprintf(
			"%s → %s  •  %s remaining",
			sessionTime(block.StartTime, "unknown"),
			sessionTime(block.EndTime, "unknown"),
			minutesLabel(block.RemainingMinutes),
		)))
		lines = append(lines, "")
		lines = append(lines, blockMetricRow(st, "Current", formatInt64(block.TotalTokens), "Cost", formatUSD(block.CostUSD)))
		lines = append(lines, blockMetricRow(st, "Projected", formatInt64(block.ProjectedTokens), "Cost", formatUSD(block.ProjectedCostUSD)))
		lines = append(lines, blockMetricRow(st, "Burn", fmt.Sprintf("%.0f tok/min", block.TokensPerMinute), "Hour", formatUSD(block.CostPerHour)))
		lines = append(lines, "")
		lines = append(lines, st.label.Render("Token Breakdown"))
		lines = append(lines, tokenBreakdownLine(st, "Input", block.InputTokens, st.blue))
		lines = append(lines, tokenBreakdownLine(st, "Output", block.OutputTokens, st.green))
		lines = append(lines, tokenBreakdownLine(st, "Cache Create", block.CacheCreationTokens, st.cyan))
		lines = append(lines, tokenBreakdownLine(st, "Cache Read", block.CacheReadTokens, st.amber))
		if block.TokenLimit > 0 {
			lines = append(lines, "")
			lines = append(lines, st.label.Render("Max Window"))
			lines = append(lines, quotaLine(st, "Current", block.CurrentPercentUsed, block.CurrentRemainingTokens, outerWidth-10, st.green))
			lines = append(lines, quotaLine(st, "Projected", block.ProjectedPercentUsed, block.ProjectedRemainingTokens, outerWidth-10, st.amber))
			statusStyle := st.badgeOK
			if strings.EqualFold(block.TokenLimitStatus, "warning") {
				statusStyle = st.badgeWarn
			} else if strings.EqualFold(block.TokenLimitStatus, "exceeded") || strings.EqualFold(block.TokenLimitStatus, "exceeds") || strings.EqualFold(block.TokenLimitStatus, "error") {
				statusStyle = st.badgeErr
			}
			lines = append(lines, "")
			lines = append(lines, "Status  "+statusStyle.Render(strings.ToUpper(nonEmptyOr(block.TokenLimitStatus, "unknown"))))
			lines = append(lines, fmt.Sprintf("Limit   %s", st.value.Render(formatInt64(block.TokenLimit))))
		}
		if len(block.Models) > 0 {
			lines = append(lines, "")
			lines = append(lines, st.label.Render("Models"))
			lines = append(lines, wrapStyledLines(st.base, strings.Join(block.Models, ", "), maxInt(outerWidth-6, 20))...)
		}
	case s.BlockError != "":
		lines = append(lines, st.sectionTitle.Render("Active Block"))
		lines = append(lines, st.error.Render(s.BlockError))
	default:
		lines = append(lines, st.sectionTitle.Render("Active Block"))
		lines = append(lines, st.subtle.Render("No active block data"))
	}

	return renderPanel(st.panelBlue, outerWidth, lines)
}

func (m SessionsModel) renderSessionsPanel(st sessionStyles, s *claude.Snapshot, outerWidth, maxVisible int) string {
	lines := []string{st.sectionTitle.Render("Live Sessions")}

	if len(s.Sessions) == 0 {
		lines = append(lines, st.subtle.Render("No running claude.exe processes"))
		return renderPanel(st.panelNeutral, outerWidth, lines)
	}

	visible := s.Sessions
	if len(visible) > maxVisible {
		visible = visible[:maxVisible]
	}

	header := fmt.Sprintf(
		" %-3s %-7s %-14s %-10s %-12s %-12s %-9s %-9s",
		"#", "PID", "Project", "Cost", "Total", "Cached", "Output", "Last",
	)
	lines = append(lines, st.tableHeader.Render(trimToWidth(header, outerWidth-4)))

	for idx, session := range visible {
		project := sessionProject(session.CWD)
		row := fmt.Sprintf(
			" %-3d %-7d %-14s %-10s %-12s %-12s %-9s %-9s",
			idx+1,
			session.PID,
			trimToWidth(project, 14),
			formatUSD(session.TotalCostUSD),
			formatInt64(session.TotalTokens),
			formatInt64(session.CachedTokens),
			formatInt64(session.OutputTokens),
			shortTime(session.LastActivity),
		)

		rowStyle := st.base
		switch {
		case session.MetadataError != "":
			rowStyle = st.error
		case session.UsageError != "":
			rowStyle = st.warning
		default:
			rowStyle = st.ok
		}
		lines = append(lines, rowStyle.Render(trimToWidth(row, outerWidth-4)))

		sessionID := "id " + trimMiddle(nonEmptyOr(session.SessionID, "<missing session ID>"), maxInt(outerWidth/2, 24))
		cwd := "cwd " + trimMiddle(nonEmptyOr(session.CWD, "<missing cwd>"), maxInt(outerWidth-10, 24))
		lines = append(lines, st.subtle.Render(trimToWidth(sessionID+"  •  "+cwd, outerWidth-4)))

		var detailParts []string
		if session.Name != "" {
			detailParts = append(detailParts, session.Name)
		}
		if len(session.Models) > 0 {
			detailParts = append(detailParts, strings.Join(session.Models, ", "))
		}
		if session.StartedAt != nil {
			detailParts = append(detailParts, "started "+sessionTime(session.StartedAt, "unknown"))
		}
		if len(detailParts) > 0 {
			lines = append(lines, st.subtle.Render(trimToWidth(strings.Join(detailParts, "  •  "), outerWidth-4)))
		}

		if session.MetadataError != "" {
			lines = append(lines, st.error.Render(trimToWidth("metadata: "+session.MetadataError, outerWidth-4)))
		}
		if session.UsageError != "" {
			lines = append(lines, st.warning.Render(trimToWidth("usage: "+session.UsageError, outerWidth-4)))
		}

		if idx < len(visible)-1 {
			lines = append(lines, st.divider.Render(strings.Repeat("─", maxInt(outerWidth-4, 10))))
		}
	}

	if len(s.Sessions) > len(visible) {
		lines = append(lines, st.subtle.Render(fmt.Sprintf("+ %d more session(s)", len(s.Sessions)-len(visible))))
	}

	return renderPanel(st.panelNeutral, outerWidth, lines)
}

type sessionStyles struct {
	title        lipgloss.Style
	meta         lipgloss.Style
	sectionTitle lipgloss.Style
	label        lipgloss.Style
	value        lipgloss.Style
	base         lipgloss.Style
	subtle       lipgloss.Style
	help         lipgloss.Style
	ok           lipgloss.Style
	warning      lipgloss.Style
	error        lipgloss.Style
	tableHeader  lipgloss.Style
	divider      lipgloss.Style
	panelBlue    lipgloss.Style
	panelNeutral lipgloss.Style
	badgeOK      lipgloss.Style
	badgeWarn    lipgloss.Style
	badgeErr     lipgloss.Style
	blue         lipgloss.Color
	green        lipgloss.Color
	cyan         lipgloss.Color
	amber        lipgloss.Color
	red          lipgloss.Color
}

func newSessionStyles() sessionStyles {
	blue := lipgloss.Color("#4C7DFF")
	green := lipgloss.Color("#33B36B")
	cyan := lipgloss.Color("#34C3D9")
	amber := lipgloss.Color("#E1A73C")
	red := lipgloss.Color("#E05D5D")
	ink := lipgloss.Color("#F4F7FA")
	muted := lipgloss.Color("8")
	surface := lipgloss.Color("#B0B8C5")

	return sessionStyles{
		title:        lipgloss.NewStyle().Bold(true).Foreground(ink),
		meta:         lipgloss.NewStyle().Foreground(muted),
		sectionTitle: lipgloss.NewStyle().Bold(true).Foreground(ink),
		label:        lipgloss.NewStyle().Foreground(cyan),
		value:        lipgloss.NewStyle().Bold(true).Foreground(ink),
		base:         lipgloss.NewStyle().Foreground(ink),
		subtle:       lipgloss.NewStyle().Foreground(muted),
		help:         lipgloss.NewStyle().Foreground(muted),
		ok:           lipgloss.NewStyle().Foreground(green),
		warning:      lipgloss.NewStyle().Foreground(amber),
		error:        lipgloss.NewStyle().Foreground(red),
		tableHeader:  lipgloss.NewStyle().Bold(true).Foreground(cyan),
		divider:      lipgloss.NewStyle().Foreground(surface),
		panelBlue:    lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(blue).Padding(0, 1),
		panelNeutral: lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(surface).Padding(0, 1),
		badgeOK:      lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(green).Padding(0, 1),
		badgeWarn:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(amber).Padding(0, 1),
		badgeErr:     lipgloss.NewStyle().Bold(true).Foreground(ink).Background(red).Padding(0, 1),
		blue:         blue,
		green:        green,
		cyan:         cyan,
		amber:        amber,
		red:          red,
	}
}

func renderStatCard(st sessionStyles, title, value, subtitle string, accent lipgloss.Color, outerWidth int) string {
	innerWidth := maxInt(outerWidth-4, 12)
	style := lipgloss.NewStyle().
		Width(innerWidth).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(accent).
		Padding(0, 1)

	content := []string{
		lipgloss.NewStyle().Foreground(accent).Bold(true).Render(title),
		lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#F4F7FA")).Render(value),
		st.subtle.Render(subtitle),
	}
	return style.Render(strings.Join(content, "\n"))
}

func renderPanel(style lipgloss.Style, outerWidth int, lines []string) string {
	innerWidth := maxInt(outerWidth-4, 16)
	content := make([]string, 0, len(lines))
	for _, line := range lines {
		content = append(content, trimToWidth(line, innerWidth))
	}
	return style.Width(innerWidth).Render(strings.Join(content, "\n"))
}

func blockMetricRow(st sessionStyles, leftLabel, leftValue, rightLabel, rightValue string) string {
	left := fmt.Sprintf("%-10s %s", leftLabel, st.value.Render(leftValue))
	right := fmt.Sprintf("%-6s %s", rightLabel, st.value.Render(rightValue))
	return left + "   " + right
}

func tokenBreakdownLine(st sessionStyles, label string, value int64, accent lipgloss.Color) string {
	return lipgloss.NewStyle().Foreground(accent).Render(fmt.Sprintf("%-12s %s", label, formatInt64(value)))
}

func quotaLine(st sessionStyles, label string, percent float64, remaining int64, availableWidth int, accent lipgloss.Color) string {
	barWidth := maxInt(minInt(availableWidth-30, 24), 10)
	bar := progressBar(percent, barWidth, accent)
	return fmt.Sprintf(
		"%-9s %s %5.1f%%  left %s",
		label,
		bar,
		percent,
		formatInt64(remaining),
	)
}

func progressBar(percent float64, width int, accent lipgloss.Color) string {
	if width < 4 {
		width = 4
	}
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	filled := int(math.Round((percent / 100) * float64(width)))
	if filled > width {
		filled = width
	}
	if filled < 0 {
		filled = 0
	}
	full := lipgloss.NewStyle().Foreground(accent).Render(strings.Repeat("█", filled))
	empty := lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render(strings.Repeat("░", width-filled))
	return full + empty
}

func wrapStyledLines(style lipgloss.Style, text string, width int) []string {
	var out []string
	for _, line := range wordWrap(text, width) {
		out = append(out, style.Render(line))
	}
	return out
}

func statCardWidth(totalWidth, count int) int {
	gaps := count - 1
	return maxInt((totalWidth-gaps)/count, 18)
}

func joinWithGap(items []string, gap int) []string {
	if len(items) == 0 {
		return nil
	}
	out := make([]string, 0, len(items)*2-1)
	space := strings.Repeat(" ", gap)
	for i, item := range items {
		if i > 0 {
			out = append(out, space)
		}
		out = append(out, item)
	}
	return out
}

func padBetween(left, right string, width int) string {
	leftWidth := lipgloss.Width(left)
	rightWidth := lipgloss.Width(right)
	space := width - leftWidth - rightWidth
	if space < 1 {
		space = 1
	}
	return left + strings.Repeat(" ", space) + right
}

func percentOf(numerator, denominator int64) float64 {
	if denominator <= 0 {
		return 0
	}
	return (float64(numerator) / float64(denominator)) * 100
}

func percentLabel(percent float64) string {
	return fmt.Sprintf("%.1f%%", percent)
}

func formatInt64(n int64) string {
	return humanNumber(n)
}

func humanNumber(n int64) string {
	sign := ""
	if n < 0 {
		sign = "-"
		n = -n
	}
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return sign + s
	}
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	parts = append([]string{s}, parts...)
	return sign + strings.Join(parts, ",")
}

func formatUSD(amount float64) string {
	return fmt.Sprintf("$%.4f", amount)
}

func sessionTime(t *time.Time, fallback string) string {
	if t == nil {
		return fallback
	}
	return format.FmtTime(t)
}

func shortTime(t *time.Time) string {
	if t == nil {
		return "unknown"
	}
	return t.Local().Format("3:04 PM")
}

func minutesLabel(minutes int) string {
	if minutes <= 0 {
		return "0m"
	}
	hours := minutes / 60
	remaining := minutes % 60
	if hours == 0 {
		return fmt.Sprintf("%dm", remaining)
	}
	return fmt.Sprintf("%dh %dm", hours, remaining)
}

func nonEmptyOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func sessionProject(cwd string) string {
	clean := strings.TrimSpace(cwd)
	if clean == "" {
		return "<unknown>"
	}
	base := filepath.Base(clean)
	if base == "." || base == string(filepath.Separator) || base == "" {
		return clean
	}
	return base
}

func trimMiddle(s string, max int) string {
	if max < 7 || len(s) <= max {
		return s
	}
	left := (max - 1) / 2
	right := max - left - 1
	return s[:left] + "…" + s[len(s)-right:]
}

func trimToWidth(s string, width int) string {
	if width <= 0 {
		return s
	}
	plain := lipgloss.Width(s)
	if plain <= width {
		return s
	}
	runes := []rune(stripANSI(s))
	if len(runes) <= width {
		return string(runes)
	}
	if width <= 1 {
		return string(runes[:width])
	}
	return string(runes[:width-1]) + "…"
}

func stripANSI(s string) string {
	var out []rune
	inEscape := false
	for _, r := range s {
		if r == '\x1b' {
			inEscape = true
			continue
		}
		if inEscape {
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
				inEscape = false
			}
			continue
		}
		out = append(out, r)
	}
	return string(out)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
