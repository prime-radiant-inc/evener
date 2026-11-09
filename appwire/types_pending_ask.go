package appwire

// MaxPendingAskComposites bounds the composite list on projected thread rows.
const MaxPendingAskComposites = 10

// PendingAskState identifies the calls still owned by the live pending-question
// set. Nil means unknown. Empty Calls and zero CalllessCount mean absence.
// BootGeneration and Revision together fence conditional answer admission.
type PendingAskState struct {
	Version        int              `json:"version"`
	InstanceID     string           `json:"instanceId"`
	Revision       uint64           `json:"revision"`
	Calls          []PendingAskCall `json:"calls"`
	BootGeneration string           `json:"bootGeneration,omitempty"`
	CalllessCount  int              `json:"calllessCount,omitempty"`
	CompositeTotal int              `json:"compositeTotal,omitempty"`
	CallsTruncated bool             `json:"callsTruncated,omitempty"`
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
