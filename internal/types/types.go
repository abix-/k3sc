package types

import (
	"strconv"
	"strings"
	"time"
)


// Namespace and Repos are set by config.Load() at startup.
// Defaults here match the config defaults as a safety net.
var Namespace = "claude-agents"

// GitHubURL is the base URL for the GitHub instance (e.g. "https://github.com" or "https://code.ssnc.dev").
// Set by config.Load() at startup.
var GitHubURL = "https://github.com"

// DispatchStateName is the singleton scheduler object reconciled by the operator.
const DispatchStateName = "default"

type Repo struct {
	Owner string
	Name  string
}

func (r Repo) CloneURL() string {
	return strings.TrimRight(GitHubURL, "/") + "/" + r.Owner + "/" + r.Name + ".git"
}

var Repos []Repo

// AgentFamily is "claude" or "codex".
type AgentFamily string

const (
	FamilyClaude AgentFamily = "claude"
	FamilyCodex  AgentFamily = "codex"
)

var LocalReviewLeaseTTL = 12 * time.Hour

// SlotLetter converts a 1-based slot number to a letter (1=a, 2=b, ..., 26=z).
func SlotLetter(slot int) string {
	if slot < 1 || slot > 26 {
		return "?"
	}
	return string(rune('a' + slot - 1))
}

// AgentName returns the agent ID for a k3s slot (e.g. "claude-a", "codex-b").
func AgentName(family AgentFamily, slot int) string {
	return string(family) + "-" + SlotLetter(slot)
}

func ParseWorkerFamily(workerID string) (AgentFamily, bool) {
	workerID = strings.ToLower(strings.TrimSpace(workerID))
	switch {
	case strings.HasPrefix(workerID, "claude-") && len(workerID) > len("claude-"):
		return FamilyClaude, true
	case strings.HasPrefix(workerID, "codex-") && len(workerID) > len("codex-"):
		return FamilyCodex, true
	default:
		return "", false
	}
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
	Family   AgentFamily
	Phase    PodPhase
	Started  *time.Time
	Finished *time.Time
	LogTail  string
	Repo     Repo
	JobKind  string
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

// TaskInfo is a TUI-friendly view of an AgentJob CR.
type TaskInfo struct {
	Name         string
	Repo         Repo
	Issue        int
	Phase        string // Pending, Running, Succeeded, Failed, Blocked
	Agent        string
	Slot         int
	NextAction   string
	Started      *time.Time
	Finished     *time.Time
	RuntimePhase PodPhase
	LogTail      string
}

type TimberbotInfo struct {
	Enabled bool
	Goal    string
	Rounds  int
	Host    string
}

type DispatchFamilyStatus struct {
	Family    AgentFamily
	Available bool
	Checked   bool
	Reason    string
}

type DispatchStateInfo struct {
	FamilyStatuses     []DispatchFamilyStatus
	DisabledFamilies   []AgentFamily
	ReviewReservations []ReviewReservation
}

type ReviewReservation struct {
	Name       string
	Repo       Repo
	PRNumber   int
	PRURL      string
	Branch     string
	Issue      int
	Family     AgentFamily
	WorkerID   string
	WorkerKind string
	ReservedAt *time.Time
	ExpiresAt  *time.Time
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
	Author    string
	State     string
	Owner     string
	Repo      Repo
	CreatedAt time.Time
}

type PullRequest struct {
	Number  int
	Title   string
	State   string // OPEN, MERGED, CLOSED
	Branch  string
	Issue   int // linked issue number (from branch name issue-N)
	Owner   string
	Waiting bool
	Repo    Repo
}

func ReviewLeaseName(repo Repo, prNumber int) string {
	return "review-" + sanitizeForName(repo.Owner) + "-" + sanitizeForName(repo.Name) + "-pr-" + strconv.Itoa(prNumber)
}

func sanitizeForName(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		isAlphaNum := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if isAlphaNum {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}
