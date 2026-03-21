package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/abix-/k3sc/internal/types"
)

func TestRenderViewShowsMergedOperatorTaskRow(t *testing.T) {
	now := time.Now()
	data := &Data{
		NodeName:    "node1",
		NodeVersion: "v1.0",
		Tasks: []types.TaskInfo{{
			Repo:         types.Repo{Owner: "abix-", Name: "endless"},
			Issue:        42,
			Agent:        "claude-a",
			Phase:        "Running",
			RuntimePhase: types.PhaseRunning,
			Started:      &now,
			LogTail:      "working",
		}},
	}
	m := NewModel(
		func() (*Data, error) { return data, nil },
		nil,
		func() (string, error) { return "", nil },
		3,
		nil,
		nil,
	)
	m.data = data
	m.width = 120
	m.height = 50

	out := m.renderView(10)
	if !strings.Contains(out, "Operator Tasks") || !strings.Contains(out, "endless") || !strings.Contains(out, "working") {
		t.Fatalf("merged task row not rendered; output:\n%s", out)
	}
}

func TestRenderViewDoesNotShowStandaloneAgentsSection(t *testing.T) {
	now := time.Now()
	data := &Data{
		NodeName:    "node1",
		NodeVersion: "v1.0",
		Pods: []types.AgentPod{
			{Name: "p", Issue: 1, Slot: 1, Phase: types.PhaseRunning, Started: &now, Repo: types.Repo{Owner: "abix-", Name: "k3sc"}},
		},
		Tasks: []types.TaskInfo{{
			Repo:         types.Repo{Owner: "abix-", Name: "k3sc"},
			Issue:        1,
			Agent:        "claude-a",
			Phase:        "Running",
			RuntimePhase: types.PhaseRunning,
			Started:      &now,
		}},
	}
	m := NewModel(
		func() (*Data, error) { return data, nil },
		nil,
		func() (string, error) { return "", nil },
		3,
		nil,
		nil,
	)
	m.data = data
	m.width = 120
	m.height = 50

	out := m.renderView(10)
	if strings.Contains(out, " Agents (") || !strings.Contains(out, "Runtime") {
		t.Fatalf("expected merged task table without standalone Agents section; output:\n%s", out)
	}
}

func TestRenderViewShowsLocalReviewReservations(t *testing.T) {
	now := time.Now()
	data := &Data{
		NodeName:    "node1",
		NodeVersion: "v1.0",
		Dispatch: types.DispatchStateInfo{
			ReviewReservations: []types.ReviewReservation{
				{
					Repo:       types.Repo{Owner: "abix-", Name: "endless"},
					PRNumber:   194,
					Issue:      186,
					Branch:     "issue-186",
					WorkerID:   "claude-a",
					ReservedAt: &now,
					ExpiresAt:  &now,
				},
			},
		},
	}
	m := NewModel(
		func() (*Data, error) { return data, nil },
		nil,
		func() (string, error) { return "", nil },
		3,
		nil,
		nil,
	)
	m.data = data
	m.width = 120
	m.height = 50

	out := m.renderView(10)
	if !strings.Contains(out, "Local Review") || !strings.Contains(out, "claude-a") || !strings.Contains(out, "#194") {
		t.Fatalf("local review reservation not rendered; output:\n%s", out)
	}
}

func TestRenderViewShowsPROwnerColumn(t *testing.T) {
	data := &Data{
		NodeName:    "node1",
		NodeVersion: "v1.0",
		PRs: []types.PullRequest{
			{
				Number: 194,
				Title:  "perf: reduce scan cost",
				Branch: "issue-186",
				Issue:  186,
				Owner:  "codex-a",
				Repo:   types.Repo{Owner: "abix-", Name: "endless"},
			},
		},
	}
	m := NewModel(
		func() (*Data, error) { return data, nil },
		nil,
		func() (string, error) { return "", nil },
		3,
		nil,
		nil,
	)
	m.data = data
	m.width = 120
	m.height = 50

	out := m.renderView(10)
	if !strings.Contains(out, "Owner") || !strings.Contains(out, "codex-a") {
		t.Fatalf("PR owner column not rendered; output:\n%s", out)
	}
}
