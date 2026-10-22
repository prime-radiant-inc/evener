package appwire

import (
	"bytes"
	"context"
	"encoding/json"

	"primeradiant.com/evener/agent/provenance"
)

// ManagerSetting is the hosting hub's durable human-controlled preference.
// HubID is a nonsecret authority identity, never a credential.
type ManagerSetting struct {
	HubID    string `json:"hubId"`
	Enabled  bool   `json:"enabled"`
	Revision uint64 `json:"revision"`
}

// ManagerView names the selected thread exactly as thread/read does. Historical
// refs remain addressable independently, even when their persisted workspace
// identity shares one private preference row after Clear.
type ManagerView struct {
	Ref        string         `json:"ref"`
	InstanceID string         `json:"instanceId"`
	Setting    ManagerSetting `json:"setting"`
}

type ThreadManagerSetParams struct {
	Ref     string `json:"ref"`
	Enabled bool   `json:"enabled"`
}

type ThreadManagerSetResponse struct {
	View ManagerView `json:"view"`
}

// ThreadManagerSetErrorData carries the unchanged current view on a refused
// write. View is absent when storage or identity could not be read.
type ThreadManagerSetErrorData struct {
	ErrorData
	View *ManagerView `json:"view,omitempty"`
}

const (
	MethodRuntimeSessionBind              = "evener/runtime/session/bind"
	MethodRuntimeSessionState             = "evener/runtime/session/state"
	MethodRuntimeSessionInvoke            = "evener/runtime/session/invoke"
	MethodRuntimeSessionWatch             = "evener/runtime/session/watch"
	MethodRuntimeSessionUnwatch           = "evener/runtime/session/unwatch"
	MethodRuntimeSessionCancel            = "evener/runtime/session/cancel"
	NotificationRuntimeSessionObservation = "evener/runtime/session/observation"
)

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
	Discover *ThreadListParams      `json:"discover,omitempty"`
	Read     *ThreadReadParams      `json:"read,omitempty"`
	Start    *TurnStartParams       `json:"start,omitempty"`
	Queue    *TurnQueueParams       `json:"queue,omitempty"`
	Steer    *TurnSteerParams       `json:"steer,omitempty"`
	Control  *SessionControlRequest `json:"control,omitempty"`
}

func (r SessionOperationRequest) Validate() error {
	if r.Start != nil && r.Start.ManagerProof != nil || r.Queue != nil && r.Queue.ManagerProof != nil || r.Steer != nil && r.Steer.ManagerProof != nil || r.Control != nil && r.Control.Approval != nil && r.Control.Approval.ManagerProof != nil {
		return InvalidParams("actor metadata is not a tool argument")
	}
	count := 0
	for _, present := range []bool{r.Discover != nil, r.Read != nil, r.Start != nil, r.Queue != nil, r.Steer != nil, r.Control != nil} {
		if present {
			count++
		}
	}
	if count != 1 {
		return InvalidParams("exactly one session operation is required")
	}
	if r.Control != nil {
		return r.Control.Validate()
	}
	if r.Read != nil && (r.Read.Subscribe || r.Read.IncludeTurns) {
		return InvalidParams("status reads do not subscribe or include history")
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

type SessionDiscovery struct {
	Threads ThreadListResponse `json:"threads"`
	Sources []SessionSource    `json:"sources"`
}

type SessionOperationResponse struct {
	Actor     SessionActor            `json:"actor"`
	Discover  *SessionDiscovery       `json:"discover,omitempty"`
	Read      *ThreadReadResponse     `json:"read,omitempty"`
	Start     *TurnStartResponse      `json:"start,omitempty"`
	Queue     *TurnQueueResponse      `json:"queue,omitempty"`
	Steer     *TurnSteerResponse      `json:"steer,omitempty"`
	Control   *SessionControlResponse `json:"control,omitempty"`
	FollowUps []SessionStatusFollowUp `json:"followUps,omitempty"`
}

// SessionControlRequest exposes only implemented, typed Manager controls.
type SessionControlRequest struct {
	Approval *SandboxEscalationResolveParams `json:"approval,omitempty"`
}

func (r SessionControlRequest) Validate() error {
	if r.Approval == nil {
		return InvalidParams("exactly one session control action is required")
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
	Result EmptyResponse `json:"result"`
	Actor  SessionActor  `json:"actor"`
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
	Ref string `json:"ref"`
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
