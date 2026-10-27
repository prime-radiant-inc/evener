package hub

import (
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
)

// reconcileCompensations resumes every open `pendingCompensation` record by
// phase (§9's arms), never by blind re-insert: an armed record checks the
// purge first, a hubtoml record restores hub.toml from its stash before
// touching rows, a rows record re-inserts exactly the rows the restored
// hub.toml revalidates, a runtime record re-applies the restored set before
// clearing, and a clear record clears without resurrecting rows. A failed step
// leaves the record where it is, with its stash intact — the next boot retries.
func (m *hubHostManager) reconcileCompensations() {
	records := m.cfg.ops.Compensations()
	for _, host := range sortedMapKeys(records) {
		record, ok := m.cfg.ops.Compensation(host)
		if !ok {
			continue
		}
		if record.Phase == hostops.CompensationArmed {
			m.resumeArmedCompensation(record)
			continue
		}
		m.resumeCompensation(record)
	}
}

// resumeArmedCompensation runs §9's armed arm: the purge is checked first.
// An intent already cleared means the commit path passed the commit point, so
// the record clears without resurrecting rows; an intent still present with the
// rows still present means the purge never landed, so hub.toml is restored from
// the stash and the rows stay untouched; an intent still present with the rows
// absent means the purge landed before the crash, so the record advances and
// the hubtoml arm follows.
func (m *hubHostManager) resumeArmedCompensation(record hostops.Compensation) {
	fileCfg, hasFile := m.hostFileRecords()
	intent, intentLive := fileCfg.PendingStoreSync[record.Host]
	rowsPresent := false
	if row, ok := m.cfg.ops.OutstandingToken(record.Host); ok {
		for _, preimage := range record.Rows {
			if preimage.Value == row.Value {
				rowsPresent = true
			}
		}
	}
	switch {
	case !hasFile || !intentLive:
		// The commit path passed the commit point: the purge stands and nothing
		// is resurrected. This arm also covers the crash window where the
		// rows-present restore below landed and the process died before the
		// advance: the record is still armed and the file is the restored
		// pre-mutation bytes, which carry no intent. That state converges by
		// construction — the boot that reads this record loaded those very
		// bytes, so its registry, store model, and runtime already serve the
		// restored set; the restored file carries no tombstone, so no mark was
		// taken; and the purge never landed, so the rows are untouched and
		// nothing needs re-inserting.
		m.clearCompensation(record, "the intent is already cleared")
		_ = intent
	case rowsPresent:
		if err := m.restoreHubTOMLFromStash(record.Stash); err != nil {
			m.logf("boot compensation for %q not resumed: %v", record.Host, err)
			return
		}
		// The restored bytes are the authority for the machine records too.
		if restored, ok := m.hostFileRecords(); ok {
			m.installRestoredRecords(restored)
		}
		// The purge never landed and the rows are untouched, but the swap
		// compensation still owes the runtime revert. Continue from the rows
		// arm: the hubtoml arm's commit-point check reads the intent off the
		// file, and the restored pre-mutation bytes carry no intent by
		// construction, so re-entering it here would clear the record without
		// re-inserting the preimage rows, re-applying the restored runtime, or
		// reversing the host-removed marks.
		// The restore just ran, so the record passes through the hubtoml phase
		// (the phase that names the restore step) and on to the rows arm. Each
		// advance is its own store write, exactly as the restorer's per-step
		// discipline requires: a crash between them leaves the record in
		// `compensating-hubtoml` with the file already restored, and the next
		// boot's hubtoml arm sees the restored bytes carry no intent and takes
		// the commit-point clear traced there.
		if err := m.cfg.ops.AdvanceCompensation(record.Host, hostops.CompensationHubTOML); err != nil {
			m.logf("boot compensation for %q not advanced past the restore: %v", record.Host, err)
			return
		}
		if err := m.cfg.ops.AdvanceCompensation(record.Host, hostops.CompensationRows); err != nil {
			m.logf("boot compensation for %q not advanced to the rows arm: %v", record.Host, err)
			return
		}
		record.Phase = hostops.CompensationRows
		m.resumeCompensation(record)
	default:
		if err := m.cfg.ops.AdvanceCompensation(record.Host, hostops.CompensationHubTOML); err != nil {
			m.logf("boot compensation for %q not advanced: %v", record.Host, err)
			return
		}
		record.Phase = hostops.CompensationHubTOML
		m.resumeCompensation(record)
	}
}

// resumeCompensation follows the non-armed arms of §9's phase machine: each
// step lands durably before the next, and a failure returns with the record in
// the phase the failing step belongs to (a runtime-revert failure leaves the
// record in `compensating-runtime` with the stash intact).
func (m *hubHostManager) resumeCompensation(record hostops.Compensation) {
	switch record.Phase {
	case hostops.CompensationHubTOML:
		// §9's commit-point check, the same one the armed arm makes.
		fileCfg, hasFile := m.hostFileRecords()
		if !hasFile {
			// No file to converge with: the boot pipeline enters here only for a
			// genuinely absent or empty document (hostFileAbsentOrEmpty gates it),
			// so there are no bytes to compare or restore and the record clears
			// as before.
			m.clearCompensation(record, "no hub.toml to converge with")
			return
		}
		if _, intentLive := fileCfg.PendingStoreSync[record.Host]; intentLive {
			// The commit path had not passed its commit point: restore and
			// follow the arms.
			if err := m.restoreHubTOMLFromStash(record.Stash); err != nil {
				m.logf("boot compensation for %q: hub.toml not restored: %v", record.Host, err)
				return
			}
			// The restored bytes are the authority for every machine record they
			// carry: the store's model converges to them here, so the rest of this
			// boot and every later write derive from the restored file rather than
			// re-emitting the compensated commit's records.
			if restored, ok := m.hostFileRecords(); ok {
				m.installRestoredRecords(restored)
			}
			if !m.advanceCompensationTo(record, hostops.CompensationRows, "advance-rows") {
				return
			}
			m.resumeFromRows(record)
			return
		}
		// The intent is cleared. Two windows land here:
		//
		//   - the commit path passed its commit point: the purge stands and the
		//     file carries the committed state — nothing is restored, nothing
		//     owed;
		//   - a previous boot's hubtoml arm restored the file and then failed (or
		//     crashed) before advancing to the rows arm: the purge landed, so the
		//     preimage rows are missing and the restored generation revalidates
		//     them — the re-insertion is still owed. Clearing here would drop the
		//     record and its stash with the rows missing, diverged forever.
		//
		// No whole-file comparison can tell these apart: any later write to
		// hub.toml — another host's mutation, another boot's reconcile — breaks
		// the restored file's byte equality with the stash, and the owed-rows
		// window would then read as "the commit stands". So the arm decides
		// per name instead: it advances to the rows arm and resumes, where the
		// rows pass re-inserts exactly the preimage rows the CURRENT file
		// revalidates (the name live, at the preimage generation and
		// incarnation) and re-applies the current file's runtime. For the
		// commit-passed removal the name is not live (or its live pair differs),
		// so the rows pass re-inserts nothing, the runtime pass converges the
		// store model to the current file, and the record clears — the same
		// outcome, decided per name rather than per file.
		if !m.advanceCompensationTo(record, hostops.CompensationRows, "advance-rows") {
			return
		}
		m.resumeFromRows(record)
	case hostops.CompensationRows:
		m.resumeFromRows(record)
	case hostops.CompensationRuntime:
		m.resumeFromRuntime(record)
	case hostops.CompensationClear:
		if !m.compensationStepAllowed(record.Host, "clear") {
			return
		}
		m.clearCompensation(record, "the rows are already converged")
	}
}

// resumeFromRows runs §9's rows arm and everything after it: the preimage rows
// the restored hub.toml's generation revalidates come back, the restored runtime
// set is re-applied to the live handles, and only then does the record clear.
func (m *hubHostManager) resumeFromRows(record hostops.Compensation) {
	restored, ok := m.hostFileRecords()
	generation, incarnation, live := uint64(0), "", false
	if ok {
		generation, incarnation, live = m.restoredIdentity(restored, record.Host)
	}
	if !m.compensationStepAllowed(record.Host, "reinsert") {
		return
	}
	inserted, err := m.cfg.ops.ReinsertCompensationRows(record.Host, func(row hostops.Token) bool {
		return live && row.Generation == generation && row.IncarnationID == incarnation
	})
	if err != nil {
		m.logf("boot compensation for %q: rows not re-inserted: %v", record.Host, err)
		return
	}
	if inserted > 0 {
		m.logf("boot compensation for %q re-inserted %d token row(s)", record.Host, inserted)
	}
	m.resumeFromRuntime(record)
}

// resumeFromRuntime runs §9's runtime arm and the clear: the restored hub.toml's
// runtime set is re-applied to the live handles first, and a failure leaves the
// record in `compensating-runtime` with its stash intact, never a cleared
// compensation beside a diverged runtime.
func (m *hubHostManager) resumeFromRuntime(record hostops.Compensation) {
	if err := m.reapplyRestoredRuntime(record.Host); err != nil {
		m.logf("boot compensation for %q: runtime revert failed, record left in %s: %v", record.Host, record.Phase, err)
		return
	}
	if !m.advanceCompensationTo(record, hostops.CompensationClear, "advance-clear") {
		return
	}
	if !m.compensationStepAllowed(record.Host, "clear") {
		return
	}
	m.clearCompensation(record, "the restoration converged")
}

// advanceCompensationTo advances one record to the next phase in its own store
// write, honoring the test-only step seam and logging a failure.
func (m *hubHostManager) advanceCompensationTo(record hostops.Compensation, to hostops.CompensationPhase, step string) bool {
	if !m.compensationStepAllowed(record.Host, step) {
		return false
	}
	if err := m.cfg.ops.AdvanceCompensation(record.Host, to); err != nil {
		m.logf("boot compensation for %q not advanced to %s: %v", record.Host, to, err)
		return false
	}
	return true
}

// compensationStepAllowed consults the test-only compensation-step seam; nil in
// production.
func (m *hubHostManager) compensationStepAllowed(host, step string) bool {
	if err := m.compensationStepFailure(host, step); err != nil {
		m.logf("boot compensation for %q: step %s failed; the record and its stash are left for the next boot: %v", host, step, err)
		return false
	}
	return true
}

// clearCompensation drops one record and its stash: the compensation
// converged, and no live record names the stash any more.
func (m *hubHostManager) clearCompensation(record hostops.Compensation, why string) {
	if err := m.cfg.ops.ClearCompensation(record.Host); err != nil {
		m.logf("boot compensation for %q not cleared (%s): %v", record.Host, why, err)
		return
	}
	m.pruneHubTOMLStash(record.Stash)
	m.logf("boot compensation for %q cleared: %s", record.Host, why)
}
