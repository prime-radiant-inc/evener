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

// SessionOperationRequest is closed. Future tasks add members only when their
// owner exists, rather than advertising reserved operations.
type SessionOperationRequest struct {
	Discover *ThreadListParams `json:"discover,omitempty"`
	Read     *ThreadReadParams `json:"read,omitempty"`
}

func (r SessionOperationRequest) Validate() error {
	if (r.Discover == nil) == (r.Read == nil) {
		return InvalidParams("exactly one session operation is required")
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
	FollowUps []SessionStatusFollowUp `json:"followUps,omitempty"`
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
	Metadata     SessionObservationMetadata `json:"metadata"`
}

type SessionRuntimeRequest struct {
	BindingID string                   `json:"bindingId"`
	Operation *SessionOperationRequest `json:"operation,omitempty"`
	Watch     *SessionWatchRequest     `json:"watch,omitempty"`
	WatchID   string                   `json:"watchId,omitempty"`
}

type SessionRuntimeObservation struct {
	BindingID string              `json:"bindingId"`
	WatchID   string              `json:"watchId"`
	Update    *SessionWatchUpdate `json:"update,omitempty"`
	Ended     bool                `json:"ended,omitempty"`
}
