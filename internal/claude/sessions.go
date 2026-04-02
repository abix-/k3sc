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

func loadUsageSummary(sessionID string) (*usageSummary, error) {
	claudePaths, err := getClaudePaths()
	if err != nil {
		return nil, err
	}

	files, err := collectSessionFiles(claudePaths, sessionID)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no JSONL files found for session %s", sessionID)
	}

	entries, err := loadUsageEntries(files, time.Time{}, true)
	if err != nil {
		return nil, err
	}

	return summarizeEntries(entries), nil
}

func collectSessionFiles(claudePaths []string, sessionID string) ([]usageFile, error) {
	var files []usageFile
	for _, claudePath := range claudePaths {
		projectsDir := filepath.Join(claudePath, claudeProjectsDirName)
		entries, err := os.ReadDir(projectsDir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, err
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			projectDir := filepath.Join(projectsDir, entry.Name())

			mainFile := filepath.Join(projectDir, sessionID+".jsonl")
			if _, err := os.Stat(mainFile); err == nil {
				files = append(files, usageFile{Path: mainFile})
			}

			subagentsDir := filepath.Join(projectDir, sessionID, "subagents")
			subEntries, err := os.ReadDir(subagentsDir)
			if err != nil {
				continue
			}
			for _, sub := range subEntries {
				if !sub.IsDir() && strings.HasSuffix(sub.Name(), ".jsonl") {
					files = append(files, usageFile{Path: filepath.Join(subagentsDir, sub.Name())})
				}
			}
		}
	}
	return files, nil
}

func summarizeEntries(entries []loadedUsageEntry) *usageSummary {
	modelSet := make(map[string]struct{})
	summary := &usageSummary{
		EntryCount: len(entries),
	}

	for _, entry := range entries {
		summary.InputTokens += entry.InputTokens
		summary.OutputTokens += entry.OutputTokens
		summary.CacheCreationTokens += entry.CacheCreationTokens
		summary.CacheReadTokens += entry.CacheReadTokens
		summary.TotalCostUSD += entry.CostUSD
		summary.TotalTokens += entry.InputTokens + entry.OutputTokens + entry.CacheCreationTokens + entry.CacheReadTokens
		if entry.Model != "" {
			modelSet[entry.Model] = struct{}{}
		}
		if summary.LastActivity == nil || entry.Timestamp.After(*summary.LastActivity) {
			ts := entry.Timestamp
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
	return summary
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
