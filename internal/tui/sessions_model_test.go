package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/abix-/k3sc/internal/claude"
)

func TestSessionsRenderShowsSessionIDAndCWD(t *testing.T) {
	started := time.Date(2026, 3, 26, 17, 0, 0, 0, time.UTC)
	last := time.Date(2026, 3, 26, 18, 0, 0, 0, time.UTC)
	blockStart := time.Date(2026, 3, 26, 21, 0, 0, 0, time.UTC)
	snapshot := &claude.Snapshot{
		SessionCount:  1,
		MetadataCount: 1,
		UsageCount:    1,
		TotalCostUSD:  1.25,
		TotalTokens:   12345,
		CachedTokens:  12000,
		OutputTokens:  345,
		ActiveBlock: &claude.BlockInfo{
			StartTime:                &blockStart,
			TotalTokens:              54321,
			CostUSD:                  12.34,
			ProjectedTokens:          77777,
			ProjectedCostUSD:         18.9,
			RemainingMinutes:         149,
			InputTokens:              100,
			OutputTokens:             200,
			CacheCreationTokens:      300,
			CacheReadTokens:          400,
			TokenLimit:               1000000,
			CurrentPercentUsed:       5.4,
			CurrentRemainingTokens:   945679,
			ProjectedPercentUsed:     7.8,
			ProjectedRemainingTokens: 922223,
			TokenLimitStatus:         "ok",
			Models:                   []string{"claude-sonnet-4-6"},
		},
		Sessions: []claude.SessionInfo{
			{
				PID:          1652,
				SessionID:    "0fd024e2-785d-445f-93f0-bfa399217d93",
				CWD:          `C:\code\timberborn`,
				StartedAt:    &started,
				LastActivity: &last,
				HasMetadata:  true,
				HasUsage:     true,
				Models:       []string{"claude-sonnet-4-6"},
				TotalCostUSD: 1.25,
				TotalTokens:  12345,
				CachedTokens: 12000,
				OutputTokens: 345,
			},
		},
	}

	model := NewSessionsModel(snapshot, func() (*claude.Snapshot, error) { return snapshot, nil })
	model.width = 140
	model.height = 40

	out := model.renderView(1)
	if !strings.Contains(out, "k3sc sessions") || !strings.Contains(out, "Active Block") || !strings.Contains(out, "0fd024e2-785d-445f-93f0-bfa399217d93") || !strings.Contains(out, `C:\code\timberborn`) {
		t.Fatalf("sessions view missing expected details; output:\n%s", out)
	}
}

func TestSessionsRenderShowsMetadataErrors(t *testing.T) {
	snapshot := &claude.Snapshot{
		SessionCount: 1,
		Sessions: []claude.SessionInfo{
			{
				PID:           32116,
				MetadataError: "missing Claude session metadata",
			},
		},
	}

	model := NewSessionsModel(snapshot, func() (*claude.Snapshot, error) { return snapshot, nil })
	model.width = 120
	model.height = 30

	out := model.renderView(1)
	if !strings.Contains(out, "missing Claude session metadata") {
		t.Fatalf("expected metadata error to render; output:\n%s", out)
	}
}
