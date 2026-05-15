package appwire

import (
	"fmt"
	"regexp"
	"strings"
)

var refPartPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)

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
