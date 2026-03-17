package operator

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	GroupVersion  = schema.GroupVersion{Group: "k3sc.abix.dev", Version: "v1"}
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)
	AddToScheme   = SchemeBuilder.AddToScheme
)

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(GroupVersion,
		&ClaudeTask{},
		&ClaudeTaskList{},
	)
	metav1.AddToGroupVersion(scheme, GroupVersion)
	return nil
}

// ClaudeTask represents one execution of work on a GitHub issue.
// Multiple ClaudeTasks can exist for the same issue (execution history).
type ClaudeTask struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ClaudeTaskSpec   `json:"spec"`
	Status            ClaudeTaskStatus `json:"status,omitempty"`
}

type ClaudeTaskSpec struct {
	Repo        string `json:"repo"`        // e.g. "abix-/endless"
	RepoName    string `json:"repoName"`    // e.g. "endless"
	IssueNumber int    `json:"issueNumber"`
	RepoURL     string `json:"repoURL"`     // clone URL
}

type TaskPhase string

const (
	TaskPhasePending   TaskPhase = "Pending"   // created, waiting for slot
	TaskPhaseAssigned  TaskPhase = "Assigned"  // slot + agent assigned, claiming on github
	TaskPhaseRunning   TaskPhase = "Running"   // job created, pod active
	TaskPhaseSucceeded TaskPhase = "Succeeded" // pod completed successfully
	TaskPhaseFailed    TaskPhase = "Failed"    // pod failed (may retry)
	TaskPhaseBlocked   TaskPhase = "Blocked"   // too many failures, needs human
)

func IsTerminal(phase TaskPhase) bool {
	return phase == TaskPhaseSucceeded || phase == TaskPhaseFailed || phase == TaskPhaseBlocked
}

type ClaudeTaskStatus struct {
	Phase     TaskPhase    `json:"phase,omitempty"`
	Agent     string       `json:"agent,omitempty"`
	Slot      int          `json:"slot,omitempty"`
	JobName   string       `json:"jobName,omitempty"`
	LastError string       `json:"lastError,omitempty"`
	Reported   bool         `json:"reported,omitempty"`   // result comment posted to github
	LogTail    string       `json:"logTail,omitempty"`    // last meaningful output line
	NextAction string       `json:"nextAction,omitempty"` // needs-review, needs-human
	StartedAt  *metav1.Time `json:"startedAt,omitempty"`
	FinishedAt *metav1.Time `json:"finishedAt,omitempty"`
}

// ClaudeTaskList contains a list of ClaudeTasks.
type ClaudeTaskList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClaudeTask `json:"items"`
}

// DeepCopyObject implementations for runtime.Object interface.
func (in *ClaudeTask) DeepCopyObject() runtime.Object {
	out := new(ClaudeTask)
	*out = *in
	out.ObjectMeta = *in.ObjectMeta.DeepCopy()
	if in.Status.StartedAt != nil {
		t := *in.Status.StartedAt
		out.Status.StartedAt = &t
	}
	if in.Status.FinishedAt != nil {
		t := *in.Status.FinishedAt
		out.Status.FinishedAt = &t
	}
	return out
}

func (in *ClaudeTaskList) DeepCopyObject() runtime.Object {
	out := new(ClaudeTaskList)
	out.TypeMeta = in.TypeMeta
	out.ListMeta = *in.ListMeta.DeepCopy()
	if in.Items != nil {
		out.Items = make([]ClaudeTask, len(in.Items))
		for i := range in.Items {
			out.Items[i] = *in.Items[i].DeepCopyObject().(*ClaudeTask)
		}
	}
	return out
}
