package schema

// TranscriptAuthorKind describes the original speaker, not the containing session.
type TranscriptAuthorKind string

const (
	TranscriptAuthorHuman   TranscriptAuthorKind = "human"
	TranscriptAuthorAgent   TranscriptAuthorKind = "agent"
	TranscriptAuthorSystem  TranscriptAuthorKind = "system"
	TranscriptAuthorUnknown TranscriptAuthorKind = "unknown"
)

type TranscriptAuthor struct {
	Kind           TranscriptAuthorKind `json:"kind"`
	SessionID      string               `json:"session_id,omitempty"`
	OwnerSessionID string               `json:"owner_session_id,omitempty"`
	DelegateID     string               `json:"delegate_id,omitempty"`
	DelegateName   string               `json:"delegate_name,omitempty"`
}

// Nil authorship means neither author nor inheritance is known.
type TranscriptAuthorship struct {
	Author    TranscriptAuthor `json:"author"`
	Inherited bool             `json:"inherited"`
}

func (a *TranscriptAuthorship) Clone() *TranscriptAuthorship {
	if a == nil {
		return nil
	}
	copy := *a
	return &copy
}
