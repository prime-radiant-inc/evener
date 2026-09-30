package delegatestore

import (
	"context"
	"errors"
	"fmt"

	"primeradiant.com/evener/agent/internal/linecap"
)

// PageCursor preserves partial lines and oversized batches between bounded
// calls. Only the reader owns it; it is not a durable delegate authority.
type PageCursor struct {
	Journal  linecap.JournalCursor
	Header   bool
	Lines    []linecap.JournalLine
	Events   []Event
	Complete bool
}

// ReadPage caps newly read bytes and returned events. Oversized batches stay
// in the cursor for later calls; callers validate/fold those events in order
// and publish authoritative outcomes only once complete. The caller binds the
// cursor to a journal incarnation and serializes access.
func ReadPage(ctx context.Context, path string, cursor *PageCursor, maxBytes int64, maxEvents int) ([]Event, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if maxBytes <= 0 || maxEvents <= 0 {
		return nil, false, errors.New("delegatestore: positive page limits required")
	}
	next := *cursor
	next.Lines = append([]linecap.JournalLine(nil), cursor.Lines...)
	next.Events = append([]Event(nil), cursor.Events...)
	if len(next.Lines) == 0 && len(next.Events) == 0 {
		lines, complete, err := linecap.ReadJournalPage(ctx, path, &next.Journal, maxBytes, maxEvents, DefaultMaxLineBytes)
		if err != nil {
			return nil, false, err
		}
		next.Lines, next.Complete = lines, complete
	}
	events := make([]Event, 0, maxEvents)
	for len(events) < maxEvents {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		if len(next.Events) > 0 {
			count := min(maxEvents-len(events), len(next.Events))
			events = append(events, next.Events[:count]...)
			next.Events = next.Events[count:]
			continue
		}
		if len(next.Lines) == 0 {
			break
		}
		line := next.Lines[0]
		if !line.Terminated {
			next.Journal.Pending = append([]byte(nil), line.Bytes...)
			next.Lines = next.Lines[1:]
			next.Complete = false
			*cursor = next
			return events, false, nil
		}
		next.Lines = next.Lines[1:]
		if !next.Header {
			var header versionRecord
			if err := decodeJSONLine(line.Bytes, &header); err != nil {
				return nil, false, err
			}
			if header.Version != CurrentVersion {
				return nil, false, fmt.Errorf("delegatestore: unsupported version %d", header.Version)
			}
			next.Header = true
			continue
		}
		var batch batchRecord
		if err := decodeJSONLine(line.Bytes, &batch); err != nil {
			return nil, false, err
		}
		if len(batch.Events) == 0 {
			return nil, false, errors.New("delegatestore: empty batch")
		}
		next.Events = batch.Events
	}
	*cursor = next
	return events, next.Complete && len(next.Lines) == 0 && len(next.Events) == 0, nil
}
