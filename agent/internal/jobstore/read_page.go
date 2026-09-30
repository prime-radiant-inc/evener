package jobstore

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"primeradiant.com/evener/agent/internal/linecap"
)

// PageCursor carries only disposable read progress. Callers bind it to a
// journal incarnation and serialize access.
type PageCursor struct {
	Journal   linecap.JournalCursor
	EventEnds []int64
}

// ReadPage yields bounded journal input without changing ScanEventsFrom's
// historical full-read contract. No outcome is authoritative until complete.
func ReadPage(ctx context.Context, path string, cursor *PageCursor, maxBytes int64, maxEvents int) ([]Event, bool, error) {
	next := *cursor
	next.Journal.ReadBytes = 0
	next.Journal.ReadLines = 0
	next.EventEnds = nil
	lines, complete, err := linecap.ReadJournalPage(ctx, path, &next.Journal, maxBytes, maxEvents, DefaultMaxLineBytes)
	if err != nil {
		return nil, false, err
	}
	events := make([]Event, 0, len(lines))
	for _, line := range lines {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if len(bytes.TrimSpace(line.Bytes)) == 0 {
			continue
		}
		var event Event
		if err := json.Unmarshal(line.Bytes, &event); err != nil {
			if !line.Terminated && isIncompleteTrailingJSON(line.Bytes, err) {
				next.Journal.Pending = append([]byte(nil), line.Bytes...)
				if err := ctx.Err(); err != nil {
					return nil, false, err
				}
				*cursor = next
				return events, false, nil
			}
			return nil, false, fmt.Errorf("jobstore: decode journal page: %w", err)
		}
		events = append(events, event)
		next.EventEnds = append(next.EventEnds, line.EndOffset)
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	*cursor = next
	return events, complete, nil
}
