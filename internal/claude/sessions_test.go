package claude

import (
	"testing"
	"time"
)

func TestSummarizeEntriesAggregatesCorrectly(t *testing.T) {
	entries := []loadedUsageEntry{
		{
			Timestamp:           time.Date(2026, 3, 26, 22, 9, 49, 429000000, time.UTC),
			InputTokens:         2,
			OutputTokens:        1,
			CacheCreationTokens: 10,
			CacheReadTokens:     20,
			Model:               "claude-sonnet-4-6",
		},
		{
			Timestamp:           time.Date(2026, 3, 26, 22, 10, 49, 429000000, time.UTC),
			InputTokens:         3,
			OutputTokens:        4,
			CacheCreationTokens: 30,
			CacheReadTokens:     40,
			Model:               "claude-opus-4-6",
		},
		{
			Timestamp:           time.Date(2026, 3, 26, 22, 10, 0, 0, time.UTC),
			InputTokens:         5,
			OutputTokens:        6,
			CacheCreationTokens: 50,
			CacheReadTokens:     60,
			Model:               "claude-sonnet-4-6",
		},
	}

	summary := summarizeEntries(entries)

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

func TestSummarizeEntriesEmpty(t *testing.T) {
	summary := summarizeEntries(nil)
	if summary.EntryCount != 0 {
		t.Fatalf("EntryCount = %d, want 0", summary.EntryCount)
	}
	if summary.TotalTokens != 0 {
		t.Fatalf("TotalTokens = %d, want 0", summary.TotalTokens)
	}
}
