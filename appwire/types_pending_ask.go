package appwire

// PendingAskState identifies the calls still owned by the live pending-question
// set. Nil means unknown, while a nonnil empty Calls means authoritative absence.
// These references are presentation hints, not a conditional answer API.
type PendingAskState struct {
	Version    int              `json:"version"`
	InstanceID string           `json:"instanceId"`
	Revision   uint64           `json:"revision"`
	Calls      []PendingAskCall `json:"calls"`
}

// PendingAskCall joins a pending call to its existing transcript item.
type PendingAskCall struct {
	TurnID string `json:"turnId"`
	CallID string `json:"callId"`
}

func ClonePendingAsk(value *PendingAskState) *PendingAskState {
	if value == nil {
		return nil
	}
	out := *value
	out.Calls = append([]PendingAskCall{}, value.Calls...)
	return &out
}
