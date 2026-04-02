package operator

import "testing"

func TestParseUsageFromLines(t *testing.T) {
	lines := []string{
		"[entrypoint] launching claude for k3sc#42 (issue)...",
		"some tool output here",
		`[usage] {"input_tokens":150000,"output_tokens":12000,"cache_creation_tokens":8000,"cache_read_tokens":95000,"total_tokens":265000,"cache_hit_rate":0.375,"output_ratio":0.045,"models":["claude-sonnet-4-6"],"entries":47}`,
		"[entrypoint] claude exited with code 0",
	}

	stats := parseUsageFromLines(lines)
	if stats == nil {
		t.Fatal("expected usage stats, got nil")
	}
	if stats.InputTokens != 150000 {
		t.Errorf("input_tokens: got %d, want 150000", stats.InputTokens)
	}
	if stats.OutputTokens != 12000 {
		t.Errorf("output_tokens: got %d, want 12000", stats.OutputTokens)
	}
	if stats.CacheCreationTokens != 8000 {
		t.Errorf("cache_creation_tokens: got %d, want 8000", stats.CacheCreationTokens)
	}
	if stats.CacheReadTokens != 95000 {
		t.Errorf("cache_read_tokens: got %d, want 95000", stats.CacheReadTokens)
	}
	if stats.TotalTokens != 265000 {
		t.Errorf("total_tokens: got %d, want 265000", stats.TotalTokens)
	}
	if stats.Entries != 47 {
		t.Errorf("entries: got %d, want 47", stats.Entries)
	}
	if len(stats.Models) != 1 || stats.Models[0] != "claude-sonnet-4-6" {
		t.Errorf("models: got %v, want [claude-sonnet-4-6]", stats.Models)
	}
	if stats.OutputRatio < 0.044 || stats.OutputRatio > 0.046 {
		t.Errorf("output_ratio: got %f, want ~0.045", stats.OutputRatio)
	}
	if stats.CacheHitRate < 0.374 || stats.CacheHitRate > 0.376 {
		t.Errorf("cache_hit_rate: got %f, want ~0.375", stats.CacheHitRate)
	}
}

func TestParseUsageFromLines_InlineWithResult(t *testing.T) {
	lines := []string{
		`[result] exit[usage] {"input_tokens":100,"output_tokens":50,"cache_creation_tokens":0,"cache_read_tokens":0,"total_tokens":150,"cache_hit_rate":0,"output_ratio":0.333,"models":["claude-sonnet-4-6"],"entries":1}`,
		"[entrypoint] claude exited with code 0",
	}
	stats := parseUsageFromLines(lines)
	if stats == nil {
		t.Fatal("expected usage stats from inline [usage], got nil")
	}
	if stats.TotalTokens != 150 {
		t.Errorf("total_tokens: got %d, want 150", stats.TotalTokens)
	}
}

func TestParseUsageFromLines_NoUsage(t *testing.T) {
	lines := []string{
		"[entrypoint] launching claude...",
		"[entrypoint] claude exited with code 1",
	}
	if stats := parseUsageFromLines(lines); stats != nil {
		t.Errorf("expected nil, got %+v", stats)
	}
}
