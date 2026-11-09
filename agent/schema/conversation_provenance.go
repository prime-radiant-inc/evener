package schema

// ConversationFrontier records one immutable change to exact request coverage.
// A base refers only to a previously recorded canonical frontier in this session.
// Repeated requests with unchanged coverage share its ID.
type ConversationFrontier struct {
	ID            string   `json:"id"`
	BaseID        string   `json:"base_id,omitempty"`
	AddedInputs   []string `json:"added_inputs,omitempty"`
	RemovedInputs []string `json:"removed_inputs,omitempty"`
}

// ConversationOutputProvenance belongs to the provider attempt, never to the
// mutable session state at the time its tools publish their results.
type ConversationOutputProvenance struct {
	Version       int    `json:"version"`
	InstanceID    string `json:"instance_id"`
	RunGeneration uint64 `json:"run_generation"`
	RequestID     string `json:"request_id"`
	FrontierID    string `json:"frontier_id"`
}
