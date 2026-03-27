package claude

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCollectUsageFilesDoesNotParseJSONLContents(t *testing.T) {
	base := t.TempDir()
	claudeRoot := filepath.Join(base, ".claude")
	projectsDir := filepath.Join(claudeRoot, claudeProjectsDirName, "demo-project")
	if err := os.MkdirAll(projectsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	first := filepath.Join(projectsDir, "broken.jsonl")
	second := filepath.Join(projectsDir, "valid.jsonl")
	if err := os.WriteFile(first, []byte("not json at all\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", first, err)
	}
	if err := os.WriteFile(second, []byte(`{"timestamp":"2026-03-26T21:00:00Z"}`+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", second, err)
	}

	files, err := collectUsageFiles([]string{claudeRoot}, time.Time{})
	if err != nil {
		t.Fatalf("collectUsageFiles() error = %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("len(files) = %d, want 2", len(files))
	}
	if files[0].Path != first || files[1].Path != second {
		t.Fatalf("files = %#v, want [%q, %q]", files, first, second)
	}
}

func TestRefreshProjectedBlockInfoRecomputesTimeDerivedFields(t *testing.T) {
	start := time.Date(2026, 3, 26, 21, 0, 0, 0, time.UTC)
	end := start.Add(activeBlockDuration)
	actualEnd := time.Date(2026, 3, 26, 22, 0, 0, 0, time.UTC)
	now := time.Date(2026, 3, 26, 22, 30, 0, 0, time.UTC)

	info := &BlockInfo{
		StartTime:       &start,
		EndTime:         &end,
		ActualEndTime:   &actualEnd,
		TotalTokens:     600,
		CostUSD:         12,
		TokensPerMinute: 10,
		CostPerHour:     6,
		TokenLimit:      1000,
	}

	refreshProjectedBlockInfo(info, now)

	if !info.IsActive {
		t.Fatalf("IsActive = false, want true")
	}
	if info.RemainingMinutes != 210 {
		t.Fatalf("RemainingMinutes = %d, want 210", info.RemainingMinutes)
	}
	if info.ProjectedTokens != 2700 {
		t.Fatalf("ProjectedTokens = %d, want 2700", info.ProjectedTokens)
	}
	if info.ProjectedUsage != 2700 {
		t.Fatalf("ProjectedUsage = %d, want 2700", info.ProjectedUsage)
	}
	if info.ProjectedCostUSD != 33 {
		t.Fatalf("ProjectedCostUSD = %v, want 33", info.ProjectedCostUSD)
	}
	if info.CurrentPercentUsed != 60 {
		t.Fatalf("CurrentPercentUsed = %v, want 60", info.CurrentPercentUsed)
	}
	if info.CurrentRemainingTokens != 400 {
		t.Fatalf("CurrentRemainingTokens = %d, want 400", info.CurrentRemainingTokens)
	}
	if info.ProjectedPercentUsed != 270 {
		t.Fatalf("ProjectedPercentUsed = %v, want 270", info.ProjectedPercentUsed)
	}
	if info.ProjectedRemainingTokens != 0 {
		t.Fatalf("ProjectedRemainingTokens = %d, want 0", info.ProjectedRemainingTokens)
	}
	if info.TokenLimitStatus != "exceeds" {
		t.Fatalf("TokenLimitStatus = %q, want %q", info.TokenLimitStatus, "exceeds")
	}
}

func TestActiveBlockCacheRoundTrip(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())

	start := time.Date(2026, 3, 26, 21, 0, 0, 0, time.UTC)
	end := start.Add(activeBlockDuration)
	actualEnd := time.Date(2026, 3, 26, 21, 45, 0, 0, time.UTC)
	now := time.Date(2026, 3, 26, 22, 0, 0, 0, time.UTC)

	info := &BlockInfo{
		StartTime:       &start,
		EndTime:         &end,
		ActualEndTime:   &actualEnd,
		IsActive:        true,
		TotalTokens:     1234,
		CostUSD:         4.56,
		TokensPerMinute: 12,
		CostPerHour:     3.4,
		TokenLimit:      5000,
		Models:          []string{"claude-sonnet-4-6"},
	}

	cache := &activeBlockCache{
		Version:            activeBlockCacheVer,
		SavedAt:            now,
		LastScanAt:         now,
		ClaudeRoots:        []string{"C:/Users/Abix/.claude"},
		MaxCompletedTokens: 5000,
		Block:              cloneBlockInfo(info),
	}
	if err := saveActiveBlockCache(cache); err != nil {
		t.Fatalf("saveActiveBlockCache() error = %v", err)
	}

	loaded, err := loadActiveBlockCache()
	if err != nil {
		t.Fatalf("loadActiveBlockCache() error = %v", err)
	}
	normalized := normalizeActiveBlockCache(loaded, []string{`C:\Users\Abix\.claude`})
	if normalized.MaxCompletedTokens != 5000 {
		t.Fatalf("normalized.MaxCompletedTokens = %d, want 5000", normalized.MaxCompletedTokens)
	}
	cached := cloneBlockInfo(normalized.Block)
	if cached == nil {
		t.Fatalf("normalized.Block = nil, want cached block")
	}
	if cached.TotalTokens != info.TotalTokens {
		t.Fatalf("cached.TotalTokens = %d, want %d", cached.TotalTokens, info.TotalTokens)
	}
	refreshProjectedBlockInfo(cached, now)
	if cached.ProjectedTokens == 0 {
		t.Fatalf("cached.ProjectedTokens = 0, want recomputed projection")
	}
	if cached.RemainingMinutes != 240 {
		t.Fatalf("cached.RemainingMinutes = %d, want 240", cached.RemainingMinutes)
	}
}

func TestNormalizeActiveBlockCacheSeedsLegacyTokenLimit(t *testing.T) {
	start := time.Date(2026, 3, 26, 21, 0, 0, 0, time.UTC)
	end := start.Add(activeBlockDuration)
	actualEnd := time.Date(2026, 3, 26, 21, 45, 0, 0, time.UTC)

	cache := &activeBlockCache{
		Version: 1,
		Block: &BlockInfo{
			StartTime:     &start,
			EndTime:       &end,
			ActualEndTime: &actualEnd,
			TokenLimit:    761429455,
		},
	}

	normalized := normalizeActiveBlockCache(cache, []string{`C:\Users\Abix\.claude`})
	if normalized.MaxCompletedTokens != 761429455 {
		t.Fatalf("normalized.MaxCompletedTokens = %d, want 761429455", normalized.MaxCompletedTokens)
	}
	if normalized.Version != activeBlockCacheVer {
		t.Fatalf("normalized.Version = %d, want %d", normalized.Version, activeBlockCacheVer)
	}
}
