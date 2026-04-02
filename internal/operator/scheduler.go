package operator

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/abix-/k3sc/internal/github"
	coretypes "github.com/abix-/k3sc/internal/types"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const MaxFailures = 3

// Verbose enables full structured logging from controller-runtime.
// Set via --verbose flag on the operator command.
var Verbose bool

var edt = time.FixedZone("EDT", -4*3600)

// olog prints a concise timestamped log line: "18:27:22 [prefix] message"
func olog(prefix, format string, args ...any) {
	t := time.Now().In(edt).Format("15:04:05")
	msg := fmt.Sprintf(format, args...)
	fmt.Printf("%s [%s] %s\n", t, prefix, msg)
}

const usageLimitLookback = 15 * time.Minute

var (
	familyMu   sync.Mutex
	nextFamily = coretypes.FamilyClaude
)

func pickFamily() coretypes.AgentFamily {
	familyMu.Lock()
	defer familyMu.Unlock()

	f := nextFamily
	if nextFamily == coretypes.FamilyClaude {
		nextFamily = coretypes.FamilyCodex
	} else {
		nextFamily = coretypes.FamilyClaude
	}
	return f
}

func fullRepo(repo coretypes.Repo) string {
	return repo.Owner + "/" + repo.Name
}

func issueKey(repo string, issue int) string {
	return strings.ToLower(fmt.Sprintf("%s#%d", repo, issue))
}

var attemptCounter uint32

func attemptTaskName(repo string, issue int) string {
	parts := strings.SplitN(repo, "/", 2)
	owner := ""
	name := repo
	if len(parts) == 2 {
		owner = parts[0]
		name = parts[1]
	}
	ts := time.Now().Unix()
	seq := atomic.AddUint32(&attemptCounter, 1)
	return fmt.Sprintf("%s-%d-%s-%d-%d", sanitizeName(name), issue, sanitizeName(owner), ts, seq)
}

func sanitizeName(s string) string {
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

// CreateTask creates a new AgentJob CRD for an issue. Used by both the CLI
// dispatch command and the controller's follow-up logic.
func CreateTask(ctx context.Context, c client.Client, issue coretypes.Issue, failureCount int) (string, error) {
	// look up PR number for review jobs
	var prNumber int
	if issue.State == "needs-review" {
		if pr, err := github.GetOpenPRNumber(ctx, issue.Repo, issue.Number); err == nil {
			prNumber = pr
		}
	}

	task := &AgentJob{
		TypeMeta: metav1.TypeMeta{
			APIVersion: GroupVersion.String(),
			Kind:       "AgentJob",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      attemptTaskName(fullRepo(issue.Repo), issue.Number),
			Namespace: coretypes.Namespace,
		},
		Spec: AgentJobSpec{
			Repo:        fullRepo(issue.Repo),
			RepoName:    issue.Repo.Name,
			IssueNumber: issue.Number,
			PRNumber:    prNumber,
			RepoURL:     issue.Repo.CloneURL(),
			OriginState: issue.State,
		},
		Status: AgentJobStatus{
			FailureCount: failureCount,
		},
	}
	if err := c.Create(ctx, task); err != nil {
		return "", err
	}
	return task.Name, nil
}
