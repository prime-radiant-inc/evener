package appwire

// ConversationProvenanceState is a complete materialized authority for one
// runtime instance. Absence, unsupported versions and invalid graphs are unknown.
// Retired inputs remain named, but no longer fence output eligibility.
type ConversationProvenanceState struct {
	Version            int                    `json:"version"`
	Authoritative      bool                   `json:"authoritative"`
	Revision           uint64                 `json:"revision"`
	InstanceID         string                 `json:"instanceId"`
	AdmittedInputs     []string               `json:"admittedInputs"`
	RetiredInputs      []string               `json:"retiredInputs"`
	ConsumedFrontierID string                 `json:"consumedFrontierId,omitempty"`
	Frontiers          []ConversationFrontier `json:"frontiers"`
}

type ConversationFrontier struct {
	ID            string   `json:"id"`
	BaseID        string   `json:"baseId,omitempty"`
	AddedInputs   []string `json:"addedInputs,omitempty"`
	RemovedInputs []string `json:"removedInputs,omitempty"`
}

// ConversationOutputProvenance identifies one immutable provider request and
// its durable output item, independently of later admissions and publications.
type ConversationOutputProvenance struct {
	Version       int    `json:"version"`
	InstanceID    string `json:"instanceId"`
	RunGeneration uint64 `json:"runGeneration"`
	RequestID     string `json:"requestId"`
	FrontierID    string `json:"frontierId"`
	SourceItemID  string `json:"sourceItemId"`
}

type ConversationProvenanceChangedParams struct {
	ThreadID   string                       `json:"threadId"`
	Ref        string                       `json:"ref"`
	Provenance *ConversationProvenanceState `json:"provenance"`
}

func (p ConversationProvenanceChangedParams) WithNotificationTarget(threadID, ref string) NotificationTargeted {
	p.ThreadID, p.Ref = threadID, ref
	return p
}
