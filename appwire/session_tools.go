package appwire

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"primeradiant.com/evener/agent/provenance"
)

// ManagerView names the selected thread exactly as thread/read does, carrying
// the session's own start-time Manager property as its daemon projects it.
// There is no hub-side setting: Manager is the whole truth.
type ManagerView struct {
	Ref        string `json:"ref"`
	InstanceID string `json:"instanceId"`
	Manager    bool   `json:"manager,omitempty"`
}

const (
	MethodHubSessionToolsIdentify         = "evener/hub/session-tools/identify"
	MethodHubSessionToolsInput            = "evener/hub/session-tools/input"
	MethodRuntimeSessionBind              = "evener/runtime/session/bind"
	MethodRuntimeSessionState             = "evener/runtime/session/state"
	MethodRuntimeSessionInvoke            = "evener/runtime/session/invoke"
	MethodRuntimeSessionWatch             = "evener/runtime/session/watch"
	MethodRuntimeSessionUnwatch           = "evener/runtime/session/unwatch"
	MethodRuntimeSessionCancel            = "evener/runtime/session/cancel"
	NotificationRuntimeSessionObservation = "evener/runtime/session/observation"
)

// SessionToolsPeerParams names the cooperating hub over an authenticated
// bridge connection. A hub token authenticates access to the host, not the
// peer's particular identity; HubID itself is nonsecret.
type SessionToolsPeerParams struct {
	HubID string `json:"hubId"`
}

type SessionToolsPeerResponse struct {
	HubID string `json:"hubId"`
}

// SessionToolsInput carries one qualified mutation on the identified bridge.
// The sending hub admits its local Manager and supplies CallerRef. The receiver
// qualifies that local ref with the connection's peer identity before recording
// attribution. Method is restricted to fixed input methods.
type SessionToolsInput struct {
	CallerRef string          `json:"callerRef"`
	Method    string          `json:"method"`
	Params    json.RawMessage `json:"params"`
}

// SessionHubClient is private runtime transport, never a launch option.
type SessionHubClient interface {
	State(context.Context) (ManagerView, error)
	Invoke(context.Context, SessionOperationRequest) (SessionOperationResponse, error)
	Watch(context.Context, SessionWatchRequest) (<-chan SessionWatchUpdate, error)
}

type SessionBinding struct {
	Ref        string `json:"ref"`
	InstanceID string `json:"instanceId"`
	HubID      string `json:"hubId"`
	BindingID  string `json:"bindingId"`
}

type SessionActor struct {
	Kind       string `json:"kind"`
	ManagerRef string `json:"managerRef,omitempty"`
}

// SessionActorProof is private hub-to-runtime transport metadata. No tool
// argument contains it; BindingID authenticates the target's live binding,
// while CallerRef labels the authenticated manager that sent the input.
type SessionActorProof struct {
	BindingID string       `json:"bindingId"`
	CallerRef string       `json:"callerRef"`
	Actor     SessionActor `json:"actor"`
}

// SessionOperationRequest is closed. Future tasks add members only when their
// owner exists, rather than advertising reserved operations.
type SessionOperationRequest struct {
	Discover  *ThreadListParams       `json:"discover,omitempty"`
	Read      *ThreadReadParams       `json:"read,omitempty"`
	Turns     *ThreadTurnsListParams  `json:"turns,omitempty"`
	Activity  *SessionActivityRequest `json:"activity,omitempty"`
	Tasks     *SessionTasksRequest    `json:"tasks,omitempty"`
	Job       *JobsGetParams          `json:"job,omitempty"`
	JobOutput *JobsOutputParams       `json:"jobOutput,omitempty"`
	Models    *ModelListParams        `json:"models,omitempty"`
	Create    *ThreadStartParams      `json:"create,omitempty"`
	Start     *TurnStartParams        `json:"start,omitempty"`
	Queue     *TurnQueueParams        `json:"queue,omitempty"`
	Steer     *TurnSteerParams        `json:"steer,omitempty"`
	Control   *SessionControlRequest  `json:"control,omitempty"`
	Stop      *SessionStopRequest     `json:"stop,omitempty"`
}

func (r SessionOperationRequest) Validate() error {
	if r.Start != nil && r.Start.ManagerProof != nil || r.Queue != nil && r.Queue.ManagerProof != nil || r.Steer != nil && r.Steer.ManagerProof != nil || r.Control != nil && (r.Control.Approval != nil && r.Control.Approval.ManagerProof != nil || r.Control.DrainQueue != nil && r.Control.DrainQueue.ManagerProof != nil || r.Control.PromoteQueued != nil && r.Control.PromoteQueued.ManagerProof != nil) {
		return InvalidParams("actor metadata is not a tool argument")
	}
	count := 0
	for _, present := range []bool{r.Discover != nil, r.Read != nil, r.Turns != nil, r.Activity != nil, r.Tasks != nil, r.Job != nil, r.JobOutput != nil, r.Models != nil, r.Create != nil, r.Start != nil, r.Queue != nil, r.Steer != nil, r.Control != nil, r.Stop != nil} {
		if present {
			count++
		}
	}
	if count != 1 {
		return InvalidParams("exactly one session operation is required")
	}
	if r.Create != nil && len(r.Create.Input) != 0 {
		return InvalidParams("Manager creation must start an empty session")
	}
	if r.Control != nil {
		return r.Control.Validate()
	}
	if r.Stop != nil {
		return r.Stop.Validate()
	}
	if r.Read != nil && (r.Read.Subscribe || r.Read.IncludeTurns) {
		return InvalidParams("status reads do not subscribe or include history")
	}
	if r.Turns != nil && (r.Turns.Ref == "" || r.Turns.ThreadID != "" || r.Turns.Before != nil || r.Turns.ItemsView != "" || r.Turns.ThreadContentParams != (ThreadContentParams{})) {
		return InvalidParams("Manager history requires a ref and cursor; local ranges and content filters are unsupported")
	}
	if r.Activity != nil {
		switch r.Activity.Resource {
		case SessionActivityResourceSummary, SessionActivityResourceDelegates, SessionActivityResourceJobs, SessionActivityResourceWatches:
		default:
			return InvalidParams("unknown activity resource")
		}
	}
	if r.Tasks != nil && (r.Tasks.Offset < 0 || r.Tasks.Limit < 0 || r.Tasks.Limit > 200 || r.Tasks.Offset > 0 && r.Tasks.Snapshot == "") {
		return InvalidParams("task paging requires a nonnegative offset, a page of at most 200, and a snapshot for continuation")
	}
	return nil
}

func (r *SessionOperationRequest) UnmarshalJSON(data []byte) error {
	type plain SessionOperationRequest
	var decoded plain
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&decoded); err != nil {
		return InvalidParams(err.Error())
	}
	*r = SessionOperationRequest(decoded)
	return r.Validate()
}

type SessionSource struct {
	ID string `json:"id"`
}

// SessionActivityRequest selects one existing activity owner; cursor and scope
// pass unchanged to the owner's bounded list method.
type SessionActivityRequest struct {
	Ref      string                  `json:"ref"`
	Resource SessionActivityResource `json:"resource"`
	Scope    SessionActivityScope    `json:"scope,omitempty"`
	Cursor   string                  `json:"cursor,omitempty"`
	Limit    int                     `json:"limit,omitempty"`
}

type SessionActivityResponse struct {
	Summary   *SessionActivitySummary   `json:"summary,omitempty"`
	Delegates *SessionDelegatesResponse `json:"delegates,omitempty"`
	Jobs      *SessionJobsResponse      `json:"jobs,omitempty"`
	Watches   *SessionWatchesResponse   `json:"watches,omitempty"`
}

type SessionTasksRequest struct {
	Ref      string `json:"ref"`
	Offset   int    `json:"offset,omitempty"`
	Limit    int    `json:"limit,omitempty"`
	Snapshot string `json:"snapshot,omitempty"`
}

type SessionTasksResponse struct {
	Data       []Task `json:"data" appwire:"nullable"`
	Snapshot   string `json:"snapshot,omitempty"`
	NextOffset *int   `json:"nextOffset,omitempty"`
}

// SessionTaskSnapshotChanged is a typed refusal, not an empty task page.
// Clients restart from offset zero when the full authoritative list changed.
func SessionTaskSnapshotChanged() WireError {
	return WireError{Code: CodeInvalidRequest, Message: "task snapshot changed; restart at offset 0", Data: struct {
		ErrorData
		Reason string `json:"reason"`
	}{ErrorData: ErrorData{EvenerErrorInfo: ErrorInvalidParams}, Reason: "snapshot_changed"}}
}

type SessionDiscovery struct {
	Threads ThreadListResponse `json:"threads"`
	Sources []SessionSource    `json:"sources"`
}

type SessionOperationResponse struct {
	Actor     SessionActor             `json:"actor"`
	Discover  *SessionDiscovery        `json:"discover,omitempty"`
	Read      *ThreadReadResponse      `json:"read,omitempty"`
	Turns     *ThreadTurnsListResponse `json:"turns,omitempty"`
	Activity  *SessionActivityResponse `json:"activity,omitempty"`
	Tasks     *SessionTasksResponse    `json:"tasks,omitempty"`
	Job       *JobsGetResponse         `json:"job,omitempty"`
	JobOutput *JobsOutputResponse      `json:"jobOutput,omitempty"`
	Models    *ModelListResponse       `json:"models,omitempty"`
	Create    *ThreadStartResponse     `json:"create,omitempty"`
	Start     *TurnStartResponse       `json:"start,omitempty"`
	Queue     *TurnQueueResponse       `json:"queue,omitempty"`
	Steer     *TurnSteerResponse       `json:"steer,omitempty"`
	Control   *SessionControlResponse  `json:"control,omitempty"`
	Stop      *SessionStopResponse     `json:"stop,omitempty"`
	FollowUps []SessionStatusFollowUp  `json:"followUps,omitempty"`
}

// SessionControlRequest exposes only implemented, typed Manager controls.
type SessionControlRequest struct {
	Action          string                          `json:"action"`
	Approval        *SandboxEscalationResolveParams `json:"approval,omitempty"`
	Resume          *ThreadResumeParams             `json:"resume,omitempty"`
	Fork            *ThreadForkParams               `json:"fork,omitempty"`
	Clear           *ThreadClearParams              `json:"clear,omitempty"`
	Model           *ThreadModelSetParams           `json:"model,omitempty"`
	VisionModel     *ThreadVisionModelSetParams     `json:"vision_model,omitempty"`
	ReasoningEffort *ThreadReasoningEffortSetParams `json:"reasoning_effort,omitempty"`
	Compact         *ThreadCompactStartParams       `json:"compact,omitempty"`
	DrainQueue      *TurnDrainAsSteerParams         `json:"drain_queue,omitempty"`
	PromoteQueued   *TurnPromoteQueuedAsSteerParams `json:"promote_queued,omitempty"`
	CancelQueued    *TurnCancelQueuedParams         `json:"cancel_queued,omitempty"`
	Rename          *ThreadNameSetParams            `json:"rename,omitempty"`
	Goal            *GoalSetParams                  `json:"goal,omitempty"`
	HumanNotes      *NotesHumanSetParams            `json:"human_notes,omitempty"`
	RemoveURL       *UrlsRemoveParams               `json:"remove_url,omitempty"`
	Archive         *ArchiveParams                  `json:"archive,omitempty"`
	Delete          *SessionDeleteParams            `json:"delete,omitempty"`
	Pin             *SessionPinAssignParams         `json:"pin,omitempty"`
	Unpin           *SessionPinUnpinParams          `json:"unpin,omitempty"`
	Seen            *SessionSeenSetParams           `json:"seen,omitempty"`
}

func (r SessionControlRequest) Validate() error {
	matching := 0
	for action, present := range map[string]bool{
		"approval": r.Approval != nil, "resume": r.Resume != nil, "fork": r.Fork != nil,
		"clear": r.Clear != nil, "model": r.Model != nil, "vision_model": r.VisionModel != nil,
		"reasoning_effort": r.ReasoningEffort != nil, "compact": r.Compact != nil,
		"drain_queue": r.DrainQueue != nil, "promote_queued": r.PromoteQueued != nil,
		"cancel_queued": r.CancelQueued != nil, "rename": r.Rename != nil, "goal": r.Goal != nil,
		"human_notes": r.HumanNotes != nil, "remove_url": r.RemoveURL != nil,
		"archive": r.Archive != nil, "delete": r.Delete != nil, "pin": r.Pin != nil,
		"unpin": r.Unpin != nil, "seen": r.Seen != nil,
	} {
		if present {
			matching++
			if r.Action != action {
				return InvalidParams("session control payload does not match action")
			}
		}
	}
	if matching != 1 {
		return InvalidParams("exactly one matching session control payload is required")
	}
	if r.Archive != nil && r.Archive.Kind != ArchiveTargetSession {
		return InvalidParams("Manager archive controls sessions only")
	}
	return nil
}

func (r *SessionControlRequest) UnmarshalJSON(data []byte) error {
	type plain SessionControlRequest
	var decoded plain
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&decoded); err != nil {
		return InvalidParams(err.Error())
	}
	*r = SessionControlRequest(decoded)
	return r.Validate()
}

type SessionControlResponse struct {
	Result        EmptyResponse                     `json:"result"`
	Actor         SessionActor                      `json:"actor"`
	Resume        *ThreadResumeResponse             `json:"resume,omitempty"`
	Fork          *ThreadForkResponse               `json:"fork,omitempty"`
	Clear         *ThreadClearResponse              `json:"clear,omitempty"`
	DrainQueue    *TurnDrainAsSteerResponse         `json:"drain_queue,omitempty"`
	PromoteQueued *TurnPromoteQueuedAsSteerResponse `json:"promote_queued,omitempty"`
	CancelQueued  *TurnCancelQueuedResponse         `json:"cancel_queued,omitempty"`
	Goal          *GoalSetResponse                  `json:"goal,omitempty"`
	HumanNotes    *NotesHumanSetResponse            `json:"human_notes,omitempty"`
	RemoveURL     *UrlsRemoveResponse               `json:"remove_url,omitempty"`
	Archive       *ArchiveResponse                  `json:"archive,omitempty"`
	Delete        *SessionDeleteResponse            `json:"delete,omitempty"`
	Pin           *SessionPinAssignResponse         `json:"pin,omitempty"`
	Unpin         *SessionPinUnpinResponse          `json:"unpin,omitempty"`
	Seen          *SessionSeenSetResponse           `json:"seen,omitempty"`
}

// SessionJobStopParams names a shell job on an addressed session. The flag
// mirrors the human stopChildren owner: immediate children only, not grandchildren.
type SessionJobStopParams struct {
	Ref             string `json:"ref"`
	JobID           string `json:"jobId"`
	IncludeChildren bool   `json:"includeChildren,omitempty"`
	MaxWaitMS       int    `json:"maxWaitMs,omitempty"`
}

type SessionOwnedDelegateStopParams struct {
	Ref        string `json:"ref"`
	DelegateID string `json:"delegateId"`
	MaxWaitMS  int    `json:"maxWaitMs,omitempty"`
}

type SessionJobStopResponse struct {
	JobID   string `json:"jobId"`
	Status  string `json:"status"`
	Outcome string `json:"outcome"`
}

// SessionStopRequest keeps single-run delegate, recursive owned-delegate,
// and one-level shell-child stop as distinct typed controls.
type SessionStopRequest struct {
	Mode          string                          `json:"mode"`
	Interrupt     *TurnInterruptParams            `json:"interrupt,omitempty"`
	Delegate      *DelegateStopParams             `json:"delegate,omitempty"`
	OwnedDelegate *SessionOwnedDelegateStopParams `json:"owned_delegate,omitempty"`
	Job           *SessionJobStopParams           `json:"job,omitempty"`
	Shutdown      *ThreadShutdownParams           `json:"shutdown,omitempty"`
	ForceStop     *ThreadForceStopParams          `json:"force_stop,omitempty"`
}

func (r SessionStopRequest) Validate() error {
	count := 0
	for mode, present := range map[string]bool{"interrupt": r.Interrupt != nil, "delegate": r.Delegate != nil, "owned_delegate": r.OwnedDelegate != nil, "job": r.Job != nil, "shutdown": r.Shutdown != nil, "force_stop": r.ForceStop != nil} {
		if present {
			count++
			if r.Mode != mode {
				return InvalidParams("session stop payload does not match mode")
			}
		}
	}
	if count != 1 {
		return InvalidParams("exactly one matching session stop payload is required")
	}
	if r.Delegate != nil && !strings.HasPrefix(r.Delegate.DelegateID, "dlg_") {
		return InvalidParams("delegate stop requires a dlg_ target")
	}
	if r.OwnedDelegate != nil && (!strings.HasPrefix(r.OwnedDelegate.DelegateID, "dlg_") || r.OwnedDelegate.MaxWaitMS < 0) {
		return InvalidParams("owned delegate stop requires a dlg_ target and non-negative maxWaitMs")
	}
	if r.Job != nil && (!strings.HasPrefix(r.Job.JobID, "job_") || r.Job.MaxWaitMS < 0) {
		return InvalidParams("job stop requires a job_ target and non-negative maxWaitMs")
	}
	return nil
}

func (r *SessionStopRequest) UnmarshalJSON(data []byte) error {
	type plain SessionStopRequest
	var decoded plain
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&decoded); err != nil {
		return InvalidParams(err.Error())
	}
	*r = SessionStopRequest(decoded)
	return r.Validate()
}

type SessionStopResponse struct {
	Actor     SessionActor            `json:"actor"`
	Interrupt *TurnInterruptResponse  `json:"interrupt,omitempty"`
	Delegate  *DelegateStopResponse   `json:"delegate,omitempty"`
	Job       *SessionJobStopResponse `json:"job,omitempty"`
	Result    EmptyResponse           `json:"result"`
}

// SessionStatusFollowUp names the currently implemented status tool, not a
// reserved future control. Its arguments preserve the source-qualified ref.
type SessionStatusFollowUp struct {
	Tool      string                 `json:"tool"`
	Arguments SessionStatusArguments `json:"arguments"`
}

type SessionStatusArguments struct {
	Hub SessionStatusTarget `json:"hub"`
}
type SessionStatusTarget struct {
	Ref         string                  `json:"ref"`
	Mode        string                  `json:"mode,omitempty"`
	Resource    SessionActivityResource `json:"resource,omitempty"`
	Scope       SessionActivityScope    `json:"scope,omitempty"`
	Cursor      string                  `json:"cursor,omitempty"`
	Limit       int                     `json:"limit,omitempty"`
	ItemLimit   int                     `json:"itemLimit,omitempty"`
	Offset      int                     `json:"offset,omitempty"`
	Snapshot    string                  `json:"snapshot,omitempty"`
	JobID       string                  `json:"jobId,omitempty"`
	BeforeBytes *int64                  `json:"beforeBytes,omitempty"`
	MaxBytes    int64                   `json:"maxBytes,omitempty"`
}

type SessionWatchRequest struct {
	Ref        string   `json:"ref"`
	Events     []string `json:"events,omitempty"`
	Completion bool     `json:"completion,omitempty"`
}

type SessionObservationMetadata struct {
	Ref              string             `json:"ref"`
	InstanceID       string             `json:"instanceId,omitempty"`
	GenerationID     string             `json:"generationId,omitempty"`
	TurnID           string             `json:"turnId,omitempty"`
	AnswerToTurnIDs  []string           `json:"answerToTurnIds,omitempty"`
	EntryID          string             `json:"entryId,omitempty"`
	EndTurn          bool               `json:"endTurn,omitempty"`
	EndReason        string             `json:"endReason,omitempty"`
	TerminalAccepted *bool              `json:"terminalAccepted,omitempty"`
	CausalKnown      bool               `json:"causalKnown"`
	Causal           *provenance.Causal `json:"causal,omitempty"`
}

type SessionWatchUpdate struct {
	Snapshot     *ThreadReadResponse        `json:"snapshot,omitempty"`
	Notification *Notification              `json:"notification,omitempty"`
	Event        string                     `json:"event,omitempty"`
	Metadata     SessionObservationMetadata `json:"metadata"`
}

type SessionRuntimeRequest struct {
	BindingID string                   `json:"bindingId"`
	Operation *SessionOperationRequest `json:"operation,omitempty"`
	Watch     *SessionWatchRequest     `json:"watch,omitempty"`
	WatchID   string                   `json:"watchId,omitempty"`
}

// SessionRuntimeCancel cancels only an in-flight reverse call on this binding.
// RequestID is its negative numeric wire ID, not an operation selector.
type SessionRuntimeCancel struct {
	BindingID string `json:"bindingId"`
	RequestID int64  `json:"requestId"`
}

type SessionRuntimeObservation struct {
	BindingID string              `json:"bindingId"`
	WatchID   string              `json:"watchId"`
	Update    *SessionWatchUpdate `json:"update,omitempty"`
	Ended     bool                `json:"ended,omitempty"`
}
