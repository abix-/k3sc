package claude

import (
	"encoding/json"
	"testing"
	"time"
)

func TestParseCCUsageSummaryAggregatesEntries(t *testing.T) {
	raw := []byte(`{
  "sessionId": "abc",
  "totalCost": 1.25,
  "totalTokens": 12345,
  "entries": [
    {
      "timestamp": "2026-03-26T22:09:49.429Z",
      "inputTokens": 2,
      "outputTokens": 1,
      "cacheCreationTokens": 10,
      "cacheReadTokens": 20,
      "model": "claude-sonnet-4-6"
    },
    {
      "timestamp": "2026-03-26T22:10:49.429Z",
      "inputTokens": 3,
      "outputTokens": 4,
      "cacheCreationTokens": 30,
      "cacheReadTokens": 40,
      "model": "claude-opus-4-6"
    },
    {
      "timestamp": "2026-03-26T22:10:00.000Z",
      "inputTokens": 5,
      "outputTokens": 6,
      "cacheCreationTokens": 50,
      "cacheReadTokens": 60,
      "model": "claude-sonnet-4-6"
    }
  ]
}`)

	summary, err := parseCCUsageSummary(raw)
	if err != nil {
		t.Fatalf("parseCCUsageSummary() error = %v", err)
	}

	if summary.TotalCostUSD != 1.25 {
		t.Fatalf("TotalCostUSD = %v, want 1.25", summary.TotalCostUSD)
	}
	if summary.TotalTokens != 12345 {
		t.Fatalf("TotalTokens = %d, want 12345", summary.TotalTokens)
	}
	if summary.InputTokens != 10 {
		t.Fatalf("InputTokens = %d, want 10", summary.InputTokens)
	}
	if summary.CacheCreationTokens != 90 {
		t.Fatalf("CacheCreationTokens = %d, want 90", summary.CacheCreationTokens)
	}
	if summary.CacheReadTokens != 120 {
		t.Fatalf("CacheReadTokens = %d, want 120", summary.CacheReadTokens)
	}
	if summary.CachedTokens != 210 {
		t.Fatalf("CachedTokens = %d, want 210", summary.CachedTokens)
	}
	if summary.OutputTokens != 11 {
		t.Fatalf("OutputTokens = %d, want 11", summary.OutputTokens)
	}
	if summary.EntryCount != 3 {
		t.Fatalf("EntryCount = %d, want 3", summary.EntryCount)
	}
	if len(summary.Models) != 2 || summary.Models[0] != "claude-opus-4-6" || summary.Models[1] != "claude-sonnet-4-6" {
		t.Fatalf("Models = %#v, want sorted unique models", summary.Models)
	}
	wantLast := time.Date(2026, 3, 26, 22, 10, 49, 429000000, time.UTC)
	if summary.LastActivity == nil || !summary.LastActivity.Equal(wantLast) {
		t.Fatalf("LastActivity = %v, want %v", summary.LastActivity, wantLast)
	}
}

func TestLoadActiveBlockParsesTokenLimitStatus(t *testing.T) {
	raw := []byte(`{
  "blocks": [
    {
      "id": "2026-03-26T21:00:00.000Z",
      "startTime": "2026-03-26T21:00:00.000Z",
      "endTime": "2026-03-27T02:00:00.000Z",
      "actualEndTime": "2026-03-26T23:31:26.831Z",
      "isActive": true,
      "isGap": false,
      "entries": 396,
      "tokenCounts": {
        "inputTokens": 12974,
        "outputTokens": 27146,
        "cacheCreationInputTokens": 659592,
        "cacheReadInputTokens": 32884072
      },
      "totalTokens": 33583784,
      "costUSD": 17.481298099999997,
      "models": ["claude-opus-4-6", "claude-sonnet-4-6"],
      "burnRate": {
        "tokensPerMinute": 256668.52042964843,
        "tokensPerMinuteForIndicator": 306.6224175226203,
        "costPerHour": 8.016188262495895
      },
      "projection": {
        "totalTokens": 71708843,
        "totalCost": 37.33,
        "remainingMinutes": 149
      },
      "tokenLimitStatus": {
        "limit": 761429455,
        "projectedUsage": 71708843,
        "percentUsed": 9.41766076018165,
        "status": "ok"
      }
    }
  ]
}`)

	var report ccusageBlocksReport
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if len(report.Blocks) != 1 {
		t.Fatalf("len(report.Blocks) = %d, want 1", len(report.Blocks))
	}
	block := report.Blocks[0]
	if block.TokenLimitStatus.Limit != 761429455 {
		t.Fatalf("TokenLimitStatus.Limit = %d, want 761429455", block.TokenLimitStatus.Limit)
	}
	if block.TokenLimitStatus.ProjectedUsage != 71708843 {
		t.Fatalf("ProjectedUsage = %d, want 71708843", block.TokenLimitStatus.ProjectedUsage)
	}
	if block.Projection.RemainingMinutes != 149 {
		t.Fatalf("RemainingMinutes = %d, want 149", block.Projection.RemainingMinutes)
	}
}
