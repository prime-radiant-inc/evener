package delegatestore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"primeradiant.com/evener/agent/internal/linecap"
)

// PageCursor preserves raw partial lines and position within an oversized
// batch. Callers bind it to a journal incarnation and serialize access.
type PageCursor struct {
	Journal       linecap.JournalCursor
	Header        bool
	Lines         []linecap.JournalLine
	Complete      bool
	Batch         []byte
	BatchEnd      int64
	EventEnds     []int64
	BatchOffset   int
	BatchEvents   int
	DecodedEvents uint64
}

// ReadPage caps newly read journal bytes and individual event decodes. One
// event is atomic under the store's existing line-size ceiling; cancellation is
// checked before and after decoding it. No whole batch of Events is allocated.
// Callers fold in order and publish authoritative outcomes only once complete.
func ReadPage(ctx context.Context, path string, cursor *PageCursor, maxBytes int64, maxEvents int) ([]Event, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if maxBytes <= 0 || maxEvents <= 0 {
		return nil, false, errors.New("delegatestore: positive page limits required")
	}
	next := *cursor
	next.EventEnds = nil
	next.Lines = append([]linecap.JournalLine(nil), cursor.Lines...)
	if len(next.Lines) == 0 && len(next.Batch) == 0 {
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
		if len(next.Batch) == 0 {
			if len(next.Lines) == 0 {
				break
			}
			line := next.Lines[0]
			next.Lines = next.Lines[1:]
			if !line.Terminated {
				next.Journal.Pending = append([]byte(nil), line.Bytes...)
				next.Complete = false
				break
			}
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
			offset, err := batchArrayOffset(line.Bytes)
			if err != nil {
				return nil, false, err
			}
			next.Batch, next.BatchOffset, next.BatchEvents = line.Bytes, offset, 0
			next.BatchEnd = line.EndOffset
		}
		// Re-enter the remaining array with a fresh decoder. This allows a canceled
		// page to discard its local position without mutating a shared decoder.
		start := next.BatchOffset
		for start < len(next.Batch) && isJSONSpace(next.Batch[start]) {
			start++
		}
		if next.BatchEvents > 0 && start < len(next.Batch) && next.Batch[start] == ',' {
			start++
		}
		decoder := json.NewDecoder(io.MultiReader(bytes.NewReader([]byte{'['}), bytes.NewReader(next.Batch[start:])))
		decoder.DisallowUnknownFields()
		if _, err := decoder.Token(); err != nil {
			return nil, false, err
		}
		for len(events) < maxEvents && decoder.More() {
			if err := ctx.Err(); err != nil {
				return nil, false, err
			}
			var event Event
			if err := decoder.Decode(&event); err != nil {
				return nil, false, err
			}
			if err := ctx.Err(); err != nil {
				return nil, false, err
			}
			events = append(events, event)
			next.EventEnds = append(next.EventEnds, next.BatchEnd)
			next.BatchEvents++
			next.DecodedEvents++
			next.BatchOffset = start + int(decoder.InputOffset()) - 1
		}
		if !decoder.More() {
			token, err := decoder.Token()
			if err != nil {
				return nil, false, err
			}
			if token != json.Delim(']') {
				return nil, false, errors.New("delegatestore: invalid batch array")
			}
			end := start + int(decoder.InputOffset()) - 1
			if !bytes.Equal(bytes.TrimSpace(next.Batch[end:]), []byte{'}'}) {
				return nil, false, errors.New("delegatestore: invalid batch envelope")
			}
			if next.BatchEvents == 0 {
				return nil, false, errors.New("delegatestore: empty batch")
			}
			next.Batch = nil
			next.BatchOffset = 0
			next.BatchEvents = 0
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	*cursor = next
	return events, next.Complete && len(next.Lines) == 0 && len(next.Batch) == 0, nil
}

func batchArrayOffset(raw []byte) (int, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	for _, want := range []any{json.Delim('{'), "events", json.Delim('[')} {
		token, err := decoder.Token()
		if err != nil {
			return 0, err
		}
		if token != want {
			return 0, errors.New("delegatestore: invalid batch envelope")
		}
	}
	return int(decoder.InputOffset()), nil
}

func isJSONSpace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\n' || value == '\r'
}
