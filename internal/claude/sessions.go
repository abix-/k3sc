package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const claudeProcessScript = `$processes = @(Get-CimInstance Win32_Process | Where-Object { $_.Name -ieq 'claude.exe' } | Select-Object ProcessId,Name,CommandLine)
@{ processes = @($processes) } | ConvertTo-Json -Depth 4`

type Snapshot struct {
	GeneratedAt         time.Time     `json:"generatedAt"`
	SessionCount        int           `json:"sessionCount"`
	MetadataCount       int           `json:"metadataCount"`
	UsageCount          int           `json:"usageCount"`
	TotalCostUSD        float64       `json:"totalCostUSD"`
	TotalTokens         int64         `json:"totalTokens"`
	InputTokens         int64         `json:"inputTokens"`
	CacheCreationTokens int64         `json:"cacheCreationTokens"`
	CacheReadTokens     int64         `json:"cacheReadTokens"`
	CachedTokens        int64         `json:"cachedTokens"`
	OutputTokens        int64         `json:"outputTokens"`
	ActiveBlock         *BlockInfo    `json:"activeBlock,omitempty"`
	BlockError          string        `json:"blockError,omitempty"`
	Timings             TimingInfo    `json:"timings"`
	Sessions            []SessionInfo `json:"sessions"`
}

type TimingInfo struct {
	TotalMS            float64 `json:"totalMs"`
	ProcessScanMS      float64 `json:"processScanMs"`
	MetadataReadMS     float64 `json:"metadataReadMs"`
	SessionUsageWallMS float64 `json:"sessionUsageWallMs"`
	SessionUsageSumMS  float64 `json:"sessionUsageSumMs"`
	ActiveBlockMS      float64 `json:"activeBlockMs"`
}

type BlockInfo struct {
	ID                       string     `json:"id,omitempty"`
	StartTime                *time.Time `json:"startTime,omitempty"`
	EndTime                  *time.Time `json:"endTime,omitempty"`
	ActualEndTime            *time.Time `json:"actualEndTime,omitempty"`
	IsActive                 bool       `json:"isActive"`
	Entries                  int        `json:"entries"`
	InputTokens              int64      `json:"inputTokens"`
	OutputTokens             int64      `json:"outputTokens"`
	CacheCreationTokens      int64      `json:"cacheCreationTokens"`
	CacheReadTokens          int64      `json:"cacheReadTokens"`
	TotalTokens              int64      `json:"totalTokens"`
	CostUSD                  float64    `json:"costUSD"`
	Models                   []string   `json:"models,omitempty"`
	TokensPerMinute          float64    `json:"tokensPerMinute"`
	CostPerHour              float64    `json:"costPerHour"`
	ProjectedTokens          int64      `json:"projectedTokens"`
	ProjectedCostUSD         float64    `json:"projectedCostUSD"`
	RemainingMinutes         int        `json:"remainingMinutes"`
	TokenLimit               int64      `json:"tokenLimit"`
	CurrentPercentUsed       float64    `json:"currentPercentUsed"`
	CurrentRemainingTokens   int64      `json:"currentRemainingTokens"`
	ProjectedUsage           int64      `json:"projectedUsage"`
	ProjectedPercentUsed     float64    `json:"projectedPercentUsed"`
	ProjectedRemainingTokens int64      `json:"projectedRemainingTokens"`
	TokenLimitStatus         string     `json:"tokenLimitStatus,omitempty"`
}

type SessionInfo struct {
	PID                 int        `json:"pid"`
	SessionID           string     `json:"sessionId,omitempty"`
	CWD                 string     `json:"cwd,omitempty"`
	StartedAt           *time.Time `json:"startedAt,omitempty"`
	LastActivity        *time.Time `json:"lastActivity,omitempty"`
	Kind                string     `json:"kind,omitempty"`
	Entrypoint          string     `json:"entrypoint,omitempty"`
	Name                string     `json:"name,omitempty"`
	CommandLine         string     `json:"commandLine,omitempty"`
	MetadataFile        string     `json:"metadataFile,omitempty"`
	HasMetadata         bool       `json:"hasMetadata"`
	MetadataError       string     `json:"metadataError,omitempty"`
	HasUsage            bool       `json:"hasUsage"`
	UsageError          string     `json:"usageError,omitempty"`
	Models              []string   `json:"models,omitempty"`
	EntryCount          int        `json:"entryCount"`
	TotalCostUSD        float64    `json:"totalCostUSD"`
	TotalTokens         int64      `json:"totalTokens"`
	InputTokens         int64      `json:"inputTokens"`
	CacheCreationTokens int64      `json:"cacheCreationTokens"`
	CacheReadTokens     int64      `json:"cacheReadTokens"`
	CachedTokens        int64      `json:"cachedTokens"`
	OutputTokens        int64      `json:"outputTokens"`
	UsageDurationMS     float64    `json:"usageDurationMs,omitempty"`
}

type processList struct {
	Processes []claudeProcess `json:"processes"`
}

type claudeProcess struct {
	ProcessID   int    `json:"ProcessId"`
	Name        string `json:"Name"`
	CommandLine string `json:"CommandLine"`
}

type sessionMetadata struct {
	PID        int    `json:"pid"`
	SessionID  string `json:"sessionId"`
	CWD        string `json:"cwd"`
	StartedAt  int64  `json:"startedAt"`
	Kind       string `json:"kind"`
	Entrypoint string `json:"entrypoint"`
	Name       string `json:"name"`
}

type ccusageReport struct {
	SessionID   string         `json:"sessionId"`
	TotalCost   float64        `json:"totalCost"`
	TotalTokens int64          `json:"totalTokens"`
	Entries     []ccusageEntry `json:"entries"`
}

type ccusageBlocksReport struct {
	Blocks []ccusageBlock `json:"blocks"`
}

type ccusageEntry struct {
	Timestamp           string  `json:"timestamp"`
	InputTokens         int64   `json:"inputTokens"`
	OutputTokens        int64   `json:"outputTokens"`
	CacheCreationTokens int64   `json:"cacheCreationTokens"`
	CacheReadTokens     int64   `json:"cacheReadTokens"`
	Model               string  `json:"model"`
	CostUSD             float64 `json:"costUSD"`
}

type ccusageBlock struct {
	ID            string `json:"id"`
	StartTime     string `json:"startTime"`
	EndTime       string `json:"endTime"`
	ActualEndTime string `json:"actualEndTime"`
	IsActive      bool   `json:"isActive"`
	IsGap         bool   `json:"isGap"`
	Entries       int    `json:"entries"`
	TokenCounts   struct {
		InputTokens              int64 `json:"inputTokens"`
		OutputTokens             int64 `json:"outputTokens"`
		CacheCreationInputTokens int64 `json:"cacheCreationInputTokens"`
		CacheReadInputTokens     int64 `json:"cacheReadInputTokens"`
	} `json:"tokenCounts"`
	TotalTokens int64    `json:"totalTokens"`
	CostUSD     float64  `json:"costUSD"`
	Models      []string `json:"models"`
	BurnRate    struct {
		TokensPerMinute             float64 `json:"tokensPerMinute"`
		TokensPerMinuteForIndicator float64 `json:"tokensPerMinuteForIndicator"`
		CostPerHour                 float64 `json:"costPerHour"`
	} `json:"burnRate"`
	Projection struct {
		TotalTokens      int64   `json:"totalTokens"`
		TotalCost        float64 `json:"totalCost"`
		RemainingMinutes int     `json:"remainingMinutes"`
	} `json:"projection"`
	TokenLimitStatus struct {
		Limit          int64   `json:"limit"`
		ProjectedUsage int64   `json:"projectedUsage"`
		PercentUsed    float64 `json:"percentUsed"`
		Status         string  `json:"status"`
	} `json:"tokenLimitStatus"`
}

type usageSummary struct {
	Models              []string
	EntryCount          int
	TotalCostUSD        float64
	TotalTokens         int64
	InputTokens         int64
	CacheCreationTokens int64
	CacheReadTokens     int64
	CachedTokens        int64
	OutputTokens        int64
	LastActivity        *time.Time
}

func CollectSessions() (*Snapshot, error) {
	totalStart := time.Now()
	if runtime.GOOS != "windows" {
		return nil, errors.New("k3sc sessions is only supported on Windows")
	}
	if _, err := exec.LookPath("ccusage"); err != nil {
		return nil, fmt.Errorf("ccusage not found in PATH: %w", err)
	}

	processScanStart := time.Now()
	processes, err := listClaudeProcesses()
	processScanDuration := time.Since(processScanStart)
	if err != nil {
		return nil, err
	}
	if len(processes) == 0 {
		return nil, errors.New("no running claude.exe processes found")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home directory: %w", err)
	}
	sessionsDir := filepath.Join(home, ".claude", "sessions")

	snapshot := &Snapshot{
		GeneratedAt: time.Now(),
		Sessions:    make([]SessionInfo, 0, len(processes)),
	}
	snapshot.Timings.ProcessScanMS = durationMS(processScanDuration)

	var (
		blockWG sync.WaitGroup
	)
	blockWG.Add(1)
	go func() {
		defer blockWG.Done()
		blockStart := time.Now()
		block, err := loadActiveBlock()
		snapshot.Timings.ActiveBlockMS = durationMS(time.Since(blockStart))
		if err != nil {
			snapshot.BlockError = err.Error()
			return
		}
		snapshot.ActiveBlock = block
	}()

	metadataStart := time.Now()
	for _, proc := range processes {
		session := SessionInfo{
			PID:         proc.ProcessID,
			CommandLine: strings.TrimSpace(proc.CommandLine),
			MetadataFile: filepath.Join(sessionsDir,
				fmt.Sprintf("%d.json", proc.ProcessID)),
		}

		meta, err := readSessionMetadata(session.MetadataFile)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				session.MetadataError = "missing Claude session metadata"
			} else {
				session.MetadataError = fmt.Sprintf("read metadata: %v", err)
			}
			snapshot.Sessions = append(snapshot.Sessions, session)
			continue
		}

		session.HasMetadata = true
		session.SessionID = strings.TrimSpace(meta.SessionID)
		session.CWD = strings.TrimSpace(meta.CWD)
		session.Kind = strings.TrimSpace(meta.Kind)
		session.Entrypoint = strings.TrimSpace(meta.Entrypoint)
		session.Name = strings.TrimSpace(meta.Name)
		if meta.StartedAt > 0 {
			started := time.UnixMilli(meta.StartedAt)
			session.StartedAt = &started
		}
		if session.SessionID == "" {
			session.MetadataError = "session metadata missing sessionId"
			snapshot.Sessions = append(snapshot.Sessions, session)
			continue
		}

		snapshot.Sessions = append(snapshot.Sessions, session)
	}
	snapshot.Timings.MetadataReadMS = durationMS(time.Since(metadataStart))

	sessionUsageWall, sessionUsageSum := loadSessionUsage(snapshot.Sessions)
	snapshot.Timings.SessionUsageWallMS = durationMS(sessionUsageWall)
	snapshot.Timings.SessionUsageSumMS = durationMS(sessionUsageSum)
	blockWG.Wait()

	sortSessions(snapshot.Sessions)
	summarizeSnapshot(snapshot)
	snapshot.Timings.TotalMS = durationMS(time.Since(totalStart))
	return snapshot, nil
}

func listClaudeProcesses() ([]claudeProcess, error) {
	cmd := exec.Command("powershell", "-NoProfile", "-Command", claudeProcessScript)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("list claude.exe processes: %s", formatCommandError(err, out))
	}

	var payload processList
	if err := json.Unmarshal(trimBOM(out), &payload); err != nil {
		return nil, fmt.Errorf("parse PowerShell process output: %w", err)
	}
	return payload.Processes, nil
}

func readSessionMetadata(path string) (*sessionMetadata, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var meta sessionMetadata
	if err := json.Unmarshal(trimBOM(data), &meta); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &meta, nil
}

func loadUsageReport(sessionID string) (*ccusageReport, error) {
	cmd := exec.Command("ccusage", "session", "--json", "-i", sessionID, "--offline")
	out, stderr, err := runCommandJSON(cmd)
	if err != nil {
		return nil, fmt.Errorf("ccusage %s: %s", sessionID, formatCommandError(err, stderr))
	}

	var report ccusageReport
	if err := json.Unmarshal(trimBOM(out), &report); err != nil {
		return nil, err
	}
	return &report, nil
}

func loadUsageSummary(sessionID string) (*usageSummary, error) {
	report, err := loadUsageReport(sessionID)
	if err != nil {
		return nil, err
	}

	summary, err := summarizeCCUsageReport(report)
	if err != nil {
		return nil, fmt.Errorf("parse ccusage %s: %w", sessionID, err)
	}
	return summary, nil
}

func loadActiveBlockViaCCUsage() (*BlockInfo, error) {
	cmd := exec.Command("ccusage", "blocks", "--active", "--offline", "--json", "--token-limit", "max")
	out, stderr, err := runCommandJSON(cmd)
	if err != nil {
		return nil, fmt.Errorf("ccusage active block: %s", formatCommandError(err, stderr))
	}

	var report ccusageBlocksReport
	if err := json.Unmarshal(trimBOM(out), &report); err != nil {
		return nil, err
	}
	if len(report.Blocks) == 0 {
		return nil, errors.New("no active block data returned")
	}

	block := report.Blocks[0]
	info := &BlockInfo{
		ID:                   block.ID,
		IsActive:             block.IsActive,
		Entries:              block.Entries,
		InputTokens:          block.TokenCounts.InputTokens,
		OutputTokens:         block.TokenCounts.OutputTokens,
		CacheCreationTokens:  block.TokenCounts.CacheCreationInputTokens,
		CacheReadTokens:      block.TokenCounts.CacheReadInputTokens,
		TotalTokens:          block.TotalTokens,
		CostUSD:              block.CostUSD,
		Models:               append([]string(nil), block.Models...),
		TokensPerMinute:      block.BurnRate.TokensPerMinute,
		CostPerHour:          block.BurnRate.CostPerHour,
		ProjectedTokens:      block.Projection.TotalTokens,
		ProjectedCostUSD:     block.Projection.TotalCost,
		RemainingMinutes:     block.Projection.RemainingMinutes,
		TokenLimit:           block.TokenLimitStatus.Limit,
		ProjectedUsage:       block.TokenLimitStatus.ProjectedUsage,
		ProjectedPercentUsed: block.TokenLimitStatus.PercentUsed,
		TokenLimitStatus:     block.TokenLimitStatus.Status,
	}
	info.StartTime = parseOptionalTime(block.StartTime)
	info.EndTime = parseOptionalTime(block.EndTime)
	info.ActualEndTime = parseOptionalTime(block.ActualEndTime)

	if info.TokenLimit > 0 {
		info.CurrentPercentUsed = (float64(info.TotalTokens) / float64(info.TokenLimit)) * 100
		info.CurrentRemainingTokens = maxInt64(info.TokenLimit-info.TotalTokens, 0)
		projected := info.ProjectedUsage
		if projected == 0 && info.ProjectedTokens > 0 {
			projected = info.ProjectedTokens
			info.ProjectedUsage = projected
		}
		if projected > 0 {
			info.ProjectedRemainingTokens = maxInt64(info.TokenLimit-projected, 0)
		}
	}

	return info, nil
}

func parseCCUsageSummary(raw []byte) (*usageSummary, error) {
	var report ccusageReport
	if err := json.Unmarshal(trimBOM(raw), &report); err != nil {
		return nil, err
	}
	return summarizeCCUsageReport(&report)
}

func summarizeCCUsageReport(report *ccusageReport) (*usageSummary, error) {
	if report == nil {
		return nil, errors.New("nil ccusage report")
	}

	modelSet := make(map[string]struct{})
	summary := &usageSummary{
		EntryCount:   len(report.Entries),
		TotalCostUSD: report.TotalCost,
		TotalTokens:  report.TotalTokens,
	}

	for _, entry := range report.Entries {
		summary.InputTokens += entry.InputTokens
		summary.CacheCreationTokens += entry.CacheCreationTokens
		summary.CacheReadTokens += entry.CacheReadTokens
		summary.OutputTokens += entry.OutputTokens
		if entry.Model != "" {
			modelSet[entry.Model] = struct{}{}
		}
		if entry.Timestamp == "" {
			continue
		}
		timestamp, err := time.Parse(time.RFC3339Nano, entry.Timestamp)
		if err != nil {
			continue
		}
		if summary.LastActivity == nil || timestamp.After(*summary.LastActivity) {
			ts := timestamp
			summary.LastActivity = &ts
		}
	}

	summary.CachedTokens = summary.CacheCreationTokens + summary.CacheReadTokens
	if len(modelSet) > 0 {
		summary.Models = make([]string, 0, len(modelSet))
		for model := range modelSet {
			summary.Models = append(summary.Models, model)
		}
		sort.Strings(summary.Models)
	}
	return summary, nil
}

func loadSessionUsage(sessions []SessionInfo) (time.Duration, time.Duration) {
	start := time.Now()
	var wg sync.WaitGroup
	var sumMu sync.Mutex
	var total time.Duration
	for idx := range sessions {
		if sessions[idx].SessionID == "" {
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sessionStart := time.Now()
			usage, err := loadUsageSummary(sessions[i].SessionID)
			duration := time.Since(sessionStart)
			sessions[i].UsageDurationMS = durationMS(duration)
			sumMu.Lock()
			total += duration
			sumMu.Unlock()
			if err != nil {
				sessions[i].UsageError = err.Error()
				return
			}

			sessions[i].HasUsage = true
			sessions[i].Models = usage.Models
			sessions[i].EntryCount = usage.EntryCount
			sessions[i].TotalCostUSD = usage.TotalCostUSD
			sessions[i].TotalTokens = usage.TotalTokens
			sessions[i].InputTokens = usage.InputTokens
			sessions[i].CacheCreationTokens = usage.CacheCreationTokens
			sessions[i].CacheReadTokens = usage.CacheReadTokens
			sessions[i].CachedTokens = usage.CachedTokens
			sessions[i].OutputTokens = usage.OutputTokens
			sessions[i].LastActivity = usage.LastActivity
		}(idx)
	}
	wg.Wait()
	return time.Since(start), total
}

func summarizeSnapshot(snapshot *Snapshot) {
	snapshot.SessionCount = len(snapshot.Sessions)
	for _, session := range snapshot.Sessions {
		if session.HasMetadata {
			snapshot.MetadataCount++
		}
		if session.HasUsage {
			snapshot.UsageCount++
		}
		snapshot.TotalCostUSD += session.TotalCostUSD
		snapshot.TotalTokens += session.TotalTokens
		snapshot.InputTokens += session.InputTokens
		snapshot.CacheCreationTokens += session.CacheCreationTokens
		snapshot.CacheReadTokens += session.CacheReadTokens
		snapshot.CachedTokens += session.CachedTokens
		snapshot.OutputTokens += session.OutputTokens
	}
}

func runCommandJSON(cmd *exec.Cmd) ([]byte, []byte, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

func sortSessions(sessions []SessionInfo) {
	sort.SliceStable(sessions, func(i, j int) bool {
		ti := sortTime(sessions[i])
		tj := sortTime(sessions[j])
		switch {
		case ti.Equal(tj):
			return sessions[i].PID < sessions[j].PID
		case ti.IsZero():
			return false
		case tj.IsZero():
			return true
		default:
			return ti.After(tj)
		}
	})
}

func sortTime(session SessionInfo) time.Time {
	if session.LastActivity != nil {
		return *session.LastActivity
	}
	if session.StartedAt != nil {
		return *session.StartedAt
	}
	return time.Time{}
}

func formatCommandError(err error, out []byte) string {
	text := strings.TrimSpace(string(out))
	if text == "" {
		return err.Error()
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	if len(lines) > 3 {
		lines = lines[:3]
	}
	return strings.Join(lines, " | ")
}

func trimBOM(raw []byte) []byte {
	return bytes.TrimPrefix(bytes.TrimSpace(raw), []byte{0xEF, 0xBB, 0xBF})
}

func parseOptionalTime(value string) *time.Time {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil
	}
	return &t
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func durationMS(d time.Duration) float64 {
	return math.Round(d.Seconds()*1000*10) / 10
}
