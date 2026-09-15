package appwire

import (
	"fmt"
	"regexp"
	"strings"
)

var refPartPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)

// ValidRefPart reports whether s is a valid part of an AppWire ref: the
// source ID or thread ID of a "source:thread" string. A part may contain only
// ASCII letters, digits, '.', '_', '~', and '-'.
//
// This is the raw grammar only. Callers that use a part as a URL or filesystem
// segment must apply their own stricter rules: this pattern permits "..", which
// ParseRef rejects in the thread part but not in the source part.
func ValidRefPart(s string) bool {
	return refPartPattern.MatchString(s)
}

type Ref struct {
	SourceID string `json:"sourceId"`
	ThreadID string `json:"threadId"`
}

func (r Ref) String() string {
	if r.SourceID == "" || r.ThreadID == "" {
		return ""
	}
	return r.SourceID + ":" + r.ThreadID
}

func ParseRef(raw string) (Ref, error) {
	sourceID, threadID, ok := strings.Cut(raw, ":")
	if !ok || sourceID == "" || threadID == "" {
		return Ref{}, fmt.Errorf("invalid ref %q", raw)
	}
	if !refPartPattern.MatchString(sourceID) || !refPartPattern.MatchString(threadID) {
		return Ref{}, fmt.Errorf("invalid ref %q", raw)
	}
	if strings.Contains(threadID, "..") {
		return Ref{}, fmt.Errorf("invalid ref %q", raw)
	}
	return Ref{SourceID: sourceID, ThreadID: threadID}, nil
}
