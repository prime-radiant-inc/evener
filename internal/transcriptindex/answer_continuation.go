package transcriptindex

import (
	"context"
	"fmt"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

// AnswerContinuation answers from one indexed cut, including its identity.
// held is a minimum same-incarnation cut, never a frozen length. No graph is
// retained: transient state is O(reachable executions + their answer edges +
// largest decoded entry). For V visited executions and E answer edges, work is
// O(V*(turn records + item records) + E*turn records + decoded contributor bytes).
// Cancellation is checked between reads, not within OS I/O.
// A contended index returns ErrLockBusy rather than waiting for its writer.
func (x *Index) AnswerContinuation(ctx context.Context, ancestor, candidate string, held *appwire.SnapshotIdentity) (bool, appwire.SnapshotIdentity, error) {
	if err := ctx.Err(); err != nil {
		return false, appwire.SnapshotIdentity{}, err
	}
	if ancestor == "" || candidate == "" {
		return false, appwire.SnapshotIdentity{}, appwire.InvalidParams("answer continuation requires both execution IDs")
	}
	if !x.mu.TryLock() {
		return false, appwire.SnapshotIdentity{}, ErrLockBusy
	}
	defer x.mu.Unlock()
	var matches bool
	var cut appwire.SnapshotIdentity
	err := x.lockedMode(false, true, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		cut = appwire.SnapshotIdentity{Incarnation: x.meta.Incarnation, Length: x.meta.Length}
		if held != nil && (held.Incarnation != cut.Incarnation || held.Length > cut.Length) {
			return appwire.TranscriptItemCursorStale()
		}
		load := func(id string) (uint64, turnRecord, error) {
			slot, found, err := x.findTurn(ctx, id)
			if err != nil {
				return 0, turnRecord{}, err
			}
			if !found {
				return 0, turnRecord{}, fmt.Errorf("answer continuation: execution %q absent at cut", id)
			}
			buf, err := x.turns.read(slot, 1)
			if err != nil {
				return 0, turnRecord{}, err
			}
			rec := decodeTurn(buf)
			if rec.Kind != turnKindExecution {
				return 0, turnRecord{}, fmt.Errorf("answer continuation: %q is not an execution", id)
			}
			return slot, rec, nil
		}
		_, _, err := load(ancestor)
		if err != nil {
			return err
		}
		pending := []string{candidate}
		seen := map[string]bool{}
		for len(pending) > 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
			id := pending[len(pending)-1]
			pending = pending[:len(pending)-1]
			if seen[id] {
				continue
			}
			seen[id] = true
			slot, rec, err := load(id)
			if err != nil {
				return err
			}
			if id == ancestor {
				matches = true
			}
			// Inputs already have indexed item openers. Read those contributors,
			// not projected windows or a second persisted ancestry index.
			for start := uint64(0); start < x.items.n; {
				if err := ctx.Err(); err != nil {
					return err
				}
				count := min(uint64(256), x.items.n-start)
				buf, err := x.items.read(start, int(count))
				if err != nil {
					return err
				}
				for i := range count {
					if err := ctx.Err(); err != nil {
						return err
					}
					item := decodeItem(buf[i*itemRecordSize:])
					if uint64(item.Turn) != slot {
						continue
					}
					entry, err := x.readEntry(item.Opener.Offset, item.Opener.Length)
					if err != nil {
						return err
					}
					if entry.Kind != schema.TurnUserInput && entry.Kind != schema.TurnSteering {
						continue
					}
					for _, parent := range entry.AnswerToTurnIDs {
						_, prior, err := load(parent)
						if err != nil {
							return err
						}
						if prior.FirstOrdinal >= rec.FirstOrdinal {
							return fmt.Errorf("answer continuation: non-earlier edge %q to %q", id, parent)
						}
						if !seen[parent] {
							pending = append(pending, parent)
						}
					}
				}
				start += count
			}
		}
		return ctx.Err()
	})
	if err != nil {
		return false, appwire.SnapshotIdentity{}, err
	}
	return matches, cut, nil
}
