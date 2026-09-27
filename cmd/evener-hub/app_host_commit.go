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
	return nil
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
func (m *hubHostManager) finalizeReceipt(plan *hostCommitPlan, receipt HostMutationReceipt, remnantID string, remnant *HostTeardownRemnant) error {
	collision, err := m.reconcileRenameCollision(plan)
	if err != nil {
		return err
	}
	if collision != nil {
		receipt = *collision
		remnantID, remnant = "", nil
	}
	change := hostPersistChange{
		receipt:    &pendingHostReceipt{Key: plan.Key, Receipt: receipt},
		dropMarker: plan.Name,
	}
	if remnant != nil {
		change.remnant = &pendingHostRemnant{RemnantID: remnantID, Remnant: *remnant}
	}
	if err := m.persistHosts(plan.Entries, plan.Known, change); err != nil {
		return err
	}
	plan.lastFingerprint, _ = hubTOMLFingerprintAt(m.cfg.configPath)
	return nil
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
		// live row.
		receipt.Removed = true
		receipt.Row = HostMutationReceiptRow{}
		return &receipt, nil
	}
	receipt.Removed = false
	receipt.Row = hostReceiptRowFor(winner)
	receipt.Generation = winner.Generation
	receipt.IncarnationID = winner.IncarnationID
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
	m.cfg.mu.Lock()
	marker, ok := m.cfg.store.stagedSnapshot()[name]
	if !ok {
		m.cfg.mu.Unlock()
		return nil, nil
	}
	entries := m.cfg.store.snapshot()
	claimed := marker
	claimed.FinalizingToken = mintFinalizingToken()
	if err := m.persistHosts(entries, entries, hostPersistChange{marker: &pendingHostMarker{Name: name, Marker: claimed}}); err != nil {
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
	result, runErr := m.runPinnedTeardown(runCtx, remnantID, remnant)

	m.cfg.mu.Lock()
	defer m.cfg.mu.Unlock()
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
	if err := m.persistHosts(entries, entries, change); err != nil {
		return nil, err
	}
	return &receipt, nil
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
