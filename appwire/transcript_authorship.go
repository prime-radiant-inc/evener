package appwire

// TranscriptAuthorKind identifies the proven source of a transcript item.
type TranscriptAuthorKind string

const (
	TranscriptAuthorHuman   TranscriptAuthorKind = "human"
	TranscriptAuthorAgent   TranscriptAuthorKind = "agent"
	TranscriptAuthorSystem  TranscriptAuthorKind = "system"
	TranscriptAuthorUnknown TranscriptAuthorKind = "unknown"
)

// TranscriptAuthor is recorded provenance, never inferred from thread placement.
type TranscriptAuthor struct {
	Kind         TranscriptAuthorKind `json:"kind"`
	SessionRef   string               `json:"sessionRef,omitempty"`
	OwnerRef     string               `json:"ownerRef,omitempty"`
	DelegateID   string               `json:"delegateId,omitempty"`
	DelegateName string               `json:"delegateName,omitempty"`
}

// TranscriptAuthorship preserves original author and proven copy inheritance.
// An absent pointer means both author and inheritance are unknown.
type TranscriptAuthorship struct {
	Author    TranscriptAuthor `json:"author"`
	Inherited bool             `json:"inherited"`
}
