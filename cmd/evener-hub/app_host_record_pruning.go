package hub

// This file owns the derivation and pruning every hub.toml write and every
// boot runs over the machine-managed records: the tombstone expiry and
// capacity rules of registry spec 08 §15, and the receipt/marker/audit
// compaction rules of §6/§11. One function — deriveHostTOMLRecords — projects
// the store's record set plus the mutation's staged records into exactly what
// one atomic write carries; a successful write installs that projection into
// the store, so the file and the in-memory maps are always the same set.

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"sort"
	"strings"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// recordsSnapshot is the store's current machine-record set, in the derivation's
// shape: the comparison side of the boot reconciliation and the set a
// successful write installs.
func (s *hostStore) recordsSnapshot() hostTOMLRecords {
	return hostTOMLRecords{
		highWater:      s.highWaterSnapshot(),
		receipts:       s.receiptsSnapshot(),
		tombstones:     s.tombstoneSnapshot(),
		prunedReceipts: s.prunedReceiptSnapshot(),
		remnants:       s.remnantSnapshot(),
		stagedReceipts: s.stagedSnapshot(),
		attempts:       s.attemptsSnapshot(),
		storeSync:      s.storeSyncSnapshot(),
	}
}

// sameHostTOMLRecords reports whether two record sets are the same; a boot
// whose derivation equals the store's writes nothing. Maps and values compare
// deeply: a HostMutationReceipt carries a Roots slice, so equality cannot be a
// plain map comparison.
func sameHostTOMLRecords(a, b hostTOMLRecords) bool {
	return reflect.DeepEqual(nonNilRecords(a), nonNilRecords(b))
}

// nonNilRecords normalizes nil maps so two empty record sets compare equal
// whichever spelling each side carries.
func nonNilRecords(records hostTOMLRecords) hostTOMLRecords {
	if records.highWater == nil {
		records.highWater = map[string]HostGeneration{}
	}
	if records.receipts == nil {
		records.receipts = map[string]HostMutationReceipt{}
	}
	if records.tombstones == nil {
		records.tombstones = map[string]HostTombstone{}
	}
	if records.storeSync == nil {
		records.storeSync = map[string]HostStoreSyncIntent{}
	}
	if records.droppedStoreSync == nil {
		records.droppedStoreSync = map[string]struct{}{}
	}
	if records.raisedHighWater == nil {
		records.raisedHighWater = map[string]struct{}{}
	}
	if records.prunedReceipts == nil {
		records.prunedReceipts = map[string]PrunedReceiptMarker{}
	}
	if records.droppedTombstones == nil {
		records.droppedTombstones = map[string]struct{}{}
	}
	if records.droppedMarkers == nil {
		records.droppedMarkers = map[string]struct{}{}
	}
	if records.droppedReceipts == nil {
		records.droppedReceipts = map[string]struct{}{}
	}
	if records.remnants == nil {
		records.remnants = map[string]HostTeardownRemnant{}
	}
	if records.stagedReceipts == nil {
		records.stagedReceipts = map[string]HostStagedReceipt{}
	}
	if records.attempts == nil {
		records.attempts = map[string]HostTeardownAttempt{}
	}
	if records.droppedRemnants == nil {
		records.droppedRemnants = map[string]struct{}{}
	}
	if records.droppedStaged == nil {
		records.droppedStaged = map[string]struct{}{}
	}
	if records.droppedAttempts == nil {
		records.droppedAttempts = map[string]struct{}{}
	}
	return records
}

// defaultHostRecordPolicy is the one retention policy the host-record
// derivation runs under: the bounds registry spec 08 §6/§11/§15 names. Only the
// tombstone retention is operator-tunable (host_tombstone_retention).
func defaultHostRecordPolicy() hostRecordPolicy {
	const week = 7 * 24 * time.Hour
	return hostRecordPolicy{
		tombstoneRetention:   DefaultHostTombstoneRetention,
		tombstoneMaxRows:     500,
		tombstoneMaxRowBytes: 1 << 20,
		tombstoneMaxCount:    64,
		tombstoneMaxBytes:    16 << 20,
		// The superseded-receipt, pruned-marker, and cleared/recovery-remnant
		// windows match the tombstone retention: a replay past it refuses through
		// the retained pruned marker instead.
		supersededMaxCount: 8,
		supersededTTL:      week,
		prunedMaxCount:     64,
		prunedTTL:          week,
		// The keyless-add audit trail outlives the tombstone window: its purpose
		// is post-hoc forensics rather than outcome recovery.
		auditMaxCount:    64,
		auditTTL:         30 * 24 * time.Hour,
		clearedMaxCount:  64,
		clearedTTL:       week,
		recoveryMaxCount: 64,
		recoveryTTL:      week,
		attemptMaxCount:  8,
		// A local teardown drains well inside two minutes; a stuck remote then
		// surfaces its terminal outcome with a live retry handle instead of
		// holding the host gate.
		teardownTimeout: 2 * time.Minute,
		// An hour lets a transient teardown failure resolve through one retry and
		// still tells the operator about a remnant that is not going away.
		escalationAge: time.Hour,
	}
}

// hostRecordPolicyFor resolves the policy for one WebConfig: the defaults, with
// the operator's tombstone retention when it is positive. A zero WebConfig
// (tests, embedders) gets the shipped numbers.
func hostRecordPolicyFor(cfg hubcore.WebConfig) hostRecordPolicy {
	policy := defaultHostRecordPolicy()
	if cfg.HostTombstoneRetention > 0 {
		policy.tombstoneRetention = cfg.HostTombstoneRetention
	}
	return policy
}

// nowTime is the derivation's clock: the wall clock, unless a test pinned one.
func (m *hubHostManager) nowTime() time.Time {
	if m.cfg.now != nil {
		return m.cfg.now()
	}
	return time.Now()
}

// remnantGated reports whether name holds an open teardown remnant, the fence
// spec 08 §6's retention and §15's capacity rules skip: an open remnant pins
// its name's tombstone until the remnant resolves, so such a tombstone is
// never an expiry or eviction candidate.
func (m *hubHostManager) remnantGated(name string) bool {
	_, open := m.openRemnantID(name)
	return open
}

// hostNameSet returns the names in a host slice.
func hostNameSet(entries []hostreg.Host) map[string]struct{} {
	names := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		names[entry.Name] = struct{}{}
	}
	return names
}

// deriveHostTOMLRecords projects the machine-record set one hub.toml write
// carries, applying every retention rule in the same derivation so no path can
// forget one:
//
//   - re-add purge: a tombstone whose name this write carries live is dropped,
//     and that name's superseded receipts go with it — except the newest
//     same-key superseded `remove` receipt per key, which the purge retains so
//     a lost-response remove retry after the re-add still returns its recorded
//     receipt (§6);
//   - expiry: a tombstone past `removed_at + retention` is dropped, its name's
//     presence epoch advanced and persisted in the high-water entry, and its
//     receipts dropped with bounded pruned markers persisted for them (§15,
//     §6) — never a tombstone whose name holds an open remnant;
//   - capacity: the tombstone set is evicted oldest-first by removal timestamp
//     until it fits the global count and byte caps; remnant-gated tombstones
//     are never candidates, and a set that fits only by evicting one refuses
//     the typed tombstone-capacity error (§15);
//   - receipt compaction: for every live name the write owns, superseded
//     receipts past the count bound or the TTL are dropped (with markers);
//     a tombstoned name's receipts are not dropped before its purge (§6);
//   - marker compaction: markers past the count bound or TTL drop, except a
//     tombstoned name's remove-retry backstop marker (§6);
//   - audit compaction: keyless-add audit records past the count bound or the
//     audit TTL are dropped (§11).
//
// stage, when set, is the tombstone (and its high-water triple) the removal
// this write commits is staging; it is merged before every rule runs, so the
// incoming tombstone is pruned, gated, or evicted exactly like the rest.
// change.receipt, when set, is the receipt the mutation this write commits is
// staging; it enters the receipt set before compaction, so the current-
// generation receipt it pins is never compacted away by its own write.
func (m *hubHostManager) deriveHostTOMLRecords(entries, known []hostreg.Host, change hostPersistChange) (hostTOMLRecords, error) {
	now := m.nowTime()
	policy := m.cfg.policy
	records := hostTOMLRecords{
		highWater:      m.cfg.store.highWaterSnapshot(),
		receipts:       m.cfg.store.receiptsSnapshot(),
		tombstones:     m.cfg.store.tombstoneSnapshot(),
		prunedReceipts: m.cfg.store.prunedReceiptSnapshot(),
		remnants:       m.cfg.store.remnantSnapshot(),
		stagedReceipts: m.cfg.store.stagedSnapshot(),
		attempts:       m.cfg.store.attemptsSnapshot(),
		storeSync:      m.cfg.store.storeSyncSnapshot(),
	}
	if records.highWater == nil {
		records.highWater = map[string]HostGeneration{}
	}
	if records.receipts == nil {
		records.receipts = map[string]HostMutationReceipt{}
	}
	if records.tombstones == nil {
		records.tombstones = map[string]HostTombstone{}
	}
	if records.prunedReceipts == nil {
		records.prunedReceipts = map[string]PrunedReceiptMarker{}
	}
	if records.droppedTombstones == nil {
		records.droppedTombstones = map[string]struct{}{}
	}
	if records.droppedMarkers == nil {
		records.droppedMarkers = map[string]struct{}{}
	}
	if records.droppedReceipts == nil {
		records.droppedReceipts = map[string]struct{}{}
	}
	if records.remnants == nil {
		records.remnants = map[string]HostTeardownRemnant{}
	}
	if records.stagedReceipts == nil {
		records.stagedReceipts = map[string]HostStagedReceipt{}
	}
	if records.attempts == nil {
		records.attempts = map[string]HostTeardownAttempt{}
	}
	if records.storeSync == nil {
		records.storeSync = map[string]HostStoreSyncIntent{}
	}
	if records.droppedStoreSync == nil {
		records.droppedStoreSync = map[string]struct{}{}
	}
	if records.raisedHighWater == nil {
		records.raisedHighWater = map[string]struct{}{}
	}
	if records.droppedRemnants == nil {
		records.droppedRemnants = map[string]struct{}{}
	}
	if records.droppedStaged == nil {
		records.droppedStaged = map[string]struct{}{}
	}
	if records.droppedAttempts == nil {
		records.droppedAttempts = map[string]struct{}{}
	}
	prunedBefore := maps.Clone(records.prunedReceipts)
	receiptsBefore := maps.Clone(records.receipts)
	attemptsBefore := maps.Clone(records.attempts)
	for key, receipt := range change.carryReceipts {
		if _, carried := records.receipts[key]; !carried {
			records.receipts[key] = receipt
		}
	}
	if change.dropReceipt != "" {
		delete(records.receipts, change.dropReceipt)
		records.droppedReceipts[change.dropReceipt] = struct{}{}
	}
	if change.dropMarker != "" {
		delete(records.stagedReceipts, change.dropMarker)
		records.droppedStaged[change.dropMarker] = struct{}{}
	}
	if change.dropTombstone != "" {
		delete(records.tombstones, change.dropTombstone)
		records.droppedTombstones[change.dropTombstone] = struct{}{}
		delete(records.highWater, change.dropTombstone)
	}
	if restore := change.restore; restore != nil {
		name := restore.Tombstone.Name
		records.tombstones[name] = restore.Tombstone
		records.highWater[name] = HostGeneration{
			Generation:    max(restore.Tombstone.Generation, change.restoreSpent.Generation),
			IncarnationID: restore.Tombstone.IncarnationID,
			PresenceEpoch: max(restore.Tombstone.PresenceEpoch, change.restoreSpent.PresenceEpoch),
		}
		for key, receipt := range restore.Receipts {
			if _, held := records.receipts[key]; !held {
				records.receipts[key] = receipt
			}
			// The marker the purge left for a receipt is not needed once the
			// receipt is back.
			if _, marked := records.prunedReceipts[key]; marked {
				delete(records.prunedReceipts, key)
				records.droppedMarkers[key] = struct{}{}
			}
		}
		for id, attempt := range restore.Attempts {
			if _, held := records.attempts[id]; !held {
				records.attempts[id] = attempt
				delete(records.droppedAttempts, id)
			}
		}
		for key, marker := range restore.Pruned {
			if _, held := records.prunedReceipts[key]; !held {
				records.prunedReceipts[key] = marker
				delete(records.droppedMarkers, key)
			}
		}
		for id, remnant := range restore.Remnants {
			if _, held := records.remnants[id]; !held {
				records.remnants[id] = remnant
			}
		}
	}
	if change.restore == nil && change.restoreName != "" {
		// A rolled-back add with no tombstone to give back still spent its
		// counters: an existing mark rises to them.
		if mark, ok := records.highWater[change.restoreName]; ok {
			mark.Generation = max(mark.Generation, change.restoreSpent.Generation)
			mark.PresenceEpoch = max(mark.PresenceEpoch, change.restoreSpent.PresenceEpoch)
			records.highWater[change.restoreName] = mark
		}
	}
	if change.dropStoreSync != "" {
		// The follow-up write that clears a converged intent, and the
		// compensation that drops the intent with its hub.toml restore: the
		// dropped ledger records the name so the writer's preservation rule
		// cannot ride the file's older copy back in for a name this write does
		// not own (an intent keyed by a name neither live nor tombstoned).
		delete(records.storeSync, change.dropStoreSync)
		records.droppedStoreSync[change.dropStoreSync] = struct{}{}
	}
	// The boot mirror pass's raises land after the tombstone stage: a raise is
	// the file's new mark, and the tombstone keeps the identity of the removal
	// its operation records are marked by.
	for name, raised := range change.highWaterRaises {
		records.highWater[name] = raised
		records.raisedHighWater[name] = struct{}{}
	}
	if change.marker != nil {
		records.stagedReceipts[change.marker.Name] = change.marker.Marker
	}
	if change.remnant != nil {
		records.remnants[change.remnant.RemnantID] = change.remnant.Remnant
	}
	if change.resolved != nil {
		records.remnants[change.resolved.RemnantID] = change.resolved.Remnant
	}
	if change.fencedAttempt != nil {
		records.attempts[change.fencedAttempt.AttemptID] = change.fencedAttempt.Attempt
	}
	if change.attempt != nil {
		records.attempts[change.attempt.AttemptID] = change.attempt.Attempt
	}
	if change.tombstone != nil {
		tombstone := change.tombstone.Tombstone
		records.tombstones[tombstone.Name] = tombstone
		records.highWater[tombstone.Name] = HostGeneration{
			Generation:    tombstone.Generation,
			IncarnationID: tombstone.IncarnationID,
			PresenceEpoch: tombstone.PresenceEpoch,
		}
	}
	if change.receipt != nil {
		records.receipts[change.receipt.Key] = change.receipt.Receipt
	}

	live := hostNameSet(entries)
	// protectedKey is the receipt this very write is finalizing: no purge or
	// compaction rule may drop it, or the write's own commit would be durable
	// in the store and absent from the file (and a lost-response replay after
	// a restart would refuse stale-entry instead of returning the recorded
	// outcome).
	protectedKey := ""
	if change.receipt != nil {
		protectedKey = change.receipt.Key
	}
	// protectedMarker is the staged-receipt marker this very write carries: the
	// purge rules below must no more delete it than they may drop the receipt
	// this write finalizes.
	protectedMarker := ""
	if change.marker != nil {
		protectedMarker = change.marker.Name
	}

	// Re-add purge: the write carries the name live, so the tombstone is
	// consumed by the live entry (§6: "a tombstone whose name matches a live
	// host at boot is discarded — the live host wins") and the name's
	// superseded receipts go with it except the retained same-key remove
	// receipts (§6).
	for name := range records.tombstones {
		if _, ok := live[name]; !ok || change.leaveReturningNames {
			continue
		}
		consumed := &HostConsumedRecords{Tombstone: records.tombstones[name]}
		beforeReceipts, beforeRemnants := maps.Clone(records.receipts), maps.Clone(records.remnants)
		delete(records.tombstones, name)
		records.droppedTombstones[name] = struct{}{}
		delete(records.highWater, name)
		m.purgeNameReceipts(records, name, purgeRetainNewestRemove, protectedKey, now)
		// Re-add purges the name's stale remnants and staged markers: spec §6
		// "only after that name's open remnants are resolved ... its stale
		// remnants are dropped in the same atomic write that mints the new
		// generation". The fence refuses a re-add while one is open, so in
		// practice only resolved records are here; an open one is kept either
		// way, because dropping it would strand a live teardown handle.
		m.purgeNameTeardownRecords(records, name, protectedMarker)
		if marker, staging := records.stagedReceipts[name]; staging && name == protectedMarker {
			// The write also stages this re-add: the marker keeps what the purge
			// took, so a re-add that never reaches hub.toml can be rolled back
			// without losing the removed incarnation's history.
			consumed.Receipts = removedEntries(beforeReceipts, records.receipts)
			consumed.Remnants = removedEntries(beforeRemnants, records.remnants)
			marker.Consumed = consumed
			records.stagedReceipts[name] = marker
		}
	}

	// Expiry prune: past the retention period the tombstone is dropped in this
	// very write, its name's presence epoch advanced and persisted in the
	// high-water entry, and its receipts dropped behind bounded markers. An
	// open remnant gates the name out entirely.
	for name, tombstone := range records.tombstones {
		if m.remnantGated(name) {
			continue
		}
		if _, unsettled := records.stagedReceipts[name]; unsettled {
			// Recovery reads this tombstone to classify the staged commit.
			continue
		}
		if !tombstoneExpired(tombstone, now, policy.tombstoneRetention) {
			continue
		}
		advanced, err := m.advancedPresenceEpoch(name, records, tombstone.PresenceEpoch)
		if err != nil {
			return records, err
		}
		// The mark may already sit above the tombstone (a rolled-back re-add's
		// spent counters); expiry never lowers it.
		retained := records.highWater[name]
		records.highWater[name] = HostGeneration{
			Generation:    max(tombstone.Generation, retained.Generation),
			IncarnationID: tombstone.IncarnationID,
			PresenceEpoch: max(advanced, retained.PresenceEpoch),
		}
		delete(records.tombstones, name)
		records.droppedTombstones[name] = struct{}{}
		m.purgeNameReceipts(records, name, purgeDropAll, protectedKey, now)
		m.purgeNameTeardownRecords(records, name, protectedMarker)
	}

	// Receipt compaction for every name the write's receipt set carries — the
	// names this write owns (its entries and its pre-mutation snapshot) and
	// any other name that still has receipts — so "every `hub.toml` mutation
	// ... compacts past either bound" holds for the audit and superseded
	// bounds alike. A tombstoned name's receipts stay readable until its purge
	// (§6), a live name keeps its current-generation receipts plus the bounded
	// newest superseded ones, and keyless audit records compact under their
	// own dual bound.
	owned := hostNameSet(entries)
	for name := range hostNameSet(known) {
		owned[name] = struct{}{}
	}
	for key := range records.receipts {
		if scope, ok := parseHostReceiptScopedKey(key); ok {
			owned[scope.Name] = struct{}{}
		}
	}
	for name := range owned {
		if _, tombstoned := records.tombstones[name]; tombstoned {
			continue
		}
		m.compactNameReceipts(records, name, entries, protectedKey, now)
	}

	if err := m.enforceTombstoneCaps(records, change, now); err != nil {
		return records, err
	}
	// Marker compaction runs last: capacity eviction purges whole names and
	// persists their markers, and those must meet the marker bound in this
	// same atomic write (§6: "every `hub.toml` mutation and every boot
	// compacts markers past either bound in the same atomic write").
	m.compactPrunedMarkers(records, now)
	// The teardown-repair records compact last for the same reason the markers
	// do: a purge above drops whole names' remnants, and those drops must meet
	// their bounds in this same atomic write (§6: every `hub.toml` mutation and
	// every boot compacts past either bound in the same atomic write).
	m.compactTeardownRecords(records, now)
	if marker, staging := records.stagedReceipts[protectedMarker]; staging && marker.Consumed != nil {
		// The re-add this write stages consumed receipts, remnants and their
		// attempts, compacts the name's receipts after the purge, and may have
		// pushed replay guards out of a full marker set doing it; rolling it back gives them all back. What an earlier write of
		// this staging already saved stays saved, and the marker's record is
		// copied before it changes.
		consumed := *marker.Consumed
		consumed.Receipts = mergeEvicted(consumed.Receipts, receiptsOfName(removedEntries(receiptsBefore, records.receipts), protectedMarker))
		consumed.Pruned = mergeEvicted(consumed.Pruned, removedEntries(prunedBefore, records.prunedReceipts))
		consumed.Attempts = mergeEvicted(consumed.Attempts, removedEntries(attemptsBefore, records.attempts))
		marker.Consumed = &consumed
		records.stagedReceipts[protectedMarker] = marker
	}
	return records, nil
}

// receiptsOfName keeps the receipts whose scoped key names the host.
func receiptsOfName(receipts map[string]HostMutationReceipt, name string) map[string]HostMutationReceipt {
	kept := map[string]HostMutationReceipt{}
	for key, receipt := range receipts {
		if scope, ok := parseHostReceiptScopedKey(key); ok && scope.Name == name {
			kept[key] = receipt
		}
	}
	return kept
}

// mergeEvicted returns saved plus evicted as a map of its own, nil when both are
// empty.
func mergeEvicted[V any](saved, evicted map[string]V) map[string]V {
	if len(saved) == 0 && len(evicted) == 0 {
		return nil
	}
	merged := maps.Clone(saved)
	if merged == nil {
		merged = map[string]V{}
	}
	maps.Copy(merged, evicted)
	return merged
}

// purgeNameTeardownRecords drops one name's resolved remnants and every staged
// marker the name holds. An open remnant is never dropped: it is the forward
// repair handle, and dropping it would lose the only way to finish the
// teardown (spec §6: "a remnant never depends on the live registry to execute"
// — but only the record itself can name it).
func (m *hubHostManager) purgeNameTeardownRecords(records hostTOMLRecords, name, protectedMarker string) {
	for id, remnant := range records.remnants {
		if remnant.Host != name || remnant.open() {
			continue
		}
		delete(records.remnants, id)
		records.droppedRemnants[id] = struct{}{}
	}
	for hostName := range records.stagedReceipts {
		if hostName != name {
			continue
		}
		if hostName == protectedMarker {
			// The marker THIS write stages: a re-add's own step-(2) write both
			// stages a marker and runs the re-add purge, and the purge must not
			// delete the record the same write exists to persist — without it a
			// crash before the finalizing receipt leaves a re-add with no durable
			// marker, and a later keyed replay finds no receipt at all. The
			// receipt's `protectedKey` is the same rule.
			continue
		}
		delete(records.stagedReceipts, hostName)
		records.droppedStaged[hostName] = struct{}{}
	}
}

// compactTeardownRecords applies the teardown-repair bounds: cleared-remnant
// markers compact under their own TTL and at-most-64-newest-per-name count
// bound, recovery markers under theirs, and the attempt set drops attempts
// whose remnant is gone and keeps at most attemptMaxCount newest per remnant —
// all in the same atomic write that carries the rest of the derivation (spec
// §6: "every boot and every `hub.toml` mutation compacts retry records past
// either bound in the same atomic write").
func (m *hubHostManager) compactTeardownRecords(records hostTOMLRecords, now time.Time) {
	policy := m.cfg.policy
	type candidate struct {
		id        string
		clearedAt time.Time
	}
	retry := map[string][]candidate{}
	recoveries := map[string][]candidate{}
	for id, remnant := range records.remnants {
		resolved := remnant.Resolved
		if resolved == nil {
			continue
		}
		clearedAt, _ := time.Parse(time.RFC3339, resolved.ClearedAt)
		switch resolved.ResolutionKind {
		case hostRemnantResolutionRetry:
			retry[remnant.Host] = append(retry[remnant.Host], candidate{id: id, clearedAt: clearedAt})
		case hostRemnantResolutionRecover:
			recoveries[remnant.Host] = append(recoveries[remnant.Host], candidate{id: id, clearedAt: clearedAt})
		}
	}
	compact := func(byName map[string][]candidate, maxCount int, ttl time.Duration) {
		for _, candidates := range byName {
			sort.SliceStable(candidates, func(i, j int) bool {
				if !candidates[i].clearedAt.Equal(candidates[j].clearedAt) {
					return candidates[i].clearedAt.After(candidates[j].clearedAt)
				}
				return candidates[i].id < candidates[j].id
			})
			for i, candidate := range candidates {
				tooOld := !candidate.clearedAt.IsZero() && candidate.clearedAt.Add(ttl).Before(now)
				if i < maxCount && !tooOld {
					continue
				}
				delete(records.remnants, candidate.id)
				records.droppedRemnants[candidate.id] = struct{}{}
			}
		}
	}
	compact(retry, policy.clearedMaxCount, policy.clearedTTL)
	compact(recoveries, policy.recoveryMaxCount, policy.recoveryTTL)
	// Attempts belong to a remnant: a dropped remnant takes its attempts with
	// it, and a remnant keeps at most attemptMaxCount newest.
	perRemnant := map[string][]candidate{}
	for id, attempt := range records.attempts {
		if _, alive := records.remnants[attempt.RemnantID]; !alive {
			delete(records.attempts, id)
			records.droppedAttempts[id] = struct{}{}
			continue
		}
		startedAt, _ := time.Parse(time.RFC3339, attempt.StartedAt)
		perRemnant[attempt.RemnantID] = append(perRemnant[attempt.RemnantID], candidate{id: id, clearedAt: startedAt})
	}
	for _, candidates := range perRemnant {
		sort.SliceStable(candidates, func(i, j int) bool {
			if !candidates[i].clearedAt.Equal(candidates[j].clearedAt) {
				return candidates[i].clearedAt.After(candidates[j].clearedAt)
			}
			return candidates[i].id < candidates[j].id
		})
		for i, candidate := range candidates {
			// A still-open attempt is never compacted away: it is the fence the
			// name reads, and dropping it would let a lifecycle path start over
			// possibly-live cleanup (spec §6: "The open attempt record is an
			// attempt fence").
			if records.attempts[candidate.id].open() {
				continue
			}
			if i >= policy.attemptMaxCount {
				delete(records.attempts, candidate.id)
				records.droppedAttempts[candidate.id] = struct{}{}
			}
		}
	}
}

// advancedPresenceEpoch returns name's next presence epoch as of this write:
// strictly above every value the name's records currently carry — the
// tombstone's own epoch, its high-water entry, and the registry's counter —
// and raises the registry's counter to that value before returning, so the
// persisted advance and the live counter cannot disagree.
//
// The raise is what keeps a re-add's mint strictly above the retained mark in
// the SAME process: NextPresenceEpoch is a pure read, so without it the file
// would carry an epoch one above a counter that stayed put, and the next mint
// would land exactly ON the persisted mark — while a restart (whose seed
// raises the counter) would mint one above it, making the durable identity
// outcome restart-dependent (spec §1: "Re-add mints strictly above every
// retained high-water mark for the name").
//
// It runs under the mutation lock like the rest of the derivation. A write
// that then fails leaves the counter raised without the file advancing: that
// is benign and deliberate — the counter is monotonic and never reused, the
// raise is idempotent, and the next boot's seed re-raises it from the file —
// while lowering it to match a failed write is the direction that could let a
// later re-add reuse a value the name already carried.
func (m *hubHostManager) advancedPresenceEpoch(name string, records hostTOMLRecords, tombstoneEpoch uint64) (uint64, error) {
	highest := tombstoneEpoch
	if mark, ok := records.highWater[name]; ok && mark.PresenceEpoch > highest {
		highest = mark.PresenceEpoch
	}
	if m.cfg.hosts != nil {
		if next, err := m.cfg.hosts.NextPresenceEpoch(name); err == nil && next > highest {
			highest = next - 1
		} else if err != nil {
			return 0, fmt.Errorf("prune tombstone %q: %w", name, err)
		}
	}
	advanced := highest + 1
	if m.cfg.hosts != nil {
		// Generation 0 is inert: SeedHighWater raises the name's counter (and
		// would carry a live entry, which a tombstone prune never has) without
		// registering a generation.
		m.cfg.hosts.SeedHighWater(map[string]hostreg.HighWater{name: {PresenceEpoch: advanced}})
	}
	return advanced, nil
}

// purgeMode selects how much of a purged name's receipt history is retained.
type purgeMode int

const (
	// purgeRetainNewestRemove keeps the newest same-key superseded `remove`
	// receipt per (mutationId, kind) key: the re-add purge's retention, which
	// keeps the superseded arm satisfiable for a lost-response remove retry
	// after a re-add (§4/§6).
	purgeRetainNewestRemove purgeMode = iota
	// purgeDropAll drops every receipt of the name: the retention-expiry and
	// capacity-eviction purges, which also persist the bounded markers a
	// post-purge replay refuses on (§6: "The tombstone purge is clean-slate").
	purgeDropAll
)

// purgeNameReceipts applies a tombstone purge's receipt rule to records: the
// name's receipts are partitioned, the retained subset (per mode) is kept, and
// every dropped non-audit receipt persists a pruned marker keyed by its full
// scope, so a same-key replay refuses as `stale-entry` (pruned-generation)
// instead of fresh-applying. prunedAt is the purge instant.
func (m *hubHostManager) purgeNameReceipts(records hostTOMLRecords, name string, mode purgeMode, protectedKey string, now time.Time) {
	byKey := make(map[string]hostReceiptScope)
	for key := range records.receipts {
		scope, ok := parseHostReceiptScopedKey(key)
		if !ok || scope.Name != name {
			continue
		}
		byKey[key] = scope
	}
	if len(byKey) == 0 {
		return
	}
	retained := map[string]struct{}{}
	if mode == purgeRetainNewestRemove {
		// Keyless-add audit records survive a re-add's purge: they carry no
		// client idempotency semantics and their own count/TTL bound is what
		// keeps the audit trail bounded across add/remove churn (§11: "audit
		// records compact under the same dual bound as receipts ... so
		// keyless-add spam against one name cannot grow hub.toml without
		// limit"). A tombstone purge still drops them with the name's other
		// receipts (§6).
		// Group by the key without its generation/incarnation halves and keep
		// the newest remove receipt per group: "that name's newest same-key
		// superseded `remove` receipts, which the purge retains" (§6).
		type group struct {
			generation uint64
			key        string
		}
		newest := map[string]group{}
		var ordered []group
		for key, scope := range byKey {
			if scope.Kind != hostMutationRemove {
				continue
			}
			groupKey := scope.MutationID + "\x00" + string(scope.Kind)
			current, ok := newest[groupKey]
			if !ok || scope.Generation > current.generation ||
				(scope.Generation == current.generation && key < current.key) {
				newest[groupKey] = group{generation: scope.Generation, key: key}
			}
		}
		for _, keep := range newest {
			ordered = append(ordered, keep)
		}
		// Only the bounded newest handful survives: one receipt per remove key
		// would otherwise retain a receipt for every remove in the name's
		// history, growing hub.toml without limit under remove/re-add churn.
		sort.SliceStable(ordered, func(i, j int) bool {
			if ordered[i].generation != ordered[j].generation {
				return ordered[i].generation > ordered[j].generation
			}
			return ordered[i].key < ordered[j].key
		})
		for i, keep := range ordered {
			if i >= m.cfg.policy.supersededMaxCount {
				break
			}
			retained[keep.key] = struct{}{}
		}
	}
	for key := range byKey {
		if _, keep := retained[key]; keep {
			continue
		}
		if key == protectedKey {
			// The receipt this write is finalizing: never dropped, never
			// marked — the committed mutation must leave its receipt behind
			// (spec §5: the commit and its receipt are one atomic write).
			continue
		}
		receipt := records.receipts[key]
		if receipt.Audit {
			if mode == purgeRetainNewestRemove {
				// Kept for the audit trail; compactNameReceipts bounds it.
				continue
			}
			// A tombstone purge drops audit records with the name's other
			// receipts; they carry no client idempotency semantics, so no
			// marker is persisted for them (§11).
			delete(records.receipts, key)
			records.droppedReceipts[key] = struct{}{}
			continue
		}
		delete(records.receipts, key)
		records.droppedReceipts[key] = struct{}{}
		records.prunedReceipts[key] = PrunedReceiptMarker{PrunedAt: hostInstant(now)}
	}
}

// compactNameReceipts applies the live-name receipt bounds to one name's
// receipts: current-generation receipts stay, at most supersededMaxCount newest
// superseded receipts stay (the rest, and any older than the TTL, drop behind
// markers), and keyless audit records compact to the newest auditMaxCount under
// the audit TTL. entries is the write's live set, which defines the name's
// current pair.
func (m *hubHostManager) compactNameReceipts(records hostTOMLRecords, name string, entries []hostreg.Host, protectedKey string, now time.Time) {
	policy := m.cfg.policy
	var current hostMutationIdentity
	currentKnown := false
	for _, entry := range entries {
		if entry.Name == name && entry.Generation != 0 && entry.IncarnationID != "" {
			current = hostMutationIdentity{Generation: entry.Generation, IncarnationID: entry.IncarnationID}
			currentKnown = true
			break
		}
	}
	type candidate struct {
		key       string
		scope     hostReceiptScope
		committed time.Time
		audit     bool
	}
	candidates := make([]candidate, 0)
	for key, receipt := range records.receipts {
		scope, ok := parseHostReceiptScopedKey(key)
		if !ok || scope.Name != name {
			continue
		}
		committed, _ := time.Parse(time.RFC3339, receipt.CommittedAt)
		candidates = append(candidates, candidate{key: key, scope: scope, committed: committed, audit: receipt.Audit})
	}
	if len(candidates) == 0 {
		return
	}
	drop := func(candidate candidate) {
		if candidate.key == protectedKey {
			// The receipt this very write is finalizing is never compacted by
			// its own write: the write's single committed mutation must leave
			// its receipt behind, both in the file and in the store.
			return
		}
		delete(records.receipts, candidate.key)
		records.droppedReceipts[candidate.key] = struct{}{}
		if candidate.audit {
			return
		}
		records.prunedReceipts[candidate.key] = PrunedReceiptMarker{PrunedAt: hostInstant(now)}
	}
	var superseded []candidate
	var audits []candidate
	for _, candidate := range candidates {
		switch {
		case candidate.audit:
			audits = append(audits, candidate)
		case currentKnown && candidate.scope.Generation == current.Generation && candidate.scope.IncarnationID == current.IncarnationID:
			// A current-generation receipt is never compacted: it is the
			// outcome a live replay returns.
		default:
			superseded = append(superseded, candidate)
		}
	}
	// The receipt this write is finalizing ranks first: it is the newest
	// commit by construction, so it counts within its bound and can never be
	// the candidate its own write drops.
	first := func(candidate candidate) bool { return candidate.key == protectedKey }
	// Newest-first by generation, with the scoped key as the total tie-break;
	// everything past the count bound or the TTL drops.
	sort.SliceStable(superseded, func(i, j int) bool {
		if first(superseded[i]) != first(superseded[j]) {
			return first(superseded[i])
		}
		if superseded[i].scope.Generation != superseded[j].scope.Generation {
			return superseded[i].scope.Generation > superseded[j].scope.Generation
		}
		return superseded[i].key < superseded[j].key
	})
	for i, candidate := range superseded {
		tooOld := !candidate.committed.IsZero() && candidate.committed.Add(policy.supersededTTL).Before(now)
		if first(candidate) {
			continue
		}
		if i >= policy.supersededMaxCount || tooOld {
			drop(candidate)
		}
	}
	sort.SliceStable(audits, func(i, j int) bool {
		if first(audits[i]) != first(audits[j]) {
			return first(audits[i])
		}
		if !audits[i].committed.Equal(audits[j].committed) {
			return audits[i].committed.After(audits[j].committed)
		}
		return audits[i].key < audits[j].key
	})
	for i, candidate := range audits {
		tooOld := !candidate.committed.IsZero() && candidate.committed.Add(policy.auditTTL).Before(now)
		if first(candidate) {
			continue
		}
		if i >= policy.auditMaxCount || tooOld {
			drop(candidate)
		}
	}
}

// compactPrunedMarkers bounds the pruned-marker set: for each name, at most
// prunedMaxCount newest markers survive under the marker TTL, except a
// tombstoned name's remove-retry backstop marker — the newest remove-kind
// marker — which the bound never drops while the tombstone lives (§6).
func (m *hubHostManager) compactPrunedMarkers(records hostTOMLRecords, now time.Time) {
	policy := m.cfg.policy
	type candidate struct {
		key      string
		scope    hostReceiptScope
		prunedAt time.Time
	}
	byName := map[string][]candidate{}
	for key, marker := range records.prunedReceipts {
		scope, ok := parseHostReceiptScopedKey(key)
		if !ok {
			continue
		}
		prunedAt, _ := time.Parse(time.RFC3339, marker.PrunedAt)
		byName[scope.Name] = append(byName[scope.Name], candidate{key: key, scope: scope, prunedAt: prunedAt})
	}
	for name, candidates := range byName {
		backstop := ""
		if _, tombstoned := records.tombstones[name]; tombstoned {
			type removeCandidate struct {
				key      string
				prunedAt time.Time
			}
			var removes []removeCandidate
			for _, candidate := range candidates {
				if candidate.scope.Kind == hostMutationRemove {
					removes = append(removes, removeCandidate{key: candidate.key, prunedAt: candidate.prunedAt})
				}
			}
			sort.SliceStable(removes, func(i, j int) bool {
				if !removes[i].prunedAt.Equal(removes[j].prunedAt) {
					return removes[i].prunedAt.After(removes[j].prunedAt)
				}
				return removes[i].key < removes[j].key
			})
			if len(removes) > 0 {
				backstop = removes[0].key
			}
		}
		rest := make([]candidate, 0, len(candidates))
		for _, candidate := range candidates {
			if candidate.key == backstop {
				continue
			}
			rest = append(rest, candidate)
		}
		sort.SliceStable(rest, func(i, j int) bool {
			if !rest[i].prunedAt.Equal(rest[j].prunedAt) {
				return rest[i].prunedAt.After(rest[j].prunedAt)
			}
			return rest[i].key < rest[j].key
		})
		for i, candidate := range rest {
			tooOld := !candidate.prunedAt.IsZero() && candidate.prunedAt.Add(policy.prunedTTL).Before(now)
			if i >= policy.prunedMaxCount || tooOld {
				delete(records.prunedReceipts, candidate.key)
				records.droppedMarkers[candidate.key] = struct{}{}
			}
		}
	}
}

// enforceTombstoneCaps evicts the oldest tombstones until the set fits the
// global count and byte caps, skipping every remnant-gated name, and refuses
// with the typed capacity error when the set still exceeds a bound after every
// evictable candidate is gone. Eviction is a purge: the evicted name's receipts
// drop behind bounded markers, exactly like a retention prune.
func (m *hubHostManager) enforceTombstoneCaps(records hostTOMLRecords, change hostPersistChange, now time.Time) error {
	policy := m.cfg.policy
	incoming := ""
	protectedKey := ""
	if change.tombstone != nil {
		incoming = change.tombstone.Tombstone.Name
	}
	if change.receipt != nil {
		protectedKey = change.receipt.Key
	}
	fits := func() bool {
		return len(records.tombstones) <= policy.tombstoneMaxCount &&
			tombstoneSetBytes(records.tombstones) <= policy.tombstoneMaxBytes
	}
	if fits() {
		return nil
	}
	type evictionCandidate struct {
		name      string
		removedAt time.Time
	}
	var candidates []evictionCandidate
	for name, tombstone := range records.tombstones {
		if name == incoming || m.remnantGated(name) {
			continue
		}
		removedAt, _ := time.Parse(time.RFC3339, tombstone.RemovedAt)
		candidates = append(candidates, evictionCandidate{name: name, removedAt: removedAt})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if !candidates[i].removedAt.Equal(candidates[j].removedAt) {
			return candidates[i].removedAt.Before(candidates[j].removedAt)
		}
		return candidates[i].name < candidates[j].name
	})
	for _, candidate := range candidates {
		if fits() {
			return nil
		}
		delete(records.tombstones, candidate.name)
		records.droppedTombstones[candidate.name] = struct{}{}
		m.purgeNameReceipts(records, candidate.name, purgeDropAll, protectedKey, now)
		m.purgeNameTeardownRecords(records, candidate.name, "")
	}
	if fits() {
		return nil
	}
	// Nothing evictable is left. Name the exceeded bound and the remnant-gated
	// names that blocked the eviction scan (§15/§12).
	bound := tombstoneBoundBytes
	if len(records.tombstones) > policy.tombstoneMaxCount {
		bound = tombstoneBoundCount
	}
	var blocking []string
	for name := range records.tombstones {
		if name == incoming || !m.remnantGated(name) {
			continue
		}
		blocking = append(blocking, name)
	}
	sort.Strings(blocking)
	return &hostTombstoneCapacityError{bound: bound, blockingNames: blocking}
}

// tombstoneCapacityRefusal maps the internal capacity error onto appwire's
// typed `tombstone-capacity` envelope; any other error passes through.
func tombstoneCapacityRefusal(err error) error {
	var capacity *hostTombstoneCapacityError
	if !errors.As(err, &capacity) {
		return err
	}
	return appwire.TombstoneCapacity(capacity.bound, capacity.blockingNames,
		fmt.Sprintf("hub.toml's tombstones would exceed the global %s bound; resolve the open teardown remnants of %s first, then retry",
			capacity.bound, strings.Join(capacity.blockingNames, ", ")))
}

// removedEntries returns the entries of before that after no longer holds.
func removedEntries[V any](before, after map[string]V) map[string]V {
	removed := map[string]V{}
	for key, value := range before {
		if _, kept := after[key]; !kept {
			removed[key] = value
		}
	}
	if len(removed) == 0 {
		return nil
	}
	return removed
}
