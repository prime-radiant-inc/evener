package hub

// This file owns spec 08 §5's two-write staged-receipt protocol and §4/§6's
// fingerprint-bound commit discipline: the staged marker a commit's step-(2)
// write carries, the `swapStarted`/`teardownStarted`/runtime-phase flip writes,
// the pre-write final fingerprint check with its typed `concurrent-edit`
// refusal, the post-rename re-read with its forward reconcile, and the
// phase-aware finalization (replay, foreign marker, boot) that replaces the
// marker with the finalized receipt.
//
// Spec §5, verbatim on the marker: "The step-(2) `hub.toml` write carries a
// transient staged-receipt marker: the scoped key plus `stagedAt`, a
// `swapStarted` intent (false at stage time, flipped true in its own atomic
// `hub.toml` write under the mutation lock before the runtime transition begins
// ...), a `teardownStarted` flag (false at stage time, flipped true in its own
// atomic write after the swap and before the first teardown ...), the
// collision-reconcile armed intent (the validation-read `hub.toml` fingerprint
// the commit staged against; the post-commit write replaces the marker with the
// finalized receipt, clearing the armed intent with it, and the reconcile
// staging supersedes it with the re-read fingerprint when a race is found —
// §6), and the staged provisional payload (explicitly provisional outcome, row,
// generation, a pre-minted `remnantId`, and the pinned teardown target ...)".
//
// And on the phase rule every finalization path shares: "no phase finalizes
// `committed` while old lifecycle handles remain: for a `remove` or
// binding-changing `update` the pinned teardown destroys the superseded
// supervisor/channel/fan-out, and where the pinned target is empty the re-run
// is a no-op."

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// concurrentEditRetries and concurrentEditBackoff bound the pre-write final
// fingerprint check (spec §6 step (4): "after bounded retries"): the file is
// re-read up to concurrentEditRetries times, waiting concurrentEditBackoff
// between attempts, because an external writer's rename is not instantaneous
// and a check that read mid-write should not refuse a mutation the next read
// would have let through.
const (
	concurrentEditRetries = 3
	concurrentEditBackoff = 25 * time.Millisecond
)

// hubTOMLFingerprint is the whole-document content hash of one hub.toml byte
// string: the identity every write the mutation makes records, and the value
// the typed `concurrent-edit` refusal names. Sha256 hex, so two fingerprints
// compare as strings and ride the wire unchanged.
func hubTOMLFingerprint(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// hubTOMLFingerprintAt reads path's current fingerprint. ok is false when the
// file cannot be read at all — an absent file has the empty fingerprint, which
// is a real value a write can leave behind, so absence and unreadability are
// distinguished by ok rather than by an empty string.
func hubTOMLFingerprintAt(path string) (string, bool) {
	if path == "" {
		return "", true
	}
	data, err := configReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", true
		}
		return "", false
	}
	return hubTOMLFingerprint(data), true
}

// hostCommitPlan is one mutation's commit as the shared staged-commit machinery
// needs it: the records steps (2)-(4) write, the planned teardown, and the
// fingerprints the discipline compares.
type hostCommitPlan struct {
	Kind hostMutationKind
	Name string
	// Key is the scoped receipt key the commit finalizes under, and Receipt is
	// the provisional receipt (the marker's staged payload, carrying the
	// pre-minted remnantId).
	Key     string
	Receipt HostMutationReceipt
	// Entries and Known are what the step-(2) write carries: the post-mutation
	// live set and the pre-mutation snapshot the preservation rule reads.
	Entries []hostreg.Host
	Known   []hostreg.Host
	// Tombstone rides a removal's own write, exactly as before.
	Tombstone *hostTombstoneStage
	// StoreSync rides a removal's own write when it has a token row to purge:
	// the cross-file commit intent (deploy-pipeline 08b §9) hub.toml's atomic
	// write carries, so the store purge that applies it is recoverable after a
	// crash.
	StoreSync *pendingHostStoreSync
	// Pinned is the committed teardown target the marker stages before any
	// teardown executes.
	Pinned HostPendingTeardown
	// Entry is the entry the commit pinned (the removed entry for a removal, the
	// stamped entry for an add or update): what the finalized receipt's row and
	// the remnant's cleanup handle are built from.
	Entry hostreg.Host
	// Marker is the store's live marker row for this host once step (2) landed.
	Marker HostStagedReceipt
	// lastFingerprint is the fingerprint of the bytes the mutation's own last
	// write left on disk. The pre-write final check compares the file against
	// it.
	lastFingerprint string
	// reconcileFingerprint is the fingerprint the commit staged against (the
	// validation read's).
	reconcileFingerprint string
}

// stagedMarker builds the marker one commit's step-(2) write carries.
func (m *hubHostManager) stagedMarker(plan *hostCommitPlan, now time.Time) HostStagedReceipt {
	return HostStagedReceipt{
		Key:                  plan.Key,
		StagedAt:             now.UTC().Format(time.RFC3339),
		SwapStarted:          false,
		TeardownStarted:      false,
		Phase:                hostStagedPhaseStaged,
		ReconcileFingerprint: plan.reconcileFingerprint,
		Provisional:          plan.Receipt,
		PendingTeardown:      plan.Pinned,
	}
}

// handleStagedWriteChange builds the staged-change set for one marker write.
func stagedChange(plan *hostCommitPlan, marker HostStagedReceipt) hostPersistChange {
	change := hostPersistChange{
		receipt: &pendingHostReceipt{Key: plan.Key, Receipt: marker.Provisional},
		marker:  &pendingHostMarker{Name: plan.Name, Marker: marker},
	}
	if plan.Tombstone != nil {
		change.tombstone = plan.Tombstone
	}
	if plan.StoreSync != nil {
		change.storeSync = plan.StoreSync
	}
	return change
}

// stageCommit runs spec §5's step (2): one atomic hub.toml write carrying the
// mutation's durable change plus the staged-receipt marker. It is the write
// spec §5 calls the single explicit receipt write point's first half — "The
// step-(2) write lands before the post-commit rebind executes the first
// teardown, so the target and its remnant are durable before any irreversible
// teardown destroys a handle".
//
// The caller holds the mutation lock. A failure returns with nothing staged:
// a failed write installs nothing, so the marker and the entry change are both
// absent and the caller compensates exactly as a pre-commit failure always did.
func (m *hubHostManager) stageCommit(plan *hostCommitPlan, now time.Time) error {
	marker := m.stagedMarker(plan, now)
	entries, known := plan.Entries, plan.Known
	if err := m.persistHosts(entries, known, stagedChange(plan, marker)); err != nil {
		return err
	}
	plan.Marker = marker
	plan.lastFingerprint, _ = hubTOMLFingerprintAt(m.cfg.configPath)
	if m.testOnlyAfterStage != nil {
		// The seam opens the staged-write→flip window deterministically, so a
		// test can prove what the compensation does with the marker and the
		// provisional receipt the staged write just installed.
		m.testOnlyAfterStage(plan.Name)
	}
	return nil
}

// compensationChange is the record-set change every pre-commit compensation
// after a landed staged write carries: the staged marker AND the provisional
// receipt that write installed go with the mutation that is being un-committed.
// The derivation starts from the store's snapshots — which the staged write
// installed into — so without both drops the rollback write re-emits them, the
// marker survives to be finalized (fabricating a `committed` receipt for a
// refused mutation) and the provisional receipt survives as a dedup hit.
func compensationChange(plan *hostCommitPlan) hostPersistChange {
	return hostPersistChange{
		dropMarker: plan.Name, dropReceipt: plan.Key, dropStoreSync: plan.Name,
	}
}

// flipRuntimeSwapped runs spec §5's step (3)'s second half: the runtime phase
// flips to `runtime-swapped` in the same atomic write that flips
// `teardownStarted`, after the swap landed and before the first teardown
// executes. "A marker found in phase `runtime-swapped` ... recovers with the
// pinned teardown target even when `teardownStarted` reads false — the swap may
// have applied."
func (m *hubHostManager) flipRuntimeSwapped(plan *hostCommitPlan) error {
	if err := m.checkOwnFingerprint(plan); err != nil {
		return err
	}
	marker := plan.Marker
	marker.SwapStarted = true
	marker.TeardownStarted = true
	marker.Phase = hostStagedPhaseRuntimeSwapped
	if err := m.persistHosts(plan.Entries, plan.Known, stagedChange(plan, marker)); err != nil {
		return err
	}
	plan.Marker = marker
	plan.lastFingerprint, _ = hubTOMLFingerprintAt(m.cfg.configPath)
	return nil
}

// checkOwnFingerprint is the bounded pre-write final fingerprint check: it
// reads the file and, when the mutation's own last write no longer holds it,
// distinguishes an external edit from a stale read by retrying a bounded number
// of times. A mismatch that survives the retries is returned as the typed
// `concurrent-edit` refusal naming both fingerprints; the caller compensates
// with the re-read preimage (adoptedPreimage) so no window's edit is erased.
func (m *hubHostManager) checkOwnFingerprint(plan *hostCommitPlan) error {
	if plan.lastFingerprint == "" && plan.reconcileFingerprint == "" {
		// No file: the in-memory store is authoritative and nothing can have
		// edited what was never written.
		return nil
	}
	var observed string
	for attempt := range concurrentEditRetries {
		if attempt > 0 {
			time.Sleep(concurrentEditBackoff)
		}
		fingerprint, ok := hubTOMLFingerprintAt(m.cfg.configPath)
		if !ok {
			// Unreadable is not a concurrent edit: the writer below will refuse
			// with the loader's own error, which is the honest one.
			return nil
		}
		observed = fingerprint
		if observed == plan.lastFingerprint {
			return nil
		}
	}
	return concurrentEditRefusal(plan.lastFingerprint, observed)
}

// adoptedPreimage returns the entry set a compensation must write when the
// mutation's staged write was overtaken by an external edit: the pre-mutation
// snapshot with the mutated name's entry replaced by whatever the file now
// carries for it, so the foreign edit survives the rollback instead of the
// rollback restoring over it (spec §6 step (4): "the compensation adopts the
// re-read file and reconciles the runtime to the reconciled set instead of
// restoring over it").
func (m *hubHostManager) adoptedPreimage(plan *hostCommitPlan, previous []hostreg.Host) []hostreg.Host {
	out := make([]hostreg.Host, 0, len(previous)+1)
	for _, entry := range previous {
		if entry.Name == plan.Name {
			continue
		}
		out = append(out, entry)
	}
	adopted, ok := m.hubTOMLFileEntry(plan.Name)
	if !ok || sameEffectiveHostEntryFields(adopted, plan.Entry) {
		// No foreign edit: either the file no longer carries the name, or it
		// carries exactly what this mutation staged (its own write landed). In
		// both cases the pre-mutation snapshot's copy is what the rollback
		// restores — adopting the file here would resurrect the very change the
		// compensation exists to drop.
		for _, entry := range previous {
			if entry.Name == plan.Name {
				out = append(out, entry)
			}
		}
		return out
	}
	out = append(out, adopted)
	return out
}

// hubTOMLFileEntry reads the selected hub.toml and returns the live entry it
// carries for name. ok is false when the file is unreadable, undecodable, or
// carries no such entry — every case where there is nothing to adopt.
func (m *hubHostManager) hubTOMLFileEntry(name string) (hostreg.Host, bool) {
	cfg, ok := m.hostFileRecords()
	if !ok {
		return hostreg.Host{}, false
	}
	for _, entry := range hostRegistryEntries(cfg) {
		if entry.Name == name {
			return entry, true
		}
	}
	return hostreg.Host{}, false
}

// finalizeReceipt runs spec §5's step (4)'s durable half: one atomic hub.toml
// write that replaces the marker with the finalized receipt, drops the staged
// marker, and — when the commit left a remnant — persists the durable remnant
// record beside it: "The same atomic `hub.toml` write that persists the
// committed receipt also persists a durable teardown-remnant record".
//
// The write is preceded by the final fingerprint check. When the file moved
// across the mutation's own last write the commit did not lose the race: the
// post-rename re-read observed a foreign write, the file's bytes win, and the
// receipt is the `collision-dropped` arm (spec §11) — never a `committed`
// receipt for an entry the file does not carry.
func (m *hubHostManager) finalizeReceipt(plan *hostCommitPlan, receipt HostMutationReceipt, remnantID string, remnant *HostTeardownRemnant) (HostMutationReceipt, error) {
	collision, err := m.reconcileRenameCollision(plan)
	if err != nil {
		return receipt, err
	}
	// The write carries the LIVE set as it stands now, never the plan-time
	// snapshot: the mutation lock is released across the post-commit teardown
	// (spec 08b §5), so a concurrent mutation of a DIFFERENT host can commit in
	// that window, and persistHosts installs the set this write derives — writing
	// plan.Entries verbatim would overwrite the sibling's commit on disk (the
	// file would lose it at the next restart, or resurrect a removal).
	// `known` stays the plan's pre-mutation snapshot: it is the ownership half of
	// the preservation rule, which is about what this mutation may change, not
	// about what the file currently holds.
	entries, known := m.cfg.store.snapshot(), plan.Known
	var carryReceipts map[string]HostMutationReceipt
	if collision != nil {
		// The file's bytes win (spec §11: "a hand edit the post-rename re-read
		// observes is adopted (the file's bytes win, the receipt says
		// `collision-dropped`)"). Writing plan.Entries here would put the staged
		// set back over the winning foreign write, so the write carries the
		// file's own set — and its records with it — and the receipt the caller
		// returns is the dropped arm.
		receipt = *collision
		remnantID, remnant = "", nil
		if adopted, ok := m.hubTOMLFileEntries(); ok {
			entries = adopted
		}
		if fileCfg, ok := m.hostFileRecords(); ok {
			carryReceipts = fileCfg.MutationReceipts
		}
	}
	// The change is built from the FINAL receipt: the collision branch above may
	// have replaced it, and the durable receipt the write carries must be the one
	// the response renders.
	change := hostPersistChange{
		receipt:       &pendingHostReceipt{Key: plan.Key, Receipt: receipt},
		dropMarker:    plan.Name,
		carryReceipts: carryReceipts,
	}
	if remnant != nil {
		change.remnant = &pendingHostRemnant{RemnantID: remnantID, Remnant: *remnant}
	}
	if err := m.persistHosts(entries, known, change); err != nil {
		return receipt, err
	}
	if collision != nil {
		// The runtime converges to the adopted set: "the forward reconcile when
		// the file changed across the rename (file-side adopt in a follow-up
		// atomic write + runtime re-apply of the reconciled host set ...)".
		// BOUNDARY (S13/S17): the re-apply reaches the registry and the store
		// rows here; a manager-owned channel for a name the adopted set drops is
		// the supervisor/teardown slices' to drain.
		m.reconcileRuntimeToFileLocked(plan.Name)
	}
	plan.lastFingerprint, _ = hubTOMLFingerprintAt(m.cfg.configPath)
	return receipt, nil
}

// reconcileRuntimeToFileLocked re-applies the adopted file's entry for name to
// the live registry and the store: the file's bytes won, so the live set must
// describe them rather than the commit that lost the race. Callers hold the
// mutation lock (the finalizing write runs under it), so it takes nothing.
func (m *hubHostManager) reconcileRuntimeToFileLocked(name string) {
	if strings.TrimSpace(m.cfg.configPath) == "" {
		return
	}
	entry, present := m.hubTOMLFileEntry(name)
	if !present {
		m.cfg.store.remove(name)
		if err := m.cfg.hosts.Remove(name); err != nil && !errors.Is(err, hostreg.ErrUnknownHost) {
			m.logf("collision reconcile: host %q not dropped from the live set: %v", name, err)
		}
		m.dropHostDerivedState(name)
		return
	}
	if current, ok := m.cfg.hosts.Get(name); ok && sameEffectiveHostEntry(current, entry) {
		return
	}
	if err := m.cfg.hosts.Update(entry); err != nil {
		m.logf("collision reconcile: host %q not re-applied to the live set: %v", name, err)
		return
	}
	m.cfg.store.replace(entry)
}

// hubTOMLFileEntries reads the selected hub.toml and returns the live entry set
// it carries, in file order — the set a collision write adopts.
func (m *hubHostManager) hubTOMLFileEntries() ([]hostreg.Host, bool) {
	cfg, ok := m.hostFileRecords()
	if !ok {
		return nil, false
	}
	return hostRegistryEntries(cfg), true
}

// reconcileRenameCollision is the post-rename re-read (spec §11: "the dropped
// arm carries the authoritative `HostRow` whenever the file still holds the
// name ... and when the re-read instead finds the name gone entirely (a
// hand-edit deletion) the arm carries no `host` and sets `removed: true`"). It
// returns the `collision-dropped` receipt to finalize under when the file no
// longer carries the entry the commit staged, and nil when the commit's own
// write still holds the file.
func (m *hubHostManager) reconcileRenameCollision(plan *hostCommitPlan) (*HostMutationReceipt, error) {
	if plan.lastFingerprint == "" && plan.reconcileFingerprint == "" {
		return nil, nil
	}
	observed, ok := hubTOMLFingerprintAt(m.cfg.configPath)
	if !ok {
		return nil, fmt.Errorf("hub.toml re-read %s failed", m.cfg.configPath)
	}
	if observed == plan.lastFingerprint {
		return nil, nil
	}
	// The file moved, but a moved file is not by itself a lost race: another
	// mutation of this hub may have committed a DIFFERENT name in the window
	// (every write rewrites the whole document). What the dropped arm means is
	// narrower, and it is what §11 says: "the re-read instead finds the name
	// gone entirely" or carrying something other than the entry this commit
	// staged — the authoritative result is the winning hub.toml live entry. So
	// the check reads the name's own entry, not the document hash.
	entry, present := m.hubTOMLFileEntry(plan.Name)
	if present && sameEffectiveHostEntry(entry, plan.Entry) {
		// The name still carries the entry this commit staged: the file moved
		// because another mutation of this hub committed a different name in the
		// window, which is not this commit losing a race.
		return nil, nil
	}
	if plan.Kind == hostMutationRemove && !present {
		// A removal's staged state IS the absence of the name, so an absent name
		// is the commit's own outcome rather than a foreign deletion — and a
		// concurrent mutation of a different name rewrote the document around
		// it. Only a name present again (a foreign re-add) is a lost race here.
		return nil, nil
	}
	// The name carries something other than the staged entry, or an add/update's
	// staged entry is gone entirely — the hand-edit deletion, whose arm "carries
	// no `host` and sets `removed: true`".
	return m.collisionDroppedReceipt(plan, observed)
}

// collisionDroppedReceipt builds the `collision-dropped` receipt the re-read's
// observation implies, adopting the winning file entry (or its absence).
func (m *hubHostManager) collisionDroppedReceipt(plan *hostCommitPlan, observed string) (*HostMutationReceipt, error) {
	stagedRow := hostReceiptRowFor(plan.Entry)
	receipt := plan.Marker.Provisional
	receipt.Outcome = hostReceiptOutcomeCollisionDropped
	receipt.DroppedEntry = &stagedRow
	receipt.WinningFingerprint = observed
	receipt.RemnantID = ""
	receipt.BootRecovered = false
	winner, ok := m.hubTOMLFileEntry(plan.Name)
	if !ok {
		// The hand-edit deletion: the winning arm is the deletion, with the
		// marker tombstone rows use and no tombstone minted, never a fabricated
		// live row. The record keeps the pair its scoped key pins (§6 pins the
		// two together), so the deletion arm renders without a row and the
		// record still validates.
		receipt.Removed = true
		receipt.Row = HostMutationReceiptRow{}
		receipt.Generation = plan.Entry.Generation
		receipt.IncarnationID = plan.Entry.IncarnationID
		return &receipt, nil
	}
	receipt.Removed = false
	receipt.Row = hostReceiptRowFor(winner)
	// The record's pair is the one its scoped key pins (§6: the record and the
	// key agree), so the winning row's own values stay in `row`, where a reader
	// takes them from: a hand edit carries no persisted identity, and the pair
	// the commit pinned is what the key and the record must keep.
	receipt.Generation = plan.Entry.Generation
	receipt.IncarnationID = plan.Entry.IncarnationID
	return &receipt, nil
}

// ---------------------------------------------------------------------------
// Orphan-marker finalization
// ---------------------------------------------------------------------------

// finalizeOrphanMarkerIfAny finalizes a staged-receipt marker this call did not
// stage, for the host it is about to mutate: spec §5's foreign-marker rule —
// "Any mutation-path write that finds a marker it did not stage for its own
// host finalizes that marker first" — and the boot half of the same rule ("Boot
// finalizes each host entry by the persisted phase and flags").
//
// The finalization is phase-aware and claim-protected: it atomically claims the
// marker with a server-generated attempt token, runs the pinned teardown to
// completion with the mutation lock released (the caller already holds the
// host's gate, so §5's gate-first order holds and "the finder releases the
// mutation lock after the atomic claim lands, then try-acquires the remnant's
// host gate" is the caller's own reservation), then re-takes the lock and
// finalizes the receipt from the observed result. The marker's persisted
// `swapStarted`/`teardownStarted`/phase decide whether the staged runtime set is
// re-applied first — "no phase finalizes `committed` while old lifecycle
// handles remain" — and the receipt records `bootRecovered: true` exactly on the
// boot path.
//
// A marker for another host rides along untouched: only the marker keyed by the
// name this call owns is touched.
func (m *hubHostManager) finalizeOrphanMarkerIfAny(ctx context.Context, name string, bootRecovered bool) (*HostMutationReceipt, error) {
	if strings.TrimSpace(m.cfg.configPath) == "" {
		// No file: nothing durable to carry a marker.
		return nil, nil
	}
	// The host gate is taken BEFORE the claim. A claim's live claimant holds the
	// gate for as long as its attempt is live, so a gate we can acquire proves
	// any existing claim is dead and ours to adopt — and a gate we cannot
	// acquire means someone owns the host, so the marker is theirs to finalize
	// and this finder leaves it alone (spec §5: "A held gate means a live
	// committer still owns the host").
	releaseGate, gateErr := m.acquireHostGate(name, hostops.Holder{Kind: hostops.HolderManager, Activity: "finalize-marker"})
	if gateErr != nil {
		return nil, nil
	}
	// The gate is held only for the claim; the run below releases it, exactly as
	// `teardown-retry` does. It has to: the pinned teardown routes a remove or
	// binding-changing update through the manager's self-acquiring paths
	// (RemoveHost/UpdateHost), which take the same non-reentrant per-host gate —
	// holding the reservation across the run would hang this goroutine (and boot
	// with it) instead of repairing the marker. (A mutation's own teardown is
	// the stronger form: it runs through the gate-inheriting entries with its
	// reservation held throughout — spec 08 §4's "gate released last".)
	// Re-acquiring it before the finalizing write keeps the write inside the
	// same discipline the mutations apply.
	gateHeld := true
	releaseOnce := func() {
		if gateHeld {
			gateHeld = false
			releaseGate()
		}
	}
	defer releaseOnce()
	m.cfg.mu.Lock()
	if m.isMutating(name) {
		// The commit that staged this marker is still in flight in this process
		// (the name's mutation mark spans the committing mutation's whole
		// commit-and-teardown window, held under its reservation since spec 08
		// §4's "gate released last"; a finder holding the gate only reaches a
		// live mark through a path that does not present that reservation). Spec
		// §5: "A replay naming a still-staged marker never re-applies. While the
		// original commit holds the mutation lock the replay fails fast with the
		// transient busy form" — so the finder leaves the marker alone and the
		// caller's own mutation refuses as a conflict.
		m.cfg.mu.Unlock()
		return nil, nil
	}
	marker, ok := m.cfg.store.stagedSnapshot()[name]
	if !ok {
		m.cfg.mu.Unlock()
		return nil, nil
	}
	entries := m.cfg.store.snapshot()
	claimed := marker
	claimed.FinalizingToken = mintFinalizingToken()
	// The name this finalization owns: the write's records for it must win over
	// the file's older copies. A removal's name is not in the live set, so
	// without this the preservation rule would ride the file's older receipt
	// (the staged provisional one) back over the finalized receipt this write
	// carries — the marker would drop while its finalized outcome, remnant, or
	// `bootRecovered` marker never rendered.
	known := ownedEntrySet(entries, name)
	if err := m.persistHosts(entries, known, hostPersistChange{marker: &pendingHostMarker{Name: name, Marker: claimed}}); err != nil {
		m.cfg.mu.Unlock()
		return nil, err
	}
	marker = claimed
	m.cfg.mu.Unlock()

	// The phase-aware recovery, with no mutation lock held. Phase
	// `runtime-swapped` (or an unknown post-swap phase) and phase `staged` with
	// `teardownStarted: true` re-run the pinned teardown; phase `staged` with
	// both flags false re-applies the staged runtime set first — here the
	// registry already holds the file's set, so the re-apply is the same
	// reconcile the mutation's own swap performed, and the visible step is the
	// teardown.
	remnant := HostTeardownRemnant{
		Host:            name,
		Kind:            string(hostMutationKindForMarker(marker)),
		Seam:            hostSeamForMarker(marker),
		PendingTeardown: marker.PendingTeardown,
		Generation:      marker.Provisional.Generation,
		IncarnationID:   marker.Provisional.IncarnationID,
		MutationKey:     marker.Key,
		CommittedAt:     marker.Provisional.CommittedAt,
		CleanupHandle: HostCleanupHandle{
			Kind:          cleanupHandleKindLocal,
			Generation:    marker.Provisional.Generation,
			IncarnationID: marker.Provisional.IncarnationID,
			PresenceEpoch: m.presenceEpochFor(name),
		},
	}
	remnantID := marker.Provisional.RemnantID
	runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), m.cfg.policy.teardownTimeout)
	defer cancel()
	releaseOnce()
	result, runErr := m.runPinnedTeardown(runCtx, remnantID, remnant)
	if reacquire, gateErr := m.acquireHostGate(name, hostops.Holder{Kind: hostops.HolderManager, Activity: "finalize-marker"}); gateErr == nil {
		gateHeld = true
		releaseGate = reacquire
	}

	m.cfg.mu.Lock()
	defer m.cfg.mu.Unlock()
	// Verify the claim before finalizing: another path may have adopted the
	// marker while this run was in flight (its token would differ), in which
	// case this caller finalizes nothing and the adopter's write stands.
	current, ok := m.cfg.store.stagedSnapshot()[name]
	if !ok {
		return nil, nil
	}
	if current.FinalizingToken != marker.FinalizingToken {
		return nil, nil
	}
	receipt := marker.Provisional
	receipt.RemnantID = remnantID
	receipt.BootRecovered = bootRecovered
	change := hostPersistChange{
		receipt:    &pendingHostReceipt{Key: marker.Key, Receipt: receipt},
		dropMarker: name,
	}
	if runErr != nil {
		seam := remnant.Seam
		if result.Seam != "" {
			seam = result.Seam
		}
		remnant.Seam = seam
		receipt.Outcome = hostReceiptOutcomeTeardownFailure
		change.receipt = &pendingHostReceipt{Key: marker.Key, Receipt: receipt}
		change.remnant = &pendingHostRemnant{RemnantID: remnantID, Remnant: remnant}
	} else {
		receipt.Outcome = hostReceiptOutcomeCommitted
		receipt.RemnantID = ""
	}
	entries = m.cfg.store.snapshot()
	if err := m.persistHosts(entries, known, change); err != nil {
		return nil, err
	}
	return &receipt, nil
}

// ownedEntrySet returns entries plus a name-only placeholder for name, so the
// writer's ownership rules treat name as this write's to change. Callers pass
// it as `known` when the write must carry a removed (or otherwise non-live)
// name's records authoritatively; the placeholder never becomes a live entry —
// the write's `entries` slice is what renders.
func ownedEntrySet(entries []hostreg.Host, name string) []hostreg.Host {
	out := make([]hostreg.Host, 0, len(entries)+1)
	out = append(out, entries...)
	out = append(out, hostreg.Host{Name: name})
	return out
}

// hostMutationKindForMarker recovers the mutation kind a marker belongs to from
// its scoped key — the key is the durable identity, the marker carries no
// separate kind field.
func hostMutationKindForMarker(marker HostStagedReceipt) hostMutationKind {
	if scope, ok := parseHostReceiptScopedKey(marker.Key); ok {
		return scope.Kind
	}
	return hostMutationUpdate
}

// hostSeamForMarker names the rebind step a marker's pinned target re-runs.
func hostSeamForMarker(marker HostStagedReceipt) string {
	if marker.PendingTeardown.Kind == hostTeardownKindRemove {
		return "remove-host"
	}
	return "update-host"
}

// presenceEpochFor returns the presence epoch the durable records carry for
// name: the live entry's, else the retained high-water mark's, else zero. It is
// provenance on a cleanup handle, never the identity the handle resolves
// through.
func (m *hubHostManager) presenceEpochFor(name string) uint64 {
	if host, ok := m.cfg.store.entryByName(name); ok && host.PresenceEpoch != 0 {
		return host.PresenceEpoch
	}
	if mark, ok := m.cfg.store.highWaterSnapshot()[name]; ok {
		return mark.PresenceEpoch
	}
	return 0
}
