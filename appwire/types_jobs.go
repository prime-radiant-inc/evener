package appwire

import "encoding/json"

type TaskListParams struct {
	Ref string `json:"ref,omitempty"`
}

type TaskListResponse struct {
	// Data is nil when task data is unavailable and non-nil (possibly empty)
	// for an authoritative list. The nullable annotation preserves that
	// distinction in the generated SDK without a pointer to the slice.
	Data []Task `json:"data" appwire:"nullable"`
}

type JobsListParams struct {
	Ref          string `json:"ref,omitempty"`
	Continuation string `json:"continuation,omitempty"`
}

type JobsListResponse struct {
	Data any `json:"data"`
}

type JobsTreeUpdatedParams struct {
	ThreadID string `json:"threadId"`
	Ref      string `json:"ref"`
	Revision uint64 `json:"revision"`
}

type JobActivityCounts struct {
	Active    int  `json:"active"`
	Failed    int  `json:"failed"`
	Completed int  `json:"completed"`
	Complete  bool `json:"complete"`
}

type JobActivityBranchState struct {
	Error        string `json:"error,omitempty"`
	Truncated    bool   `json:"truncated,omitempty"`
	Continuation string `json:"continuation,omitempty"`
}

type JobActivityJob struct {
	JobID          string `json:"jobId"`
	OwnerSessionID string `json:"ownerSessionId"`
	OwnerRef       string `json:"ownerRef"`
	TranscriptRef  string `json:"transcriptRef,omitempty"`
	Type           string `json:"type"`
	Status         string `json:"status"`
	Outcome        string `json:"outcome,omitempty"`
	Terminal       bool   `json:"terminal"`
	Background     bool   `json:"background"`
	HasOutput      bool   `json:"hasOutput"`
	Description    string `json:"description"`
	Command        string `json:"command,omitempty"`
	Task           string `json:"task,omitempty"`
	Reason         string `json:"reason,omitempty"`
	StartedAt      string `json:"startedAt"`
	EndedAt        string `json:"endedAt,omitempty"`
	ExitCode       *int   `json:"exitCode,omitempty"`
	OutputBytes    int64  `json:"outputBytes"`
	// LastOutputAt is the RFC3339 timestamp of the job's most recent
	// parent-observable output/activity. Live-only: retained jobs omit it, and
	// clients fall back to startedAt (or hide quiet time for terminal rows).
	LastOutputAt string `json:"lastOutputAt,omitempty"`
}

type JobActivityDelegate struct {
	DelegateID          string                 `json:"delegateId"`
	OwnerSessionID      string                 `json:"ownerSessionId,omitempty"`
	RootSessionID       string                 `json:"rootSessionId,omitempty"`
	ChildSessionID      string                 `json:"childSessionId"`
	ChildRef            string                 `json:"childRef"`
	ParentDelegateID    string                 `json:"parentDelegateId,omitempty"`
	Type                string                 `json:"type,omitempty"`
	Lifecycle           string                 `json:"lifecycle,omitempty"`
	Phase               string                 `json:"phase,omitempty"`
	Status              string                 `json:"status,omitempty"`
	ProjectionRevision  uint64                 `json:"projectionRevision,omitempty"`
	Outcome             string                 `json:"outcome,omitempty"`
	Reason              string                 `json:"reason,omitempty"`
	Error               string                 `json:"error,omitempty"`
	Terminal            bool                   `json:"terminal,omitempty"`
	Resumable           bool                   `json:"resumable,omitempty"`
	NotResumableReason  string                 `json:"notResumableReason,omitempty"`
	Mandate             string                 `json:"mandate,omitempty"`
	Task                string                 `json:"task,omitempty"`
	Description         string                 `json:"description,omitempty"`
	AgentType           string                 `json:"agentType,omitempty"`
	RequestedModel      string                 `json:"requestedModel,omitempty"`
	ResolvedProfileID   string                 `json:"resolvedProfileId,omitempty"`
	ResolvedModel       string                 `json:"resolvedModel,omitempty"`
	Model               string                 `json:"model,omitempty"`
	ReasoningEffort     string                 `json:"reasoningEffort,omitempty"`
	OriginTurnID        string                 `json:"originTurnId,omitempty"`
	OriginToolCallID    string                 `json:"originToolCallId,omitempty"`
	OriginItemID        string                 `json:"originItemId,omitempty"`
	RunStartedAt        string                 `json:"runStartedAt,omitempty"`
	RunEndedAt          string                 `json:"runEndedAt,omitempty"`
	LatestActivityAt    string                 `json:"latestActivityAt,omitempty"`
	RunningForMS        *int64                 `json:"runningForMs,omitempty"`
	QuietForMS          *int64                 `json:"quietForMs,omitempty"`
	DurationMS          *int64                 `json:"durationMs,omitempty"`
	PacketKind          string                 `json:"packetKind,omitempty"`
	Message             json.RawMessage        `json:"message,omitempty"`
	StructuredResult    json.RawMessage        `json:"structuredResult,omitempty"`
	StructuredValid     *bool                  `json:"structuredResultValid,omitempty"`
	StructuredReason    string                 `json:"structuredResultReason,omitempty"`
	Warnings            []string               `json:"warnings,omitempty"`
	Diagnostics         []string               `json:"diagnostics,omitempty"`
	ExhaustionBudget    string                 `json:"exhaustionBudget,omitempty"`
	ExhaustionLimit     int                    `json:"exhaustionLimit,omitempty"`
	ExhaustionResumable *bool                  `json:"exhaustionResumable,omitempty"`
	DelegationAllowance int                    `json:"delegationAllowance,omitempty"`
	ParentWatchGranted  bool                   `json:"parentWatchGranted,omitempty"`
	Worktree            *JobActivityWorktree   `json:"worktree,omitempty"`
	Turns               []JobActivityJob       `json:"turns"`
	Child               *JobActivitySession    `json:"child,omitempty"`
	Branch              JobActivityBranchState `json:"branch"`
	// Usage is the child session's cumulative self-only token totals. Nil when
	// the child has no token data (fresh session, old daemon, shell-only work).
	Usage *EvenerUsage `json:"usage,omitempty"`
}

type JobActivityWorktree struct {
	Path    string `json:"path"`
	Branch  string `json:"branch"`
	HeadSHA string `json:"headSha"`
	Ahead   int    `json:"ahead"`
	Dirty   bool   `json:"dirty"`
}

type JobActivityEntry struct {
	Kind     string               `json:"kind"` // shell | delegate
	Job      *JobActivityJob      `json:"job,omitempty"`
	Delegate *JobActivityDelegate `json:"delegate,omitempty"`
}

type JobActivitySession struct {
	SessionID   string                 `json:"sessionId"`
	Ref         string                 `json:"ref"`
	Label       string                 `json:"label"`
	Aggregate   string                 `json:"aggregate"`
	Counts      JobActivityCounts      `json:"counts"`
	Entries     []JobActivityEntry     `json:"entries"`
	Diagnostics []string               `json:"diagnostics,omitempty"`
	Branch      JobActivityBranchState `json:"branch"`
}

// JobActivityTree is ONE bounded page of a session's job-activity tree, not a
// complete snapshot: Root.Branch carries a continuation token when the page
// stopped short of what the session retains. Revision identifies the page
// generation the page was rendered against; a continuation is only meaningful
// within the revision it was minted from, so a client that receives a page at a
// different revision must discard it and fetch a fresh root page rather than
// splice its positions into the tree it already holds.
type JobActivityTree struct {
	Revision uint64             `json:"revision"`
	Root     JobActivitySession `json:"root"`
}

// AllJobActivityTypes is the explicit reachability root for the replacement
// jobs activity-tree contract. The AppWire generators walk this list in
// addition to Methods and Notifications so the JobActivity* wire types stay
// emitted even though evener/jobs/list itself keeps JobsListResponse.Data as any.
var AllJobActivityTypes = []any{
	JobActivityTree{},
	JobActivitySession{},
	JobActivityEntry{},
	JobActivityJob{},
	JobActivityDelegate{},
	JobActivityWorktree{},
	JobActivityCounts{},
	JobActivityBranchState{},
}

// JobOutputPage describes exact lifetime bytes [OffsetBytes,
// OffsetBytes+BytesReturned). Data is lossless utf8 or standard base64.
type JobOutputPage struct {
	OffsetBytes        int64  `json:"offsetBytes"`
	BytesReturned      int64  `json:"bytesReturned"`
	TotalBytes         int64  `json:"totalBytes"`
	RetainedStartBytes int64  `json:"retainedStartBytes"`
	Encoding           string `json:"encoding"`
	Data               string `json:"data"`
}

// JobsOutputParams reads up to MaxBytes ending at BeforeBytes, exclusively.
// An omitted BeforeBytes selects the latest page. Explicit zero selects the
// empty page at zero when zero is retained. MaxBytes defaults to 4 KiB and is
// capped at 64 KiB; a calculated page start is clipped to the retention floor.
type JobsOutputParams struct {
	Ref         string `json:"ref,omitempty"`
	JobID       string `json:"jobId"`
	MaxBytes    int64  `json:"maxBytes,omitempty"`
	BeforeBytes *int64 `json:"beforeBytes,omitempty"`
}

type JobsOutputResponse struct {
	Data JobOutputPage `json:"data"`
}

// JobsGetParams reads ONE job's metadata (the activity-tree job shape),
// including its untruncated command. Ref names the owning session; JobID names
// the job.
type JobsGetParams struct {
	Ref   string `json:"ref,omitempty"`
	JobID string `json:"jobId"`
}

// JobsGetResponse carries an appwire.JobActivityJob in Data: the same job node
// the activity tree renders, so a client can show the job's full command beside
// its output.
type JobsGetResponse struct {
	Data JobActivityJob `json:"data"`
}
