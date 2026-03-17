package types

import (
	"testing"
)

// TestSlotLetterMatchesShellFormula verifies that SlotLetter(slot) produces the
// same result as the shell expression SLOT_LETTER=$(printf '\x%02x' $((slot+96))).
// The entrypoint.sh used to compute this independently with AGENT_SLOT+96; it now
// relies on the SLOT_LETTER env var set by Go via types.SlotLetter. This test
// ensures both formulas remain equivalent so a divergence is caught immediately.
func TestSlotLetterMatchesShellFormula(t *testing.T) {
	for slot := 1; slot <= 26; slot++ {
		// shell formula: chr(slot + 96)
		want := string(rune(slot + 96))
		got := SlotLetter(slot)
		if got != want {
			t.Errorf("SlotLetter(%d) = %q, want %q (shell formula: slot+96=%d)", slot, got, want, slot+96)
		}
	}
}

func TestSlotLetterBoundaries(t *testing.T) {
	if got := SlotLetter(1); got != "a" {
		t.Errorf("SlotLetter(1) = %q, want \"a\"", got)
	}
	if got := SlotLetter(26); got != "z" {
		t.Errorf("SlotLetter(26) = %q, want \"z\"", got)
	}
	if got := SlotLetter(0); got != "?" {
		t.Errorf("SlotLetter(0) = %q, want \"?\"", got)
	}
	if got := SlotLetter(27); got != "?" {
		t.Errorf("SlotLetter(27) = %q, want \"?\"", got)
	}
}

func TestAgentName(t *testing.T) {
	if got := AgentName(1); got != "claude-a" {
		t.Errorf("AgentName(1) = %q, want \"claude-a\"", got)
	}
	if got := AgentName(3); got != "claude-c" {
		t.Errorf("AgentName(3) = %q, want \"claude-c\"", got)
	}
}
