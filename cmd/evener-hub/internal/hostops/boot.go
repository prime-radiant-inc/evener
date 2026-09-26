package hostops

import "errors"

// RecoverInterrupted is the boot pass of spec §7's interrupted transition: with
// the store loaded and before it serves any request, every record still in
// `pending`/`running` transitions to `interrupted` — a terminal unknown outcome
// — with a note naming the crash. Each moved record is stamped with the value
// the durable sequence advanced to for it, in stored order, and the whole pass
// lands in one atomic write (spec §4 advances the sequence once per record the
// write moves into a terminal state).
//
// The pass is one-shot by construction: with nothing left pending/running it
// moves nothing, advances nothing, and does not rewrite the file, so a second
// call after a successful pass is a no-op.
//
// Two record families are deliberately not this pass's business. A record in
// `orphan-unverified` is the one exception §7 names: only the fencing paths
// resolve it, and only that resolution advances the sequence. An epoch-only
// probe row — a persisted probe epoch with no token consumed and no worker
// launched — is deleted silently rather than transitioned; it is a separate row
// family owned by the probe path, with no deploy/restart kind, and no such row
// can exist in this store's record schema.
//
// The returned count is how many records the pass moved, for the boot log.
func (s *Store) RecoverInterrupted() (int, error) {
	if s == nil {
		return 0, errors.New("hostops: store is not configured")
	}
	s.cell.mu.Lock()
	defer s.cell.mu.Unlock()

	next := cloneSnapshot(s.cell.state)
	moved := 0
	// One clock read for the pass: the whole pass is one atomic write, and
	// these timestamps are display-only.
	now := nowUTC()
	for i := range next.Records {
		record := &next.Records[i]
		if !record.State.InFlight() {
			continue
		}
		record.State = StateInterrupted
		record.Result = &Result{OK: false, Message: InterruptedNote}
		record.UpdatedAt = now
		next.advanceSequence(record)
		moved++
	}
	if moved == 0 {
		return 0, nil
	}
	landed, err := s.commitLocked(next)
	if err != nil {
		if landed {
			// The rename landed: the records are durably interrupted and memory
			// adopted that state, so the count is the truth a boot log reports
			// (see RenameLanded). A failure before the rename moved nothing.
			return moved, err
		}
		return 0, err
	}
	return moved, nil
}
