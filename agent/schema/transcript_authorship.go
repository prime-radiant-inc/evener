package schema

// TranscriptAuthorKind describes the original speaker, not the containing session.
type TranscriptAuthorKind string

// Original transcript speaker kinds.
const (
	TranscriptAuthorHuman   TranscriptAuthorKind = "human"
	TranscriptAuthorAgent   TranscriptAuthorKind = "agent"
	TranscriptAuthorSystem  TranscriptAuthorKind = "system"
	TranscriptAuthorUnknown TranscriptAuthorKind = "unknown"
)

// TranscriptAuthor records the original speaker and its canonical agent identity.
type TranscriptAuthor struct {
	Kind           TranscriptAuthorKind `json:"kind"`
	SessionID      string               `json:"session_id,omitempty"`
	OwnerSessionID string               `json:"owner_session_id,omitempty"`
	DelegateID     string               `json:"delegate_id,omitempty"`
	DelegateName   string               `json:"delegate_name,omitempty"`
}

// TranscriptAuthorship records original authorship and proven inheritance.
// Nil authorship means neither author nor inheritance is known.
type TranscriptAuthorship struct {
	Author    TranscriptAuthor `json:"author"`
	Inherited bool             `json:"inherited"`
}

// Clone returns an independent snapshot, preserving absent authorship as nil.
func (a *TranscriptAuthorship) Clone() *TranscriptAuthorship {
	if a == nil {
		return nil
	}
	cloned := *a
	return &cloned
}
