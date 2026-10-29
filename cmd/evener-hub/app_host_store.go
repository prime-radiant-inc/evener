package hub

import (
	"maps"
	"sync"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// pendingHostReceipt is one receipt a single hub.toml write carries beyond the
// store's own set: the mutation the write is committing names it, and the
// store adopts it only once the write returned success (persistOrCompensate).
type pendingHostReceipt struct {
	Key     string
	Receipt HostMutationReceipt
}

// hostPersistChange is the set of machine records one hub.toml write stages
// beyond the store's own set: the mutation's receipt and, for a removal, the
// tombstone the write persists. The derivation merges both into the record set
// it writes, and a successful write installs the derived set — so a failed
// write, and a compensation behind it, leave the store exactly as it was.
type hostPersistChange struct {
	receipt   *pendingHostReceipt
	tombstone *hostTombstoneStage
	// marker, remnant, resolved, and attempt are the teardown-repair records
	// one write stages beyond the store's own set: the staged-receipt marker a
	// commit's step-(2) write carries and its flip writes move, the durable
	// remnant a committed-with-teardown-failure writes in the write that
	// finalizes its receipt, the typed resolved record a clearance writes, and
	// the attempt record a `teardown-retry`/`teardown-recover` claim writes.
	// Like the receipt and the tombstone they are merged by the derivation and
	// installed only by a successful write, so a failed write and the
	// compensation behind it leave the store exactly as it was.
	marker        *pendingHostMarker
	remnant       *pendingHostRemnant
	resolved      *pendingHostRemnant
	attempt       *pendingHostAttempt
	fencedAttempt *pendingHostAttempt
	// carryReceipts are receipts this write carries beyond the store's own set
	// and its own commit's: the collision write adopts the file's bytes when a
	// foreign edit won the race, and must adopt that file's records with it
	// rather than drop them as an owned name's superseded history.
	carryReceipts map[string]HostMutationReceipt
	// dropReceipt names the scoped receipt key this write removes: a
	// compensation must take the provisional receipt its own staged write
	// installed with it, or an un-committed mutation leaves a receipt behind
	// that a later replay reads as a recorded outcome (S10's un-commit paths
	// dropped it the same way).
	dropReceipt string
	// dropMarker names a host whose staged-receipt marker this write removes:
	// the post-commit write that replaces the marker with the finalized receipt
	// is a write that carries no marker for its host, and the derivation must
	// drop the stored one rather than preserve it.
	dropMarker string
	// dropTombstone names a host whose removal tombstone this write removes:
	// the rollback of a removal that never reached hub.toml, which leaves the
	// host live.
	dropTombstone string
	// recordsOnly commits the machine records without rewriting the host list in
	// hub.toml: boot recovery that settles records must not write the registry's
	// idea of the hosts over what the file holds now.
	recordsOnly bool
	// inMemory installs the derived records in the manager without writing any
	// file: the settlement of a late attach over records that are about to move
	// into the store, where a write of the host list would only put the
	// registry's startup list over what the operator has since changed.
	inMemory bool
	// leaveReturningNames keeps the tombstone of a removed name that is live
	// again, with its history, for the boot collision rule to settle; the
	// re-add purge that would consume it is skipped.
	leaveReturningNames bool
	// restore puts back the history a re-add's staging write purged, when the
	// re-add is rolled back.
	restore *HostConsumedRecords
	// storeSync, when set, is the cross-file commit intent this write carries
	// (deploy-pipeline 08b §9): a removal's staged write stages the exact store
	// rows it is about to purge. dropStoreSync names a host whose intent this
	// write removes — the follow-up write that clears it once the purge landed,
	// and the compensation that drops it with the restored hub.toml.
	storeSync     *pendingHostStoreSync
	dropStoreSync string
	// highWaterRaises carries the boot mirror pass's raised marks: the
	// discarded generations §4 preserves as the names' high-water marks. The
	// derivation merges them after the tombstone stage so a raise wins over the
	// file's older mark, and a tombstoned name's tombstone is raised with its
	// [generations] twin (validateHostTombstones requires the pair to agree).
	highWaterRaises map[string]HostGeneration
}

// pendingHostStoreSync is one cross-file intent a hub.toml write carries,
// keyed by the host name the intent is keyed by.
type pendingHostStoreSync struct {
	Name   string
	Intent HostStoreSyncIntent
}

// pendingHostMarker is the staged-receipt marker one hub.toml write carries,
// keyed by the host name the marker map is keyed by.
type pendingHostMarker struct {
	Name   string
	Marker HostStagedReceipt
}

// pendingHostRemnant is one teardown-remnant record (open or resolved) a
// hub.toml write carries.
type pendingHostRemnant struct {
	RemnantID string
	Remnant   HostTeardownRemnant
}

// pendingHostAttempt is one teardown-attempt record a hub.toml write carries.
type pendingHostAttempt struct {
	AttemptID string
	Attempt   HostTeardownAttempt
}

// hostStore is the durable host set: every live entry, in the order a rewrite
// writes it — the boot set in the registry's own (name-sorted) order, then
// entries in add order, with an edit replacing in place. The file's host order
// is the hub's, not the operator's: the machine-managed banner says formatting
// does not survive, and a rewrite normalizes a hand-authored out-of-order file.
// The zero value is usable; all methods are safe for concurrent use. Callers that also
// mutate the registry hold hostManagerConfig.mu across both, so the two cannot
// drift apart under concurrency.
type hostStore struct {
	mu      sync.Mutex
	entries []hostreg.Host
	// highWater is the per-name generation high-water record the file carries
	// for names with no live entry: a removal records the removed incarnation's
	// (generation, incarnation id, presence epoch) triple with the epoch the
	// removal advanced to, and it stays until the name's history is pruned
	// (retention belongs to a later slice). A live name's record is always
	// derived from its entry, so an entry here for a live name is inert. The
	// boot load seeds it from the file, so a removal that lands before the next
	// start keeps its high-water record.
	highWater map[string]HostGeneration
	// receipts is the durable mutation-receipt set the file carries, keyed by
	// the five-part scoped key (spec 08 §5/§6; app_host_receipts.go). Boot
	// loads it from the file's [mutation_receipts] tables, a committed
	// mutation adds its own under the mutation mutex, and a mutation that
	// un-commits drops the one its commit wrote — so the map never names a
	// mutation whose commit did not stand. Unlike highWater the map is not
	// keyed by name alone: one name's receipts span its generations, and the
	// scoped key is what dedup matches.
	receipts map[string]HostMutationReceipt
	// tombstones is the durable removed-host set the file carries, keyed by
	// name (spec 08 §15; app_host_tombstones.go). Boot loads it from the
	// file's [tombstones] tables (validated there), a removal's commit
	// installs the tombstone its atomic write persisted, and a re-add or a
	// retention/capacity purge removes it — so the map never names a tombstone
	// the file does not carry.
	tombstones map[string]HostTombstone
	// prunedReceipts is the durable pruned-marker set the file carries, keyed
	// by the full five-part scoped receipt key (spec 08 §6): the bounded proof
	// that a superseded receipt was compacted away, so a replay naming the
	// marked key refuses `stale-entry` instead of fresh-applying.
	prunedReceipts map[string]PrunedReceiptMarker
	// stagedReceipts is the durable staged-receipt marker set the file carries,
	// keyed by host name (spec 08 §5; app_host_remnants.go): at most one staged
	// entry per host, so a staged marker for host A never blocks a mutation on
	// host B. A commit's step-(2) write installs its own marker here, the flip
	// writes move it, and the post-commit write replaces it with the finalized
	// receipt.
	stagedReceipts map[string]HostStagedReceipt
	// remnants is the durable teardown-remnant set the file carries, keyed by
	// the server-generated remnantId (spec 08 §6; app_host_remnants.go). An open
	// remnant fences its host name for every lifecycle and attach path; a
	// cleared one stays as the typed resolved record the `already-cleared` and
	// `recovered-cleared` replays render.
	remnants map[string]HostTeardownRemnant
	// attempts is the durable teardown-attempt set the file carries, keyed by
	// the server-generated attempt id: the claim `teardown-retry` and
	// `teardown-recover` write in the same atomic hub.toml write that claims the
	// remnant, and the fence a timed-out run leaves standing.
	attempts map[string]HostTeardownAttempt
	// storeSync is the durable cross-file commit intent set the file carries,
	// keyed by host name (deploy-pipeline spec 08b §9): the exact store rows a
	// removal's swap is deleting, plus the hub.toml generation the intent
	// belongs to. The commit's staged write installs it, the store purge applies
	// it after the swap, and a follow-up write drops it.
	storeSync map[string]HostStoreSyncIntent
}

// set installs entries as the store's contents; the constructor calls it once
// with the registry's boot set. The caller transfers ownership of the slice —
// the boot set is freshly built and held nowhere else — so set adopts it.
func (s *hostStore) set(entries []hostreg.Host) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = entries
}

// setHighWaterMap installs the file's high-water records at boot; the
// constructor calls it once with the records the file carried. The caller
// transfers ownership.
func (s *hostStore) setHighWaterMap(marks map[string]HostGeneration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.highWater = marks
}

// highWaterSnapshot returns copies of the retained high-water records, keyed by
// name. Callers hold hostManagerConfig.mu.
func (s *hostStore) highWaterSnapshot() map[string]HostGeneration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.highWater)
}

// setReceipts installs the file's receipt set at boot; the constructor calls it
// once with the records the file carried. The caller transfers ownership.
func (s *hostStore) setReceipts(receipts map[string]HostMutationReceipt) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.receipts = receipts
}

// addReceipt records one committed mutation's receipt. It is called only after
// the atomic write that persisted the commit returned success, so the map never
// names a receipt the file does not carry. Callers hold hostManagerConfig.mu.
func (s *hostStore) addReceipt(key string, receipt HostMutationReceipt) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.receipts == nil {
		s.receipts = map[string]HostMutationReceipt{}
	}
	s.receipts[key] = receipt
}

// receiptsSnapshot returns a copy of the stored receipt set, keyed by scoped
// key. It takes only the store's own mutex, so the dedup read can run before
// the host gate and the mutation lock (§5 orders dedup first); a writer's
// read-modify-write is serialized by the mutation mutex it already holds.
func (s *hostStore) receiptsSnapshot() map[string]HostMutationReceipt {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.receipts)
}

// setRecordMaps installs the file's tombstone and pruned-marker sets at boot;
// the constructor calls it once with the records the file carried. The caller
// transfers ownership.
func (s *hostStore) setRecordMaps(tombstones map[string]HostTombstone, prunedReceipts map[string]PrunedReceiptMarker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tombstones = tombstones
	s.prunedReceipts = prunedReceipts
}

// setStoreSync installs the file's cross-file intent set at boot; the
// constructor calls it once with the intents the file carried. The caller
// transfers ownership.
func (s *hostStore) setStoreSync(intents map[string]HostStoreSyncIntent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.storeSync = intents
}

// storeSyncSnapshot returns a copy of the stored cross-file intent set, keyed
// by host name.
func (s *hostStore) storeSyncSnapshot() map[string]HostStoreSyncIntent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.storeSync)
}

// tombstoneSnapshot returns a copy of the stored tombstone set, keyed by name.
// It takes only the store's own mutex: `list`'s in-memory expiry filter and
// the tree merge read it without the mutation lock (spec 08 §4: "list takes no
// mutation lock").
func (s *hostStore) tombstoneSnapshot() map[string]HostTombstone {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.tombstones)
}

// entryByName returns the store's live entry for name, if it holds one.
func (s *hostStore) entryByName(name string) (hostreg.Host, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, entry := range s.entries {
		if entry.Name == name {
			return entry, true
		}
	}
	return hostreg.Host{}, false
}

// hasTombstone reports whether name is present solely as a tombstone — the
// re-add condition an `add` checks under the mutation mutex before its write
// purges the tombstone.
func (s *hostStore) hasTombstone(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.tombstones[name]
	return ok
}

// prunedReceiptSnapshot returns a copy of the stored pruned-marker set, keyed
// by the full scoped receipt key.
func (s *hostStore) prunedReceiptSnapshot() map[string]PrunedReceiptMarker {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.prunedReceipts)
}

// installRecords replaces the store's machine-record maps with the set one
// successful hub.toml write persisted. The derivation and the write ran
// together, so installing the same projection keeps the file and the store the
// same set; a failed write installs nothing. The tombstone and marker maps are
// always installed whole (the derivation owns them), while the high-water and
// receipt maps are installed only when non-nil so a caller cannot silently
// blank a section it did not derive. Callers hold hostManagerConfig.mu.
func (s *hostStore) installRecords(records hostTOMLRecords) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tombstones = records.tombstones
	s.prunedReceipts = records.prunedReceipts
	s.stagedReceipts = records.stagedReceipts
	s.remnants = records.remnants
	s.attempts = records.attempts
	s.storeSync = records.storeSync
	if records.highWater != nil {
		s.highWater = records.highWater
	}
	if records.receipts != nil {
		s.receipts = records.receipts
	}
}

// add inserts entry at the end. Callers hold hostManagerConfig.mu.
func (s *hostStore) add(entry hostreg.Host) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, entry)
}

// remove deletes name, reporting whether it was present. Removed stays
// removed: nothing is retained, so a later add of the same name starts clean.
// Callers hold hostManagerConfig.mu.
func (s *hostStore) remove(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, e := range s.entries {
		if e.Name == name {
			s.entries = append(s.entries[:i], s.entries[i+1:]...)
			return true
		}
	}
	return false
}

// snapshot returns entries in add order; the slice is a copy, and it is never
// nil — not even for an empty store. nil is the writer's exact-write sentinel
// ("drop nothing back"), so an empty store that handed back nil would disable
// file-entry preservation, and the first Add on a host-less hub would silently
// drop an operator's hand-added entry. Callers hold hostManagerConfig.mu.
func (s *hostStore) snapshot() []hostreg.Host {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]hostreg.Host, 0, len(s.entries))
	return append(out, s.entries...)
}

// without returns a copy of the entries minus name, in add order — the
// snapshot a removal persists before it mutates anything. Callers hold
// hostManagerConfig.mu.
func (s *hostStore) without(name string) []hostreg.Host {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]hostreg.Host, 0, len(s.entries))
	for _, e := range s.entries {
		if e.Name != name {
			out = append(out, e)
		}
	}
	return out
}

// replace swaps entry in for the entry already stored under entry.Name, in
// place, so an edit is a minimal change to the write order rather than a
// reordering nothing asked for. Callers hold hostManagerConfig.mu.
func (s *hostStore) replace(entry hostreg.Host) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = replaceEntry(s.entries, entry)
}

// withReplaced returns the entries a durable replace would write: the stored
// entries with entry swapped in for the same-name one, in place. Callers hold
// hostManagerConfig.mu.
func (s *hostStore) withReplaced(entry hostreg.Host) []hostreg.Host {
	return replaceEntry(s.snapshot(), entry)
}

// replaceEntry swaps entry in for the same-name entry, in place, appending when
// the name is absent — unreachable on the update path, whose commit checks
// liveness first, but keeping this helper total rather than silently dropping an
// edit.
func replaceEntry(entries []hostreg.Host, entry hostreg.Host) []hostreg.Host {
	for i, e := range entries {
		if e.Name == entry.Name {
			entries[i] = entry
			return entries
		}
	}
	return append(entries, entry)
}
