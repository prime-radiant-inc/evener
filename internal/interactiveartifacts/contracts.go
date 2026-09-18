package interactiveartifacts

import (
	"encoding/json"
	"errors"
)

// Version is a positive integer in the browser-safe range.
type Version int64

func (v *Version) UnmarshalJSON(data []byte) error {
	parsed, err := ParseJSON(data, MaxRequestBytes)
	if err != nil {
		return err
	}
	token, ok := parsed.(json.Number)
	if !ok {
		return errors.New("invalid artifact version")
	}
	n, integer, safe := exactSafeInteger(string(token))
	if !integer || !safe || n <= 0 {
		return errors.New("invalid artifact version")
	}
	*v = Version(n)
	return nil
}

type Format string
type FormatVersion Version

func (v *FormatVersion) UnmarshalJSON(data []byte) error {
	var version Version
	if err := json.Unmarshal(data, &version); err != nil {
		return err
	}
	*v = FormatVersion(version)
	return nil
}

type Include string
type DiagnosticKind string

// PublishRequest is validated before defaults are applied. Creation defaults its
// state to {}; updates leave InitialState nil and preserve the stored checkpoint.
type PublishRequest struct {
	MutationID             string          `json:"mutationId"`
	ArtifactID             string          `json:"artifactId,omitempty"`
	ExpectedSourceRevision Version         `json:"expectedSourceRevision,omitempty"`
	ExpectedStateVersion   Version         `json:"expectedStateVersion,omitempty"`
	Title                  string          `json:"title"`
	Summary                string          `json:"summary"`
	HTML                   string          `json:"html"`
	InitialState           json.RawMessage `json:"initialState,omitempty"`
	Format                 Format          `json:"format,omitempty"`
	FormatVersion          FormatVersion   `json:"formatVersion,omitempty"`
}
type ReadRequest struct {
	ArtifactID      string    `json:"artifactId"`
	Include         []Include `json:"include,omitempty"`
	SourceStartLine Version   `json:"sourceStartLine,omitempty"`
	SourceEndLine   Version   `json:"sourceEndLine,omitempty"`
}
type ListRequest struct {
	Cursor string  `json:"cursor,omitempty"`
	Limit  Version `json:"limit,omitempty"`
}
type OpenRequest struct {
	ArtifactID string `json:"artifactId"`
}
type GetViewRequest struct {
	ArtifactID          string  `json:"artifactId"`
	KnownSourceRevision Version `json:"knownSourceRevision,omitempty"`
	KnownStateVersion   Version `json:"knownStateVersion,omitempty"`
}
type SaveStateRequest struct {
	ArtifactID             string          `json:"artifactId"`
	MutationID             string          `json:"mutationId"`
	ExpectedSourceRevision Version         `json:"expectedSourceRevision"`
	ExpectedStateVersion   Version         `json:"expectedStateVersion"`
	State                  json.RawMessage `json:"state"`
}
type ReportDiagnosticRequest struct {
	ArtifactID     string         `json:"artifactId"`
	SourceRevision Version        `json:"sourceRevision"`
	Message        string         `json:"message"`
	Kind           DiagnosticKind `json:"kind,omitempty"`
}

// ReceiptKey contains authenticated durable identity. Connections, generations
// and credentials never enter receipt identity or the semantic fingerprint.
type ReceiptKey struct {
	RealmID     string
	PrincipalID string
	Operation   string
	MutationID  string
}
type CommittedStatus string
type RejectedStatus string
type AcknowledgedStatus string

const (
	StatusCommitted    CommittedStatus    = "committed"
	StatusRejected     RejectedStatus     = "rejected"
	StatusAcknowledged AcknowledgedStatus = "acknowledged"
)

type MutationReceipt struct {
	Status         CommittedStatus `json:"status"`
	MutationID     string          `json:"mutationId"`
	ArtifactID     string          `json:"artifactId"`
	SourceRevision Version         `json:"sourceRevision"`
	StateVersion   Version         `json:"stateVersion"`
}
type ErrorCode string

const (
	NotFoundOrForbidden ErrorCode = "NOT_FOUND_OR_FORBIDDEN"
	SourceConflict      ErrorCode = "SOURCE_CONFLICT"
	StateConflict       ErrorCode = "STATE_CONFLICT"
	MutationIDReused    ErrorCode = "MUTATION_ID_REUSED"
	UnsupportedFormat   ErrorCode = "UNSUPPORTED_FORMAT"
	InvalidSource       ErrorCode = "INVALID_SOURCE"
	InvalidState        ErrorCode = "INVALID_STATE"
	TooLarge            ErrorCode = "TOO_LARGE"
	QuotaExceeded       ErrorCode = "QUOTA_EXCEEDED"
	Deleted             ErrorCode = "DELETED"
	ServiceUnavailable  ErrorCode = "SERVICE_UNAVAILABLE"
	Busy                ErrorCode = "BUSY"
)

// Current versions are permitted only on authorized conflicts.
type DomainError struct {
	Code           ErrorCode `json:"code"`
	Retryable      bool      `json:"retryable"`
	SourceRevision Version   `json:"sourceRevision,omitempty"`
	StateVersion   Version   `json:"stateVersion,omitempty"`
}

func (e *DomainError) Error() string { return string(e.Code) }

type RejectedResult struct {
	Status RejectedStatus `json:"status"`
	Error  DomainError    `json:"error"`
}
type ArtifactMetadata struct {
	ArtifactID     string        `json:"artifactId"`
	Title          string        `json:"title"`
	Summary        string        `json:"summary"`
	Format         Format        `json:"format"`
	FormatVersion  FormatVersion `json:"formatVersion"`
	SourceRevision Version       `json:"sourceRevision"`
	StateVersion   Version       `json:"stateVersion"`
	CreatedAt      string        `json:"createdAt"`
	UpdatedAt      string        `json:"updatedAt"`
}
type SourceBody struct {
	HTML         string  `json:"html"`
	SourceSHA256 string  `json:"sourceSha256"`
	StartLine    Version `json:"startLine,omitempty"`
	EndLine      Version `json:"endLine,omitempty"`
}
type Diagnostic struct {
	SourceRevision Version        `json:"sourceRevision"`
	Message        string         `json:"message"`
	Kind           DiagnosticKind `json:"kind"`
}
type ReadResult struct {
	ArtifactMetadata
	Source      *SourceBody     `json:"source,omitempty"`
	State       json.RawMessage `json:"state,omitempty"`
	Diagnostics []Diagnostic    `json:"diagnostics,omitempty"`
}
type ListResult struct {
	Artifacts  []ArtifactMetadata `json:"artifacts"`
	NextCursor string             `json:"nextCursor,omitempty"`
}
type LaunchData struct {
	ArtifactID string `json:"artifactId"`
}
type OpenResult struct {
	ArtifactMetadata
	Launch LaunchData `json:"launch"`
}
type GetViewResult struct {
	ArtifactMetadata
	Source *SourceBody     `json:"source,omitempty"`
	State  json.RawMessage `json:"state,omitempty"`
}
type DiagnosticResult struct {
	Status AcknowledgedStatus `json:"status"`
}
