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
	// Diagnostics names what a retained read could not establish about the
	// history behind this row, one code per fact.
	//
	// forwarded_fallback means the SCOPED copy supplied the selected record: the
	// row is a forwarded one, served because the owner's own record either does
	// not exist or could not be established as authoritative. It does not mean
	// the owner's journal was empty.
	//
	// owner_unavailable means the lookup could not obtain a USABLE owner record,
	// so a forwarded copy had to stand in for it. That covers more than a read
	// failure: the owner's journal may be absent or unreadable, it may be
	// readable and hold no record of this job, or a legacy owner hint may have
	// failed corroboration. A caller must not read this code as proof that the
	// journal itself was broken.
	//
	// The three integrity codes — journal_torn_tail, incomplete_lifecycle and
	// journal_scan_incomplete — describe damage in EITHER journal the read
	// consulted, not only the one that supplied the row: a competing branch being
	// torn or incoherent is still a fact about this job's history, and a read
	// that resolved through a damaged owner says so even when the returned row
	// came from the intact scoped journal. journal_torn_tail means a journal
	// ended in an unterminated append, incomplete_lifecycle that its events do
	// not describe a whole job, and journal_scan_incomplete that it stopped at
	// durable corruption — where a non-terminal status is not a settled state
	// because the finish may be the line that could not be decoded.
	//
	// Live reads leave it empty. Clients render or ignore it, but a stale
	// forwarded row is no longer indistinguishable from a live running one.
	Diagnostics []string `json:"diagnostics,omitempty"`
	// StatusUnconfirmed reports that this row's nonterminal Status is not
	// established — the evidence behind it cannot say whether the job is still
	// running. It is a fact about the SELECTED RECORD, which is what makes it
	// different from Diagnostics: those describe every journal the read
	// consulted, and a clean owner record can inherit an ancestor's damage while
	// still resting on intact bytes. A client that gates live polling on the
	// aggregate would stop following a healthy running job and miss its finish,
	// so the decision belongs to this field alone.
	//
	// Retained reads set it from evidence about THIS record alone: the supplying
	// journal ended in an unterminated append or stopped at durable corruption,
	// the record is a fragment the journal named without ever stating its status,
	// its own owner claim contradicts the validated identifier, or it is a
	// forwarded copy whose owner could not be consulted. Lifecycle damage the
	// fold reports for a DIFFERENT job sharing that journal is not evidence
	// against this row, so it does not set this field. Terminal rows never set
	// it: the first terminal write is a complete observation on its own. Live
	// reads never set it either.
	StatusUnconfirmed bool `json:"statusUnconfirmed,omitempty"`
	// StatusRecheckable reports that a LATER read could still establish the
	// status of an unconfirmed row, so a client that stops following it should
	// look again on a slow cadence rather than never.
	//
	// It is set only alongside StatusUnconfirmed, and it answers the same
	// question, asked of every journal the read consulted. Almost every cause of
	// doubt is a statement some eligible source can still amend while the client
	// is connected: an append that landed without its newline may complete, a
	// journal that named this job without stating its status may yet state it, a
	// finish that declared no end may be followed by a terminal one, a forwarded
	// copy's owner may become consultable, and a journal whose scan ended at a
	// clean record boundary may still receive the finish as a later append.
	//
	// It is false only when NO eligible source can still answer: this record's
	// own journal stopped at durable corruption, and any other journal the read
	// consulted is damaged the same way or does not exist. Damage to the selected
	// record's journal alone is not enough to make it false — an intact ancestor
	// journal that can still record the finish keeps the recheck on, and a
	// terminal observation it writes there outranks this row on the next read.
	//
	// This is deliberately NOT derived from Diagnostics. Those describe every
	// journal the read consulted, so an intact owner record with a torn tail can
	// inherit an ancestor's durable corruption; a client gating the recheck on
	// that would never look again and would miss a finish that arrives.
	StatusRecheckable bool `json:"statusRecheckable,omitempty"`
}

// JobActivityDelegate is one whole delegate in the activity tree. Activity
// updates replace a whole delegate, ordered by ProjectionRevision; a delegate
// has no independently patched turn containers.
type JobActivityDelegate struct {
	DelegateID         string `json:"delegateId"`
	OwnerSessionID     string `json:"ownerSessionId,omitempty"`
	RootSessionID      string `json:"rootSessionId,omitempty"`
	ChildSessionID     string `json:"childSessionId"`
	ChildRef           string `json:"childRef"`
	ParentDelegateID   string `json:"parentDelegateId,omitempty"`
	Type               string `json:"type,omitempty"`
	Lifecycle          string `json:"lifecycle,omitempty"`
	Phase              string `json:"phase,omitempty"`
	Status             string `json:"status,omitempty"`
	ProjectionRevision uint64 `json:"projectionRevision,omitempty"`
	Outcome            string `json:"outcome,omitempty"`
	Reason             string `json:"reason,omitempty"`
	Error              string `json:"error,omitempty"`
	Terminal           bool   `json:"terminal,omitempty"`
	Resumable          bool   `json:"resumable,omitempty"`
	// NeedsAttention means the delegate has delivered input it has not consumed
	// yet, such as a question, a descendant report or a job completion. The
	// delegate processes it autonomously; it is not by itself a human question.
	NeedsAttention      bool                   `json:"needsAttention,omitempty"`
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
