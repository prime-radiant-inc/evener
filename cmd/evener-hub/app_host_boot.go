package hub

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// breakBootTombstoneCollision applies spec §15's boot collision rule: a
// retained tombstone that collides with a newly live entry for the same name
// is consumed by the live entry, and the live host is rebased as a new
// incarnation — its restored generation set strictly above the tombstone's
// high-water mark, with a fresh incarnation id — before any historical receipt
// can be read as current. The tombstone itself drops in the boot's atomic
// write (the derivation's live-name purge). It reports whether any name was
// rebased, which forces the boot write.
//
// §15's remnant arm: a colliding name that holds an open teardown remnant
// excludes the live entry from the live set as `blocked-pending-teardown`
// instead of rebasing.
func (m *hubHostManager) breakBootTombstoneCollision(records hostRecordSet) bool {
	collided := false
	for _, entry := range m.cfg.store.snapshot() {
		if _, collides := records.Tombstones[entry.Name]; !collides {
			continue
		}
		if remnantID, open := m.openRemnantID(entry.Name); open {
			// Spec §6's fenced arm: "a boot collision involving a
			// remnant-gated name leaves the open remnant resumable by
			// `remnantId`: the boot-collision above-mark bump is forbidden
			// while a remnant is open for that name — boot excludes the
			// colliding live entry from the live set as
			// `blocked-pending-teardown` (never published live) until
			// `teardown-retry` resolves it, so no live incarnation is ever
			// created over an open remnant". The name stays in hub.toml and
			// leaves the live set here; `teardown-retry` resolves the remnant
			// and the operator's next boot (or re-add) publishes it.
			m.logf("host %q is blocked-pending-teardown: open remnant %s fences the colliding live entry; resume it through evener/host/teardown-retry", entry.Name, remnantID)
			m.cfg.store.remove(entry.Name)
			if err := m.cfg.hosts.Remove(entry.Name); err != nil {
				m.logf("host %q not excluded from the live set: %v", entry.Name, err)
			}
			collided = true
			continue
		}
		rebased, err := m.cfg.hosts.RebaseBootCollision(entry.Name)
		if err != nil {
			m.logf("boot collision on host %q not rebased: %v", entry.Name, err)
			continue
		}
		m.cfg.store.replace(rebased)
		// The name's name-keyed attach state is process state a fresh boot
		// cannot hold; nothing to clear here. The rebased pair is persisted by
		// the boot write the caller forces.
		collided = true
	}
	return collided
}

// reconcileBootRecords runs the boot's durable record reconciliation: it
// derives the record set the file should hold (retention prune, live-name
// tombstone consumption, receipt/marker/audit compaction), and when that set
// differs from the store's — or a boot collision rebased a live entry, which
// must reach the file before the hub serves — it persists it in one atomic
// write. It reports whether a write was attempted, so the caller knows not to
// run the separate materialize write.
//
// A derivation that refuses (a hand-edited file already over a global
// tombstone cap with every candidate remnant-gated) is logged and leaves the
// file alone; the next mutation surfaces the typed refusal to its caller.
func (m *hubHostManager) reconcileBootRecords(hasFile bool, collided bool, write bootWrite) (bool, error) {
	if !hasFile || strings.TrimSpace(m.cfg.configPath) == "" {
		return false, nil
	}
	entries := m.cfg.store.snapshot()
	required := collided
	if !required {
		records, err := m.deriveHostTOMLRecords(entries, entries, hostPersistChange{})
		if err != nil {
			m.logf("boot host-record prune refused: %v", err)
			return false, nil
		}
		required = !sameHostTOMLRecords(records, m.cfg.store.recordsSnapshot())
	}
	if !required {
		return false, nil
	}
	if err := m.persistBootRecords(entries, write); err != nil {
		m.logf("boot host-record prune for %s not written: %v", m.cfg.configPath, err)
		return true, err
	}
	return true, nil
}

// bootWrite is where the boot's record maintenance writes land: with the host
// list in hub.toml (the legacy boot), as records alone (records in the store),
// or nowhere yet (a late attach whose records are about to move into the store).
type bootWrite int

const (
	bootWriteHostFile bootWrite = iota
	bootWriteRecordsOnly
	bootWriteMemory
)

// bootMaintenanceFault, when set, fails the boot's record-maintenance writes
// (the prune and the identity mint). It is nil in production.
var bootMaintenanceFault func() error

// persistBootRecords is the one write the boot's record maintenance makes.
func (m *hubHostManager) persistBootRecords(entries []hostreg.Host, write bootWrite) error {
	if bootMaintenanceFault != nil {
		if err := bootMaintenanceFault(); err != nil {
			return err
		}
	}
	return m.persistHosts(entries, entries, hostPersistChange{recordsOnly: write == bootWriteRecordsOnly, inMemory: write == bootWriteMemory})
}

// seedHighWater seeds the store's retained high-water records plus the
// registry's counters from the file's [generations] tables: a name with no live
// entry keeps the triple the removal recorded, so a later rewrite carries it
// through instead of dropping the advance, and a re-add of it mints above the
// marks it left (spec 08 §1) — its generation above the file's and its presence
// epoch above the removal's advance, never back at 1.
func (m *hubHostManager) seedHighWater(records hostRecordSet) {
	m.cfg.store.setHighWaterMap(records.Generations)
	m.cfg.hosts.SeedHighWater(records.highWaterMarks())
}

// materializeHostRecords performs spec 08 §15's boot mint: a hub.toml host the
// file carries no identity record for gets its initial (generation, incarnation
// id, presence epoch) triple recorded, "in that same atomic hub.toml write".
// The registry minted the pair at load (hostreg.New); this write is what makes
// it durable, so the pair survives the next reload unchanged instead of being
// minted again. A file that already records every live host is left
// byte-identical — no boot rewrites it.
//
// The achievement is not a re-registration: the write only fills records the
// file did not carry, it mints no generation above any mark, and the name stays
// live throughout — nothing a re-add does (a generation strictly above the
// retained high-water mark, the purge of the removed name's history) happens
// here. A write that cannot land is logged and retried by the next boot (or by
// the next mutation, which records the same in-memory pair); the hub still
// serves, because the pair is already the registry's current identity.
func (m *hubHostManager) materializeHostRecords(records hostRecordSet, hasFile bool, write bootWrite) error {
	if !hasFile {
		return nil
	}
	entries := m.cfg.store.snapshot()
	for _, entry := range entries {
		record := records.HostRecords[entry.Name]
		mark := records.Generations[entry.Name]
		// Both records must be present and complete: a file carrying only one
		// of them would leave the other's value unrecorded — the incarnation
		// pair or the generation the next boot restores — so the boot write
		// repairs the pair in the canonical shape instead of skipping it.
		// The stored pair must also be the one the registry serves: a hub that
		// started before its records were readable minted its own, and guarded
		// mutations compare against what is served.
		drifted := record.IncarnationID != entry.IncarnationID || record.PresenceEpoch != entry.PresenceEpoch ||
			mark.Generation != entry.Generation || mark.IncarnationID != entry.IncarnationID || mark.PresenceEpoch != entry.PresenceEpoch
		if !record.complete() || !mark.complete() || drifted {
			if err := m.persistBootRecords(entries, write); err != nil {
				m.logf("host %q records for %s not recorded yet: %v", entry.Name, m.cfg.configPath, err)
				return err
			}
			return nil
		}
	}
	return nil
}

// hostFileAbsentOrEmpty reports whether the selected config path genuinely holds
// no document: the file does not exist, or it exists with zero bytes. It
// deliberately differs from the record store's load ok — which is also false for a file
// that cannot be read or that fails to decode — because the boot compensation
// pass may only converge over real absence: clearing an armed record on a read
// or decode failure would discard the compensation record and its stash, the
// only durable copy of the pre-mutation hub.toml. An unset path is the caller's
// to screen out (reconcilePipelineBoot returns before this for an empty path);
// it is not "absence" here, so this helper reports false for it.
func (m *hubHostManager) hostFileAbsentOrEmpty() bool {
	return m.recordStore().absentOrEmpty()
}

// reconcilePipelineBoot runs spec 08b §7's boot passes in their fixed order,
// after hub.toml's records are loaded and before the manager serves:
// interrupted transition (idempotent — the store-open pass already moved
// in-flight records), tombstone-derived host-removed pass, bidirectional
// generation-mirror reconciliation, then the cross-file intent reconciliation
// (§9), which runs the compensation arms before the generic intent rules. Each
// pass writes nothing when it finds nothing to converge, so an untouched file
// stays byte-identical.
func (m *hubHostManager) reconcilePipelineBoot() {
	if m.opsStore() == nil || strings.TrimSpace(m.cfg.configPath) == "" {
		return
	}
	doc, hasFile := m.recordStore().load()
	if !hasFile {
		// load reports false for a genuinely absent or empty document
		// AND for a file it could not read or decode. Only the first is a state
		// the compensation arms may converge over: an armed record clearing
		// through its explicit absent-file arm would delete the record and its
		// stash — the only durable copy of the pre-mutation hub.toml — on a mere
		// read or decode failure. An unreadable or undecodable document leaves
		// everything as it was for the operator to repair.
		if !m.hostFileAbsentOrEmpty() {
			return
		}
		// The document is genuinely absent or empty. The compensation arms that
		// need no bytes still converge here — an armed record (or one in the
		// hub.toml phase) clears through its own explicit `!hasFile` arm, and the
		// orphan-stash prune then removes the cleared record's stash. Returning
		// without this pass left the record and its stash until a file
		// reappeared, with `resumeCompensation`'s explicit absent-file clear arm
		// unreachable from the boot path.
		m.reconcileCompensations()
		m.pruneOrphanHubTOMLStash()
		return
	}
	// §7's interrupted transition, in its own position (store load, hub.toml
	// load, interrupted, host-removed, mirror, intents). The store-open pass
	// already ran it as an interim; the pass is idempotent — with nothing left
	// pending/running it moves nothing and writes nothing.
	m.recoverInterruptedOnce(m.opsStore())
	m.reconcileHostRemovedPass(doc.Records)
	m.reconcileGenerationMirror(doc)
	m.reconcileCompensations()
	m.reconcileStoreSyncIntents()
	m.reconcileTokenRows()
	m.pruneOrphanHubTOMLStash()
}

// pruneOrphanHubTOMLStash removes the canonical stash when no open
// compensation record names it: "a stash left by a crash is ignored (safe to
// prune) at boot — EXCEPT a stash named by a live `pendingCompensation`
// record's stash reference … that record is the compensation's sole durable
// authority" (registry spec 08 §6). Only the canonical path is this pass's to
// prune; a record naming any other path keeps that path, and this pass leaves
// it alone.
func (m *hubHostManager) pruneOrphanHubTOMLStash() {
	path := strings.TrimSpace(m.cfg.configPath)
	if path == "" {
		return
	}
	dir := filepath.Dir(path)
	prefix := filepath.Base(path) + ".stash"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	named := make(map[string]struct{})
	for _, record := range m.opsStore().Compensations() {
		named[record.Stash] = struct{}{}
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		stash := filepath.Join(dir, entry.Name())
		if _, keep := named[stash]; keep {
			continue
		}
		m.pruneHubTOMLStash(stash)
	}
}

// installRestoredRecords converges the store's machine-record model to the
// restored hub.toml's bytes: the live entries, the high-water marks, the
// tombstones and pruned markers, the receipts, the teardown-repair records, and
// the cross-file intents. Without it the model keeps the compensated commit's
// records (the intent, the staged marker, the tombstone, the provisional
// receipt) and the next write re-emits them over the restored bytes — the
// cleared intent would come back and the boot after that would purge the very
// row the compensation restored, and a stale tombstone beside a live entry
// refuses the write outright.
func (m *hubHostManager) installRestoredRecords(doc hostRecordDocument) {
	records := doc.Records
	m.cfg.store.set(doc.Hosts)
	m.cfg.store.setHighWaterMap(nonNilMap(records.Generations))
	m.cfg.store.setReceipts(nonNilMap(records.MutationReceipts))
	m.cfg.store.setRecordMaps(nonNilMap(records.Tombstones), nonNilMap(records.PrunedReceipts))
	m.cfg.store.setRemnantMaps(nonNilMap(records.StagedReceipts), nonNilMap(records.TeardownRemnants), nonNilMap(records.TeardownAttempts))
	m.cfg.store.setStoreSync(nonNilMap(records.PendingStoreSync))
	m.cfg.hosts.SeedHighWater(records.highWaterMarks())
}

// nonNilMap returns m, or an empty map of its type when m is nil: a machine
// record set this store installs is always writable.
func nonNilMap[K comparable, V any](m map[K]V) map[K]V {
	if m == nil {
		return map[K]V{}
	}
	return m
}

// reconcileHostRemovedPass applies every loaded tombstone to the store (§4's
// tombstone-derived pass): the removed incarnation's records are marked
// `host-removed` and the name's token rows drop with it (§7). A crash between
// a remove's hub.toml commit and its live mark recovers exactly here, and a
// tombstone colliding with a live re-add's different incarnation id matches
// nothing.
func (m *hubHostManager) reconcileHostRemovedPass(records hostRecordSet) {
	if len(records.Tombstones) == 0 {
		return
	}
	marks := make(map[string]hostops.HostRemovedMark, len(records.Tombstones))
	for name, tombstone := range records.Tombstones {
		marks[name] = hostops.HostRemovedMark{Generation: tombstone.Generation, IncarnationID: tombstone.IncarnationID}
	}
	marked, dropped, err := m.opsStore().ApplyHostRemovedPass(marks)
	if err != nil {
		m.logf("boot host-removed pass not applied: %v", err)
		return
	}
	if marked > 0 || dropped > 0 {
		m.logf("boot host-removed pass marked %d operation record(s) and dropped %d token row(s)", marked, dropped)
	}
}

// reconcileGenerationMirror runs §4/§7's bidirectional generation-mirror
// reconciliation: the store mirror is rolled back to the file's mark where it
// is ahead with no matching commit marker (the discarded generation's records
// transition to interrupted, its dedup tombstones drop, and the number is
// preserved as the name's high-water), pushed forward where the file's mark is
// ahead, and preserved where the file carries no entry.
func (m *hubHostManager) reconcileGenerationMirror(doc hostRecordDocument) {
	view := hostops.MirrorView{
		Marks:   map[string]hostops.Boundary{},
		Live:    map[string]struct{}{},
		Removed: map[string]struct{}{},
		Commits: map[string]hostops.MirrorCommit{},
	}
	for name, mark := range doc.Records.Generations {
		if !mark.complete() {
			continue
		}
		view.Marks[name] = hostops.Boundary{Generation: mark.Generation, IncarnationID: mark.IncarnationID, PresenceEpoch: mark.PresenceEpoch}
	}
	for _, entry := range doc.Hosts {
		view.Live[entry.Name] = struct{}{}
		if !completeIdentity(entry) {
			continue
		}
		if _, carried := view.Marks[entry.Name]; !carried {
			view.Marks[entry.Name] = hostops.Boundary{
				Generation:    entry.Generation,
				IncarnationID: entry.IncarnationID,
				PresenceEpoch: entry.PresenceEpoch,
			}
		}
	}
	for name, tombstone := range doc.Records.Tombstones {
		view.Removed[name] = struct{}{}
		if _, carried := view.Marks[name]; !carried {
			view.Marks[name] = hostops.Boundary{
				Generation:    tombstone.Generation,
				IncarnationID: tombstone.IncarnationID,
				PresenceEpoch: tombstone.PresenceEpoch,
			}
		}
	}
	for name, commit := range doc.Records.MirrorCommits {
		view.Commits[name] = hostops.MirrorCommit{
			HubTOMLGeneration: commit.HubTOMLGeneration,
			StoreGeneration:   commit.StoreGeneration,
		}
	}
	result, err := m.opsStore().ReconcileMirror(view)
	if err != nil {
		m.logf("boot generation-mirror reconciliation not applied: %v", err)
		return
	}
	if len(result.RolledBack) > 0 || result.RecordsMoved > 0 || result.TombstonesDropped > 0 {
		m.logf("boot generation-mirror rollback: %v rolled back, %d record(s) interrupted, %d dedup tombstone(s) dropped",
			result.RolledBack, result.RecordsMoved, result.TombstonesDropped)
	}
	if len(result.PushedForward) > 0 {
		m.logf("boot generation-mirror push-forward: %v", result.PushedForward)
	}
	if len(result.HighWater) == 0 {
		return
	}
	m.applyMirrorHighWater(result.HighWater)
}

// applyMirrorHighWater writes the mirror pass's high-water raises into
// hub.toml in one atomic write: each discarded generation (or surviving
// mirror) becomes the name's [generations] mark, and a tombstoned name's
// tombstone twin is raised with it. The live registry's counters are seeded up
// (never lowered), so this process cannot mint a generation the raise covers.
//
// The live registry's entry for a name keeps its own generation until the next
// boot reads the raised mark: hostreg has no boot-restore setter, and an
// Update would mint a fresh identity. The durable high-water is what prevents
// reuse, and it is the file's own record.
func (m *hubHostManager) applyMirrorHighWater(highWater map[string]hostops.Boundary) {
	entries := m.cfg.store.snapshot()
	raises := make(map[string]HostGeneration, len(highWater))
	marks := make(map[string]hostreg.HighWater, len(highWater))
	for name, boundary := range highWater {
		raised := HostGeneration{
			Generation:    boundary.Generation,
			IncarnationID: boundary.IncarnationID,
			PresenceEpoch: boundary.PresenceEpoch,
		}
		raises[name] = raised
		marks[name] = hostreg.HighWater{Generation: raised.Generation, PresenceEpoch: raised.PresenceEpoch}
		for i := range entries {
			if entries[i].Name != name {
				continue
			}
			if entries[i].IncarnationID != raised.IncarnationID {
				// The raise came from a store-mirror identity the live entry
				// does not carry (a preserved mirror under an unmaterialized
				// name): only the counters are seeded, never the entry's own
				// incarnation.
				continue
			}
			// The entry carries the whole raised triple: a live name's
			// incarnation is the file mark's own (the raise preserves it), and
			// writing the full triple keeps the live entry, its [generations]
			// mark and the registry's seeded counters one operation rather than
			// three that could drift.
			entries[i].Generation = raised.Generation
			entries[i].IncarnationID = raised.IncarnationID
			entries[i].PresenceEpoch = raised.PresenceEpoch
		}
	}
	if err := m.persistHosts(entries, entries, hostPersistChange{highWaterRaises: raises}); err != nil {
		m.logf("boot generation-mirror high-water for %s not written: %v", m.cfg.configPath, err)
		return
	}
	// SeedHighWater raises the live registry's entry for a name whose counter
	// sits below the mark (§1: a live entry is never left below its mark), so
	// the next removal or edit records the raised generation, never the
	// discarded one.
	m.cfg.hosts.SeedHighWater(marks)
	m.cfg.store.set(entries)
}

// reconcileTokenRows runs §9's closing reverse direction: store rows with no
// covering intent whose hub.toml generation already advanced past them — or
// whose host resolves to a tombstone — are dropped, before the store serves.
func (m *hubHostManager) reconcileTokenRows() {
	doc, hasFile := m.recordStore().load()
	if !hasFile {
		return
	}
	view := hostops.TokenRowReconcile{
		Live:    map[string]uint64{},
		Removed: map[string]hostops.HostRemovedMark{},
		Covered: map[string]map[string]struct{}{},
	}
	for _, entry := range doc.Hosts {
		view.Live[entry.Name] = entry.Generation
	}
	for name, tombstone := range doc.Records.Tombstones {
		if _, live := view.Live[name]; live {
			// A live re-add supersedes the tombstone: its rows belong to the new
			// incarnation and are not the removed one's to delete.
			continue
		}
		view.Removed[name] = hostops.HostRemovedMark{
			Generation:    tombstone.Generation,
			IncarnationID: tombstone.IncarnationID,
		}
	}
	for name, intent := range doc.Records.PendingStoreSync {
		covered := view.Covered[name]
		if covered == nil {
			covered = map[string]struct{}{}
			view.Covered[name] = covered
		}
		for _, value := range intent.TokenValues {
			covered[value] = struct{}{}
		}
	}
	dropped, err := m.opsStore().ReconcileTokenRows(view)
	if err != nil {
		m.logf("boot token-row reconciliation not applied: %v", err)
		return
	}
	if dropped > 0 {
		m.logf("boot token-row reconciliation dropped %d stale row(s)", dropped)
	}
}

// reconcileStoreSyncIntents applies §9's generic intent rules for every intent
// hub.toml still carries and no open compensation owns: the purge is re-applied
// where the intent's rows are still present (the swap landed, the store
// lagged), and the intent is cleared in its own follow-up atomic write once
// hub.toml and the store agree — the store-already-applied no-op included. No
// converged intent survives its boot; a store failure leaves the intent for the
// next boot or the next mutation's write.
func (m *hubHostManager) reconcileStoreSyncIntents() {
	doc, hasFile := m.recordStore().load()
	if !hasFile {
		return
	}
	for _, name := range sortedMapKeys(doc.Records.PendingStoreSync) {
		intent := doc.Records.PendingStoreSync[name]
		if _, open := m.opsStore().Compensation(name); open {
			// A live compensation record owns this name's convergence: its phase
			// arms decide whether the purge stands or the rows come back.
			continue
		}
		purged, err := m.opsStore().ApplyStoreSync(hostops.StoreSyncIntent{
			Host:       name,
			Generation: intent.Generation,
			Values:     intent.TokenValues,
		})
		if err != nil {
			m.logf("boot store-sync intent for %q not applied: %v", name, err)
			continue
		}
		entries := m.cfg.store.snapshot()
		if err := m.persistHosts(entries, entries, hostPersistChange{dropStoreSync: name}); err != nil {
			m.logf("boot store-sync intent for %q not cleared: %v", name, err)
			continue
		}
		if purged > 0 {
			m.logf("boot re-applied the store-sync purge for %q (%d row(s)) and cleared the intent", name, purged)
		} else {
			m.logf("boot found the store-sync intent for %q already converged and cleared it", name)
		}
	}
}

// restoredIdentity returns the identity the rows arm revalidates a preimage row
// against: the restored hub.toml's live entry for name, with the generation and
// incarnation the restored bytes record. A hand-authored restored file (or one
// whose machine records were lost) carries no [generations] mark or no
// [host_records] record; the live registry's own pair is then the identity the
// row must bind to, exactly what the registry's load minted for the name — a
// zero pair would re-insert nothing.
func (m *hubHostManager) restoredIdentity(doc hostRecordDocument, name string) (uint64, string, bool) {
	entry, present := hostEntryNamed(doc.Hosts, name)
	if !present {
		return 0, "", false
	}
	if entry.Generation == 0 || entry.IncarnationID == "" {
		if current, ok := m.cfg.hosts.Get(name); ok {
			if entry.Generation == 0 {
				entry.Generation = current.Generation
			}
			if entry.IncarnationID == "" {
				entry.IncarnationID = current.IncarnationID
			}
		}
	}
	return entry.Generation, entry.IncarnationID, true
}

// reapplyRestoredRuntime re-applies the restored hub.toml's runtime set to the
// live handles (§9's runtime arm): the durable entry set converges to the
// restored file, the registry's counters are seeded to its marks, and a name
// the restored file no longer carries drops its derived state. The process must
// actually serve the restored entry before the record may clear: hostreg has no
// boot-restore setter for a persisted identity, so a name the boot registry did
// not register (or registered under another identity) returns an error and
// leaves the compensation open — the next boot loads the restored file with its
// registry and converges it.
func (m *hubHostManager) reapplyRestoredRuntime(host string) error {
	if m.testOnlyFailRuntimeRevert != nil {
		if err := m.testOnlyFailRuntimeRevert(host); err != nil {
			return err
		}
	}
	restored, ok := m.recordStore().load()
	if !ok {
		return fmt.Errorf("restored %s is unreadable", m.cfg.configPath)
	}
	entry, present := hostEntryNamed(restored.Hosts, host)
	if !present {
		m.installRestoredRecords(restored)
		m.cfg.mu.Lock()
		defer m.cfg.mu.Unlock()
		m.dropHostDerivedState(host)
		return nil
	}
	// §9's runtime arm re-applies the restored set to the live handles: the
	// process must actually serve the restored entry before the record may
	// clear. A name the boot registry did not register (the compensation
	// restored it after the registry was built) is registered now, under the
	// identity the restored file records.
	current, registered := m.cfg.hosts.Get(host)
	if !registered {
		if err := m.cfg.hosts.AddRestored(entry); err != nil {
			return fmt.Errorf("host %q is live in the restored %s but could not be registered: %w", host, m.cfg.configPath, err)
		}
		current, _ = m.cfg.hosts.Get(host)
		m.registerSource(current)
	}
	if entry.IncarnationID != "" && current.IncarnationID != entry.IncarnationID {
		return fmt.Errorf("host %q carries a different restored incarnation id; leaving the compensation open", host)
	}
	if completeIdentity(entry) && !sameEffectiveHostEntry(current, entry) {
		return fmt.Errorf("host %q carries a different identity in the restored %s than the live registry; leaving the compensation open",
			host, m.cfg.configPath)
	}
	m.installRestoredRecords(restored)
	// §4's host-removed pass may have marked the restored incarnation's records
	// (the boot ran it against the compensated commit's tombstone) and dropped
	// its token rows. The restored file carries the incarnation live again, so
	// the marks the removed pair left are reversed: a marked record would make a
	// same-key retry read as a current-generation `host-removed` record
	// (conflicting-operation-id) instead of replaying the interrupted record,
	// and the row would render removed.
	if entry.Generation != 0 && entry.IncarnationID != "" {
		if cleared, err := m.opsStore().ClearHostRemovedMarks(host, hostops.HostRemovedMark{
			Generation:    entry.Generation,
			IncarnationID: entry.IncarnationID,
		}); err != nil {
			m.logf("boot compensation for %q: restored incarnation's host-removed marks not cleared: %v", host, err)
		} else if cleared > 0 {
			m.logf("boot compensation for %q cleared %d host-removed mark(s) on the restored incarnation", host, cleared)
		}
	}
	return nil
}
