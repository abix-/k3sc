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
		&ReviewLease{},
		&ReviewLeaseList{},
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
	TriggerNonce     int64    `json:"triggerNonce,omitempty"`
	DisabledFamilies []string `json:"disabledFamilies,omitempty"`
}

type DispatchFamilyStatus struct {
	Family    string `json:"family"`
	Available bool   `json:"available"`
	Checked   bool   `json:"checked,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

type DispatchStateStatus struct {
	ObservedTriggerNonce int64                             `json:"observedTriggerNonce,omitempty"`
	IdleScans            int                               `json:"idleScans,omitempty"`
	LastError            string                            `json:"lastError,omitempty"`
	LastScanTime         *metav1.Time                      `json:"lastScanTime,omitempty"`
	LastWorkTime         *metav1.Time                      `json:"lastWorkTime,omitempty"`
	FamilyStatuses       []DispatchFamilyStatus            `json:"familyStatuses,omitempty"`
	ReviewReservations   []DispatchReviewReservationStatus `json:"reviewReservations,omitempty"`
}

type DispatchStateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DispatchState `json:"items"`
}

type ReviewLease struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              ReviewLeaseSpec   `json:"spec"`
	Status            ReviewLeaseStatus `json:"status,omitempty"`
}

type ReviewLeaseSpec struct {
	Repo        string       `json:"repo"`
	RepoName    string       `json:"repoName"`
	PRNumber    int          `json:"prNumber"`
	PRURL       string       `json:"prUrl,omitempty"`
	Branch      string       `json:"branch,omitempty"`
	IssueNumber int          `json:"issueNumber,omitempty"`
	Family      string       `json:"family"`
	WorkerID    string       `json:"workerId"`
	WorkerKind  string       `json:"workerKind,omitempty"`
	ReservedAt  *metav1.Time `json:"reservedAt,omitempty"`
	ExpiresAt   *metav1.Time `json:"expiresAt,omitempty"`
}

type ReviewLeaseStatus struct {
	Phase string `json:"phase,omitempty"`
}

type ReviewLeaseList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ReviewLease `json:"items"`
}

type DispatchReviewReservationStatus struct {
	Repo        string       `json:"repo"`
	RepoName    string       `json:"repoName"`
	PRNumber    int          `json:"prNumber"`
	PRURL       string       `json:"prUrl,omitempty"`
	Branch      string       `json:"branch,omitempty"`
	IssueNumber int          `json:"issueNumber,omitempty"`
	Family      string       `json:"family"`
	WorkerID    string       `json:"workerId"`
	WorkerKind  string       `json:"workerKind,omitempty"`
	ReservedAt  *metav1.Time `json:"reservedAt,omitempty"`
	ExpiresAt   *metav1.Time `json:"expiresAt,omitempty"`
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
	if in.Spec.DisabledFamilies != nil {
		out.Spec.DisabledFamilies = append([]string(nil), in.Spec.DisabledFamilies...)
	}
	if in.Status.LastScanTime != nil {
		t := *in.Status.LastScanTime
		out.Status.LastScanTime = &t
	}
	if in.Status.LastWorkTime != nil {
		t := *in.Status.LastWorkTime
		out.Status.LastWorkTime = &t
	}
	if in.Status.FamilyStatuses != nil {
		out.Status.FamilyStatuses = append([]DispatchFamilyStatus(nil), in.Status.FamilyStatuses...)
	}
	if in.Status.ReviewReservations != nil {
		out.Status.ReviewReservations = make([]DispatchReviewReservationStatus, len(in.Status.ReviewReservations))
		copy(out.Status.ReviewReservations, in.Status.ReviewReservations)
		for i := range out.Status.ReviewReservations {
			if in.Status.ReviewReservations[i].ReservedAt != nil {
				t := *in.Status.ReviewReservations[i].ReservedAt
				out.Status.ReviewReservations[i].ReservedAt = &t
			}
			if in.Status.ReviewReservations[i].ExpiresAt != nil {
				t := *in.Status.ReviewReservations[i].ExpiresAt
				out.Status.ReviewReservations[i].ExpiresAt = &t
			}
		}
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

func (in *ReviewLease) DeepCopyObject() runtime.Object {
	out := new(ReviewLease)
	*out = *in
	out.ObjectMeta = *in.ObjectMeta.DeepCopy()
	if in.Spec.ReservedAt != nil {
		t := *in.Spec.ReservedAt
		out.Spec.ReservedAt = &t
	}
	if in.Spec.ExpiresAt != nil {
		t := *in.Spec.ExpiresAt
		out.Spec.ExpiresAt = &t
	}
	return out
}

func (in *ReviewLeaseList) DeepCopyObject() runtime.Object {
	out := new(ReviewLeaseList)
	out.TypeMeta = in.TypeMeta
	out.ListMeta = *in.ListMeta.DeepCopy()
	if in.Items != nil {
		out.Items = make([]ReviewLease, len(in.Items))
		for i := range in.Items {
			out.Items[i] = *in.Items[i].DeepCopyObject().(*ReviewLease)
		}
	}
	return out
}
