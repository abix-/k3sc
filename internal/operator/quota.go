package operator

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/abix-/k3sc/internal/k8s"
	coretypes "github.com/abix-/k3sc/internal/types"
	"k8s.io/client-go/kubernetes"
)

var (
	ansiPattern    = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
	oscPattern     = regexp.MustCompile(`\x1b\][^\x07]*(?:\x07|\x1b\\)`)
	percentPattern = regexp.MustCompile(`(?i)(\d{1,3})%\s*(used|left|remaining)`)
)

type familyDispatchState struct {
	Available bool
	Checked   bool
	Reason    string
}

type claudeUsageSnapshot struct {
	SessionPercentLeft *int
	WeeklyPercentLeft  *int
	ModelPercentLeft   *int
	SessionReset       string
	WeeklyReset        string
	ModelReset         string
	Raw                string
}

type codexStatusSnapshot struct {
	Credits             *float64
	FiveHourPercentLeft *int
	WeeklyPercentLeft   *int
	FiveHourReset       string
	WeeklyReset         string
	LastUpdated         time.Time
	Raw                 string
}

type familyProbeResults struct {
	Codex    *codexStatusSnapshot
	CodexErr error
}

type recentUsageLimit struct {
	PodName string
	ResetAt *time.Time
}

func probeFamilyDispatchStates(ctx context.Context, cs *kubernetes.Clientset, lookback time.Duration) (map[coretypes.AgentFamily]familyDispatchState, []string) {
	now := time.Now()
	recent := map[coretypes.AgentFamily]recentUsageLimit{}
	recentChecked := false
	var warnings []string

	limitedPods, logs, err := k8s.FindRecentUsageLimitPods(ctx, cs, lookback)
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("usage limit fallback check error: %v", err))
	} else {
		recentChecked = true
		for family, pod := range limitedPods {
			entry := recentUsageLimit{PodName: pod.Name}
			if logText := logs[pod.Name]; logText != "" {
				if resetAt, ok := k8s.ParseUsageLimitResetTime(now, logText); ok {
					resetAtCopy := resetAt
					entry.ResetAt = &resetAtCopy
				}
			}
			recent[family] = entry
		}
	}

	probes := runFamilyQuotaProbes(ctx)
	states, probeWarnings := buildFamilyDispatchStates(now, lookback, recentChecked, probes, recent)
	warnings = append(warnings, probeWarnings...)
	return states, warnings
}

func runFamilyQuotaProbes(ctx context.Context) familyProbeResults {
	var probes familyProbeResults
	snap, err := probeCodexStatus(ctx)
	probes.CodexErr = err
	if snap != nil {
		probes.Codex = snap
	}
	return probes
}

func buildFamilyDispatchStates(now time.Time, lookback time.Duration, recentChecked bool, probes familyProbeResults, recent map[coretypes.AgentFamily]recentUsageLimit) (map[coretypes.AgentFamily]familyDispatchState, []string) {
	states := map[coretypes.AgentFamily]familyDispatchState{
		coretypes.FamilyClaude: fallbackDispatchState(now, lookback, recentChecked, recent[coretypes.FamilyClaude]),
		coretypes.FamilyCodex:  fallbackDispatchState(now, lookback, recentChecked, recent[coretypes.FamilyCodex]),
	}

	var warnings []string

	if probes.CodexErr != nil {
		warnings = append(warnings, fmt.Sprintf("codex quota probe error: %v", probes.CodexErr))
		// Don't override -- fallback state from initial build already accounts for recent failures
	} else if probes.Codex != nil {
		state := familyDispatchState{
			Available: true,
			Checked:   true,
			Reason:    codexStatusSummary(probes.Codex),
		}
		if reason := codexLimitReason(probes.Codex); reason != "" {
			state.Available = false
			state.Reason = reason
		}
		states[coretypes.FamilyCodex] = state
	}

	for _, family := range []coretypes.AgentFamily{coretypes.FamilyClaude, coretypes.FamilyCodex} {
		state := states[family]
		if state.Checked {
			continue
		}
		recentLimit, ok := recent[family]
		if !ok || !recentLimitActive(now, recentLimit) {
			continue
		}
		state.Available = false
		state.Reason = recentUsageLimitReason(recentLimit)
		states[family] = state
	}

	return states, warnings
}

func fallbackDispatchState(now time.Time, lookback time.Duration, recentChecked bool, recent recentUsageLimit) familyDispatchState {
	if !recentChecked {
		return familyDispatchState{
			Available: true,
			Reason:    "recent usage-limit fallback unavailable",
		}
	}

	if recentLimitActive(now, recent) {
		return familyDispatchState{
			Available: false,
			Checked:   true,
			Reason:    recentUsageLimitReason(recent),
		}
	}

	return familyDispatchState{
		Available: true,
		Checked:   true,
		Reason:    noRecentUsageLimitReason(lookback),
	}
}

func recentLimitActive(now time.Time, limit recentUsageLimit) bool {
	if limit.PodName == "" {
		return false
	}
	if limit.ResetAt != nil && !limit.ResetAt.After(now) {
		return false
	}
	return true
}

func dispatchFamilyStatuses(states map[coretypes.AgentFamily]familyDispatchState) []DispatchFamilyStatus {
	if len(states) == 0 {
		return nil
	}

	var result []DispatchFamilyStatus
	for _, family := range []coretypes.AgentFamily{coretypes.FamilyClaude, coretypes.FamilyCodex} {
		state, ok := states[family]
		if !ok {
			continue
		}
		result = append(result, DispatchFamilyStatus{
			Family:    string(family),
			Available: state.Available,
			Checked:   state.Checked,
			Reason:    state.Reason,
		})
	}
	return result
}

func recentUsageLimitReason(limit recentUsageLimit) string {
	if limit.ResetAt != nil {
		return fmt.Sprintf("recent usage-limit failure in pod %s (resets %s)", limit.PodName, limit.ResetAt.Local().Format(time.RFC822))
	}
	return fmt.Sprintf("recent usage-limit failure in pod %s", limit.PodName)
}

func noRecentUsageLimitReason(lookback time.Duration) string {
	return fmt.Sprintf("no recent usage-limit failures in last %s", formatLookback(lookback))
}

func codexStatusSummary(snap *codexStatusSnapshot) string {
	if snap == nil {
		return ""
	}

	var parts []string
	if snap.FiveHourPercentLeft != nil {
		parts = append(parts, fmt.Sprintf("5h %d%% left", *snap.FiveHourPercentLeft))
	}
	if snap.WeeklyPercentLeft != nil {
		parts = append(parts, fmt.Sprintf("weekly %d%% left", *snap.WeeklyPercentLeft))
	}
	if !snap.LastUpdated.IsZero() {
		parts = append(parts, fmt.Sprintf("updated %s", snap.LastUpdated.Local().Format(time.RFC822)))
	}
	if len(parts) == 0 {
		return "shared session data available"
	}
	return "shared session data: " + strings.Join(parts, ", ")
}

func claudeLimitReason(snap *claudeUsageSnapshot) string {
	if snap == nil {
		return ""
	}

	windows := []struct {
		name  string
		left  *int
		reset string
	}{
		{name: "Current session", left: snap.SessionPercentLeft, reset: snap.SessionReset},
		{name: "Current week (all models)", left: snap.WeeklyPercentLeft, reset: snap.WeeklyReset},
		{name: "Current week (model-specific)", left: snap.ModelPercentLeft, reset: snap.ModelReset},
	}

	for _, window := range windows {
		if window.left == nil || *window.left > 0 {
			continue
		}
		if window.reset != "" {
			return fmt.Sprintf("%s reports 0%% left (%s)", window.name, window.reset)
		}
		return fmt.Sprintf("%s reports 0%% left", window.name)
	}

	return ""
}

func codexLimitReason(snap *codexStatusSnapshot) string {
	if snap == nil {
		return ""
	}

	windows := []struct {
		name  string
		left  *int
		reset string
	}{
		{name: "5h limit", left: snap.FiveHourPercentLeft, reset: snap.FiveHourReset},
		{name: "Weekly limit", left: snap.WeeklyPercentLeft, reset: snap.WeeklyReset},
	}

	for _, window := range windows {
		if window.left == nil || *window.left > 0 {
			continue
		}
		if window.reset != "" {
			return fmt.Sprintf("%s reports 0%% left (%s)", window.name, window.reset)
		}
		return fmt.Sprintf("%s reports 0%% left", window.name)
	}

	return ""
}

func parseClaudeUsage(text string) (*claudeUsageSnapshot, error) {
	clean := cleanTerminalText(text)
	if clean == "" {
		return nil, fmt.Errorf("empty claude usage output")
	}

	lower := strings.ToLower(clean)
	if strings.Contains(lower, "rate_limit_error") || strings.Contains(lower, "you're out of extra usage") {
		zero := 0
		return &claudeUsageSnapshot{
			SessionPercentLeft: &zero,
			Raw:                clean,
		}, nil
	}
	if strings.Contains(lower, "subscription plans") {
		return nil, fmt.Errorf("claude /usage is unavailable for API-authenticated sessions")
	}

	sessionLeft, sessionReset := extractClaudeSection(clean, []string{"currentsession", "curretsession"})
	weeklyLeft, weeklyReset := extractClaudeSection(clean, []string{"currentweekallmodels"})
	modelLeft, modelReset := extractClaudeSection(clean, []string{"currentweeksonnetonly", "currentweeksonnet", "currentweekopus"})

	if sessionLeft == nil {
		return nil, fmt.Errorf("missing Current session in claude usage output")
	}

	return &claudeUsageSnapshot{
		SessionPercentLeft: sessionLeft,
		WeeklyPercentLeft:  weeklyLeft,
		ModelPercentLeft:   modelLeft,
		SessionReset:       sessionReset,
		WeeklyReset:        weeklyReset,
		ModelReset:         modelReset,
		Raw:                clean,
	}, nil
}

func parseCodexStatus(text string) (*codexStatusSnapshot, error) {
	clean := cleanTerminalText(text)
	if clean == "" {
		return nil, fmt.Errorf("empty codex status output")
	}

	lower := strings.ToLower(clean)
	if strings.Contains(lower, "data not available yet") {
		return nil, fmt.Errorf("codex status data not available yet")
	}
	if strings.Contains(lower, "application panicked") && strings.Contains(lower, "/status") {
		return nil, fmt.Errorf("codex /status crashed in the current CLI build")
	}
	if strings.Contains(lower, "updating codex via") {
		return nil, fmt.Errorf("codex attempted a self-update before rendering /status")
	}
	if strings.Contains(lower, "update available") && strings.Contains(lower, "codex") {
		return nil, fmt.Errorf("codex CLI update required before /status can render")
	}

	lines := strings.Split(clean, "\n")
	var credits *float64
	var fiveHourLine, weeklyLine string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		lowerLine := strings.ToLower(trimmed)
		switch {
		case strings.Contains(lowerLine, "credits:"):
			if credits == nil {
				if value := parseCredits(trimmed); value != nil {
					credits = value
				}
			}
		case strings.Contains(lowerLine, "5h limit"):
			fiveHourLine = trimmed
		case strings.Contains(lowerLine, "weekly limit"):
			weeklyLine = trimmed
		}
	}

	fiveHourLeft := extractPercentLeft(fiveHourLine)
	weeklyLeft := extractPercentLeft(weeklyLine)
	if credits == nil && fiveHourLeft == nil && weeklyLeft == nil {
		return nil, fmt.Errorf("missing Codex credits and limit rows")
	}

	return &codexStatusSnapshot{
		Credits:             credits,
		FiveHourPercentLeft: fiveHourLeft,
		WeeklyPercentLeft:   weeklyLeft,
		FiveHourReset:       extractReset(fiveHourLine),
		WeeklyReset:         extractReset(weeklyLine),
		Raw:                 clean,
	}, nil
}

func extractClaudeSection(text string, labels []string) (*int, string) {
	lines := strings.Split(text, "\n")
	normalizedLabels := make([]string, len(labels))
	for i, label := range labels {
		normalizedLabels[i] = normalizeLabel(label)
	}

	matchIndex := -1
	for i, line := range lines {
		normalized := normalizeLabel(line)
		for _, label := range normalizedLabels {
			if strings.Contains(normalized, label) {
				matchIndex = i
			}
		}
	}
	if matchIndex == -1 {
		return nil, ""
	}

	end := len(lines)
	limit := matchIndex + 5
	if limit < end {
		end = limit
	}
	for i := matchIndex + 1; i < end; i++ {
		if isClaudeSectionLabel(lines[i]) {
			end = i
			break
		}
	}

	block := strings.Join(lines[matchIndex:end], "\n")
	return extractPercentLeft(block), extractReset(block)
}

func isClaudeSectionLabel(line string) bool {
	normalized := normalizeLabel(line)
	switch {
	case strings.Contains(normalized, "currentsession"):
		return true
	case strings.Contains(normalized, "curretsession"):
		return true
	case strings.Contains(normalized, "currentweekallmodels"):
		return true
	case strings.Contains(normalized, "currentweeksonnetonly"):
		return true
	case strings.Contains(normalized, "currentweeksonnet"):
		return true
	case strings.Contains(normalized, "currentweekopus"):
		return true
	default:
		return false
	}
}

func extractPercentLeft(text string) *int {
	matches := percentPattern.FindStringSubmatch(text)
	if len(matches) != 3 {
		return nil
	}

	value, err := strconv.Atoi(matches[1])
	if err != nil {
		return nil
	}
	if value < 0 {
		value = 0
	}
	if value > 100 {
		value = 100
	}

	if strings.EqualFold(matches[2], "used") {
		value = 100 - value
	}
	return &value
}

func extractReset(text string) string {
	for _, line := range strings.Split(text, "\n") {
		lower := strings.ToLower(line)
		idx := strings.Index(lower, "resets")
		if idx == -1 {
			continue
		}

		reset := strings.TrimSpace(line[idx:])
		for strings.HasSuffix(reset, ")") && strings.Count(reset, "(") < strings.Count(reset, ")") {
			reset = strings.TrimSuffix(reset, ")")
		}
		return strings.TrimSpace(reset)
	}
	return ""
}

func parseCredits(line string) *float64 {
	idx := strings.Index(strings.ToLower(line), "credits:")
	if idx == -1 {
		return nil
	}

	fields := strings.Fields(line[idx+len("credits:"):])
	if len(fields) == 0 {
		return nil
	}

	value, err := strconv.ParseFloat(strings.ReplaceAll(fields[0], ",", ""), 64)
	if err != nil {
		return nil
	}
	return &value
}

func cleanTerminalText(text string) string {
	clean := strings.ReplaceAll(text, "\r\n", "\n")
	clean = strings.ReplaceAll(clean, "\r", "\n")
	clean = oscPattern.ReplaceAllString(clean, "")
	clean = ansiPattern.ReplaceAllString(clean, "")
	return strings.TrimSpace(clean)
}

func normalizeLabel(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func formatLookback(d time.Duration) string {
	if d%(time.Hour) == 0 {
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	if d%(time.Minute) == 0 {
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	return d.String()
}
