package hub

// This file owns the three registry mutations' conversion to spec 08 §5's
// staged commit and §6's commit-point rule, as the union-returning handlers
// `evener/host/add|update|remove` return the §11 mutation-result union.
//
// Spec §6, verbatim on the commit point: "The commit point is the start of the
// post-commit rebind phase: once the first planned teardown executes, the
// mutation is committed and there is no compensation path back — a failure at
// or after the commit point is reported as a committed-with-teardown-failure
// with the seam named, and recovery is forward (retry the teardown / re-apply),
// never a restore of the prior bytes — through the teardown-repair operation,
// never a blind full-mutation retry (replaying the original mutationId stays a
// no-op receipt return, so the API has a repair path that is not the replay).
// Restoring bytes after a teardown cannot rebuild destroyed handles, so
// compensation covers pre-commit failures only."
//
// Spec §5, verbatim on the replay arms: "A replay naming a still-staged marker
// never re-applies. ... Once the lock is free and the marker persists ... the
// replay first reads the marker's persisted runtime phase and `swapStarted`
// intent and finalizes by phase ... a marker with `teardownStarted: false` AND
// `swapStarted: false` re-applies the staged runtime set to the live handles
// first (`hub.toml` already holds the new config, so the live runtime must
// converge to it before the receipt finalizes), then re-runs the pinned
// teardown to completion and finalizes from the observed result".
//
// Spec §6, verbatim on the remnant fence: "Any non-replay mutation (a same-key
// replay matching a current-generation receipt returns before this fence — §5)
// that would advance or remove the affected name while it holds an open remnant
// — re-add, `update` of the remnant's name, and `remove` of the remnant's name
// — is refused with the typed `remnant-open` conflict refusal carrying the
// blocking `remnantId`."

import (
	"context"
	"fmt"
	"strings"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// committedRow narrows a mutation result to the committed row a legacy caller
// asked for, and reports a non-committed arm as the error that arm means. It is
// the adapter the three compatibility wrappers below use: the wire handlers
// return the union, and a caller that only speaks the pre-union shape hears
// about a teardown failure or a dropped commit instead of reading it as success.
func committedRow(result appwire.HostMutationResult) (appwire.HostRow, error) {
	switch {
	case result.HostMutationCommitted != nil:
		return result.HostMutationCommitted.Host, nil
	case result.HostMutationCommittedRemoved != nil:
		row := result.HostMutationCommittedRemoved.Host
		return appwire.HostRow{
			Name:             row.Name,
			Address:          row.Address,
			User:             row.User,
			KeyPath:          row.KeyPath,
			EvenerPath:       row.EvenerPath,
			ConfigPath:       row.ConfigPath,
			Addr:             row.Addr,
			Roots:            row.Roots,
			Origin:           row.Origin,
			Generation:       row.Generation,
			IncarnationID:    row.IncarnationID,
			Removed:          true,
			RetainedRows:     row.RetainedRows,
			RowsTruncated:    row.RowsTruncated,
			EscalationAgeSec: row.EscalationAgeSec,
			OpenRemnantID:    row.OpenRemnantID,
		}, nil
	case result.HostMutationTeardownFailure != nil:
		return appwire.HostRow{}, appwire.Conflict(fmt.Sprintf(
			"the mutation committed but its %s teardown failed; resume it through evener/host/teardown-retry with remnantId %q",
			result.HostMutationTeardownFailure.Seam, result.HostMutationTeardownFailure.RemnantID))
	case result.HostMutationTeardownFailureRemoved != nil:
		return appwire.HostRow{}, appwire.Conflict(fmt.Sprintf(
			"the mutation committed but its %s teardown failed; resume it through evener/host/teardown-retry with remnantId %q",
			result.HostMutationTeardownFailureRemoved.Seam, result.HostMutationTeardownFailureRemoved.RemnantID))
	case result.HostMutationCollisionDropped != nil:
		return appwire.HostRow{}, appwire.Conflict(fmt.Sprintf(
			"a concurrent hub.toml edit won the race (%s); re-read the host list", result.WinningFingerprint))
	}
	return appwire.HostRow{}, appwire.Conflict("the mutation returned no committed arm")
}

// committedHostRow is committedRow with the caller's error already handled, for
// the compatibility wrappers.
func committedHostRow(result appwire.HostMutationResult, err error) (appwire.HostRow, error) {
	if err != nil {
		return appwire.HostRow{}, err
	}
	return committedRow(result)
}

// teardownCommittedArm renders the union's teardown-failure arm for one
// mutation kind: "every committed-with-teardown-failure response carries that
// `remnantId` alongside the committed receipt", and "the committed row is
// always present so the UI renders the row with a teardown-retry affordance".
func teardownCommittedArm(kind hostMutationKind, seam, remnantID string, row appwire.HostRow) appwire.HostMutationResult {
	if kind == hostMutationRemove {
		return appwire.HostMutationResult{HostMutationTeardownFailureRemoved: &appwire.HostMutationTeardownFailureRemoved{
			Outcome:   appwire.HostMutationOutcomeTeardownFailure,
			Seam:      seam,
			RemnantID: remnantID,
			Host:      removedRowForRow(row),
		}}
	}
	return appwire.HostMutationResult{HostMutationTeardownFailure: &appwire.HostMutationTeardownFailure{
		Outcome:   appwire.HostMutationOutcomeTeardownFailure,
		Seam:      seam,
		RemnantID: remnantID,
		Host:      row,
	}}
}

// committedArm renders the union's committed arm for one mutation kind.
func committedArm(kind hostMutationKind, row appwire.HostRow) appwire.HostMutationResult {
	if kind == hostMutationRemove {
		return appwire.HostMutationResult{HostMutationCommittedRemoved: &appwire.HostMutationCommittedRemoved{
			Outcome: appwire.HostMutationOutcomeCommitted,
			Host:    removedRowForRow(row),
		}}
	}
	return appwire.HostMutationResult{HostMutationCommitted: &appwire.HostMutationCommitted{
		Outcome: appwire.HostMutationOutcomeCommitted,
		Host:    row,
	}}
}

// receiptArm renders the union arm one recorded receipt replays: "a replay
// carrying a known key returns the recorded receipt without re-applying".
func (m *hubHostManager) receiptArm(hit *hostReceiptHit, kind hostMutationKind) appwire.HostMutationResult {
	receipt := hit.Receipt
	switch receipt.Outcome {
	case hostReceiptOutcomeTeardownFailure:
		row := hostReceiptRow(receipt, kind)
		seam := seamForReceipt(receipt, kind)
		if remnant, ok := m.cfg.store.remnantByID(receipt.RemnantID); ok {
			if remnant.open() {
				row.OpenRemnantID = receipt.RemnantID
				row.EscalationAgeSec = m.escalationAgeSec(remnant)
			}
			if remnant.Seam != "" {
				seam = remnant.Seam
			}
		}
		return teardownCommittedArm(kind, seam, receipt.RemnantID, row)
	case hostReceiptOutcomeCollisionDropped:
		return m.collisionArm(receipt)
	default:
		return committedArm(kind, hostReceiptRow(receipt, kind))
	}
}

// collisionArm renders the recorded `collision-dropped` receipt as the union's
// dropped arm — "a replay renders the dropped arm from these receipt fields, so
// a lost-response retry, even after restart, reconstructs both what was dropped
// and which fingerprint won".
func (m *hubHostManager) collisionArm(receipt HostMutationReceipt) appwire.HostMutationResult {
	arm := appwire.HostMutationCollisionDropped{
		Outcome:            appwire.HostMutationOutcomeCollisionDropped,
		WinningFingerprint: receipt.WinningFingerprint,
		Removed:            receipt.Removed,
	}
	if receipt.DroppedEntry != nil {
		arm.DroppedEntry = hostRowForReceiptRow(*receipt.DroppedEntry)
	}
	if !receipt.Removed {
		row := hostRowForReceiptRow(receipt.Row)
		row.Generation = receipt.Generation
		row.IncarnationID = receipt.IncarnationID
		arm.Host = &row
	}
	return appwire.HostMutationResult{HostMutationCollisionDropped: &arm}
}

// hostRowForReceiptRow renders a stored receipt row as the wire's row shape.
func hostRowForReceiptRow(row HostMutationReceiptRow) appwire.HostRow {
	return appwire.HostRow{
		Name:       row.Name,
		Address:    row.Address,
		User:       row.User,
		KeyPath:    row.KeyPath,
		EvenerPath: row.EvenerPath,
		ConfigPath: row.ConfigPath,
		Addr:       row.Addr,
		Roots:      append([]string(nil), row.Roots...),
		Origin:     hostOriginHubTOML,
	}
}

// seamForReceipt names the failed rebind step a replay reports. §6's receipt
// record does not carry the seam — the durable remnant does — so the remnant is
// the authority while it survives (the caller looks it up), and a receipt whose
// remnant is gone derives the same name from the mutation kind the scoped key
// pins: a removal's rebind step is "remove-host", every other mutation's is
// "update-host". The value is never the generic placeholder a client could not
// act on.
func seamForReceipt(receipt HostMutationReceipt, kind hostMutationKind) string {
	if kind == hostMutationRemove {
		return "remove-host"
	}
	return "update-host"
}

// ---------------------------------------------------------------------------
// Add
// ---------------------------------------------------------------------------

// AddResult is `evener/host/add` (registry spec 08 §5/§11), returning the
// mutation-result union.
func (m *hubHostManager) AddResult(ctx context.Context, params appwire.HostAddParams) (appwire.HostMutationResult, error) {
	if err := guardControllerLocalHosts(ctx); err != nil {
		return appwire.HostMutationResult{}, err
	}
	name := strings.TrimSpace(params.Entry.Name)
	if params.MutationID != "" {
		if err := mutationIDShapeRefusal(name, params.MutationID); err != nil {
			return appwire.HostMutationResult{}, err
		}
	}
	if params.MutationID != "" {
		current, currentKnown := m.currentHostIdentity(name)
		hit, err := m.lookupHostMutationReceipt(hostReceiptQuery{
			MutationID:   params.MutationID,
			Name:         name,
			Kind:         hostMutationAdd,
			Current:      current,
			CurrentKnown: currentKnown,
		})
		if err != nil {
			return appwire.HostMutationResult{}, err
		}
		if hit != nil {
			return m.receiptArm(hit, hostMutationAdd), nil
		}
	}
	auditKey := params.MutationID
	if auditKey == "" {
		auditKey = mintRemnantID()
	}
	entry := hostreg.Normalize(hostEntryToHost(params.Entry.Name, params.Entry))
	if err := validateHostEntry(entry); err != nil {
		return appwire.HostMutationResult{}, hostValidationRefusal(entry.Name, err)
	}
	// A leftover staged-receipt marker for this host is finalized first (spec
	// §5's foreign-marker rule): "Any mutation-path write that finds a marker it
	// did not stage for its own host finalizes that marker first". The
	// finalization runs before this mutation stages anything, and its result is
	// re-checked by the dedup lookup below — a marker whose key is this
	// request's own key is exactly the lost-response replay that must return the
	// finalized receipt instead of re-applying.
	if _, err := m.finalizeOrphanMarkerIfAny(ctx, name, false); err != nil {
		return appwire.HostMutationResult{}, err
	}
	addCurrent, addCurrentKnown := m.currentHostIdentity(name)
	if hit, err := m.lookupHostMutationReceipt(hostReceiptQuery{
		MutationID:   params.MutationID,
		Name:         name,
		Kind:         hostMutationAdd,
		Current:      addCurrent,
		CurrentKnown: addCurrentKnown,
	}); err != nil {
		return appwire.HostMutationResult{}, err
	} else if hit != nil && params.MutationID != "" {
		return m.receiptArm(hit, hostMutationAdd), nil
	}
	m.cfg.mu.Lock()
	if m.isMutating(entry.Name) {
		m.cfg.mu.Unlock()
		return appwire.HostMutationResult{}, hostMutationConflict(entry.Name)
	}
	// The remnant fence, past the dedup check and under the mutation lock:
	// "re-add ... is refused with the typed `remnant-open` conflict refusal
	// carrying the blocking `remnantId`". The fence outranks the keyless
	// read-after-unknown path below — a keyless route is a non-replay mutation
	// too, and a new incarnation must never start while the old lifecycle still
	// owns supervisors, channels, or fan-outs (spec §6: "The remnant fence takes
	// precedence over the tombstone not-found rule").
	if err := m.remnantRefusal(entry.Name); err != nil {
		m.cfg.mu.Unlock()
		return appwire.HostMutationResult{}, err
	}
	// The keyless read-after-unknown path: "a keyless `add` retry that observes
	// a matching listed row (the listed entry hash equals the intended entry)
	// returns the explicit ambiguous outcome ... instead of claiming the
	// mutation committed".
	if params.MutationID == "" {
		if observed, ok := m.keylessAddObservedRowLocked(entry); ok {
			m.cfg.mu.Unlock()
			return appwire.HostMutationResult{HostMutationAmbiguous: &appwire.HostMutationAmbiguous{
				Outcome:     appwire.HostMutationOutcomeAmbiguous,
				ObservedRow: observed,
			}}, nil
		}
	}
	if _, ok := m.cfg.hosts.Get(entry.Name); ok {
		m.cfg.mu.Unlock()
		return appwire.HostMutationResult{}, appwire.InvalidParams(fmt.Sprintf("host %q: %v", entry.Name, hostreg.ErrDuplicateHost))
	}
	priorState := m.cfg.state.detach(entry.Name)
	if m.testOnlyParkInCommit != nil {
		// The park lands after the retained-state reset and before the first
		// store take: that is the window a concurrent attach's lifecycle event
		// can land in, and the reset's position relative to it is exactly what
		// the seam exists to pin.
		m.testOnlyParkInCommit(entry.Name)
	}
	reAddedFromTombstone := m.cfg.store.hasTombstone(entry.Name)
	identity, err := m.cfg.hosts.Stamp(entry)
	if err != nil {
		m.cfg.state.restore(entry.Name, priorState)
		m.cfg.mu.Unlock()
		return appwire.HostMutationResult{}, err
	}
	entry = stampedEntry(entry, identity)
	receipt := newHostMutationReceipt(auditKey, hostMutationAdd, entry, m.nowTime())
	receipt.Audit = params.MutationID == ""
	// The marker's staged payload always carries a pre-minted `remnantId` (spec
	// §5). An add plans no teardown — its pinned target is empty — so the id is
	// cleared again on a clean finalize: §6 has the receipt carry `remnantId`
	// exactly when a remnant record exists, and a clean add leaves none.
	receipt.RemnantID = mintRemnantID()
	receiptKey := hostMutationReceiptKey(auditKey, entry.Name, hostMutationAdd, hostMutationIdentity{
		Generation:    entry.Generation,
		IncarnationID: entry.IncarnationID,
	})
	prev := m.cfg.store.snapshot()
	plan := &hostCommitPlan{
		Kind:    hostMutationAdd,
		Name:    entry.Name,
		Key:     receiptKey,
		Receipt: receipt,
		Entries: append(append([]hostreg.Host(nil), prev...), entry),
		Known:   prev,
		Pinned:  pendingTeardownFor(entry, hostTeardownKindUpdate),
		Entry:   entry,
	}
	plan.reconcileFingerprint, _ = hubTOMLFingerprintAt(m.cfg.configPath)
	if err := m.stageCommit(plan, m.nowTime()); err != nil {
		err = m.compensateStagedWrite(plan, prev, err)
		m.cfg.state.restore(entry.Name, priorState)
		m.cfg.mu.Unlock()
		return appwire.HostMutationResult{}, err
	}
	// The add's runtime transition is the registry insert below, and it has no
	// planned teardown (its pinned target is empty), so its marker goes straight
	// to the post-swap phase: the finalizing write carries `runtime-swapped` +
	// `teardownStarted` and the observed committed outcome. A crash before that
	// write leaves a `staged` marker whose boot recovery re-applies the staged
	// runtime set and finalizes from the re-run, which for an empty pinned
	// target is the no-op §5 defines.
	if err := m.addHostToRegistry(entry); err != nil {
		m.cfg.state.restore(entry.Name, priorState)
		err = m.rollbackHubTOML(prev, unionHosts(prev, append([]hostreg.Host(nil), entry)), err, compensationChange(plan))
		m.cfg.mu.Unlock()
		return appwire.HostMutationResult{}, err
	}
	if stored, ok := m.cfg.hosts.Get(entry.Name); ok {
		entry = stored
	}
	m.cfg.store.add(entry)
	m.cfg.store.addReceipt(receiptKey, receipt)
	if reAddedFromTombstone {
		if m.cfg.remoteCache != nil {
			m.cfg.remoteCache.RemoveSource(entry.Name)
		}
		if m.cfg.forgetLastGoodThreads != nil {
			m.cfg.forgetLastGoodThreads(entry.Name)
		}
	}
	m.registerSource(entry)
	// The add's post-commit rebind is the registry insert and the source
	// registration above — there are no superseded handles to destroy — so its
	// pinned target is empty and the re-run is a no-op (spec §5: "where the
	// pinned target is empty the re-run is a no-op").
	plan.Marker = m.stagedMarker(plan, m.nowTime())
	plan.Marker.SwapStarted = true
	plan.Marker.TeardownStarted = true
	plan.Marker.Phase = hostStagedPhaseRuntimeSwapped
	finalizeReceipt := receipt
	finalizeReceipt.RemnantID = ""
	finalized, err := m.finalizeReceipt(plan, finalizeReceipt, "", nil)
	if err != nil {
		m.cfg.mu.Unlock()
		return appwire.HostMutationResult{}, err
	}
	m.cfg.mu.Unlock()
	if finalized.Outcome == hostReceiptOutcomeCollisionDropped {
		return m.collisionArm(finalized), nil
	}
	return committedArm(hostMutationAdd, m.hostRow(ctx, entry)), nil
}

// compensateStagedWrite is the step-(2) write's compensation: a failure the
// rename already committed left the file holding the staged change while
// nothing live changed, so the file is restored to the pre-mutation set (with
// any foreign edit adopted, spec §6 step (4)) and the failure is returned. A
// pre-rename failure wrote nothing and passes through.
func (m *hubHostManager) compensateStagedWrite(plan *hostCommitPlan, previous []hostreg.Host, cause error) error {
	if !hubTOMLRenameCommitted(cause) {
		return cause
	}
	adopted := m.adoptedPreimage(plan, previous)
	// The staged records go with the write that failed: the compensation drops
	// the marker and the provisional receipt the staged write installed.
	return m.rollbackHubTOML(adopted, unionHosts(adopted, plan.Entries), cause, compensationChange(plan))
}

// compensateStagedCommit is the pre-commit compensation for a staged commit
// whose flip refused: nothing live changed, so the store's rows are already the
// pre-mutation set; the file is restored to the re-read preimage so a foreign
// edit in the window survives (spec §6 step (4)), the staged records go with it,
// and the typed refusal is returned.
func (m *hubHostManager) compensateStagedCommit(plan *hostCommitPlan, previous []hostreg.Host, priorState *hostAttachRecord, cause error) (appwire.HostMutationResult, error) {
	adopted := m.adoptedPreimage(plan, previous)
	err := m.rollbackHubTOML(adopted, unionHosts(adopted, plan.Entries), cause, compensationChange(plan))
	m.cfg.state.restore(plan.Name, priorState)
	m.cfg.mu.Unlock()
	return appwire.HostMutationResult{}, err
}

// keylessAddObservedRowLocked is spec §5's read-after-unknown comparison for a keyless
// add: it reads the listed entry for the name and reports it when the effective
// fields plus `generation`/`origin` match the intended entry — "a keyless `add`
// retry that observes a matching listed row (the listed entry hash equals the
// intended entry) returns the explicit ambiguous outcome". Volatile live state
// and age counters are deliberately not compared.
func (m *hubHostManager) keylessAddObservedRowLocked(entry hostreg.Host) (appwire.HostRow, bool) {
	stored, ok := m.cfg.hosts.Get(entry.Name)
	if !ok {
		return appwire.HostRow{}, false
	}
	if !sameEffectiveHostEntryFields(stored, entry) {
		return appwire.HostRow{}, false
	}
	row := hostEntryRow(stored)
	return row, true
}

// sameEffectiveHostEntry compares the effective HostConfig fields plus the
// pinned pair — never volatile live state or age counters (spec §5: "Read-after-
// unknown compares only the effective `HostConfig` fields plus
// `generation`/`origin`").
func sameEffectiveHostEntry(a, b hostreg.Host) bool {
	return sameEffectiveHostEntryFields(a, b) &&
		a.Generation == b.Generation && a.IncarnationID == b.IncarnationID
}

// sameEffectiveHostEntryFields compares the effective HostConfig fields alone.
// It is the keyless add's comparison: the intended entry of a fresh add carries
// no (generation, incarnation id) pair yet — the commit mints it — so a
// keyless retry cannot compare one, and comparing the fields plus the origin
// marker is the whole effective-row test the spec's read-after-unknown rule can
// apply there.
func sameEffectiveHostEntryFields(a, b hostreg.Host) bool {
	return a.Name == b.Name && a.SSH == b.SSH && a.User == b.User &&
		a.EvenerPath == b.EvenerPath && a.ConfigPath == b.ConfigPath &&
		a.Addr == b.Addr && a.KeyPath == b.KeyPath &&
		strings.Join(a.Roots, "\x00") == strings.Join(b.Roots, "\x00")
}

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

// UpdateResult is `evener/host/update` (registry spec 08 §5/§11), returning the
// mutation-result union.
func (m *hubHostManager) UpdateResult(ctx context.Context, params appwire.HostUpdateParams) (appwire.HostMutationResult, error) {
	if err := guardControllerLocalHosts(ctx); err != nil {
		return appwire.HostMutationResult{}, err
	}
	name := strings.TrimSpace(params.Name)
	if err := hostGuardedMutationRefusal(name, params.MutationID, params.ExpectedGeneration, params.ExpectedIncarnationID); err != nil {
		return appwire.HostMutationResult{}, err
	}
	current, currentKnown := m.currentHostIdentity(name)
	hit, err := m.lookupHostMutationReceipt(hostReceiptQuery{
		MutationID:   params.MutationID,
		Name:         name,
		Kind:         hostMutationUpdate,
		Current:      current,
		CurrentKnown: currentKnown,
	})
	if err != nil {
		return appwire.HostMutationResult{}, err
	}
	if hit != nil {
		return m.receiptArm(hit, hostMutationUpdate), nil
	}
	current2, currentKnown2 := m.currentHostIdentity(name)
	// The foreign-marker rule (spec §5): a leftover marker for this host is
	// finalized before this edit stages, and a finalized receipt under this
	// request's own key is the lost-response replay.
	if _, err := m.finalizeOrphanMarkerIfAny(ctx, name, false); err != nil {
		return appwire.HostMutationResult{}, err
	}
	if hit, err := m.lookupHostMutationReceipt(hostReceiptQuery{
		MutationID:   params.MutationID,
		Name:         name,
		Kind:         hostMutationUpdate,
		Current:      current2,
		CurrentKnown: currentKnown2,
	}); err != nil {
		return appwire.HostMutationResult{}, err
	} else if hit != nil {
		return m.receiptArm(hit, hostMutationUpdate), nil
	}
	releaseGate, err := m.acquireHostGate(name, hostops.Holder{Kind: hostops.HolderManager, Activity: "update"})
	if err != nil {
		return appwire.HostMutationResult{}, err
	}
	defer releaseGate()
	entry := hostreg.Normalize(hostEntryToHost(name, params.Entry))
	m.cfg.mu.Lock()
	if m.isMutating(name) {
		m.cfg.mu.Unlock()
		return appwire.HostMutationResult{}, hostMutationConflict(name)
	}
	// The remnant fence, past the dedup check and under the mutation lock:
	// "`update` of the remnant's name ... is refused with the typed
	// `remnant-open` conflict refusal carrying the blocking `remnantId`".
	if err := m.remnantRefusal(name); err != nil {
		m.cfg.mu.Unlock()
		return appwire.HostMutationResult{}, err
	}
	before, ok := m.cfg.hosts.Get(name)
	if !ok {
		m.cfg.mu.Unlock()
		return appwire.HostMutationResult{}, appwire.InvalidParams(fmt.Sprintf("unknown host %q", name))
	}
	if before.Generation != params.ExpectedGeneration || before.IncarnationID != params.ExpectedIncarnationID {
		m.cfg.mu.Unlock()
		return appwire.HostMutationResult{}, hostStaleEntryRefusal(name, hostMutationIdentity{
			Generation:    params.ExpectedGeneration,
			IncarnationID: params.ExpectedIncarnationID,
		}, hostMutationIdentity{Generation: before.Generation, IncarnationID: before.IncarnationID})
	}
	if err := validateHostEntry(entry); err != nil {
		m.cfg.mu.Unlock()
		return appwire.HostMutationResult{}, hostValidationRefusal(name, err)
	}
	identity, err := m.cfg.hosts.Stamp(entry)
	if err != nil {
		m.cfg.mu.Unlock()
		return appwire.HostMutationResult{}, err
	}
	entry = stampedEntry(entry, identity)
	remnantID := mintRemnantID()
	receipt := newHostMutationReceipt(params.MutationID, hostMutationUpdate, entry, m.nowTime())
	receipt.RemnantID = remnantID
	receiptKey := hostMutationReceiptKey(params.MutationID, entry.Name, hostMutationUpdate, hostMutationIdentity{
		Generation:    entry.Generation,
		IncarnationID: entry.IncarnationID,
	})
	// The pinned teardown target is the *retired* identity's: the handles the
	// rebind destroys are the ones tagged with the pair the edit replaced.
	pinned := pendingTeardownFor(before, hostTeardownKindUpdate)
	// An identity-only edit — no effective field changed — has nothing to tear
	// down; its pinned target is empty so the finalization's re-run is a no-op.
	if sameEffectiveHostEntry(before, entry) {
		pinned = HostPendingTeardown{Name: before.Name, Kind: hostTeardownKindUpdate,
			Generation: before.Generation, IncarnationID: before.IncarnationID}
	}
	prev := m.cfg.store.snapshot()
	plan := &hostCommitPlan{
		Kind:    hostMutationUpdate,
		Name:    name,
		Key:     receiptKey,
		Receipt: receipt,
		Entries: m.cfg.store.withReplaced(entry),
		Known:   prev,
		Pinned:  pinned,
		Entry:   entry,
	}
	plan.reconcileFingerprint, _ = hubTOMLFingerprintAt(m.cfg.configPath)
	if err := m.stageCommit(plan, m.nowTime()); err != nil {
		err = m.compensateStagedWrite(plan, prev, err)
		m.cfg.mu.Unlock()
		return appwire.HostMutationResult{}, err
	}
	// The intent flip lands as the post-swap form: for an edit and a removal the
	// runtime transition and the planned teardown are one manager call
	// (UpdateHost / RemoveHost tears down as it swaps), so there is no window a
	// separate `swapStarted`-only flip could describe — a crash inside the call
	// leaves either side applied, which is exactly what
	// `runtime-swapped` + `teardownStarted: true` records. The phase-aware
	// recovery then re-runs the pinned teardown to completion, and that re-run is
	// idempotent ("an already-applied swap lands on the same values").
	if err := m.flipRuntimeSwapped(plan); err != nil {
		return m.compensateStagedCommit(plan, prev, nil, err)
	}
	m.cfg.store.replace(entry)
	m.cfg.store.addReceipt(receiptKey, receipt)
	m.markMutating(name)
	m.cfg.mu.Unlock()
	releaseGate()
	if m.testOnlyParkPostCommit != nil {
		m.testOnlyParkPostCommit(name)
	}

	var liveErr error
	switch {
	case m.testOnlyTeardown != nil:
		if err := m.testOnlyTeardown(ctx, name); err != nil {
			liveErr = fmt.Errorf("update host %q: %w", name, err)
		}
	case m.cfg.manager != nil:
		if err := m.cfg.manager.UpdateHost(entry, func(retired hostreg.Host) {
			m.cfg.state.retire(name, retired.Generation)
		}); err != nil {
			liveErr = fmt.Errorf("update host %q: %w", name, err)
		}
	default:
		prior, _ := m.cfg.hosts.Get(name)
		if err := m.cfg.hosts.Update(entry); err != nil {
			liveErr = err
		} else {
			m.cfg.state.retire(name, prior.Generation)
		}
	}

	m.cfg.mu.Lock()
	m.unmarkMutating(name)
	if stored, ok := m.cfg.hosts.Get(name); !ok && liveErr == nil {
		// A directly driven registry dropped the name while the edit's rebind
		// ran: the swap landed (the registry no longer carries the name, so the
		// rebind had nothing left to retire) and the commit stands, so this is
		// the same forward-repair state a refused rebind leaves — the committed
		// entry plus a remnant whose retry re-applies the staged runtime set,
		// never an un-commit of an edit the file already holds.
		_ = stored
		liveErr = fmt.Errorf("update host %q: the live entry vanished during the rebind", name)
	}
	if liveErr != nil {
		// The commit point: the staged runtime transition is durable and the
		// planned teardown is running, so a failure here is
		// committed-with-teardown-failure — forward repair through
		// `teardown-retry`, never a restore of the prior bytes (spec §6).
		// The remnant pins the identity whose handles the rebind could not
		// finish destroying — the entry the edit replaced. The receipt keeps the
		// pair its scoped key pins (the post-bump identity the commit landed),
		// because the record and its key must agree (§6).
		remnant := newTeardownRemnant(hostRemnantKindUpdate, "update-host", before, receiptKey, m.nowTime())
		receipt.Outcome = hostReceiptOutcomeTeardownFailure
		finalized, err := m.finalizeReceipt(plan, receipt, remnantID, &remnant)
		if err != nil {
			m.cfg.mu.Unlock()
			return appwire.HostMutationResult{}, err
		}
		row := hostReceiptRow(receipt, hostMutationUpdate)
		m.cfg.mu.Unlock()
		if finalized.Outcome == hostReceiptOutcomeCollisionDropped {
			return m.collisionArm(finalized), nil
		}
		return teardownCommittedArm(hostMutationUpdate, "update-host", remnantID, row), nil
	}
	stored, ok := m.cfg.hosts.Get(name)
	if !ok {
		// Unreachable: the vanished-entry case was folded into liveErr above.
		m.cfg.mu.Unlock()
		return appwire.HostMutationResult{}, appwire.InvalidParams(fmt.Sprintf("unknown host %q", name))
	}
	if !slicesEqualStrs(before.Roots, stored.Roots) {
		if m.cfg.remoteCache != nil {
			m.cfg.remoteCache.RemoveSource(name)
		}
		if m.cfg.sources != nil {
			m.cfg.sources.Remove(name)
		}
		if m.cfg.forgetLastGoodThreads != nil {
			m.cfg.forgetLastGoodThreads(name)
		}
		m.registerSource(stored)
	}
	clean := receipt
	// A clean finalize leaves no remnant record, so the receipt carries no
	// remnantId (§6: the field is present exactly when a remnant exists).
	clean.RemnantID = ""
	finalized, err := m.finalizeReceipt(plan, clean, "", nil)
	if err != nil {
		m.cfg.mu.Unlock()
		return appwire.HostMutationResult{}, err
	}
	m.cfg.mu.Unlock()
	if finalized.Outcome == hostReceiptOutcomeCollisionDropped {
		return m.collisionArm(finalized), nil
	}
	// The row's retained-state fold is fenced on the entry's generation, so the
	// reread above is what makes the returned row the identity this call
	// committed; it is built lock-free, like every other row (hostRow's lookups
	// run on the network).
	return committedArm(hostMutationUpdate, m.hostRow(ctx, stored)), nil
}

// slicesEqualStrs compares two string slices element-wise.
func slicesEqualStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Remove
// ---------------------------------------------------------------------------

// RemoveResult is `evener/host/remove` (registry spec 08 §5/§6/§11), returning
// the mutation-result union whose committed and teardown-failure arms carry the
// dedicated RemovedRow row shape.
func (m *hubHostManager) RemoveResult(ctx context.Context, params appwire.HostRemoveParams) (appwire.HostMutationResult, error) {
	if err := guardControllerLocalHosts(ctx); err != nil {
		return appwire.HostMutationResult{}, err
	}
	name := strings.TrimSpace(params.Name)
	if err := hostGuardedMutationRefusal(name, params.MutationID, params.ExpectedGeneration, params.ExpectedIncarnationID); err != nil {
		return appwire.HostMutationResult{}, err
	}
	current, currentKnown := m.currentHostIdentity(name)
	hit, err := m.lookupHostMutationReceipt(hostReceiptQuery{
		MutationID:   params.MutationID,
		Name:         name,
		Kind:         hostMutationRemove,
		Current:      current,
		CurrentKnown: currentKnown,
	})
	if err != nil {
		return appwire.HostMutationResult{}, err
	}
	if hit != nil {
		return m.receiptArm(hit, hostMutationRemove), nil
	}
	// The foreign-marker rule (spec §5), the removal half.
	if _, err := m.finalizeOrphanMarkerIfAny(ctx, name, false); err != nil {
		return appwire.HostMutationResult{}, err
	}
	if hit, err := m.lookupHostMutationReceipt(hostReceiptQuery{
		MutationID:   params.MutationID,
		Name:         name,
		Kind:         hostMutationRemove,
		Current:      current,
		CurrentKnown: currentKnown,
	}); err != nil {
		return appwire.HostMutationResult{}, err
	} else if hit != nil {
		return m.receiptArm(hit, hostMutationRemove), nil
	}
	releaseGate, err := m.acquireHostGate(name, hostops.Holder{Kind: hostops.HolderManager, Activity: "remove"})
	if err != nil {
		return appwire.HostMutationResult{}, err
	}
	defer releaseGate()
	m.cfg.mu.Lock()
	if m.isMutating(name) {
		m.cfg.mu.Unlock()
		return appwire.HostMutationResult{}, hostMutationConflict(name)
	}
	// The remnant fence, past the dedup check and under the mutation lock: a
	// tombstone-only name WITH an open remnant refuses `remnant-open`, never
	// not-found (spec §6: "The remnant fence takes precedence over the
	// tombstone not-found rule").
	if err := m.remnantRefusal(name); err != nil {
		m.cfg.mu.Unlock()
		return appwire.HostMutationResult{}, err
	}
	host, ok := m.cfg.hosts.Get(name)
	if !ok {
		m.cfg.mu.Unlock()
		return appwire.HostMutationResult{}, appwire.InvalidParams(fmt.Sprintf("unknown host %q", name))
	}
	if host.Generation != params.ExpectedGeneration || host.IncarnationID != params.ExpectedIncarnationID {
		m.cfg.mu.Unlock()
		return appwire.HostMutationResult{}, hostStaleEntryRefusal(name, hostMutationIdentity{
			Generation:    params.ExpectedGeneration,
			IncarnationID: params.ExpectedIncarnationID,
		}, hostMutationIdentity{Generation: host.Generation, IncarnationID: host.IncarnationID})
	}
	advanced, err := m.cfg.hosts.NextPresenceEpoch(host.Name)
	if err != nil {
		m.cfg.mu.Unlock()
		return appwire.HostMutationResult{}, fmt.Errorf("remove host %q: %w", name, err)
	}
	var retainedRows []appwire.Thread
	if m.cfg.lastGoodThreads != nil {
		retainedRows = m.cfg.lastGoodThreads(host.Name)
	}
	tombstone := newHostTombstone(host, advanced, retainedRows, m.nowTime(), m.cfg.policy)
	remnantID := mintRemnantID()
	receipt := newHostMutationReceipt(params.MutationID, hostMutationRemove, host, m.nowTime())
	receipt.RemnantID = remnantID
	receiptKey := hostMutationReceiptKey(params.MutationID, host.Name, hostMutationRemove, hostMutationIdentity{
		Generation:    host.Generation,
		IncarnationID: host.IncarnationID,
	})
	prev := m.cfg.store.snapshot()
	plan := &hostCommitPlan{
		Kind:      hostMutationRemove,
		Name:      host.Name,
		Key:       receiptKey,
		Receipt:   receipt,
		Entries:   m.cfg.store.without(host.Name),
		Known:     prev,
		Tombstone: &hostTombstoneStage{Tombstone: tombstone},
		Pinned:    pendingTeardownFor(host, hostTeardownKindRemove),
		Entry:     host,
	}
	plan.reconcileFingerprint, _ = hubTOMLFingerprintAt(m.cfg.configPath)
	if err := m.stageCommit(plan, m.nowTime()); err != nil {
		err = m.compensateStagedWrite(plan, prev, err)
		m.cfg.mu.Unlock()
		return appwire.HostMutationResult{}, err
	}
	// The intent flip lands as the post-swap form: for an edit and a removal the
	// runtime transition and the planned teardown are one manager call
	// (UpdateHost / RemoveHost tears down as it swaps), so there is no window a
	// separate `swapStarted`-only flip could describe — a crash inside the call
	// leaves either side applied, which is exactly what
	// `runtime-swapped` + `teardownStarted: true` records. The phase-aware
	// recovery then re-runs the pinned teardown to completion, and that re-run is
	// idempotent ("an already-applied swap lands on the same values").
	if err := m.flipRuntimeSwapped(plan); err != nil {
		return m.compensateStagedCommit(plan, prev, nil, err)
	}
	m.cfg.store.remove(host.Name)
	m.cfg.store.addReceipt(receiptKey, receipt)
	m.markMutating(host.Name)
	m.cfg.mu.Unlock()
	releaseGate()
	if m.testOnlyParkPostCommit != nil {
		m.testOnlyParkPostCommit(host.Name)
	}
	m.revokeHostTokens(host.Name)

	var teardownErr error
	switch {
	case m.testOnlyTeardown != nil:
		if err := m.testOnlyTeardown(ctx, host.Name); err != nil {
			teardownErr = fmt.Errorf("remove host %q: %w", host.Name, err)
		}
	case m.cfg.manager != nil:
		if err := m.cfg.manager.RemoveHost(host.Name); err != nil {
			teardownErr = fmt.Errorf("remove host %q: %w", host.Name, err)
		}
	default:
		if err := m.cfg.hosts.Remove(host.Name); err != nil {
			teardownErr = err
		}
	}

	m.cfg.mu.Lock()
	defer m.cfg.mu.Unlock()
	m.unmarkMutating(host.Name)
	removedRow := hostEntryRow(host)
	removedRow.Removed = true
	if teardownErr != nil {
		// The commit point: the tombstone and the receipt are durable and the
		// planned teardown has run, so the failure is forward-repair only.
		remnant := newTeardownRemnant(hostRemnantKindRemove, "remove-host", host, receiptKey, m.nowTime())
		receipt.Outcome = hostReceiptOutcomeTeardownFailure
		receipt.Row = hostReceiptRowFor(host)
		finalized, err := m.finalizeReceipt(plan, receipt, remnantID, &remnant)
		if err != nil {
			return appwire.HostMutationResult{}, err
		}
		if finalized.Outcome == hostReceiptOutcomeCollisionDropped {
			return m.collisionArm(finalized), nil
		}
		row := hostReceiptRow(receipt, hostMutationRemove)
		row.OpenRemnantID = remnantID
		row.EscalationAgeSec = m.escalationAgeSec(remnant)
		return teardownCommittedArm(hostMutationRemove, "remove-host", remnantID, row), nil
	}
	clean := receipt
	clean.RemnantID = ""
	finalized, err := m.finalizeReceipt(plan, clean, "", nil)
	if err != nil {
		return appwire.HostMutationResult{}, err
	}
	if finalized.Outcome == hostReceiptOutcomeCollisionDropped {
		return m.collisionArm(finalized), nil
	}
	m.dropHostDerivedState(host.Name)
	return committedArm(hostMutationRemove, removedRow), nil
}

// Update is the pre-union adapter over UpdateResult: it keeps the shipped
// response shape for callers that predate the union and reports a
// non-committed arm as the error that arm means.
func (m *hubHostManager) Update(ctx context.Context, params appwire.HostUpdateParams) (appwire.HostUpdateResponse, error) {
	result, err := m.UpdateResult(ctx, params)
	if err != nil {
		return appwire.HostUpdateResponse{}, err
	}
	row, err := committedRow(result)
	if err != nil {
		return appwire.HostUpdateResponse{}, err
	}
	return appwire.HostUpdateResponse{Host: row}, nil
}

// Remove is the pre-union adapter over RemoveResult, keeping the shipped
// response shape.
func (m *hubHostManager) Remove(ctx context.Context, params appwire.HostRemoveParams) (appwire.HostRemoveResponse, error) {
	result, err := m.RemoveResult(ctx, params)
	if err != nil {
		return appwire.HostRemoveResponse{}, err
	}
	row, err := committedRow(result)
	if err != nil {
		return appwire.HostRemoveResponse{}, err
	}
	return appwire.HostRemoveResponse{Host: row}, nil
}
