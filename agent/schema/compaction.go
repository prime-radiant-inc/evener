package schema

import (
	"fmt"
	"reflect"
	"time"

	"primeradiant.com/evener/llm"
)

// TurnOccurrence is an in-memory occurrence identity. Copies of a turn retain
// it; JSON never carries it. Its nonzero size makes distinct allocations unique.
type TurnOccurrence struct{ identity byte }

func (t Turn) Occurrence() *TurnOccurrence { return t.occurrence }

// EnsureOccurrence initializes a turn at its append or decode boundary.
func (t *Turn) EnsureOccurrence() *TurnOccurrence {
	if t.occurrence == nil {
		t.occurrence = &TurnOccurrence{}
	}
	return t.occurrence
}

// CompactionLocator names one canonical occurrence in this transcript. An
// AddedIndex points directly to an inline artifact, never to another reference.
type CompactionLocator struct {
	EntrySeq   int  `json:"entry_seq"`
	AddedIndex *int `json:"added_index,omitempty"`
}

type CompactionHistoryItem struct {
	Source *CompactionLocator `json:"source,omitempty"`
	Added  *Turn              `json:"added,omitempty"`
}

// CompactionManifest is the retained tail of one authoritative final marker.
// Local storage references confer no session or tool authority.
type CompactionManifest struct {
	Version   int                     `json:"version"`
	SessionID string                  `json:"session_id"`
	History   []CompactionHistoryItem `json:"history"`
}

// ValidateCompactionArtifact admits only the text-only forms produced by fold
// staging. Canonical staging provenance is checked separately by the publisher.
func ValidateCompactionArtifact(t Turn) error {
	switch t.Kind {
	case TurnCheckpoint, TurnSummary, TurnSteering:
	default:
		return fmt.Errorf("invalid compaction artifact kind %q", t.Kind)
	}
	if t.Message.Role != llm.RoleUser || t.Message.Name != "" || t.Message.ToolCallID != "" {
		return fmt.Errorf("invalid compaction artifact message")
	}
	for _, p := range t.Message.Content {
		if p.Kind != llm.ContentText || !reflect.DeepEqual(p, llm.ContentPart{Kind: llm.ContentText, Text: p.Text}) {
			return fmt.Errorf("compaction artifact is not plain text")
		}
	}
	t.Kind = ""
	t.Message = llm.Message{}
	t.Timestamp = time.Time{}
	t.SteeringKind = ""
	t.occurrence = nil
	if !reflect.DeepEqual(t, Turn{}) {
		return fmt.Errorf("compaction artifact carries non-context fields")
	}
	return nil
}
