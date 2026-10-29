package hub

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// This file owns how the manager finds its machine records at boot. Records
// live in the operation store; hub.toml keeps only the [[hosts]]. A hub that
// last ran a build that kept the records in hub.toml's reserved sections
// settles any half-done work of that build with the recovery code that build
// shipped, imports the records into the store in one atomic write that also
// records the import, and then drops the sections from hub.toml. A crash
// between the import and the strip leaves the store as the home and the
// sections as inert leftovers the next boot drops without importing again.

// hostPreRecordsSuffix names the one-time copy of the hub.toml the migration
// found, kept beside it so a bad import, or a downgrade to a build that reads
// the reserved sections, can be recovered by hand.
const hostPreRecordsSuffix = ".pre-records"

// hostRefusal is why host mutations are refused: the store that holds the
// machine records is unavailable, or what it holds could not be settled.
type hostRefusal struct{ reason string }

// opsStore is the operation store, nil while none is attached.
func (m *hubHostManager) opsStore() *hostops.Store { return m.cfg.ops.Load() }

// recordStore is the durable home the manager reads and writes records
// through.
func (m *hubHostManager) recordStore() hostRecordStore {
	m.cfg.recordsMu.RLock()
	defer m.cfg.recordsMu.RUnlock()
	return m.cfg.records
}

func (m *hubHostManager) useRecordStore(store hostRecordStore) {
	m.cfg.recordsMu.Lock()
	defer m.cfg.recordsMu.Unlock()
	m.cfg.records = store
}

// recordsInStore reports whether the machine records live in the operation
// store.
func (m *hubHostManager) recordsInStore() bool {
	_, ok := m.recordStore().(hostOpsRecordStore)
	return ok
}

// refuseMutations records why host mutations refuse, and logs it once.
func (m *hubHostManager) refuseMutations(reason string) {
	m.logf("host add, edit and remove are refused until the host records settle: %s", reason)
	m.cfg.refusal.Store(&hostRefusal{reason: reason})
}

// mutationRefusal is the typed refusal a host mutation returns while the
// manager cannot keep the machine records: retryable, because the hub keeps
// working to clear the condition.
func (m *hubHostManager) mutationRefusal() error {
	refusal := m.cfg.refusal.Load()
	if refusal == nil {
		return nil
	}
	return appwire.Unavailable("host changes are paused while the hub restores its host records: " + refusal.reason)
}

// bootRecords loads the machine records and brings them to a state the manager
// can serve and mutate from, by what the store holds:
//
//   - a store that already holds the records is the home: serve from it;
//   - a store that does not has the records still in hub.toml: settle the old
//     build's half-done work, import, and strip;
//   - no store: the records stay in hub.toml (tests, embedders), or, when a
//     configured store failed to open, hosts keep serving and mutations refuse.
func (m *hubHostManager) bootRecords() {
	ops := m.opsStore()
	if ops == nil {
		m.bootLegacyRecords()
		if m.cfg.opsUnavailable {
			m.refuseMutations("the operation store is unavailable")
		}
		return
	}
	if _, _, imported := ops.HostRecords(); imported {
		m.bootStoreRecords(ops)
		return
	}
	m.keepPreRecordsCopy()
	m.bootLegacyRecords()
	m.migrateLegacyRecords(ops)
}

// attachOps brings the records to the state a boot would have, after the hub
// started serving without that state. store is a store that opened since, or
// nil to retry settling the records of the one already attached. Host
// mutations are refused until this returns clean, so none is in flight.
func (m *hubHostManager) attachOps(store *hostops.Store) {
	attaching := &hostRefusal{reason: "the operation store is attaching"}
	m.cfg.refusal.Store(attaching)
	if store != nil && m.opsStore() == nil {
		m.cfg.ops.Store(store)
	}
	m.bootRecords()
	// A boot that could not settle the records left its own refusal in place.
	m.cfg.refusal.CompareAndSwap(attaching, nil)
}

// migrateLegacyRecords moves the records the legacy boot just settled from
// hub.toml into the operation store.
func (m *hubHostManager) migrateLegacyRecords(ops *hostops.Store) {
	if len(ops.Compensations()) != 0 || len(m.cfg.store.storeSyncSnapshot()) != 0 {
		m.refuseMutations("an earlier removal's cross-file work is still open; it is retried on the next attempt")
		return
	}
	entries := m.cfg.store.snapshot()
	records, err := m.deriveHostTOMLRecords(entries, entries, hostPersistChange{})
	if err != nil {
		m.refuseMutations(fmt.Sprintf("the existing records could not be prepared for the move: %v", err))
		return
	}
	set := recordSetFor(entries, records)
	family, err := encodeHostRecords(set)
	if err != nil {
		m.refuseMutations(fmt.Sprintf("the existing records could not be encoded for the move: %v", err))
		return
	}
	switch err := ops.ImportHostRecords(family, hostMirrorFor(set, ops.Boundaries())); {
	case errors.Is(err, hostops.ErrHostRecordsImported):
		// A previous attempt got the import durable and this process lost the
		// news: the store is the home.
		m.bootStoreRecords(ops)
		return
	case err != nil && !hostops.RenameLanded(err):
		m.refuseMutations(fmt.Sprintf("the existing records were not moved: %v", err))
		return
	}
	m.cfg.store.installRecords(records)
	m.useRecordStore(hostOpsRecordStore{path: m.cfg.configPath, ops: ops, logf: m.cfg.logf})
	m.logf("moved %d host record(s) from %s into the operation store", len(set.HostRecords), m.cfg.configPath)
	m.stripLegacySections(false)
}

// keepPreRecordsCopy keeps the hub.toml an upgrade found, before the legacy boot
// rewrites it. Only a file that holds record sections has an original worth
// keeping: a file with the hosts alone loses nothing the upgrade did not
// already lose to every earlier rewrite.
func (m *hubHostManager) keepPreRecordsCopy() {
	path := m.cfg.configPath
	if path == "" {
		return
	}
	doc, raw, err := readHubTOMLDocument(path)
	if err != nil || len(legacyRecordSections(doc)) == 0 {
		return
	}
	if err := keepPreRecordsCopy(path, raw); err != nil {
		m.logf("the original %s was not kept: %v", path, err)
	}
}

// stripLegacySections drops the reserved record sections from hub.toml once
// the store holds the records. The original was kept before the legacy boot
// rewrote it (keepPreRecordsCopy), or, for a file that reached this point
// without a copy, is kept now. The strip is idempotent and failure is retried
// by the next boot.
func (m *hubHostManager) stripLegacySections(keepCopy bool) {
	path := m.cfg.configPath
	if path == "" {
		return
	}
	doc, raw, err := readHubTOMLDocument(path)
	if err != nil {
		m.logf("hub.toml record sections not dropped: %v", err)
		return
	}
	sections := legacyRecordSections(doc)
	if len(sections) == 0 {
		return
	}
	cfg, state := readHubTOMLConfig(path)
	if state != hostFileReadable {
		m.logf("hub.toml record sections not dropped: %s is unreadable", path)
		return
	}
	if keepCopy {
		if err := keepPreRecordsCopy(path, raw); err != nil {
			m.logf("hub.toml record sections not dropped: %v", err)
			return
		}
	}
	hosts := hostEntriesWithIdentity(cfg.Hosts, hostRecordSet{})
	if err := writeHubTOMLHostsOnly(path, hosts, hosts); err != nil {
		m.logf("hub.toml record sections not dropped: %v", err)
		return
	}
	m.logf("dropped the %s record section(s) from %s; the original is kept at %s", strings.Join(sections, ", "), path, path+hostPreRecordsSuffix)
}

// keepPreRecordsCopy writes the one-time copy of the original hub.toml. An
// existing copy is the original already: it is never overwritten by a later
// file.
func keepPreRecordsCopy(path string, original []byte) error {
	copyPath := path + hostPreRecordsSuffix
	if _, err := os.Stat(copyPath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", copyPath, err)
	}
	if err := hubTOMLReplaceWithBytes(copyPath, original, hubTOMLSyncDir); err != nil {
		return fmt.Errorf("keep %s: %w", copyPath, err)
	}
	return nil
}

// bootStoreRecords serves from a store that holds the records: it installs
// them, drops any sections hub.toml still carries, rolls back a commit whose
// host change never reached hub.toml, finalizes the commits whose host change
// did, and rebuilds what is derived from the records.
func (m *hubHostManager) bootStoreRecords(ops *hostops.Store) {
	store := hostOpsRecordStore{path: m.cfg.configPath, ops: ops, logf: m.cfg.logf}
	m.useRecordStore(store)
	records, err := store.records()
	if err != nil {
		// The registry came up from entries minted at load; they keep serving,
		// and the unreadable records stay in the store untouched.
		m.cfg.store.set(m.cfg.hosts.All())
		m.refuseMutations(fmt.Sprintf("the host records in the operation store cannot be read: %v", err))
		return
	}
	m.installRestoredRecords(hostRecordDocument{Hosts: m.cfg.hosts.All(), Records: records})
	m.stripLegacySections(true)
	doc, hasFile := store.load()
	if hasFile {
		m.rollBackUnlandedCommits(doc)
	}
	collided := m.breakBootTombstoneCollision(records)
	for _, name := range sortedStagedMarkerNames(m.cfg.store.stagedSnapshot()) {
		if _, err := m.finalizeOrphanMarkerIfAny(context.Background(), name, true); err != nil {
			m.logf("boot finalization of the staged receipt marker for %q refused: %v", name, err)
		}
	}
	if attempted := m.reconcileBootRecords(hasFile, collided); !attempted {
		m.materializeHostRecords(records, hasFile)
	}
	m.reconcileStoreBoot()
}

// rollBackUnlandedCommits drops the staged marker and the provisional receipt
// of every commit whose host change never reached hub.toml. A commit writes the
// store first, so a crash between the two leaves a staged marker for a change
// hub.toml does not hold; finalizing it would commit a mutation the caller was
// never told succeeded. The host set hub.toml holds is the truth, and the
// generation the commit minted stays recorded so it is not handed out again.
func (m *hubHostManager) rollBackUnlandedCommits(doc hostRecordDocument) {
	entries := m.cfg.store.snapshot()
	for _, name := range sortedStagedMarkerNames(m.cfg.store.stagedSnapshot()) {
		marker := m.cfg.store.stagedSnapshot()[name]
		if markerLanded(marker, doc.Hosts) {
			continue
		}
		m.logf("boot rolled back the staged commit for %q: its host change never reached %s", name, m.cfg.configPath)
		if err := m.persistHosts(entries, entries, hostPersistChange{dropMarker: name, dropReceipt: marker.Key}); err != nil {
			m.logf("boot rollback of the staged commit for %q not written: %v", name, err)
		}
	}
}

// markerLanded reports whether hub.toml holds the host change a staged marker's
// commit made: the host present as the commit staged it for an add or an edit,
// absent for a removal.
func markerLanded(marker HostStagedReceipt, hosts []hostreg.Host) bool {
	scope, ok := parseHostReceiptScopedKey(marker.Key)
	if !ok {
		return true
	}
	entry, present := hostEntryNamed(hosts, scope.Name)
	if scope.Kind == hostMutationRemove {
		return !present
	}
	return present && sameEffectiveHostEntryFields(entry, hostFromReceiptRow(marker.Provisional.Row))
}

// hostFromReceiptRow is the effective entry a receipt row records.
func hostFromReceiptRow(row HostMutationReceiptRow) hostreg.Host {
	return hostreg.Host{
		Name:       row.Name,
		SSH:        row.Address,
		User:       row.User,
		KeyPath:    row.KeyPath,
		EvenerPath: row.EvenerPath,
		ConfigPath: row.ConfigPath,
		Addr:       row.Addr,
		Roots:      row.Roots,
	}
}

// reconcileStoreBoot runs the boot passes that depend on the records rather
// than on how they were stored: in-flight operations a crash left running are
// interrupted, removed hosts' operation records and token rows are marked
// and dropped, and the boundary mirror is rebuilt from the records.
func (m *hubHostManager) reconcileStoreBoot() {
	ops := m.opsStore()
	if moved, err := ops.RecoverInterrupted(); err != nil {
		m.logf("boot interrupted transition not completed: %v", err)
	} else if moved > 0 {
		m.logf("boot moved %d in-flight operation(s) to interrupted", moved)
	}
	if doc, ok := m.recordStore().load(); ok {
		m.reconcileHostRemovedPass(doc.Records)
	}
	m.reconcileTokenRows()
	if store, ok := m.recordStore().(hostOpsRecordStore); ok {
		if err := store.syncMirror(); err != nil {
			m.logf("boot boundary mirror not rebuilt: %v", err)
		}
	}
}

// bootLegacyRecords is the boot for records that still live in hub.toml's
// reserved sections: the store takes the file's records, the old build's
// recovery passes settle whatever it left half done, and the boot's durable
// prune and identity mint land in hub.toml.
func (m *hubHostManager) bootLegacyRecords() {
	hosts := m.cfg.hosts
	// The file's records are read once, before anything else can mint: its
	// retained marks seed the counters, so a host folded in below mints above
	// every mark the file carries, and any live host the file carries no
	// identity for gets its pair recorded in the same boot. Seeding runs before
	// the store takes its snapshot, so the entries the snapshot carries are the
	// seeded ones — a registry built before the marks were known would otherwise
	// record its pre-seed epochs.
	loaded, hasFile := m.recordStore().load()
	fileRecords := loaded.Records
	if hasFile {
		m.seedHighWater(fileRecords)
	}
	// The file's receipts are the dedup table's durable half: they are loaded
	// as-is — decodeConfig already refused any record this build cannot decode
	// — so a lost-response retry after a restart still finds the receipt its
	// commit wrote.
	m.cfg.store.setReceipts(fileRecords.MutationReceipts)
	m.cfg.store.set(hosts.All())
	// The file's tombstones and pruned markers are the other durable halves:
	// decodeConfig validated every record (the same hard startup-error posture
	// a corrupt host entry takes), so they are loaded as-is and restored
	// alongside the host entries (spec §15: "boot restores them alongside the
	// host entries, and they survive controller restarts").
	m.cfg.store.setRecordMaps(fileRecords.Tombstones, fileRecords.PrunedReceipts)
	// The cross-file commit intents load with the other machine-managed
	// sections: decodeConfig already validated each shape, so they are restored
	// as-is, and the boot pipeline below (or the next mutation's write) is what
	// converges them.
	m.cfg.store.setStoreSync(fileRecords.PendingStoreSync)
	// The teardown-repair records load with the other machine-managed sections;
	// decodeConfig already validated each shape, so they are restored as-is (the
	// same hard startup-error posture a corrupt host entry takes).
	m.cfg.store.setRemnantMaps(fileRecords.StagedReceipts, fileRecords.TeardownRemnants, fileRecords.TeardownAttempts)
	collided := false
	if hasFile {
		collided = m.breakBootTombstoneCollision(fileRecords)
	}
	// Boot finalizes each staged-receipt marker by the persisted phase and flags
	// (spec §5: "Boot finalizes each host entry by the persisted phase and flags
	// ... and the receipt records `bootRecovered: true`"). A marker is the
	// durable evidence of a commit whose post-commit write was lost, so boot
	// runs the phase-aware recovery and finalizes from the observed result
	// before the hub serves; a failure is logged and leaves the marker for the
	// next mutation-path write or the next boot.
	for _, name := range sortedStagedMarkerNames(m.cfg.store.stagedSnapshot()) {
		if m.opsStore() != nil {
			if _, open := m.opsStore().Compensation(name); open {
				// §9's compensation owns this name's convergence only while the
				// commit path has not passed its commit point, and the file still
				// carrying a live intent for the name is exactly that state: the
				// compensation may restore, and finalizing the marker as
				// committed here would run its pinned teardown and write a
				// committed receipt for a commit the compensation is about to
				// undo. An intent already cleared means the commit stands, so the
				// marker finalizes like any other — never deferred behind a
				// record the pipeline below is about to clear without restoring.
				if _, intentLive := fileRecords.PendingStoreSync[name]; intentLive {
					m.logf("boot staged-receipt marker for %q left staged: an open compensation owns its convergence", name)
					continue
				}
				m.logf("boot staged-receipt marker for %q finalized: its compensation's intent is already cleared, so the commit stands", name)
			}
		}
		if _, err := m.finalizeOrphanMarkerIfAny(context.Background(), name, true); err != nil {
			m.logf("boot finalization of the staged receipt marker for %q refused: %v", name, err)
		}
	}
	// The boot's durable prune: expired tombstones, a tombstone a live
	// entry consumed, and any record compaction past its bound land in one
	// atomic write before the hub serves requests (spec §15: "every boot
	// prunes durably in the same atomic-write posture before serving
	// requests"; §6: "every `hub.toml` mutation and every boot compacts
	// markers past either bound in the same atomic write"). A boot with
	// nothing to prune writes nothing, so an untouched file stays
	// byte-identical. A boot without that write may still owe the initial
	// identity records.
	if attempted := m.reconcileBootRecords(hasFile, collided); !attempted {
		m.materializeHostRecords(fileRecords, hasFile)
	}
	// Spec 08b §7's boot passes, in their fixed order, before the
	// manager serves: the tombstone-derived host-removed pass, the bidirectional
	// generation-mirror reconciliation, and the cross-file intent
	// reconciliation (§9).
	m.reconcilePipelineBoot()
}
