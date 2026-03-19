package cmd

import (
	"testing"

	"github.com/abix-/k3sc/internal/tui"
	"github.com/abix-/k3sc/internal/types"
)

func TestApplyLiveLogTailsUsesStreamerTail(t *testing.T) {
	pods := []types.AgentPod{
		{Name: "pod-a", Phase: types.PhaseRunning, LogTail: "stale"},
		{Name: "pod-b", Phase: types.PhasePending, LogTail: "pending"},
		{Name: "pod-c", Phase: types.PhaseRunning, LogTail: "old"},
	}
	liveLogs := []tui.LiveLog{
		{PodName: "pod-a", Tail: "fresh-a"},
		{PodName: "pod-c", Tail: "fresh-c"},
	}

	applyLiveLogTails(pods, liveLogs)

	if got := pods[0].LogTail; got != "fresh-a" {
		t.Fatalf("pod-a LogTail = %q, want fresh streamer tail", got)
	}
	if got := pods[1].LogTail; got != "pending" {
		t.Fatalf("pending pod tail = %q, want unchanged value", got)
	}
	if got := pods[2].LogTail; got != "fresh-c" {
		t.Fatalf("pod-c LogTail = %q, want fresh streamer tail", got)
	}
}

func TestApplyLiveLogTailsClearsMissingRunningTail(t *testing.T) {
	pods := []types.AgentPod{
		{Name: "pod-a", Phase: types.PhaseRunning, LogTail: "stale"},
	}

	applyLiveLogTails(pods, nil)

	if got := pods[0].LogTail; got != "" {
		t.Fatalf("running pod tail = %q, want cleared stale tail", got)
	}
}
