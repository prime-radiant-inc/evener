package contextmgr

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// transcriptMatch represents a turn that matched a search or filter.
type transcriptMatch struct {
	Index   int    `json:"index"`   // position of the turn in the history
	Kind    string `json:"kind"`    // the matched turn's TurnKind
	Preview string `json:"preview"` // first 200 chars of the turn text
}

// searchTranscript loads a snapshot from path and returns all turns containing
// the query string (case-insensitive). Each match includes the turn index, kind,
// and a preview (first 200 chars of the turn text).
func searchTranscript(path string, query string) ([]transcriptMatch, error) {
	snap, err := loadSnapshotFromPath(path)
	if err != nil {
		return nil, err
	}

	queryLower := strings.ToLower(query)
	var matches []transcriptMatch

	for i, turn := range snap.History {
		text := turnText(turn)
		if strings.Contains(strings.ToLower(text), queryLower) {
			preview := text
			if len(preview) > 200 {
				preview = preview[:200]
			}
			matches = append(matches, transcriptMatch{
				Index:   i,
				Kind:    string(turn.Kind),
				Preview: preview,
			})
		}
	}

	return matches, nil
}

// readTurnsFromSnapshot loads a snapshot and returns turns in the [start, end) range.
// Bounds are clamped to valid indices. If start >= end or both are out of bounds,
// returns an empty slice.
func readTurnsFromSnapshot(path string, start, end int) ([]schema.Turn, error) {
	snap, err := loadSnapshotFromPath(path)
	if err != nil {
		return nil, err
	}

	historyLen := len(snap.History)

	// Clamp start and end to valid range
	if start < 0 {
		start = 0
	}
	if end > historyLen {
		end = historyLen
	}
	if start >= end {
		return []schema.Turn{}, nil
	}

	return snap.History[start:end], nil
}

// filterTurns filters turns by kind, content substring, and/or error status.
// - kind: TurnKind string (e.g., "USER_INPUT", "TOOL_RESULTS"). Empty string means no kind filter.
// - contains: case-insensitive substring match on turn text. Empty string means no content filter.
// - errorsOnly: if true, only return turns where turnHasError returns true.
// Returns matching turns as transcriptMatch structs.
func filterTurns(path string, kind string, contains string, errorsOnly bool) ([]transcriptMatch, error) {
	snap, err := loadSnapshotFromPath(path)
	if err != nil {
		return nil, err
	}

	var matches []transcriptMatch
	containsLower := strings.ToLower(contains)

	for i, turn := range snap.History {
		// Filter by kind
		if kind != "" && string(turn.Kind) != kind {
			continue
		}

		// Filter by error status
		if errorsOnly && !turnHasError(turn) {
			continue
		}

		// Filter by content substring
		if contains != "" {
			text := turnText(turn)
			if !strings.Contains(strings.ToLower(text), containsLower) {
				continue
			}
		}

		// Build preview
		text := turnText(turn)
		preview := text
		if len(preview) > 200 {
			preview = preview[:200]
		}

		matches = append(matches, transcriptMatch{
			Index:   i,
			Kind:    string(turn.Kind),
			Preview: preview,
		})
	}

	return matches, nil
}

// loadSnapshotFromPath reads and unmarshals a Snapshot from a JSON file.
func loadSnapshotFromPath(path string) (Snapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read snapshot: %w", err)
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return Snapshot{}, fmt.Errorf("unmarshal snapshot: %w", err)
	}
	return snap, nil
}

// turnText extracts readable text from a Turn, handling different content types.
func turnText(t schema.Turn) string {
	var parts []string

	for _, p := range t.Message.Content {
		switch p.Kind {
		case llm.ContentText:
			if p.Text != "" {
				parts = append(parts, p.Text)
			}
		case llm.ContentToolCall:
			if p.ToolCall != nil {
				parts = append(parts, fmt.Sprintf("ToolCall[%s:%s]", p.ToolCall.Name, p.ToolCall.ID))
			}
		case llm.ContentToolResult:
			if p.ToolResult != nil {
				contentStr := fmt.Sprintf("%v", p.ToolResult.Content)
				parts = append(parts, fmt.Sprintf("ToolResult[%s:%s]", p.ToolResult.ToolCallID, contentStr))
			}
		case llm.ContentThinking:
			if p.Thinking != nil && p.Thinking.Text != "" {
				parts = append(parts, p.Thinking.Text)
			}
		}
	}

	return strings.Join(parts, " ")
}

// turnHasError returns true if the turn contains any tool result with IsError=true.
func turnHasError(t schema.Turn) bool {
	for _, p := range t.Message.Content {
		if p.Kind == llm.ContentToolResult && p.ToolResult != nil && p.ToolResult.IsError {
			return true
		}
	}
	return false
}
