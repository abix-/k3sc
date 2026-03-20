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
		&AgentJob{},
		&AgentJobList{},
		&DispatchState{},
		&DispatchStateList{},
	)
	metav1.AddToGroupVersion(scheme, GroupVersion)
	return nil
}

// AgentJob represents the current operator-managed execution state for a GitHub issue.
type AgentJob struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              AgentJobSpec   `json:"spec"`
	Status            AgentJobStatus `json:"status,omitempty"`
}

type AgentJobSpec struct {
	Repo        string `json:"repo"`     // e.g. "abix-/endless"
	RepoName    string `json:"repoName"` // e.g. "endless"
	IssueNumber int    `json:"issueNumber"`
	RepoURL     string `json:"repoURL"` // clone URL
	// Slot/Agent/Family are operator-assigned dispatch hints kept in spec for
	// compatibility with older objects and to survive create-before-status-update races.
	Slot        int    `json:"slot,omitempty"`
	Agent       string `json:"agent,omitempty"`
	Family      string `json:"family,omitempty"`
	OriginState string `json:"originState"` // "ready" or "needs-review" -- determines next state on completion
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

type AgentJobStatus struct {
	Phase        TaskPhase    `json:"phase,omitempty"`
	Agent        string       `json:"agent,omitempty"`
	Slot         int          `json:"slot,omitempty"`
	Family       string       `json:"family,omitempty"`
	JobName      string       `json:"jobName,omitempty"`
	LastError    string       `json:"lastError,omitempty"`
	FailureCount int          `json:"failureCount,omitempty"`
	Reported     bool         `json:"reported,omitempty"`   // result comment posted to github
	LogTail      string       `json:"logTail,omitempty"`    // last meaningful output line
	NextAction   string       `json:"nextAction,omitempty"` // needs-review, needs-human
	StartedAt    *metav1.Time `json:"startedAt,omitempty"`
	FinishedAt   *metav1.Time `json:"finishedAt,omitempty"`
}

// AgentJobList contains a list of AgentJobs.
type AgentJobList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AgentJob `json:"items"`
}

type DispatchState struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              DispatchStateSpec   `json:"spec,omitempty"`
	Status            DispatchStateStatus `json:"status,omitempty"`
}

type DispatchStateSpec struct {
	TriggerNonce int64 `json:"triggerNonce,omitempty"`
}

type DispatchStateStatus struct {
	ObservedTriggerNonce int64        `json:"observedTriggerNonce,omitempty"`
	IdleScans            int          `json:"idleScans,omitempty"`
	LastError            string       `json:"lastError,omitempty"`
	LastScanTime         *metav1.Time `json:"lastScanTime,omitempty"`
	LastWorkTime         *metav1.Time `json:"lastWorkTime,omitempty"`
}

type DispatchStateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DispatchState `json:"items"`
}

// DeepCopyObject implementations for runtime.Object interface.
func (in *AgentJob) DeepCopyObject() runtime.Object {
	out := new(AgentJob)
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

func (in *AgentJobList) DeepCopyObject() runtime.Object {
	out := new(AgentJobList)
	out.TypeMeta = in.TypeMeta
	out.ListMeta = *in.ListMeta.DeepCopy()
	if in.Items != nil {
		out.Items = make([]AgentJob, len(in.Items))
		for i := range in.Items {
			out.Items[i] = *in.Items[i].DeepCopyObject().(*AgentJob)
		}
	}
	return out
}

func (in *DispatchState) DeepCopyObject() runtime.Object {
	out := new(DispatchState)
	*out = *in
	out.ObjectMeta = *in.ObjectMeta.DeepCopy()
	if in.Status.LastScanTime != nil {
		t := *in.Status.LastScanTime
		out.Status.LastScanTime = &t
	}
	if in.Status.LastWorkTime != nil {
		t := *in.Status.LastWorkTime
		out.Status.LastWorkTime = &t
	}
	return out
}

func (in *DispatchStateList) DeepCopyObject() runtime.Object {
	out := new(DispatchStateList)
	out.TypeMeta = in.TypeMeta
	out.ListMeta = *in.ListMeta.DeepCopy()
	if in.Items != nil {
		out.Items = make([]DispatchState, len(in.Items))
		for i := range in.Items {
			out.Items[i] = *in.Items[i].DeepCopyObject().(*DispatchState)
		}
	}
	return out
}
