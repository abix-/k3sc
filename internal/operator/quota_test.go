package operator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	coretypes "github.com/abix-/k3sc/internal/types"
)

func TestParseCodexStatus(t *testing.T) {
	sample := "\x1b[38;5;245mCredits:\x1b[0m 557 credits\n5h limit: [█████     ] 50% left (resets 09:01)\nWeekly limit: [███████   ] 85% left (resets 04:01 on 27 Nov)\n"

	snap, err := parseCodexStatus(sample)
	if err != nil {
		t.Fatalf("parseCodexStatus() error = %v", err)
	}
	if snap.Credits == nil || *snap.Credits != 557 {
		t.Fatalf("credits = %#v, want 557", snap.Credits)
	}
	if snap.FiveHourPercentLeft == nil || *snap.FiveHourPercentLeft != 50 {
		t.Fatalf("5h left = %#v, want 50", snap.FiveHourPercentLeft)
	}
	if snap.WeeklyPercentLeft == nil || *snap.WeeklyPercentLeft != 85 {
		t.Fatalf("weekly left = %#v, want 85", snap.WeeklyPercentLeft)
	}
	if snap.FiveHourReset != "resets 09:01" {
		t.Fatalf("5h reset = %q, want %q", snap.FiveHourReset, "resets 09:01")
	}
	if snap.WeeklyReset != "resets 04:01 on 27 Nov" {
		t.Fatalf("weekly reset = %q, want %q", snap.WeeklyReset, "resets 04:01 on 27 Nov")
	}
}

func TestParseCodexStatusReportsCrash(t *testing.T) {
	sample := "The application panicked (crashed).\nMessage: byte index ... out of bounds of `/status\\n`\n"
	if _, err := parseCodexStatus(sample); err == nil {
		t.Fatal("parseCodexStatus() should reject a crashing /status run")
	}
}

func TestParseClaudeUsage(t *testing.T) {
	sample := "Settings: Status   Config   Usage (tab to cycle)\n\nCurrent session\n1% used  (Resets 5am (Europe/Vienna))\nCurrent week (all models)\n1% used  (Resets Dec 2 at 12am (Europe/Vienna))\nCurrent week (Sonnet only)\n1% used (Resets Dec 2 at 12am (Europe/Vienna))\n"

	snap, err := parseClaudeUsage(sample)
	if err != nil {
		t.Fatalf("parseClaudeUsage() error = %v", err)
	}
	if snap.SessionPercentLeft == nil || *snap.SessionPercentLeft != 99 {
		t.Fatalf("session left = %#v, want 99", snap.SessionPercentLeft)
	}
	if snap.WeeklyPercentLeft == nil || *snap.WeeklyPercentLeft != 99 {
		t.Fatalf("weekly left = %#v, want 99", snap.WeeklyPercentLeft)
	}
	if snap.ModelPercentLeft == nil || *snap.ModelPercentLeft != 99 {
		t.Fatalf("model left = %#v, want 99", snap.ModelPercentLeft)
	}
	if snap.SessionReset != "Resets 5am (Europe/Vienna)" {
		t.Fatalf("session reset = %q, want %q", snap.SessionReset, "Resets 5am (Europe/Vienna)")
	}
}

func TestParseClaudeUsageReportsAPIPlanLimitation(t *testing.T) {
	if _, err := parseClaudeUsage("/usage is only available for subscription plans."); err == nil {
		t.Fatal("parseClaudeUsage() should reject API-authenticated sessions")
	}
}

func TestLoadCodexStatusFromSessions(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, time.March, 20, 21, 0, 0, 0, time.UTC)
	fiveHourReset := now.Add(2 * time.Hour).Unix()
	weeklyReset := now.Add(48 * time.Hour).Unix()

	line := fmt.Sprintf(`{"timestamp":"2026-03-20T21:18:20.183Z","type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":2.0,"window_minutes":300,"resets_at":%d},"secondary":{"used_percent":62.0,"window_minutes":10080,"resets_at":%d},"credits":{"has_credits":false,"unlimited":false,"balance":null},"plan_type":"plus"}}}`, fiveHourReset, weeklyReset)
	path := filepath.Join(dir, "latest.jsonl")
	if err := os.WriteFile(path, []byte(line+"\n"), 0o644); err != nil {
		t.Fatalf("write session file: %v", err)
	}

	snap, err := loadCodexStatusFromSessions(context.Background(), now, dir)
	if err != nil {
		t.Fatalf("loadCodexStatusFromSessions() error = %v", err)
	}
	if snap.FiveHourPercentLeft == nil || *snap.FiveHourPercentLeft != 98 {
		t.Fatalf("5h left = %#v, want 98", snap.FiveHourPercentLeft)
	}
	if snap.WeeklyPercentLeft == nil || *snap.WeeklyPercentLeft != 38 {
		t.Fatalf("weekly left = %#v, want 38", snap.WeeklyPercentLeft)
	}
	if snap.LastUpdated.IsZero() {
		t.Fatal("LastUpdated should be populated from the session event")
	}
}

func TestBuildFamilyDispatchStatesFallsBackToRecentLimit(t *testing.T) {
	now := time.Date(2026, time.March, 20, 12, 0, 0, 0, time.UTC)
	reset := now.Add(30 * time.Minute)

	states, warnings := buildFamilyDispatchStates(now, 15*time.Minute, true, familyProbeResults{}, map[coretypes.AgentFamily]recentUsageLimit{
		coretypes.FamilyClaude: {
			PodName: "claude-issue-42",
			ResetAt: &reset,
		},
	})

	if len(warnings) != 0 {
		t.Fatalf("warnings len = %d, want 0", len(warnings))
	}
	if states[coretypes.FamilyClaude].Available {
		t.Fatal("claude should be blocked by recent usage-limit fallback")
	}
	if !states[coretypes.FamilyClaude].Checked {
		t.Fatal("claude fallback should mark the family as checked")
	}
	if !states[coretypes.FamilyCodex].Available {
		t.Fatal("codex should remain dispatchable")
	}
}

func TestBuildFamilyDispatchStatesExposesSessionReadFailure(t *testing.T) {
	now := time.Date(2026, time.March, 20, 12, 0, 0, 0, time.UTC)

	states, warnings := buildFamilyDispatchStates(now, 15*time.Minute, true, familyProbeResults{
		CodexErr: staticErr("no shared codex session data yet"),
	}, nil)

	if len(warnings) != 1 {
		t.Fatalf("warnings len = %d, want 1", len(warnings))
	}
	state := states[coretypes.FamilyCodex]
	if !state.Available {
		t.Fatal("codex should remain dispatchable when probe fails and no recent limit")
	}
	// codex now uses fallback, which marks checked when recentChecked is true
	if !state.Checked {
		t.Fatal("codex should be checked via fallback when recentChecked is true")
	}
}

func TestBuildFamilyDispatchStatesCodexBlockedByRecentLimit(t *testing.T) {
	now := time.Date(2026, time.March, 20, 12, 0, 0, 0, time.UTC)
	reset := now.Add(5 * 24 * time.Hour) // resets in 5 days

	states, warnings := buildFamilyDispatchStates(now, 15*time.Minute, true, familyProbeResults{
		CodexErr: staticErr("no shared codex session data yet"),
	}, map[coretypes.AgentFamily]recentUsageLimit{
		coretypes.FamilyCodex: {
			PodName: "codex-issue-208",
			ResetAt: &reset,
		},
	})

	if len(warnings) != 1 {
		t.Fatalf("warnings len = %d, want 1", len(warnings))
	}
	state := states[coretypes.FamilyCodex]
	if state.Available {
		t.Fatal("codex should be blocked when probe fails and recent pod hit usage limit")
	}
}

func TestBuildFamilyDispatchStatesMarksClaudeClearWhenFallbackSucceeds(t *testing.T) {
	now := time.Date(2026, time.March, 20, 12, 0, 0, 0, time.UTC)

	states, _ := buildFamilyDispatchStates(now, 15*time.Minute, true, familyProbeResults{}, nil)
	state := states[coretypes.FamilyClaude]
	if !state.Available || !state.Checked {
		t.Fatalf("claude state = %+v, want checked and available", state)
	}
	want := "no recent usage-limit failures in last 15m"
	if state.Reason != want {
		t.Fatalf("reason = %q, want %q", state.Reason, want)
	}
}

type staticErr string

func (e staticErr) Error() string { return string(e) }
