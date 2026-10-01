package appwire

import "encoding/json"

// SessionActivityScope selects logical owners within the addressed delegate graph.
type SessionActivityScope string

const (
	SessionActivityScopeSession SessionActivityScope = "session"
	SessionActivityScopeSubtree SessionActivityScope = "subtree"
)

// SessionActivityResource identifies one independently refreshed activity read.
type SessionActivityResource string

const (
	SessionActivityResourceSummary   SessionActivityResource = "summary"
	SessionActivityResourceDelegates SessionActivityResource = "delegates"
	SessionActivityResourceJobs      SessionActivityResource = "jobs"
	SessionActivityResourceWatches   SessionActivityResource = "watches"
)

// SessionActivityReadParams addresses a session explicitly. An omitted scope
// selects session ownership; it never makes an omitted ref select a root.
type SessionActivityReadParams struct {
	Ref   string               `json:"ref"`
	Scope SessionActivityScope `json:"scope,omitempty"`
}

// SessionActivityListParams selects a bounded page within one activity resource.
// Limit defaults to 50 and is capped at 200. Cursor is opaque and scope-bound.
type SessionActivityListParams struct {
	Ref    string               `json:"ref"`
	Scope  SessionActivityScope `json:"scope,omitempty"`
	Cursor string               `json:"cursor,omitempty"`
	Limit  int                  `json:"limit,omitempty"`
}

// SessionActivityAncestor provides breadcrumbs without reading sibling activity.
type SessionActivityAncestor struct {
	Ref        string `json:"ref"`
	SessionID  string `json:"sessionId"`
	DelegateID string `json:"delegateId,omitempty"`
	Title      string `json:"title"`
}

// SessionActivityContext separates the routing alias from the resolved session
// identity. Epoch identifies the read source incarnation, not its recency.
type SessionActivityContext struct {
	Ref        string                    `json:"ref"`
	SessionID  string                    `json:"sessionId"`
	RootRef    string                    `json:"rootRef"`
	ParentRef  string                    `json:"parentRef,omitempty"`
	DelegateID string                    `json:"delegateId,omitempty"`
	Ancestors  []SessionActivityAncestor `json:"ancestors"`
	// AncestryKnown distinguishes proven root/lineage from bounded retained index progress.
	AncestryKnown bool   `json:"ancestryKnown"`
	Epoch         string `json:"epoch"`
	Availability  string `json:"availability"` // live | retained
}

// SessionActivityCounts describes all retained data in the declared scope,
// independently of downloaded pages. Numeric fields make no claim when Known is false.
type SessionActivityCounts struct {
	Known     bool `json:"known"`
	Total     int  `json:"total"`
	Active    int  `json:"active"`
	Failed    int  `json:"failed"`
	Completed int  `json:"completed"`
}

type SessionActivitySummary struct {
	Context   SessionActivityContext `json:"context"`
	Scope     SessionActivityScope   `json:"scope"`
	Delegates SessionActivityCounts  `json:"delegates"`
	Jobs      SessionActivityCounts  `json:"jobs"`
	Watches   SessionActivityCounts  `json:"watches"`
}

// SessionActivityIssue makes unavailable branches and incomplete retained sources explicit.
type SessionActivityIssue struct {
	Ref  string `json:"ref"`
	Code string `json:"code"`
}

// SessionActivityPage distinguishes authoritative emptiness from an incomplete
// read. A scan may advance NextCursor without emitting rows.
type SessionActivityPage struct {
	NextCursor string                 `json:"nextCursor,omitempty"`
	Complete   bool                   `json:"complete"`
	Issues     []SessionActivityIssue `json:"issues"`
}

// SessionDelegate is a stable compact resource, independent of its activation
// jobs and child runtime. ChildRef addresses retained child history when available.
type SessionDelegate struct {
	// Name is the immutable caller display label, capped at 200 Unicode code points.
	// Unnamed descriptors omit it; delegate IDs and refs remain the addressing keys.
	Name string `json:"name,omitempty"`
	// RunGeneration identifies the current activation; zero means no run has started.
	RunGeneration uint64 `json:"runGeneration"`
	// ReportPreview is the settled current run's reported text, capped at 4096
	// Unicode code points including an ellipsis when truncated.
	ReportPreview          string               `json:"reportPreview,omitempty"`
	ReportPreviewTruncated bool                 `json:"reportPreviewTruncated,omitempty"`
	DelegateID             string               `json:"delegateId"`
	OwnerRef               string               `json:"ownerRef"`
	RootRef                string               `json:"rootRef"`
	ChildRef               string               `json:"childRef"`
	ParentDelegateID       string               `json:"parentDelegateId,omitempty"`
	Description            string               `json:"description"`
	Task                   string               `json:"task"`
	Type                   string               `json:"type"`
	Lifecycle              string               `json:"lifecycle"`
	Phase                  string               `json:"phase"`
	Status                 string               `json:"status"`
	Outcome                string               `json:"outcome,omitempty"`
	Reason                 string               `json:"reason,omitempty"`
	Error                  string               `json:"error,omitempty"`
	Terminal               bool                 `json:"terminal"`
	Resumable              bool                 `json:"resumable"`
	NotResumableReason     string               `json:"notResumableReason,omitempty"`
	Model                  string               `json:"model,omitempty"`
	ReasoningEffort        string               `json:"reasoningEffort,omitempty"`
	RunStartedAt           string               `json:"runStartedAt,omitempty"`
	RunEndedAt             string               `json:"runEndedAt,omitempty"`
	LatestActivityAt       string               `json:"latestActivityAt,omitempty"`
	Usage                  *EvenerUsage         `json:"usage,omitempty"`
	Worktree               *JobActivityWorktree `json:"worktree,omitempty"`
}

type SessionWatchState string

const (
	SessionWatchStateArmed   SessionWatchState = "armed"
	SessionWatchStateEnded   SessionWatchState = "ended"
	SessionWatchStateUnknown SessionWatchState = "unknown"
)

// SessionWatch belongs to its logical receiver even when a descendant manager
// observes the source. Unknown state means retained evidence cannot establish liveness.
type SessionWatch struct {
	OwnerRef    string            `json:"ownerRef"`
	ReceiverRef string            `json:"receiverRef"`
	State       SessionWatchState `json:"state"`
	Watch       EvenerWatchInfo   `json:"watch"`
}

type SessionDelegatesResponse struct {
	Context   SessionActivityContext `json:"context"`
	Scope     SessionActivityScope   `json:"scope"`
	Page      SessionActivityPage    `json:"page"`
	Delegates []SessionDelegate      `json:"delegates"`
}

// SessionJobsResponse contains shell jobs; output stays behind the bounded job output API.
type SessionJobsResponse struct {
	Context SessionActivityContext `json:"context"`
	Scope   SessionActivityScope   `json:"scope"`
	Page    SessionActivityPage    `json:"page"`
	Jobs    []JobActivityJob       `json:"jobs"`
}

type SessionWatchesResponse struct {
	Context SessionActivityContext `json:"context"`
	Scope   SessionActivityScope   `json:"scope"`
	Page    SessionActivityPage    `json:"page"`
	Watches []SessionWatch         `json:"watches"`
}

// SessionActivityChangedParams invalidates reads; it does not carry lifecycle
// state. ThreadID is the subscription identity; SessionID is the resolved session.
type SessionActivityChangedParams struct {
	ThreadID  string                    `json:"threadId"`
	Ref       string                    `json:"ref"`
	SessionID string                    `json:"sessionId"`
	Resources []SessionActivityResource `json:"resources"`
}

// MarshalJSON preserves the required arrays even when producers use zero values.
func (value SessionActivityContext) MarshalJSON() ([]byte, error) {
	type wire SessionActivityContext
	value.Ancestors = activityArray(value.Ancestors)
	return json.Marshal(wire(value))
}

func (value SessionActivityPage) MarshalJSON() ([]byte, error) {
	type wire SessionActivityPage
	value.Issues = activityArray(value.Issues)
	return json.Marshal(wire(value))
}

func (value SessionDelegatesResponse) MarshalJSON() ([]byte, error) {
	type wire SessionDelegatesResponse
	value.Delegates = activityArray(value.Delegates)
	return json.Marshal(wire(value))
}

func (value SessionJobsResponse) MarshalJSON() ([]byte, error) {
	type wire SessionJobsResponse
	value.Jobs = activityArray(value.Jobs)
	return json.Marshal(wire(value))
}

func (value SessionWatchesResponse) MarshalJSON() ([]byte, error) {
	type wire SessionWatchesResponse
	value.Watches = activityArray(value.Watches)
	return json.Marshal(wire(value))
}

func activityArray[T any](rows []T) []T {
	if rows == nil {
		return []T{}
	}
	return rows
}

func (value SessionActivityChangedParams) WithNotificationTarget(threadID, ref string) NotificationTargeted {
	value.ThreadID, value.Ref = threadID, ref
	return value
}
