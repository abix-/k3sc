package claude

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	claudeConfigDirEnv     = "CLAUDE_CONFIG_DIR"
	claudeProjectsDirName  = "projects"
	activeBlockDuration    = 5 * time.Hour
	activeBlockLookback    = 2 * activeBlockDuration
	scannerBufferSizeBytes = 16 * 1024 * 1024
	blockWarningThreshold  = 0.8
	activeBlockCacheName   = "claude-active-block.json"
	activeBlockCacheVer    = 2

	ccusagePricingMarker = "const PREFETCHED_CLAUDE_PRICING = "
)

var (
	pricingOnce sync.Once
	pricingData map[string]ccusageModelPricing
	pricingErr  error
)

var ccusageProviderPrefixes = []string{
	"anthropic/",
	"claude-3-5-",
	"claude-3-",
	"claude-",
	"openrouter/openai/",
}

type usageFile struct {
	Path string
}

type usageDataLine struct {
	Timestamp         string            `json:"timestamp"`
	RequestID         string            `json:"requestId"`
	CostUSD           *float64          `json:"costUSD"`
	IsAPIErrorMessage bool              `json:"isApiErrorMessage"`
	Message           *usageDataMessage `json:"message"`
}

type usageDataMessage struct {
	ID      string             `json:"id"`
	Model   string             `json:"model"`
	Content []usageContentLine `json:"content"`
	Usage   *usageTokenPayload `json:"usage"`
}

type usageContentLine struct {
	Text string `json:"text"`
}

type usageTokenPayload struct {
	InputTokens              *int64 `json:"input_tokens"`
	OutputTokens             *int64 `json:"output_tokens"`
	CacheCreationInputTokens *int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     *int64 `json:"cache_read_input_tokens"`
	Speed                    string `json:"speed"`
}

type loadedUsageEntry struct {
	Timestamp           time.Time
	InputTokens         int64
	OutputTokens        int64
	CacheCreationTokens int64
	CacheReadTokens     int64
	CostUSD             float64
	Model               string
}

type sessionBlock struct {
	ID                  string
	StartTime           time.Time
	EndTime             time.Time
	ActualEndTime       time.Time
	IsActive            bool
	IsGap               bool
	Entries             []loadedUsageEntry
	InputTokens         int64
	OutputTokens        int64
	CacheCreationTokens int64
	CacheReadTokens     int64
	CostUSD             float64
	Models              []string
}

type activeBlockCache struct {
	Version            int        `json:"version"`
	SavedAt            time.Time  `json:"savedAt"`
	LastScanAt         time.Time  `json:"lastScanAt"`
	ClaudeRoots        []string   `json:"claudeRoots,omitempty"`
	MaxCompletedTokens int64      `json:"maxCompletedTokens"`
	Block              *BlockInfo `json:"block,omitempty"`
}

type burnRate struct {
	TokensPerMinute float64
	CostPerHour     float64
}

type projectedUsage struct {
	TotalTokens      int64
	TotalCost        float64
	RemainingMinutes int
}

type ccusageModelPricing struct {
	InputCostPerToken                    float64                         `json:"input_cost_per_token"`
	OutputCostPerToken                   float64                         `json:"output_cost_per_token"`
	CacheCreationInputTokenCost          float64                         `json:"cache_creation_input_token_cost"`
	CacheReadInputTokenCost              float64                         `json:"cache_read_input_token_cost"`
	InputCostPerTokenAbove200K           float64                         `json:"input_cost_per_token_above_200k_tokens"`
	OutputCostPerTokenAbove200K          float64                         `json:"output_cost_per_token_above_200k_tokens"`
	CacheCreationInputTokenCostAbove200K float64                         `json:"cache_creation_input_token_cost_above_200k_tokens"`
	CacheReadInputTokenCostAbove200K     float64                         `json:"cache_read_input_token_cost_above_200k_tokens"`
	ProviderSpecificEntry                *ccusageProviderSpecificPricing `json:"provider_specific_entry"`
}

type ccusageProviderSpecificPricing struct {
	Fast float64 `json:"fast"`
}

func loadActiveBlock() (*BlockInfo, error) {
	return loadActiveBlockAt(time.Now())
}

func loadActiveBlockAt(now time.Time) (*BlockInfo, error) {
	claudePaths, err := getClaudePaths()
	if err != nil {
		return nil, err
	}

	cache, err := loadActiveBlockCache()
	if err != nil {
		return nil, err
	}
	cache = normalizeActiveBlockCache(cache, claudePaths)

	cutoff := time.Time{}
	if !cache.LastScanAt.IsZero() && sameClaudeRoots(cache.ClaudeRoots, claudePaths) {
		cutoff = cache.LastScanAt.Add(-activeBlockLookback)
	}

	files, err := collectUsageFiles(claudePaths, cutoff)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		if cache.Block != nil {
			block := cloneBlockInfo(cache.Block)
			refreshProjectedBlockInfo(block, now)
			if blockInfoIsActive(block, now) {
				applyTokenLimit(block, cache.MaxCompletedTokens)
				return block, nil
			}
		}
		return nil, errors.New("no Claude session files found")
	}

	entries, err := loadUsageEntries(files, cutoff, true)
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		if cache.Block != nil {
			block := cloneBlockInfo(cache.Block)
			refreshProjectedBlockInfo(block, now)
			if blockInfoIsActive(block, now) {
				applyTokenLimit(block, cache.MaxCompletedTokens)
				return block, nil
			}
		}
		return nil, errors.New("no Claude usage entries found")
	}

	blocks := identifySessionBlocks(entries, now)
	maxTokens := cache.MaxCompletedTokens
	for _, candidate := range blocks {
		if candidate.IsGap || candidate.IsActive {
			continue
		}
		if total := blockTotalTokens(&candidate); total > maxTokens {
			maxTokens = total
		}
	}

	block, err := findActiveBlock(blocks)
	if err != nil {
		cache.LastScanAt = now.UTC()
		cache.SavedAt = now.UTC()
		cache.ClaudeRoots = normalizedClaudeRoots(claudePaths)
		cache.MaxCompletedTokens = maxTokens
		cache.Block = nil
		if saveErr := saveActiveBlockCache(cache); saveErr != nil {
			// Cache writes are optional.
		}
		return nil, err
	}

	info := blockToInfo(block, now)
	applyTokenLimit(info, maxTokens)

	cache.LastScanAt = now.UTC()
	cache.SavedAt = now.UTC()
	cache.ClaudeRoots = normalizedClaudeRoots(claudePaths)
	cache.MaxCompletedTokens = maxTokens
	cache.Block = cloneBlockInfo(info)
	if err := saveActiveBlockCache(cache); err != nil {
		// Cache writes are optional; live block data is still valid.
	}
	return info, nil
}

func getClaudePaths() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home directory: %w", err)
	}

	addPath := func(paths *[]string, seen map[string]struct{}, base string) {
		base = filepath.Clean(strings.TrimSpace(base))
		if base == "" {
			return
		}
		projectsDir := filepath.Join(base, claudeProjectsDirName)
		if info, err := os.Stat(projectsDir); err != nil || !info.IsDir() {
			return
		}
		if _, ok := seen[base]; ok {
			return
		}
		seen[base] = struct{}{}
		*paths = append(*paths, base)
	}

	var paths []string
	seen := make(map[string]struct{})

	if envPaths := strings.TrimSpace(os.Getenv(claudeConfigDirEnv)); envPaths != "" {
		for _, raw := range strings.Split(envPaths, ",") {
			addPath(&paths, seen, raw)
		}
		if len(paths) > 0 {
			return paths, nil
		}
		return nil, fmt.Errorf("no valid Claude data directories found in %s", claudeConfigDirEnv)
	}

	addPath(&paths, seen, filepath.Join(home, ".config", "claude"))
	addPath(&paths, seen, filepath.Join(home, ".claude"))
	if len(paths) == 0 {
		return nil, errors.New("no valid Claude data directories found")
	}
	return paths, nil
}

func collectUsageFiles(claudePaths []string, cutoff time.Time) ([]usageFile, error) {
	files := make([]usageFile, 0, 32)
	for _, claudePath := range claudePaths {
		projectsDir := filepath.Join(claudePath, claudeProjectsDirName)
		err := filepath.WalkDir(projectsDir, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() || !strings.EqualFold(filepath.Ext(path), ".jsonl") {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			if !cutoff.IsZero() && info.ModTime().Before(cutoff) {
				return nil
			}
			files = append(files, usageFile{Path: path})
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("scan Claude session files: %w", err)
		}
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].Path < files[j].Path
	})
	return files, nil
}

func loadUsageEntries(files []usageFile, cutoff time.Time, includeCost bool) ([]loadedUsageEntry, error) {
	processed := make(map[string]struct{}, 1024)
	entries := make([]loadedUsageEntry, 0, 1024)

	var pricing map[string]ccusageModelPricing
	if includeCost {
		var err error
		pricing, err = loadInstalledCCUsagePricing()
		if err != nil {
			return nil, err
		}
	}

	for _, file := range files {
		f, err := os.Open(file.Path)
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", file.Path, err)
		}

		scanner := bufio.NewScanner(f)
		buf := make([]byte, 0, 64*1024)
		scanner.Buffer(buf, scannerBufferSizeBytes)
		for scanner.Scan() {
			entry, uniqueHash, ok := parseUsageEntry(scanner.Text(), pricing, includeCost)
			if !ok {
				continue
			}
			if uniqueHash != "" {
				if _, exists := processed[uniqueHash]; exists {
					continue
				}
				processed[uniqueHash] = struct{}{}
			}
			if !cutoff.IsZero() && entry.Timestamp.Before(cutoff) {
				continue
			}
			entries = append(entries, entry)
		}
		if err := scanner.Err(); err != nil {
			f.Close()
			return nil, fmt.Errorf("scan %s: %w", file.Path, err)
		}
		if err := f.Close(); err != nil {
			return nil, fmt.Errorf("close %s: %w", file.Path, err)
		}
	}

	return entries, nil
}

func parseUsageEntry(line string, pricing map[string]ccusageModelPricing, includeCost bool) (loadedUsageEntry, string, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return loadedUsageEntry{}, "", false
	}

	var payload usageDataLine
	if err := json.Unmarshal([]byte(line), &payload); err != nil {
		return loadedUsageEntry{}, "", false
	}
	if payload.Message == nil || payload.Message.Usage == nil || payload.Message.Usage.InputTokens == nil || payload.Message.Usage.OutputTokens == nil {
		return loadedUsageEntry{}, "", false
	}
	if strings.EqualFold(strings.TrimSpace(payload.Message.Model), "<synthetic>") {
		return loadedUsageEntry{}, "", false
	}

	timestamp, err := time.Parse(time.RFC3339Nano, payload.Timestamp)
	if err != nil {
		return loadedUsageEntry{}, "", false
	}

	cacheCreate := int64(0)
	if payload.Message.Usage.CacheCreationInputTokens != nil {
		cacheCreate = *payload.Message.Usage.CacheCreationInputTokens
	}
	cacheRead := int64(0)
	if payload.Message.Usage.CacheReadInputTokens != nil {
		cacheRead = *payload.Message.Usage.CacheReadInputTokens
	}

	cost := 0.0
	if includeCost {
		cost = calculateCostForUsage(payload, pricing)
	}

	return loadedUsageEntry{
			Timestamp:           timestamp,
			InputTokens:         *payload.Message.Usage.InputTokens,
			OutputTokens:        *payload.Message.Usage.OutputTokens,
			CacheCreationTokens: cacheCreate,
			CacheReadTokens:     cacheRead,
			CostUSD:             cost,
			Model:               displayModelName(payload.Message.Model, payload.Message.Usage.Speed),
		},
		createUniqueHash(payload.Message.ID, payload.RequestID),
		true
}

func calculateCostForUsage(payload usageDataLine, pricing map[string]ccusageModelPricing) float64 {
	if payload.CostUSD != nil {
		return *payload.CostUSD
	}
	if payload.Message == nil || payload.Message.Usage == nil {
		return 0
	}
	modelName := strings.TrimSpace(payload.Message.Model)
	if modelName == "" {
		return 0
	}

	modelPricing, ok := matchModelPricing(pricing, modelName)
	if !ok {
		return 0
	}

	baseCost := calculateCostFromPricing(payload.Message.Usage, modelPricing)
	if strings.EqualFold(strings.TrimSpace(payload.Message.Usage.Speed), "fast") && modelPricing.ProviderSpecificEntry != nil && modelPricing.ProviderSpecificEntry.Fast > 0 {
		return baseCost * modelPricing.ProviderSpecificEntry.Fast
	}
	return baseCost
}

func loadInstalledCCUsagePricing() (map[string]ccusageModelPricing, error) {
	pricingOnce.Do(func() {
		bundlePath, err := locateInstalledCCUsageBundle()
		if err != nil {
			pricingErr = err
			return
		}

		raw, err := os.ReadFile(bundlePath)
		if err != nil {
			pricingErr = fmt.Errorf("read %s: %w", bundlePath, err)
			return
		}

		jsonObject, err := extractJSONObjectAfterMarker(string(raw), ccusagePricingMarker)
		if err != nil {
			pricingErr = fmt.Errorf("extract ccusage pricing data: %w", err)
			return
		}

		var data map[string]ccusageModelPricing
		if err := json.Unmarshal([]byte(jsonObject), &data); err != nil {
			pricingErr = fmt.Errorf("parse ccusage pricing data: %w", err)
			return
		}
		pricingData = data
	})
	if pricingErr != nil {
		return nil, pricingErr
	}
	return pricingData, nil
}

func locateInstalledCCUsageBundle() (string, error) {
	ccusagePath, err := exec.LookPath("ccusage")
	if err != nil {
		return "", fmt.Errorf("ccusage not found in PATH: %w", err)
	}

	packageDir := filepath.Join(filepath.Dir(ccusagePath), "node_modules", "ccusage")
	distMatches, err := filepath.Glob(filepath.Join(packageDir, "dist", "data-loader-*.js"))
	if err != nil {
		return "", fmt.Errorf("find ccusage data-loader bundle: %w", err)
	}
	if len(distMatches) == 0 {
		return "", errors.New("find ccusage data-loader bundle: no data-loader-*.js found")
	}
	sort.Strings(distMatches)
	return distMatches[0], nil
}

func loadActiveBlockCache() (*activeBlockCache, error) {
	cachePath, err := activeBlockCachePath()
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(cachePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &activeBlockCache{Version: activeBlockCacheVer}, nil
		}
		return nil, err
	}

	var cache activeBlockCache
	if err := json.Unmarshal(raw, &cache); err != nil {
		return nil, err
	}
	return &cache, nil
}

func normalizeActiveBlockCache(cache *activeBlockCache, claudePaths []string) *activeBlockCache {
	if cache == nil {
		return &activeBlockCache{
			Version:     activeBlockCacheVer,
			ClaudeRoots: normalizedClaudeRoots(claudePaths),
		}
	}
	if cache.Version != activeBlockCacheVer {
		if cache.LastScanAt.IsZero() && !cache.SavedAt.IsZero() {
			cache.LastScanAt = cache.SavedAt
		}
		cache.Version = activeBlockCacheVer
		cache.ClaudeRoots = normalizedClaudeRoots(claudePaths)
	}
	if cache.MaxCompletedTokens <= 0 && cache.Block != nil && cache.Block.TokenLimit > 0 {
		cache.MaxCompletedTokens = cache.Block.TokenLimit
	}
	return cache
}

func saveActiveBlockCache(cache *activeBlockCache) error {
	if cache == nil {
		return nil
	}
	cachePath, err := activeBlockCachePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o755); err != nil {
		return err
	}

	raw, err := json.Marshal(cache)
	if err != nil {
		return err
	}
	return os.WriteFile(cachePath, raw, 0o644)
}

func activeBlockCachePath() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolve cache directory: %w", err)
	}
	return filepath.Join(base, "k3sc", activeBlockCacheName), nil
}

func cloneBlockInfo(info *BlockInfo) *BlockInfo {
	if info == nil {
		return nil
	}
	clone := *info
	if info.StartTime != nil {
		t := *info.StartTime
		clone.StartTime = &t
	}
	if info.EndTime != nil {
		t := *info.EndTime
		clone.EndTime = &t
	}
	if info.ActualEndTime != nil {
		t := *info.ActualEndTime
		clone.ActualEndTime = &t
	}
	if len(info.Models) > 0 {
		clone.Models = append([]string(nil), info.Models...)
	}
	return &clone
}

func blockInfoIsActive(info *BlockInfo, now time.Time) bool {
	if info == nil || info.EndTime == nil || info.ActualEndTime == nil {
		return false
	}
	return now.Sub(*info.ActualEndTime) < activeBlockDuration && now.Before(*info.EndTime)
}

func refreshProjectedBlockInfo(info *BlockInfo, now time.Time) {
	if info == nil || info.EndTime == nil || info.ActualEndTime == nil {
		return
	}

	info.IsActive = blockInfoIsActive(info, now)
	if !info.IsActive {
		return
	}

	remainingMinutes := math.Max(0, info.EndTime.Sub(now).Minutes())
	info.ProjectedTokens = int64(math.Round(float64(info.TotalTokens) + (info.TokensPerMinute * remainingMinutes)))
	info.ProjectedCostUSD = math.Round((info.CostUSD+((info.CostPerHour/60)*remainingMinutes))*100) / 100
	info.RemainingMinutes = int(math.Round(remainingMinutes))
	info.ProjectedUsage = info.ProjectedTokens
	applyTokenLimit(info, info.TokenLimit)
}

func normalizedClaudeRoots(claudePaths []string) []string {
	roots := make([]string, 0, len(claudePaths))
	for _, root := range claudePaths {
		roots = append(roots, filepath.ToSlash(filepath.Clean(root)))
	}
	sort.Strings(roots)
	return roots
}

func sameClaudeRoots(cacheRoots []string, claudePaths []string) bool {
	roots := normalizedClaudeRoots(claudePaths)
	if len(cacheRoots) != len(roots) {
		return false
	}
	for i := range roots {
		if filepath.ToSlash(filepath.Clean(cacheRoots[i])) != roots[i] {
			return false
		}
	}
	return true
}

func extractJSONObjectAfterMarker(content string, marker string) (string, error) {
	startMarker := strings.Index(content, marker)
	if startMarker == -1 {
		return "", fmt.Errorf("marker %q not found", marker)
	}
	start := strings.Index(content[startMarker+len(marker):], "{")
	if start == -1 {
		return "", errors.New("pricing object opening brace not found")
	}
	start += startMarker + len(marker)

	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(content); i++ {
		ch := content[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}

		switch ch {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return content[start : i+1], nil
			}
		}
	}
	return "", errors.New("pricing object closing brace not found")
}

func matchModelPricing(pricing map[string]ccusageModelPricing, modelName string) (ccusageModelPricing, bool) {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return ccusageModelPricing{}, false
	}

	candidates := []string{modelName}
	for _, prefix := range ccusageProviderPrefixes {
		candidates = append(candidates, prefix+modelName)
	}
	for _, candidate := range candidates {
		if match, ok := pricing[candidate]; ok {
			return match, true
		}
	}

	lowerModel := strings.ToLower(modelName)
	keys := make([]string, 0, len(pricing))
	for key := range pricing {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		lowerKey := strings.ToLower(key)
		if strings.Contains(lowerKey, lowerModel) || strings.Contains(lowerModel, lowerKey) {
			return pricing[key], true
		}
	}

	return ccusageModelPricing{}, false
}

func calculateCostFromPricing(tokens *usageTokenPayload, pricing ccusageModelPricing) float64 {
	const tieredThreshold = 200_000

	calculateTieredCost := func(total int64, basePrice float64, tieredPrice float64) float64 {
		if total <= 0 {
			return 0
		}
		if total > tieredThreshold && tieredPrice > 0 {
			tokensBelow := minInt64(total, tieredThreshold)
			tokensAbove := maxInt64(total-tieredThreshold, 0)
			cost := float64(tokensAbove) * tieredPrice
			if basePrice > 0 {
				cost += float64(tokensBelow) * basePrice
			}
			return cost
		}
		if basePrice > 0 {
			return float64(total) * basePrice
		}
		return 0
	}

	inputTokens := int64(0)
	if tokens.InputTokens != nil {
		inputTokens = *tokens.InputTokens
	}
	outputTokens := int64(0)
	if tokens.OutputTokens != nil {
		outputTokens = *tokens.OutputTokens
	}
	cacheCreate := int64(0)
	if tokens.CacheCreationInputTokens != nil {
		cacheCreate = *tokens.CacheCreationInputTokens
	}
	cacheRead := int64(0)
	if tokens.CacheReadInputTokens != nil {
		cacheRead = *tokens.CacheReadInputTokens
	}

	return calculateTieredCost(inputTokens, pricing.InputCostPerToken, pricing.InputCostPerTokenAbove200K) +
		calculateTieredCost(outputTokens, pricing.OutputCostPerToken, pricing.OutputCostPerTokenAbove200K) +
		calculateTieredCost(cacheCreate, pricing.CacheCreationInputTokenCost, pricing.CacheCreationInputTokenCostAbove200K) +
		calculateTieredCost(cacheRead, pricing.CacheReadInputTokenCost, pricing.CacheReadInputTokenCostAbove200K)
}

func findActiveBlock(blocks []sessionBlock) (*sessionBlock, error) {
	for _, block := range blocks {
		if block.IsActive && !block.IsGap {
			b := block
			return &b, nil
		}
	}
	return nil, errors.New("no active block data returned")
}

func identifySessionBlocks(entries []loadedUsageEntry, now time.Time) []sessionBlock {
	if len(entries) == 0 {
		return nil
	}

	sorted := append([]loadedUsageEntry(nil), entries...)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Timestamp.Before(sorted[j].Timestamp)
	})

	blocks := make([]sessionBlock, 0, 8)
	var (
		currentStart   time.Time
		currentEntries []loadedUsageEntry
	)

	for _, entry := range sorted {
		if currentStart.IsZero() {
			currentStart = floorToHour(entry.Timestamp)
			currentEntries = []loadedUsageEntry{entry}
			continue
		}

		timeSinceBlockStart := entry.Timestamp.Sub(currentStart)
		lastEntry := currentEntries[len(currentEntries)-1]
		timeSinceLastEntry := entry.Timestamp.Sub(lastEntry.Timestamp)

		if timeSinceBlockStart > activeBlockDuration || timeSinceLastEntry > activeBlockDuration {
			blocks = append(blocks, createBlock(currentStart, currentEntries, now))
			if timeSinceLastEntry > activeBlockDuration {
				if gap := createGapBlock(lastEntry.Timestamp, entry.Timestamp); gap != nil {
					blocks = append(blocks, *gap)
				}
			}
			currentStart = floorToHour(entry.Timestamp)
			currentEntries = []loadedUsageEntry{entry}
			continue
		}

		currentEntries = append(currentEntries, entry)
	}

	if !currentStart.IsZero() && len(currentEntries) > 0 {
		blocks = append(blocks, createBlock(currentStart, currentEntries, now))
	}
	return blocks
}

func createBlock(start time.Time, entries []loadedUsageEntry, now time.Time) sessionBlock {
	end := start.Add(activeBlockDuration)
	last := entries[len(entries)-1].Timestamp

	models := make([]string, 0, len(entries))
	modelSeen := make(map[string]struct{}, len(entries))
	block := sessionBlock{
		ID:            start.UTC().Format(time.RFC3339Nano),
		StartTime:     start,
		EndTime:       end,
		ActualEndTime: last,
		IsActive:      now.Sub(last) < activeBlockDuration && now.Before(end),
		Entries:       append([]loadedUsageEntry(nil), entries...),
	}
	for _, entry := range entries {
		block.InputTokens += entry.InputTokens
		block.OutputTokens += entry.OutputTokens
		block.CacheCreationTokens += entry.CacheCreationTokens
		block.CacheReadTokens += entry.CacheReadTokens
		block.CostUSD += entry.CostUSD
		if _, ok := modelSeen[entry.Model]; !ok {
			modelSeen[entry.Model] = struct{}{}
			models = append(models, entry.Model)
		}
	}
	block.Models = models
	return block
}

func createGapBlock(lastActivity, nextActivity time.Time) *sessionBlock {
	gapDuration := nextActivity.Sub(lastActivity)
	if gapDuration <= activeBlockDuration {
		return nil
	}

	gapStart := lastActivity.Add(activeBlockDuration)
	return &sessionBlock{
		ID:        "gap-" + gapStart.UTC().Format(time.RFC3339Nano),
		StartTime: gapStart,
		EndTime:   nextActivity,
		IsGap:     true,
	}
}

func calculateBurnRate(block *sessionBlock) *burnRate {
	if block == nil || block.IsGap || len(block.Entries) == 0 {
		return nil
	}
	first := block.Entries[0].Timestamp
	last := block.Entries[len(block.Entries)-1].Timestamp
	durationMinutes := last.Sub(first).Minutes()
	if durationMinutes <= 0 {
		return nil
	}

	return &burnRate{
		TokensPerMinute: float64(blockTotalTokens(block)) / durationMinutes,
		CostPerHour:     (block.CostUSD / durationMinutes) * 60,
	}
}

func projectBlockUsage(block *sessionBlock, now time.Time) *projectedUsage {
	if block == nil || block.IsGap || !block.IsActive {
		return nil
	}
	burn := calculateBurnRate(block)
	if burn == nil {
		return nil
	}

	remainingMinutes := math.Max(0, block.EndTime.Sub(now).Minutes())
	totalTokens := float64(blockTotalTokens(block)) + (burn.TokensPerMinute * remainingMinutes)
	totalCost := block.CostUSD + ((burn.CostPerHour / 60) * remainingMinutes)

	return &projectedUsage{
		TotalTokens:      int64(math.Round(totalTokens)),
		TotalCost:        math.Round(totalCost*100) / 100,
		RemainingMinutes: int(math.Round(remainingMinutes)),
	}
}

func blockToInfo(block *sessionBlock, now time.Time) *BlockInfo {
	if block == nil {
		return nil
	}

	info := &BlockInfo{
		ID:                  block.ID,
		StartTime:           ptrTime(block.StartTime),
		EndTime:             ptrTime(block.EndTime),
		ActualEndTime:       ptrTime(block.ActualEndTime),
		IsActive:            block.IsActive,
		Entries:             len(block.Entries),
		InputTokens:         block.InputTokens,
		OutputTokens:        block.OutputTokens,
		CacheCreationTokens: block.CacheCreationTokens,
		CacheReadTokens:     block.CacheReadTokens,
		TotalTokens:         blockTotalTokens(block),
		CostUSD:             block.CostUSD,
		Models:              append([]string(nil), block.Models...),
	}

	if burn := calculateBurnRate(block); burn != nil {
		info.TokensPerMinute = burn.TokensPerMinute
		info.CostPerHour = burn.CostPerHour
	}
	if projection := projectBlockUsage(block, now); projection != nil {
		info.ProjectedTokens = projection.TotalTokens
		info.ProjectedCostUSD = projection.TotalCost
		info.RemainingMinutes = projection.RemainingMinutes
	}
	return info
}

func blockTotalTokens(block *sessionBlock) int64 {
	if block == nil {
		return 0
	}
	return block.InputTokens + block.OutputTokens + block.CacheCreationTokens + block.CacheReadTokens
}

func applyTokenLimit(info *BlockInfo, limit int64) {
	if info == nil || limit <= 0 {
		return
	}
	info.TokenLimit = limit
	info.CurrentPercentUsed = (float64(info.TotalTokens) / float64(limit)) * 100
	info.CurrentRemainingTokens = maxInt64(limit-info.TotalTokens, 0)

	projected := info.ProjectedTokens
	if projected <= 0 {
		projected = info.ProjectedUsage
	}
	if projected > 0 {
		info.ProjectedUsage = projected
		info.ProjectedPercentUsed = (float64(projected) / float64(limit)) * 100
		info.ProjectedRemainingTokens = maxInt64(limit-projected, 0)
		switch {
		case projected > limit:
			info.TokenLimitStatus = "exceeds"
		case float64(projected) > float64(limit)*blockWarningThreshold:
			info.TokenLimitStatus = "warning"
		default:
			info.TokenLimitStatus = "ok"
		}
	}
}

func floorToHour(t time.Time) time.Time {
	utc := t.UTC()
	return time.Date(utc.Year(), utc.Month(), utc.Day(), utc.Hour(), 0, 0, 0, time.UTC)
}

func displayModelName(model string, speed string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return "unknown"
	}
	if strings.EqualFold(strings.TrimSpace(speed), "fast") {
		return model + "-fast"
	}
	return model
}

func createUniqueHash(messageID string, requestID string) string {
	messageID = strings.TrimSpace(messageID)
	requestID = strings.TrimSpace(requestID)
	if messageID == "" || requestID == "" {
		return ""
	}
	return messageID + ":" + requestID
}

func ptrTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	tt := t
	return &tt
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
