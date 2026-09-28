package hub

// This file owns the durable removed-host tombstones registry spec 08 §15
// defines: the [tombstones."<name>"] records hub.toml carries beside its
// [[hosts]] array, the retained last-known-good row projection they bound, the
// global caps and retention that prune them, and the record-derivation every
// hub.toml write and every boot runs so the tombstone set, its receipts, its
// pruned markers, and its generation high-water marks stay bounded and
// consistent in one atomic write.
//
// A tombstone is what a successful evener/host/remove leaves behind: the
// removed entry's effective HostConfig (so `list` can render the removed row
// without a live entry), the removed incarnation's identity triple, the
// presence epoch the removal advanced to, the removal instant, and the
// bounded newest-first projection of the source's last-known-good rows. It is
// purged by a re-add or by the retention period (spec §15), and capacity
// eviction drops the oldest tombstones before either global cap can be
// exceeded — never a remnant-gated one (S12 wires the real remnant records;
// today the gate is a documented empty seam).

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// tombstoneBounds names the two global bounds a tombstone-capacity refusal
// reports (spec §12's `{bound: string, blockingNames: string[]}`): appwire's
// carrier constants are the wire vocabulary.
const (
	tombstoneBoundCount = appwire.TombstoneCapacityBoundCount
	tombstoneBoundBytes = appwire.TombstoneCapacityBoundBytes
)

// HostTombstone is one [tombstones."<name>"] record (spec §6's reserved-key
// layout, §15's record): the durable removed-host record a successful removal
// writes in the same atomic hub.toml write that deletes the live entry.
//
// Field spellings are the stored snake_case spelling (spec §6). Entry is the
// removed entry's effective HostConfig in the file's own entry shape, so a
// rewrite round-trips the eight configured fields exactly as [[hosts]] does.
// The identity triple (generation, incarnation_id, presence_epoch) is the
// removed incarnation's pair with the presence epoch the removal advanced to;
// it mirrors the name's [generations."<name>"] high-water triple and boot
// validates the two agree. Rows is the bounded projection: each element the
// JSON encoding of one last-known-good appwire.Thread, newest-first by the
// row's UpdatedAt with a (UpdatedAt, row id) total tie-break, truncated to the
// per-tombstone row-count and serialized-byte bounds. RowsTruncated is the
// explicit indicator that the projection dropped rows at the bound (spec §15:
// "plus an explicit `rowsTruncated: bool` on the tombstone").
type HostTombstone struct {
	Name          string     `toml:"name"`
	Entry         HostConfig `toml:"entry"`
	Origin        string     `toml:"origin"`
	Generation    uint64     `toml:"generation"`
	IncarnationID string     `toml:"incarnation_id"`
	PresenceEpoch uint64     `toml:"presence_epoch"`
	RemovedAt     string     `toml:"removed_at"`
	Rows          []string   `toml:"rows,omitempty"`
	RowsTruncated bool       `toml:"rows_truncated"`
}

// PrunedReceiptMarker is one [pruned_receipts."<scoped-key>"] record (spec §6):
// the bounded marker a count/TTL compaction or a tombstone purge persists when
// it drops a superseded receipt. Markers carry the scoped key only — no row
// bytes (spec §6: "markers carry the scoped key only, no row bytes, so each
// stays small") — and PrunedAt is the UTC RFC3339 instant the drop landed. A
// replay naming a marked key with no retained receipt refuses as the typed
// `stale-entry` (pruned-generation) instead of fresh-applying.
type PrunedReceiptMarker struct {
	PrunedAt string `toml:"pruned_at"`
}

// hostRecordPolicy is the resolved knob set one write's record derivation
// runs under: the owner-adjustable retention and bound knobs with their
// documented defaults applied. A zero hubcore.WebConfig resolves to the
// shipped defaults, so tests and embedders get the spec's numbers.
type hostRecordPolicy struct {
	tombstoneRetention   time.Duration
	tombstoneMaxRows     int
	tombstoneMaxRowBytes int64
	tombstoneMaxCount    int
	tombstoneMaxBytes    int64
	supersededMaxCount   int
	supersededTTL        time.Duration
	prunedMaxCount       int
	prunedTTL            time.Duration
	auditMaxCount        int
	auditTTL             time.Duration
	// The teardown-repair bounds (spec §6/§11): the cleared-remnant marker
	// bounds, the recovery-marker bounds, the attempt-history bound per remnant,
	// and the retry's bounded execution deadline.
	clearedMaxCount  int
	clearedTTL       time.Duration
	recoveryMaxCount int
	recoveryTTL      time.Duration
	attemptMaxCount  int
	teardownTimeout  time.Duration
	escalationAge    time.Duration
}

// hostTOMLRecords is the machine-record set one hub.toml write carries: the
// four reserved sections this build writes and prunes. Derivation and
// installation share it, so the file's records and the in-memory store cannot
// be projected by two rules that drift apart.
type hostTOMLRecords struct {
	highWater      map[string]HostGeneration
	receipts       map[string]HostMutationReceipt
	tombstones     map[string]HostTombstone
	prunedReceipts map[string]PrunedReceiptMarker
	// droppedTombstones and droppedMarkers name the records this derivation
	// pruned (expiry, capacity eviction, compaction). The writer's reserved-key
	// preservation rule re-emits every record for a name the write does not
	// own, and a pruned record's name is not carried by the write — so without
	// these sets the file's own copy would ride back in and the prune would
	// never land on disk.
	droppedTombstones map[string]struct{}
	droppedMarkers    map[string]struct{}
	// droppedReceipts names the receipt keys this derivation dropped
	// (compaction, a re-add purge, a tombstone purge). Receipts for a name the
	// write does not own are preserved verbatim from the file, so without this
	// set a dropped receipt's older file copy would ride back in and a restart
	// would serve it as the recorded outcome.
	droppedReceipts map[string]struct{}
	// remnants, stagedReceipts, and attempts are the teardown-repair record sets
	// (registry spec 08 §5/§6; app_host_remnants.go): the open remnants whose
	// fence the derivation honours, the resolved-remnant markers it bounds, the
	// staged-receipt markers a commit's step-(2) write carries, and the attempt
	// records a claim writes.
	remnants       map[string]HostTeardownRemnant
	stagedReceipts map[string]HostStagedReceipt
	attempts       map[string]HostTeardownAttempt
	// droppedRemnants, droppedStaged, and droppedAttempts name the
	// teardown-repair records this derivation dropped (a re-add purge, a
	// tombstone purge, or a bounded compaction), so the writer's preservation
	// rule cannot ride the file's older copy back in.
	droppedRemnants map[string]struct{}
	droppedStaged   map[string]struct{}
	droppedAttempts map[string]struct{}
	// storeSync is the cross-file commit intent set (deploy-pipeline spec 08b
	// §9) the file carries, keyed by host name: the exact store rows a commit's
	// swap is deleting, plus the hub.toml generation the intent belongs to. A
	// removal's staged write merges its own intent in; the writer preserves
	// unowned names' intents verbatim.
	storeSync map[string]HostStoreSyncIntent
	// droppedStoreSync names the intents this derivation dropped. The writer's
	// preservation rule re-emits an intent for a name the write does not own, so
	// without this set an intent for a name outside the live set (a crash
	// window's, whose name is neither live nor tombstoned) could never clear:
	// the file's older copy would ride back in on every boot.
	droppedStoreSync map[string]struct{}
	// raisedHighWater names the marks this derivation raised (the boot mirror
	// pass's discarded generations). The writer's preservation rule re-emits a
	// generation record for a name the write does not own, so without this set
	// the file's older record would ride back over the raise — and for a
	// tombstoned name the twin pair would then disagree and refuse the write.
	raisedHighWater map[string]struct{}
}

// hostTombstoneStage is the tombstone a removal stages into the very write
// that commits it: the record exists on disk and in the store only if the
// write returns success, which is why the store does not hold it before the
// write — the derivation merges it in, and only a successful write installs
// the derived set.
type hostTombstoneStage struct {
	Tombstone HostTombstone
}

// hostTombstoneCapacityError is the internal shape of spec §15's typed
// `tombstone-capacity` conflict refusal: the global bound the persist would
// exceed and the remnant-gated names that blocked every eviction candidate.
// persistHostsMarked maps it onto appwire's envelope at the single write choke
// point, so every mutation path surfaces the typed refusal.
type hostTombstoneCapacityError struct {
	bound         string
	blockingNames []string
}

func (e *hostTombstoneCapacityError) Error() string {
	return fmt.Sprintf("tombstone persist would exceed the global %s bound and every eviction candidate is remnant-gated (%s)",
		e.bound, strings.Join(e.blockingNames, ", "))
}

// tombstoneRowID is the row's identity half of the projection's total tie-break
// (spec §15: "(lastUpdated, row id) lexicographic"): the same row id the tree
// uses for a remote thread — its ID, else its session id.
func tombstoneRowID(thread appwire.Thread) string {
	if thread.ID != "" {
		return thread.ID
	}
	return thread.SessionID
}

// projectTombstoneRows bounds one tombstone's retained rows: rows are
// serialized as one JSON document each, sorted newest-first by UpdatedAt with
// the (UpdatedAt, row id) lexicographic tie-break (a missing timestamp is 0 and
// therefore sorts oldest, so it never outranks a present one), then truncated
// past whichever bound hits first — at most maxRows rows, at most maxBytes of
// serialized row bytes. The sort is over the projection itself, never the
// snapshot's row order (which is tree order, not chronological). truncated
// reports whether any row was dropped, which is exactly what rowsTruncated
// surfaces.
func projectTombstoneRows(rows []appwire.Thread, maxRows int, maxBytes int64) (projected []string, truncated bool) {
	type candidate struct {
		rowID    string
		updated  int64
		serialed string
	}
	candidates := make([]candidate, 0, len(rows))
	for _, row := range rows {
		encoded, err := json.Marshal(row)
		if err != nil {
			// A row the JSON encoder cannot render is not retainable; the
			// projection drops it and reports truncation rather than storing a
			// half-record the boot load would refuse.
			truncated = true
			continue
		}
		candidates = append(candidates, candidate{rowID: tombstoneRowID(row), updated: row.UpdatedAt, serialed: string(encoded)})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].updated != candidates[j].updated {
			return candidates[i].updated > candidates[j].updated
		}
		return candidates[i].rowID < candidates[j].rowID
	})
	projected = make([]string, 0, len(candidates))
	var bytes int64
	for _, c := range candidates {
		if maxRows > 0 && len(projected) >= maxRows {
			truncated = true
			break
		}
		if maxBytes > 0 && bytes+int64(len(c.serialed)) > maxBytes {
			truncated = true
			break
		}
		projected = append(projected, c.serialed)
		bytes += int64(len(c.serialed))
	}
	return projected, truncated
}

// decodeTombstoneRows decodes a tombstone's retained projection back into the
// wire rows the tree merge and the list count consume. A row this build cannot
// decode is a validation failure, never a silently dropped row: the boot load
// refuses the file loudly (spec §6's reserved-namespace rule).
func decodeTombstoneRows(t HostTombstone) ([]appwire.Thread, error) {
	rows := make([]appwire.Thread, 0, len(t.Rows))
	for i, raw := range t.Rows {
		var row appwire.Thread
		if err := json.Unmarshal([]byte(raw), &row); err != nil {
			return nil, fmt.Errorf("tombstones[%q] row %d is not a decodable retained row: %w", t.Name, i, err)
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// tombstoneEntry converts the tombstone's stored HostConfig back into the
// registry entry shape the row renderers and validation share.
func tombstoneEntry(t HostTombstone) hostreg.Host {
	return hostreg.Host{
		Name:          t.Name,
		SSH:           t.Entry.SSH,
		User:          t.Entry.User,
		EvenerPath:    t.Entry.EvenerPath,
		ConfigPath:    t.Entry.ConfigPath,
		Addr:          t.Entry.Addr,
		Roots:         append([]string(nil), t.Entry.Roots...),
		KeyPath:       t.Entry.KeyPath,
		Generation:    t.Generation,
		IncarnationID: t.IncarnationID,
		PresenceEpoch: t.PresenceEpoch,
	}
}

// newHostTombstone builds the tombstone a removal commits: the removed entry's
// effective configuration and identity triple, the removal instant, and the
// bounded projection of the source's last-known-good rows.
func newHostTombstone(entry hostreg.Host, presenceEpoch uint64, rows []appwire.Thread, removedAt time.Time, policy hostRecordPolicy) HostTombstone {
	projected, truncated := projectTombstoneRows(rows, policy.tombstoneMaxRows, policy.tombstoneMaxRowBytes)
	return HostTombstone{
		Name: entry.Name,
		Entry: HostConfig{
			Name:       entry.Name,
			SSH:        entry.SSH,
			User:       entry.User,
			EvenerPath: entry.EvenerPath,
			ConfigPath: entry.ConfigPath,
			Addr:       entry.Addr,
			Roots:      append([]string(nil), entry.Roots...),
			KeyPath:    entry.KeyPath,
		},
		Origin:        hostOriginHubTOML,
		Generation:    entry.Generation,
		IncarnationID: entry.IncarnationID,
		PresenceEpoch: presenceEpoch,
		RemovedAt:     removedAt.UTC().Format(time.RFC3339),
		Rows:          projected,
		RowsTruncated: truncated,
	}
}

// validateHostTombstones checks the tombstones a hub.toml document carries
// against the record shape this build writes: the record's name agrees with
// its key, its effective entry validates under the same component-03 rules
// every host entry validates by, its identity triple is complete and agrees
// with the name's [generations] high-water triple, removed_at is an RFC3339
// instant, and every retained row decodes. The reserved namespace refuses "a
// reserved value whose shape this build cannot decode" loudly before any
// rewrite (spec §6), and boot's tombstone posture is the same hard startup
// error.
func validateHostTombstones(tombstones map[string]HostTombstone, generations map[string]HostGeneration) error {
	for name, tombstone := range tombstones {
		if tombstone.Name != name {
			return fmt.Errorf("tombstones[%q] names %q in its record", name, tombstone.Name)
		}
		if tombstone.Entry.Name != name {
			// The nested effective-entry name is part of the record shape this
			// build writes (it always equals the key): a hand-edited or
			// corrupted record whose entry names something else — or nothing —
			// is refused loudly rather than round-tripping a shape the hub can
			// never produce, which would leave two disagreeing names in the
			// durable file for every later reader.
			return fmt.Errorf("tombstones[%q] entry names %q in its record", name, tombstone.Entry.Name)
		}
		if err := validateHostEntry(tombstoneEntry(tombstone)); err != nil {
			return fmt.Errorf("tombstones[%q] carries an invalid entry: %w", name, err)
		}
		if tombstone.Origin != hostOriginHubTOML {
			return fmt.Errorf("tombstones[%q] carries origin %q, which this build cannot produce or decode", name, tombstone.Origin)
		}
		if tombstone.Generation == 0 {
			return fmt.Errorf("tombstones[%q] carries no generation", name)
		}
		if !validIncarnationID(tombstone.IncarnationID) {
			return fmt.Errorf("tombstones[%q] carries an incarnation id of %d bytes, over the %d-byte bound (or not valid UTF-8)",
				name, len(tombstone.IncarnationID), hostops.MaxIncarnationIDBytes)
		}
		if tombstone.PresenceEpoch == 0 {
			return fmt.Errorf("tombstones[%q] carries no presence epoch", name)
		}
		if _, err := time.Parse(time.RFC3339, tombstone.RemovedAt); err != nil {
			return fmt.Errorf("tombstones[%q] carries removed_at %q, not an RFC3339 instant: %w", name, tombstone.RemovedAt, err)
		}
		if _, err := decodeTombstoneRows(tombstone); err != nil {
			return err
		}
		mark, ok := generations[name]
		if !ok {
			// The tombstone and its [generations] high-water twin are one
			// record pair every writer of this build emits together: the twin
			// is what seeds the boot counters, so a twinless tombstone would
			// let a re-add mint at or below the retained mark and adopt the
			// removed history (spec §1: "Re-add mints strictly above every
			// retained high-water mark for the name"). A file carrying one
			// without the other is not a shape this build writes — refuse it
			// loudly, the reserved-namespace posture.
			return fmt.Errorf("tombstones[%q] carries no generations[%q] high-water twin; refusing the unresolvable record", name, name)
		}
		if mark.Generation != tombstone.Generation || mark.IncarnationID != tombstone.IncarnationID || mark.PresenceEpoch != tombstone.PresenceEpoch {
			return fmt.Errorf("tombstones[%q] carries the triple (%d, %q, %d) while generations carries (%d, %q, %d); the two must agree",
				name, tombstone.Generation, tombstone.IncarnationID, tombstone.PresenceEpoch,
				mark.Generation, mark.IncarnationID, mark.PresenceEpoch)
		}
	}
	return nil
}

// validatePrunedReceipts checks the pruned markers a hub.toml document carries:
// every key is a canonical five-part scoped key this build writes, and
// pruned_at is an RFC3339 instant. A marker whose key this build cannot decode
// is refused loudly, exactly like a receipt.
func validatePrunedReceipts(markers map[string]PrunedReceiptMarker) error {
	for key, marker := range markers {
		if _, ok := parseHostReceiptScopedKey(key); !ok {
			return fmt.Errorf("pruned_receipts[%q] is not a canonical five-part scoped key this build writes (mutation id / name / kind / generation / incarnation id)", key)
		}
		if _, err := time.Parse(time.RFC3339, marker.PrunedAt); err != nil {
			return fmt.Errorf("pruned_receipts[%q] carries pruned_at %q, not an RFC3339 instant: %w", key, marker.PrunedAt, err)
		}
	}
	return nil
}

// tombstoneSerializedBytes is one tombstone's serialized size for the global
// byte cap: the TOML encoding of the record, which is what the file actually
// grows by. A record that cannot be encoded cannot be written at all, so the
// caller has already refused it; the fallback sum keeps this total.
func tombstoneSerializedBytes(tombstone HostTombstone) int64 {
	encoded, err := tomlMarshalTombstone(tombstone)
	if err != nil {
		return int64(len(tombstone.Name) + len(tombstone.RemovedAt))
	}
	return int64(len(encoded))
}

// tombstoneSetBytes sums the serialized sizes of one tombstone set.
func tombstoneSetBytes(tombstones map[string]HostTombstone) int64 {
	var total int64
	for _, tombstone := range tombstones {
		total += tombstoneSerializedBytes(tombstone)
	}
	return total
}

// tombstoneExpired reports whether tombstone's removal instant plus the
// retention period has passed at now.
func tombstoneExpired(tombstone HostTombstone, now time.Time, retention time.Duration) bool {
	removedAt, err := time.Parse(time.RFC3339, tombstone.RemovedAt)
	if err != nil {
		// An unparsable instant cannot exist in a loaded file (validation
		// refuses it); treat it as not-expired so nothing is pruned by a value
		// this build could not read.
		return false
	}
	return !now.Before(removedAt.Add(retention))
}

// retainedTombstoneRows returns the rows a tombstone's projection holds, for
// the tree merge. A projection this build cannot decode cannot exist in a
// loaded file, so the error is only reachable on a hand-built record; the
// merge treats it as no rows rather than refusing a tree read.
func retainedTombstoneRows(tombstone HostTombstone) []appwire.Thread {
	rows, err := decodeTombstoneRows(tombstone)
	if err != nil {
		return nil
	}
	return rows
}

// tombstoneRow renders one tombstone as the `list` row spec §4/§11 define:
// `removed: true` plus the retained-row count, from the tombstone's retained
// effective HostConfig with `attached: false`, `midEnsure: false`, and the
// removed entry's origin, generation, and incarnation id; the facts/error
// optionals stay absent (never null). RowsTruncated is present only as true.
func tombstoneRow(tombstone HostTombstone) appwire.HostRow {
	row := hostEntryRow(tombstoneEntry(tombstone))
	row.Removed = true
	retained := len(tombstone.Rows)
	row.RetainedRows = &retained
	row.RowsTruncated = tombstone.RowsTruncated
	return row
}

// retainedTombstoneSources returns the per-source row sets the tree merge
// re-applies for every tombstone the read path still renders: one entry per
// unexpired tombstone, plus a remnant-gated one kept past its retention. Each
// entry's rows are tagged with the tombstone's name as their Source and the
// entry is marked Tombstoned, so the tree's live predicate can force them
// non-live without consulting the source registry (whose fail-open on an
// unknown ID is exactly the promotion §15 forbids). Nil when nothing is
// visible. It takes only the store's own mutex: a tree read must not take the
// mutation lock.
func (m *hubHostManager) retainedTombstoneSources() map[string]hubcore.RemoteSourceSnapshot {
	tombstones := m.cfg.store.tombstoneSnapshot()
	if len(tombstones) == 0 {
		return nil
	}
	now := m.nowTime()
	out := make(map[string]hubcore.RemoteSourceSnapshot, len(tombstones))
	for name, tombstone := range tombstones {
		if !m.remnantGated(name) && tombstoneExpired(tombstone, now, m.cfg.policy.tombstoneRetention) {
			continue
		}
		rows := retainedTombstoneRows(tombstone)
		for i := range rows {
			// The tag: every tombstone-sourced row names its tombstone, whatever
			// the captured row's own source spelling was.
			rows[i].Source = name
		}
		out[name] = hubcore.RemoteSourceSnapshot{
			Threads:       rows,
			Complete:      false,
			Tombstoned:    true,
			RowsTruncated: tombstone.RowsTruncated,
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// tomlMarshalTombstone encodes one tombstone record the way the hub.toml writer
// does (its own table), so the global byte cap measures the file's actual
// growth. The encoder is the same TOML library the writer uses.
func tomlMarshalTombstone(tombstone HostTombstone) ([]byte, error) {
	var buf strings.Builder
	encoder := toml.NewEncoder(&buf)
	doc := map[string]any{"tombstones": map[string]HostTombstone{tombstone.Name: tombstone}}
	if err := encoder.Encode(doc); err != nil {
		return nil, err
	}
	return []byte(buf.String()), nil
}
