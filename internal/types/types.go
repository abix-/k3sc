package types

import "time"

const (
	Namespace  = "claude-agents"
	RepoOwner  = "abix-"
	RepoName   = "endless"
	SlotOffset = 5 // k8s slot 1 = claude-6
)

type PodPhase string

const (
	PhaseRunning   PodPhase = "Running"
	PhasePending   PodPhase = "Pending"
	PhaseSucceeded PodPhase = "Succeeded"
	PhaseFailed    PodPhase = "Failed"
	PhaseUnknown   PodPhase = "Unknown"
)

func (p PodPhase) Display() string {
	if p == PhaseSucceeded {
		return "Completed"
	}
	return string(p)
}

func (p PodPhase) Order() int {
	switch p {
	case PhaseRunning, PhasePending:
		return 0
	case PhaseSucceeded:
		return 1
	case PhaseFailed:
		return 2
	default:
		return 3
	}
}

type AgentPod struct {
	Name     string
	Issue    int
	Slot     int
	Phase    PodPhase
	Started  *time.Time
	Finished *time.Time
	LogTail  string
}

type Issue struct {
	Number int
	Title  string
	State  string
	Owner  string
}

type PullRequest struct {
	Number int
	Title  string
	State  string // OPEN, MERGED, CLOSED
	Branch string
	Issue  int // linked issue number (from branch name issue-N)
}

