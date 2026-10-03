package memory

import (
	"context"
	"encoding/json"
	"time"
)

type Scope string

const (
	Personal Scope = "personal"
	Project  Scope = "project"
)

type Options struct {
	Root  string
	Scope Scope
	Now   func() time.Time
	// External-I/O boundary observer/fault injector, nil in production.
	// Test code can stop a subprocess here, return an I/O fault, or pause I/O.
	Checkpoint func(context.Context, string) error
}

type PageMeta struct {
	Hash     string     `json:"hash"`
	Created  time.Time  `json:"created"`
	Updated  time.Time  `json:"updated"`
	Reviewed *time.Time `json:"reviewed"`
}
type Metadata struct {
	Format         int                 `json:"format"`
	Revision       uint64              `json:"revision"`
	ChangeSequence uint64              `json:"change_sequence"`
	IndexHash      string              `json:"index_hash"`
	Pages          map[string]PageMeta `json:"pages"`
}
type Page struct {
	Body string
	Meta PageMeta
}
type Snapshot struct {
	Meta  Metadata
	Index string
	Pages map[string]Page
}
type PageOperation struct {
	Kind   string  `json:"kind"`
	PageID string  `json:"page_id"`
	Body   *string `json:"body,omitempty"`
}
type ApplyRequest struct {
	ExpectedRevision uint64          `json:"expected_revision"`
	OperationID      string          `json:"operation_id"`
	Pages            []PageOperation `json:"pages"`
	Index            *string         `json:"index,omitempty"`
}
type Actor struct {
	SessionRef string `json:"session_ref"`
	DelegateID string `json:"delegate_id,omitempty"`
}
type AffectedPage struct {
	PageID     string `json:"page_id"`
	Kind       string `json:"kind"`
	BeforeHash string `json:"before_hash,omitempty"`
	AfterHash  string `json:"after_hash,omitempty"`
}
type Receipt struct {
	OperationID      string         `json:"operation_id"`
	RequestHash      string         `json:"request_hash"`
	ExpectedRevision uint64         `json:"expected_revision"`
	ResultRevision   uint64         `json:"result_revision"`
	Changed          bool           `json:"changed"`
	Sequence         uint64         `json:"sequence"`
	Actor            Actor          `json:"actor"`
	At               time.Time      `json:"at"`
	Pages            []AffectedPage `json:"pages"`
	IndexBeforeHash  string         `json:"index_before_hash"`
	IndexAfterHash   string         `json:"index_after_hash"`
}
type ApplyResult struct {
	Scope    Scope   `json:"scope"`
	Revision uint64  `json:"revision"`
	Replayed bool    `json:"replayed"`
	Receipt  Receipt `json:"receipt"`
}
type Reference struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Line        int    `json:"line"`
}
type LimitDetail struct {
	Kind    string `json:"kind"`
	Actual  int    `json:"actual"`
	Maximum int    `json:"maximum"`
}
type Error struct {
	Code           string       `json:"code"`
	Scope          Scope        `json:"scope"`
	Revision       uint64       `json:"revision"`
	OperationID    string       `json:"operation_id,omitempty"`
	References     []Reference  `json:"references,omitempty"`
	Limit          *LimitDetail `json:"limit,omitempty"`
	MoreReferences bool         `json:"more_references,omitempty"`
}

// Error() marshals these bounded fields as JSON, never raw host paths or bodies.
// errors.As(err, &memoryError) is the caller's typed error route.

type Cursor struct {
	Scope       Scope  `json:"scope"`
	StoreID     string `json:"store_id"`
	Revision    uint64 `json:"revision"`
	Kind        string `json:"kind"`
	Target      string `json:"target"`
	QueryHash   string `json:"query_hash,omitempty"`
	FileOrdinal int    `json:"file_ordinal,omitempty"`
	ByteOffset  int    `json:"byte_offset,omitempty"`
	Sequence    uint64 `json:"sequence,omitempty"`
}
type ReadRequest struct {
	Target     string  `json:"target"`
	PageID     string  `json:"page_id,omitempty"`
	LimitBytes int     `json:"limit_bytes,omitempty"`
	Cursor     *Cursor `json:"cursor,omitempty"`
}
type ReadResult struct {
	Scope     Scope     `json:"scope"`
	Revision  uint64    `json:"revision"`
	Empty     bool      `json:"empty"`
	Content   string    `json:"content,omitempty"`
	Dates     *PageMeta `json:"dates,omitempty"`
	Changes   []Receipt `json:"changes,omitempty"`
	Truncated bool      `json:"truncated"`
	Next      *Cursor   `json:"next,omitempty"`
}
type SearchRequest struct {
	Query           string  `json:"query"`
	CaseInsensitive bool    `json:"case_insensitive"`
	Limit           int     `json:"limit,omitempty"`
	Cursor          *Cursor `json:"cursor,omitempty"`
}
type Hit struct {
	PageID     string `json:"page_id"`
	ByteOffset int    `json:"byte_offset"`
	Snippet    string `json:"snippet"`
}
type SearchResult struct {
	Scope     Scope   `json:"scope"`
	Revision  uint64  `json:"revision"`
	Hits      []Hit   `json:"hits"`
	Truncated bool    `json:"truncated"`
	Next      *Cursor `json:"next,omitempty"`
}
type IndexResult struct {
	Scope     Scope  `json:"scope"`
	Revision  uint64 `json:"revision"`
	Empty     bool   `json:"empty"`
	Content   string `json:"content"`
	Truncated bool   `json:"truncated"`
}

func (e *Error) Error() string {
	data, _ := json.Marshal(e)
	return string(data)
}

// Task 2 moves these declarations unchanged into transaction.go.
type afterImage struct {
	Name  string `json:"name"`
	Bytes []byte `json:"bytes"`
	Hash  string `json:"hash"`
}
type pendingTransaction struct {
	Format           int          `json:"format"`
	OperationID      string       `json:"operation_id"`
	RequestHash      string       `json:"request_hash"`
	ExpectedRevision uint64       `json:"expected_revision"`
	ResultRevision   uint64       `json:"result_revision"`
	Writes           []afterImage `json:"writes"`
	Deletes          []string     `json:"deletes"`
	Receipt          Receipt      `json:"receipt"`
}
