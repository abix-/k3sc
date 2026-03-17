package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/abix-/k3sc/internal/types"
)

func TestAgentRowShowsRepoName(t *testing.T) {
	now := time.Now()
	repo := types.Repo{Owner: "abix-", Name: "endless"}
	pod := types.AgentPod{
		Name:    "test-pod",
		Issue:   42,
		Slot:    1,
		Phase:   types.PhaseRunning,
		Started: &now,
		Repo:    repo,
	}
	data := &Data{
		NodeName:  "node1",
		NodeVersion: "v1.0",
		Pods:    []types.AgentPod{pod},
		Issues:  nil,
		PRs:     nil,
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
	if !strings.Contains(out, "endless") {
		t.Fatalf("agent row does not contain repo name 'endless'; output:\n%s", out)
	}
}

func TestAgentHeaderIncludesRepo(t *testing.T) {
	now := time.Now()
	data := &Data{
		NodeName:    "node1",
		NodeVersion: "v1.0",
		Pods: []types.AgentPod{
			{Name: "p", Issue: 1, Slot: 1, Phase: types.PhaseRunning, Started: &now, Repo: types.Repo{Owner: "abix-", Name: "k3sc"}},
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
	if !strings.Contains(out, "Repo") {
		t.Fatalf("agent header does not contain 'Repo'; output:\n%s", out)
	}
}
