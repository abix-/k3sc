package types

import "time"

// Namespace and Repos are set by config.Load() at startup.
// Defaults here match the config defaults as a safety net.
var Namespace = "claude-agents"

type Repo struct {
	Owner string
	Name  string
}

func (r Repo) CloneURL() string {
	return "https://github.com/" + r.Owner + "/" + r.Name + ".git"
}

var Repos = []Repo{
	{Owner: "abix-", Name: "endless"},
	{Owner: "abix-", Name: "k3sc"},
}

// SlotLetter converts a 1-based slot number to a letter (1=a, 2=b, ..., 26=z).
func SlotLetter(slot int) string {
	if slot < 1 || slot > 26 {
		return "?"
	}
	return string(rune('a' + slot - 1))
}

// AgentName returns the agent ID for a k3s slot (e.g. "claude-a").
func AgentName(slot int) string {
	return "claude-" + SlotLetter(slot)
}

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
	Repo     Repo
}

// RepoByName finds a repo by name from the Repos list, defaulting to the first.
func RepoByName(name string) Repo {
	for _, r := range Repos {
		if r.Name == name {
			return r
		}
	}
	return Repos[0]
}

// TaskInfo is a TUI-friendly view of a ClaudeTask CR.
type TaskInfo struct {
	Name     string
	Repo     Repo
	Issue    int
	Phase    string // Pending, Running, Succeeded, Failed, Blocked
	Agent    string
	Slot     int
	NextAction string
	Started  *time.Time
	Finished *time.Time
	LogTail  string
}

func (t TaskInfo) PhaseOrder() int {
	switch t.Phase {
	case "Running", "Pending":
		return 0
	case "Succeeded":
		return 1
	case "Failed":
		return 2
	case "Blocked":
		return 3
	default:
		return 4
	}
}

type Issue struct {
	Number    int
	Title     string
	State     string
	Owner     string
	Repo      Repo
	CreatedAt time.Time
}

type PullRequest struct {
	Number int
	Title  string
	State  string // OPEN, MERGED, CLOSED
	Branch string
	Issue  int // linked issue number (from branch name issue-N)
	Repo   Repo
}

