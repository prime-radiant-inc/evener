package hub

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

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

// bootRollbackFault, when set, fails the write that rolls back an unlanded
// staged commit at boot. It exists so the failure posture (the commit stays
// staged, never finalized forward) is falsifiable without a store write that
// fails on demand. Nil in production.
var bootRollbackFault func(name string) error

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
		// A configured store that has not opened leaves hub.toml's bytes alone:
		// the hosts serve with what was read, and every settlement or minting
		// write waits for the store, so nothing is normalized before the staged
		// commits there can be compared with the file as they saw it.
		m.bootLegacyRecords(!m.cfg.opsUnavailable)
		if m.cfg.opsUnavailable {
			m.refuseMutations("the operation store is unavailable")
		}
		return
	}
	// Operations a crash left in flight are interrupted before anything else
	// can return early: a settlement that retries later must find them done and
	// leave alone what this process admits in between.
	m.recoverInterruptedOnce(ops)
	if _, _, imported := ops.HostRecords(); imported {
		m.bootStoreRecords(ops)
		return
	}
	if err := m.keepPreRecordsCopy(); err != nil {
		// Everything that follows rewrites hub.toml; none of it starts without
		// the original kept.
		m.bootLegacyRecords(false)
		m.refuseMutations(fmt.Sprintf("the original hub.toml could not be kept before its records move: %v", err))
		return
	}
	if !m.bootLegacyRecords(true) {
		// The records to move could not be read, so what the legacy boot saw is
		// empty. Importing that would make the store their home and strand the
		// real ones in hub.toml; hosts keep serving and the move is retried.
		m.refuseMutations("hub.toml could not be read; its records move into the operation store once it can")
		return
	}
	m.migrateLegacyRecords(ops)
}

// holdLiveHosts takes the gate of every live host and tears down the
// connection of each that holds one, through the SSH manager (the registry
// entries stay as they are), so the steps that
// follow can change registrations (stored incarnations, raised counters, the
// collision rule's rebases) without any channel or supervisor staying attached
// to a registration that moved. The gates stay held until the returned release
// runs; a busy host is not touched: every gate taken is released and the error
// says which, so the settlement stays pending. Outside an attach, or with no
// SSH manager, no host can hold a connection and nothing is held.
func (m *hubHostManager) holdLiveHosts() (func(), error) {
	if m.cfg.manager == nil || !m.cfg.attaching.Load() {
		return func() {}, nil
	}
	var releases []func()
	releaseAll := sync.OnceFunc(func() {
		for _, release := range slices.Backward(releases) {
			release()
		}
	})
	holder := hostops.Holder{Kind: hostops.HolderManager, Activity: "settle-records"}
	for _, entry := range m.cfg.hosts.All() {
		release, err := m.acquireHostGate(entry.Name, holder)
		if err != nil {
			releaseAll()
			return nil, fmt.Errorf("host %q: %w", entry.Name, err)
		}
		releases = append(releases, release)
		if !m.cfg.manager.Attached(entry.Name) {
			continue
		}
		if err := m.cfg.manager.RetireHostUnderGate(entry.Name, holder); err != nil {
			releaseAll()
			return nil, fmt.Errorf("host %q: %w", entry.Name, err)
		}
		m.cfg.reconnectMu.Lock()
		m.cfg.reconnect = append(m.cfg.reconnect, entry.Name)
		m.cfg.reconnectMu.Unlock()
	}
	return releaseAll, nil
}

// attachOps brings the records to the state a boot would have, after the hub
// started serving without that state. store is a store that opened since, or
// nil to retry settling the records of the one already attached. Host
// mutations are refused until this returns clean, so none is in flight.
func (m *hubHostManager) attachOps(store *hostops.Store) {
	attaching := &hostRefusal{reason: "the operation store is attaching"}
	m.cfg.refusal.Store(attaching)
	m.cfg.attaching.Store(true)
	defer m.cfg.attaching.Store(false)
	if store != nil && m.opsStore() == nil {
		// Operations a crash left in flight are interrupted before the store is
		// published, while nothing of this process can be running on it.
		m.recoverInterruptedOnce(store)
		// Operations admitted from the moment the store is published read the
		// teardown fences, so the fences it holds are installed first.
		if family, _, imported := store.HostRecords(); imported {
			remnants, attempts := decodeTeardownFences(family)
			m.cfg.store.setRemnantMaps(m.cfg.store.stagedSnapshot(), remnants, attempts)
		}
		m.cfg.ops.Store(store)
		if m.testOnlyAfterPublish != nil {
			m.testOnlyAfterPublish()
		}
	}
	m.bootRecords()
	// A boot that could not settle the records left its own refusal in place.
	m.cfg.refusal.CompareAndSwap(attaching, nil)
	m.reconnectRetiredHosts()
}

// hostReconnectTimeout bounds one dial of a host whose connection an attach
// retired.
const hostReconnectTimeout = 2 * time.Minute

// reconnectRetiredHosts dials again the hosts whose connections the attach
// tore down, once their gates are free: sessions of those hosts are read through
// attached connections only, so without this they would stay unreachable until
// someone connected each host by hand.
func (m *hubHostManager) reconnectRetiredHosts() {
	m.cfg.reconnectMu.Lock()
	names := m.cfg.reconnect
	m.cfg.reconnect = nil
	m.cfg.reconnectMu.Unlock()
	for _, name := range names {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), hostReconnectTimeout)
			defer cancel()
			if _, err := m.cfg.manager.Ensure(ctx, name); err != nil {
				m.logf("host %q was not reconnected after the records settled: %v", name, err)
			}
		}()
	}
}

// mirrorCountersAbove returns the counters the store's mirror holds for names
// that are not live and that the legacy records either lack or carry a lower
// generation for. The
// import replaces the mirror with what the family says, so a generation or
// presence epoch left out would be forgotten and could be minted again.
func (m *hubHostManager) mirrorCountersAbove(ops *hostops.Store, entries []hostreg.Host) map[string]HostGeneration {
	live := hostNameSet(entries)
	held := m.cfg.store.highWaterSnapshot()
	raises := map[string]HostGeneration{}
	for name, boundary := range ops.Boundaries() {
		if _, isLive := live[name]; isLive {
			continue
		}
		mark, ok := held[name]
		if !ok {
			raises[name] = HostGeneration{Generation: boundary.Generation, IncarnationID: boundary.IncarnationID, PresenceEpoch: boundary.PresenceEpoch}
			continue
		}
		if boundary.Generation > mark.Generation {
			// A mark that is a tombstone's twin keeps the incarnation and epoch
			// the pair agrees on; only the spent generation moves up.
			mark.Generation = boundary.Generation
			raises[name] = mark
		}
	}
	return raises
}

// migrateLegacyRecords moves the records the legacy boot just settled from
// hub.toml into the operation store.
func (m *hubHostManager) migrateLegacyRecords(ops *hostops.Store) {
	if len(ops.Compensations()) != 0 || len(m.cfg.store.storeSyncSnapshot()) != 0 {
		m.refuseMutations("an earlier removal's cross-file work is still open; it is retried on the next attempt")
		return
	}
	entries := m.cfg.store.snapshot()
	records, err := m.deriveHostTOMLRecords(entries, entries, hostPersistChange{highWaterRaises: m.mirrorCountersAbove(ops, entries)})
	if err != nil {
		m.refuseMutations(fmt.Sprintf("the existing records could not be prepared for the move: %v", err))
		return
	}
	set := recordSetFor(entries, records)
	for name, mark := range records.highWater {
		m.cfg.hosts.SeedHighWater(map[string]hostreg.HighWater{name: {Generation: mark.Generation, PresenceEpoch: mark.PresenceEpoch}})
	}
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
func (m *hubHostManager) keepPreRecordsCopy() error {
	path := m.cfg.configPath
	if path == "" {
		return nil
	}
	raw, holdsRecords := originalHoldingRecordSections(path)
	if !holdsRecords {
		return nil
	}
	return keepPreRecordsCopy(path, raw)
}

// originalHoldingRecordSections returns hub.toml's bytes when the file reads and
// carries record sections, the only original worth keeping. An unreadable file
// is not one: the legacy boot's own readability check stops the move.
func originalHoldingRecordSections(path string) ([]byte, bool) {
	doc, raw, err := readHubTOMLDocument(path)
	if err != nil {
		return nil, false
	}
	return raw, len(legacyRecordSections(doc)) != 0
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
	if info, err := os.Stat(copyPath); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s exists and is not a regular file", copyPath)
		}
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
		family, _, _ := ops.HostRecords()
		remnants, attempts := decodeTeardownFences(family)
		m.cfg.store.setRemnantMaps(map[string]HostStagedReceipt{}, remnants, attempts)
		m.refuseMutations(fmt.Sprintf("the host records in the operation store are in a shape this build cannot read, so a build that can read them has to run: %v", err))
		return
	}
	// The legacy sections go first: the unlanded-removal check below reads
	// hub.toml as the commit saw it.
	m.stripLegacySections(true)
	releaseHosts, err := m.holdLiveHosts()
	if err != nil {
		m.cfg.store.set(m.cfg.hosts.All())
		m.refuseMutations(fmt.Sprintf("a host is busy; the settlement of its stored identity is retried: %v", err))
		return
	}
	defer releaseHosts()
	for _, entry := range m.cfg.hosts.All() {
		// A hub that started before its records were readable minted ids of its
		// own; operations and cleanup handles are bound to the stored ones.
		stored, held := records.HostRecords[entry.Name]
		if !held {
			stored, _ = unlandedRemovalIdentity(records, entry.Name, m.cfg.configPath)
		}
		if validIncarnationID(stored.IncarnationID) && stored.IncarnationID != entry.IncarnationID {
			if err := m.cfg.hosts.RestoreIncarnation(entry.Name, stored.IncarnationID); err != nil {
				m.logf("host %q keeps the incarnation minted at start: %v", entry.Name, err)
			}
		}
	}
	m.installRestoredRecords(hostRecordDocument{Hosts: m.cfg.hosts.All(), Records: records})
	// Seeding the stored marks raised the registry's counters past the entries
	// just copied; the manager's view follows what the registry serves.
	m.cfg.store.set(m.cfg.hosts.All())
	doc, hasFile := store.load()
	settle := hasFile
	if !hasFile && strings.TrimSpace(m.cfg.configPath) != "" {
		if !m.hostFileAbsentOrEmpty() {
			// A file that cannot be read says nothing about which commits
			// landed: none is rolled back or finalized until it can be.
			m.refuseMutations(m.cfg.configPath + " could not be read; staged commits are settled once it can")
			return
		}
		// No hub.toml yet is an empty host set: a first add that staged in the
		// store and crashed before creating the file never landed.
		doc, settle = hostRecordDocument{Records: records}, true
	}
	if settle {
		if unrolled := m.rollBackUnlandedCommits(doc); len(unrolled) != 0 {
			// Every later step reads a staged commit as one hub.toml holds: a
			// removal's tombstone would read as a re-add, and its marker would
			// be pruned or finalized. Hosts keep serving from hub.toml and the
			// whole boot runs again on the next attempt.
			m.refuseMutations(fmt.Sprintf("%d staged commit(s) could not be rolled back; the next attempt retries", len(unrolled)))
			return
		}
	}
	// The rollback changed the records; the rest of the boot reads them as they
	// are now.
	records, err = store.records()
	if err != nil {
		m.refuseMutations(fmt.Sprintf("the host records in the operation store cannot be read: %v", err))
		return
	}
	collided := m.breakBootTombstoneCollision(records)
	releaseHosts()
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
// It returns the names whose rollback did not land: those markers must stay out
// of forward finalization, which would commit a change hub.toml does not hold.
func (m *hubHostManager) rollBackUnlandedCommits(doc hostRecordDocument) map[string]struct{} {
	failed := map[string]struct{}{}
	entries := m.cfg.store.snapshot()
	for _, name := range sortedStagedMarkerNames(m.cfg.store.stagedSnapshot()) {
		marker := m.cfg.store.stagedSnapshot()[name]
		if markerLanded(marker, doc.Hosts) {
			continue
		}
		if scope, ok := parseHostReceiptScopedKey(marker.Key); ok && scope.Kind == hostMutationRemove && !stagedRemovalNeverLanded(marker, m.cfg.configPath) {
			// The host is configured, but hub.toml changed since the removal
			// staged against it: the removal landed and the name was added again.
			continue
		}
		if bootRollbackFault != nil {
			if err := bootRollbackFault(name); err != nil {
				m.logf("boot rollback of the staged commit for %q not written: %v", name, err)
				failed[name] = struct{}{}
				continue
			}
		}
		change := hostPersistChange{dropMarker: name, dropReceipt: marker.Key, recordsOnly: true}
		if scope, ok := parseHostReceiptScopedKey(marker.Key); ok && scope.Kind == hostMutationRemove {
			// The host was never removed: its tombstone goes with the rollback.
			change.dropTombstone = name
		}
		if consumed := marker.Consumed; consumed != nil {
			// The re-add's staging write purged the removed incarnation's
			// history; the host change never landed, so the removal stands, and
			// the generation the re-add spent stays spent.
			restore := *consumed
			restore.Tombstone.Generation = max(restore.Tombstone.Generation, marker.Provisional.Generation)
			if spent, held := m.cfg.store.highWaterSnapshot()[name]; held {
				// The staged re-add minted a presence epoch too; it stays spent.
				restore.Tombstone.Generation = max(restore.Tombstone.Generation, spent.Generation)
				restore.Tombstone.PresenceEpoch = max(restore.Tombstone.PresenceEpoch, spent.PresenceEpoch)
			}
			change.restore = &restore
		}
		// A name that still holds a removal tombstone is the collision rule's to
		// settle; keeping it out of this write keeps its tombstone beside no
		// live entry, which the records refuse.
		tombstoned := m.cfg.store.tombstoneSnapshot()
		writable := slices.DeleteFunc(slices.Clone(entries), func(entry hostreg.Host) bool {
			_, held := tombstoned[entry.Name]
			return held && entry.Name != change.dropTombstone
		})
		if err := m.persistHosts(writable, writable, change); err != nil {
			m.logf("boot rollback of the staged commit for %q not written: %v", name, err)
			failed[name] = struct{}{}
			continue
		}
		m.logf("boot rolled back the staged commit for %q: its host change never reached %s", name, m.cfg.configPath)
	}
	return failed
}

// markerLanded reports whether hub.toml holds the host change a staged marker's
// commit made: the host present as the commit staged it for an add or an edit,
// absent for a removal, or a marker whose runtime swap already started.
func markerLanded(marker HostStagedReceipt, hosts []hostreg.Host) bool {
	scope, ok := parseHostReceiptScopedKey(marker.Key)
	if !ok {
		return true
	}
	if marker.SwapStarted || marker.TeardownStarted || marker.Phase == hostStagedPhaseRuntimeSwapped {
		// The runtime swap starts only after the host change is in hub.toml, so
		// a later edit of the file does not un-land the commit.
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

// recoverInterruptedOnce moves the operations a crash left in flight to
// interrupted, the first time it runs over the attached store. A settlement
// retry runs the boot again over a store that is serving, where a running
// operation is live and must stay so; a pass that fails is therefore not
// repeated in this process either, and the next start makes it again.
func (m *hubHostManager) recoverInterruptedOnce(ops *hostops.Store) {
	if !m.cfg.interruptedHandled.CompareAndSwap(false, true) {
		return
	}
	if moved, err := ops.RecoverInterrupted(); err != nil {
		m.logf("boot interrupted transition not completed; the next start retries it: %v", err)
	} else if moved > 0 {
		m.logf("boot moved %d in-flight operation(s) to interrupted", moved)
	}
}

// reconcileStoreBoot runs the boot passes that depend on the records rather
// than on how they were stored: in-flight operations a crash left running are
// interrupted, removed hosts' operation records and token rows are marked
// and dropped, and the boundary mirror is rebuilt from the records.
func (m *hubHostManager) reconcileStoreBoot() {
	m.recoverInterruptedOnce(m.opsStore())
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
//
// With settle false it only loads what the file holds into the manager and
// writes nothing. It reports whether the records were actually read: a file that is absent or
// empty holds none, and one that cannot be read leaves the boot looking at an
// empty set that nothing may be built from.
func (m *hubHostManager) bootLegacyRecords(settle bool) bool {
	hosts := m.cfg.hosts
	// The file's records are read once, before anything else can mint: its
	// retained marks seed the counters, so a host folded in below mints above
	// every mark the file carries, and any live host the file carries no
	// identity for gets its pair recorded in the same boot. Seeding runs before
	// the store takes its snapshot, so the entries the snapshot carries are the
	// seeded ones — a registry built before the marks were known would otherwise
	// record its pre-seed epochs.
	loaded, hasFile := m.recordStore().load()
	readable := hasFile || strings.TrimSpace(m.cfg.configPath) == "" || m.hostFileAbsentOrEmpty()
	fileRecords := loaded.Records
	releaseHosts := func() {}
	if settle {
		// Seeding and the collision rule below change live registrations.
		var err error
		if releaseHosts, err = m.holdLiveHosts(); err != nil {
			m.refuseMutations(fmt.Sprintf("a host is busy; the settlement of its records is retried: %v", err))
			return false
		}
		defer releaseHosts()
	}
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
	if !settle {
		return readable
	}
	collided := false
	if hasFile {
		collided = m.breakBootTombstoneCollision(fileRecords)
	}
	releaseHosts()
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
	return readable
}
