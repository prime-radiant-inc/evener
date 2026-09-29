package hub

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"unicode/utf8"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostfence"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// This file holds the machine-managed per-host records hub.toml carries beside
// its [[hosts]] array (registry spec 08 §6's reserved-key layout):
//
//	[host_records."<name>"]  the live per-host record: the incarnation id and
//	                         the presence epoch. Records here must survive a
//	                         host edit, which is why they are not fields of the
//	                         [[hosts]] entry.
//	[generations."<name>"]   the per-name generation high-water mark, persisted
//	                         as the (generation, incarnationId, presenceEpoch)
//	                         triple (spec §1). It outlives the live entry: a
//	                         removal keeps the removed incarnation's triple, so
//	                         a re-add can never reuse it.
//
// Both records are written by the same atomic hub.toml write that persists the
// host change (spec §1: the presence epoch "advances ... in that same atomic
// write"; §15: "Every add/re-add mints a fresh incarnation id in that same
// atomic write").

// HostRecord is one [host_records."<name>"] table: the live per-host machine
// record. The zero value is the absence default — a host the file carries no
// record for — and it is deliberately unmintable: no writer of these records
// emits an empty incarnation id or a zero presence epoch, so an unrecorded host
// can never read as a recorded one (spec §6; the compatibility rule the boot
// load applies).
type HostRecord struct {
	// IncarnationID is the name's live incarnation id (spec §1): minted on
	// every add/re-add, never reused.
	IncarnationID string `toml:"incarnation_id"`
	// PresenceEpoch is the name's presence counter (spec §1): advanced on
	// every add, remove, re-add and expiry purge.
	PresenceEpoch uint64 `toml:"presence_epoch"`
	// BootstrapAttempted is crash-fencing §6:133's durable bootstrap-attempt
	// fence: a first-contact delivery started, written in its own atomic
	// hub.toml write before the attempt's first remote side effect. It is never
	// cleared — a host carrying it without HelperInstalled takes the fenced path
	// on every later attempt (§6:137).
	BootstrapAttempted bool `toml:"bootstrap_attempted,omitempty"`
	// HelperInstalled is §6:137's converged flag: the pinned fencing helper was
	// delivered to the host and the finalizing atomic write recorded it.
	HelperInstalled bool `toml:"helper_installed,omitempty"`
	// HelperVersion is §6:131's "prior helper version record": the helper
	// version the finalize recorded. Zero means no version record. It is present
	// exactly with HelperInstalled.
	HelperVersion uint64 `toml:"helper_version,omitempty"`
	// BootstrapEpochBoot and BootstrapEpochOpSeq are the fencing epoch the
	// attempt fence was minted under (crash-fencing §6:131, "under the worker's
	// persisted epoch"), stored in the same shape the teardown attempts use.
	// §6:139's recovery re-probe names the crashed attempt by this epoch after
	// any restart; a fence without it cannot identify the crashed attempt.
	BootstrapEpochBoot  string `toml:"bootstrap_epoch_boot,omitempty"`
	BootstrapEpochOpSeq uint64 `toml:"bootstrap_epoch_op_seq,omitempty"`
	// BootstrapAttemptToken is the durable ownership token the fence write minted
	// for the attempt that won it (crash-fencing §6:135). Delivery revalidates it
	// immediately before its first delivery step, and recovery retires it on the
	// verified-gone path, so a paused owner cannot deliver after recovery
	// concluded while a fresh attempt can never win the fence again.
	BootstrapAttemptToken string `toml:"bootstrap_attempt_token,omitempty"`
}

// provisioning projects the record's bootstrap half (crash-fencing §6) into the
// fencing package's shape. It is the one conversion between the file's flat
// record fields and the bootstrap rule.
func (r HostRecord) provisioning() hostfence.Provisioning {
	return hostfence.Provisioning{
		AttemptFenced:   r.BootstrapAttempted,
		AttemptEpoch:    hostfence.Epoch{BootID: r.BootstrapEpochBoot, OpSeq: r.BootstrapEpochOpSeq},
		AttemptToken:    r.BootstrapAttemptToken,
		HelperInstalled: r.HelperInstalled,
		HelperVersion:   r.HelperVersion,
	}
}

// withProvisioning returns r carrying p's bootstrap flags, leaving the identity
// fields alone: the record machinery owns the identity, the bootstrap flow owns
// the flags.
func (r HostRecord) withProvisioning(p hostfence.Provisioning) HostRecord {
	r.BootstrapAttempted = p.AttemptFenced
	r.BootstrapEpochBoot = p.AttemptEpoch.BootID
	r.BootstrapEpochOpSeq = p.AttemptEpoch.OpSeq
	r.BootstrapAttemptToken = p.AttemptToken
	r.HelperInstalled = p.HelperInstalled
	r.HelperVersion = p.HelperVersion
	return r
}

// HostGeneration is one [generations."<name>"] table: the per-name generation
// high-water mark, persisted as the (generation, incarnationId, presenceEpoch)
// triple (spec §1). For a live name it mirrors the entry's current triple; for
// a removed name it is the removed incarnation's triple with the presence epoch
// the removal advanced to, and it survives until the name's history is pruned
// (the retention slices own that).
type HostGeneration struct {
	// Generation is the greatest generation the name has carried.
	Generation uint64 `toml:"generation"`
	// IncarnationID is the incarnation that generation belongs to.
	IncarnationID string `toml:"incarnation_id"`
	// PresenceEpoch is the presence epoch recorded with that generation.
	PresenceEpoch uint64 `toml:"presence_epoch"`
}

// hostRecordFor derives name's live record from a registry entry.
func hostRecordFor(entry hostreg.Host) HostRecord {
	return HostRecord{IncarnationID: entry.IncarnationID, PresenceEpoch: entry.PresenceEpoch}
}

// completeIdentity reports whether an entry carries the whole persisted
// identity triple. Every live entry this hub mints does; the check keeps a
// fixture or a hand-built entry from contributing a partial record — the
// writer emits all three values or none, so the file never carries a record
// its own load would refuse.
func completeIdentity(entry hostreg.Host) bool {
	return entry.Generation != 0 && entry.IncarnationID != "" && entry.PresenceEpoch != 0
}

// validIncarnationID reports whether an incarnation id is one every layer of
// this series accepts: non-empty, valid UTF-8, and within spec 08 §1's bound —
// the same bound the operation store's boundary schema enforces, so a value the
// file accepts can never make the mirror's batch write refuse.
func validIncarnationID(id string) bool {
	return id != "" && len(id) <= hostops.MaxIncarnationIDBytes && utf8.ValidString(id)
}

// complete reports whether the record carries an identity: the zero value is
// the absence default (a host the file carries no record for), and no writer of
// these records emits a partial one.
func (r HostRecord) complete() bool {
	return validIncarnationID(r.IncarnationID) && r.PresenceEpoch != 0
}

// complete reports whether the high-water triple carries an identity. Its
// shape is the three-field triple, so a missing any field is not a mark.
func (g HostGeneration) complete() bool {
	return g.Generation != 0 && validIncarnationID(g.IncarnationID) && g.PresenceEpoch != 0
}

// stampedEntry applies a stamped identity to entry, the shape the durable-first
// mutation paths persist before they apply the change live.
func stampedEntry(entry hostreg.Host, identity hostreg.Identity) hostreg.Host {
	entry.Generation = identity.Generation
	entry.IncarnationID = identity.IncarnationID
	entry.PresenceEpoch = identity.PresenceEpoch
	return entry
}

// hostGenerationFor derives name's high-water triple from a registry entry.
func hostGenerationFor(entry hostreg.Host) HostGeneration {
	return HostGeneration{
		Generation:    entry.Generation,
		IncarnationID: entry.IncarnationID,
		PresenceEpoch: entry.PresenceEpoch,
	}
}

// hostProvisioningRecords projects the file's per-host records onto the
// bootstrap record set the store carries (crash-fencing §6:131-137). A name
// whose record carries no flag contributes the zero value, so the store's set
// is exactly the file's flags.
func hostProvisioningRecords(records map[string]HostRecord) map[string]hostfence.Provisioning {
	provisioning := make(map[string]hostfence.Provisioning, len(records))
	for name, record := range records {
		provisioning[name] = record.provisioning()
	}
	return provisioning
}

// resolveHostIdentity maps the records a hub.toml file carries onto the
// registry entry for one live host: the generation the file persisted (the boot
// load restores it; spec §15) and the live record's incarnation id and presence
// epoch. The [generations] record is a *high-water* mark, not a live identity:
// it restores the generation and seeds the counters (hostHighWaterMarks), but
// its incarnation id and epoch describe a removed incarnation and must never be
// inherited by a live name. A name with no live record is a new incarnation —
// spec §15: the boot "mints a fresh incarnation id for every hub.toml host name
// with no persisted incarnation" — minted at load, with its epoch advanced past
// whatever the mark retained.
func resolveHostIdentity(name string, records map[string]HostRecord, generations map[string]HostGeneration) (generation uint64, incarnationID string, presenceEpoch uint64) {
	record := records[name]
	return generations[name].Generation, record.IncarnationID, record.PresenceEpoch
}

// hostHighWaterMarks maps the file's [generations] records onto the registry's
// high-water marks: the counters a boot seeds before anything mints.
func hostHighWaterMarks(cfg Config) map[string]hostreg.HighWater {
	marks := make(map[string]hostreg.HighWater, len(cfg.Generations))
	for name, mark := range cfg.Generations {
		marks[name] = hostreg.HighWater{Generation: mark.Generation, PresenceEpoch: mark.PresenceEpoch}
	}
	return marks
}

// hostFileRecordSet derives the machine records a hub.toml write carries: every
// live entry's own triple, plus every retained high-water record whose name has
// no live entry (a removal's record). It is the one derivation both the writer
// and the boundary mirror read, so the file's records and the mirrored triples
// cannot be derived by two rules that drift apart. Entries carrying no complete
// identity (a direct writer call's fixture, never the manager's own live set,
// which is always minted) contribute nothing: the file records identities, and
// a zero one is the absence default, not a registration.
func hostFileRecordSet(entries []hostreg.Host, highWater map[string]HostGeneration) (map[string]HostRecord, map[string]HostGeneration) {
	records := make(map[string]HostRecord, len(entries))
	generations := make(map[string]HostGeneration, len(entries)+len(highWater))
	live := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		live[entry.Name] = struct{}{}
		if !completeIdentity(entry) {
			continue
		}
		records[entry.Name] = hostRecordFor(entry)
		generations[entry.Name] = hostGenerationFor(entry)
	}
	for name, mark := range highWater {
		if _, ok := live[name]; ok {
			// A live name's record is derived from its entry: a stale mark left
			// by a rolled-back removal must not outrank the live triple.
			continue
		}
		if !mark.complete() {
			continue
		}
		generations[name] = mark
	}
	return records, generations
}

// decodeHubTOMLForRewrite decodes the bytes a rewrite is about to replace. An
// absent file decodes as an empty Config (the rewrite creates it); anything
// else must decode through the loader's own rules, so a rewrite can only ever
// be derived from a file the hub could read at boot.
func decodeHubTOMLForRewrite(path string, raw []byte) (Config, error) {
	if len(raw) == 0 {
		return Config{}, nil
	}
	cfg, err := decodeConfig(path, string(raw))
	if err != nil {
		return Config{}, fmt.Errorf("hub.toml rewrite refused: %w", err)
	}
	return cfg, nil
}

// fileOnlyHostEntries returns the host entries cfg holds that neither known nor
// entries names, in file order — the entries a rewrite carries through because
// the mutation does not own them. known is the store's pre-mutation snapshot,
// which is what distinguishes a host the operator hand-added to hub.toml while
// the hub was running (absent from known, preserved) from a host this very
// mutation is removing (present in known, not preserved).
func fileOnlyHostEntries(cfg Config, known, entries []hostreg.Host) []HostConfig {
	// A name the write itself carries is never an extra: appending it would
	// write the same name twice and the round-trip would refuse the file.
	carried := make(map[string]struct{}, len(known)+len(entries))
	for _, e := range known {
		carried[e.Name] = struct{}{}
	}
	for _, e := range entries {
		carried[e.Name] = struct{}{}
	}
	var extra []HostConfig
	for _, h := range cfg.Hosts {
		if _, ok := carried[h.Name]; !ok {
			extra = append(extra, h)
		}
	}
	return extra
}

// hubTOMLRecordTables derives the machine-record tables a rewrite writes:
// hostFileRecordSet over the entries and high-water records the write carries,
// plus the preserved records described below. The write owns the records of the
// names it carries and its pre-mutation snapshot names — those are exactly the
// names whose records the mutation may change — while every other record the
// file holds is carried through, exactly as fileOnlyHostEntries carries a
// hand-added entry. (A field inside a record that this build does not decode is
// not carried: the reserved namespace's unknown-field refusal is the section-
// ownership slice's to add, alongside the other reserved records.) A name in
// known that this write no longer carries a live entry for and that has no
// high-water record loses its records: that is what a removal looks like before
// its high-water record is written, and keeping them would leave a live record
// for a name with no live entry.
//
// A nil known is the exact-write sentinel — the same one the entry rule uses —
// so nothing the file holds is carried through and the tables are exactly what
// this write derives.
//
// purgedNames exempts names whose tombstone this write pruned (expiry, capacity
// eviction, a live-name purge): their file generations entry is superseded by
// the advanced mark the derivation is writing, so the older copy must not ride
// back in through preservation.
func hubTOMLRecordTables(cfg Config, entries, known []hostreg.Host, highWater map[string]HostGeneration, provisioning map[string]hostfence.Provisioning, purgedNames map[string]struct{}) (map[string]HostRecord, map[string]HostGeneration) {
	preserve := known != nil
	owned := make(map[string]struct{}, len(known)+len(entries))
	if preserve {
		for _, e := range known {
			owned[e.Name] = struct{}{}
		}
		for _, e := range entries {
			owned[e.Name] = struct{}{}
		}
	}
	records, generations := hostFileRecordSet(entries, highWater)
	// The bootstrap flags (crash-fencing §6) ride the derived record: the write
	// carries the store's projection where it has one, and otherwise preserves
	// the flag the file already holds for the same name. The derived record
	// mints only the identity triple, so without this overlay an unrelated
	// rewrite would drop the attempt fence or the helperInstalled convergence.
	for name, record := range records {
		if p, ok := provisioning[name]; ok {
			records[name] = record.withProvisioning(p)
			continue
		}
		if fileRecord, ok := cfg.HostRecords[name]; ok {
			records[name] = record.withProvisioning(fileRecord.provisioning())
		}
	}
	if preserve {
		for name, record := range cfg.HostRecords {
			if _, ok := owned[name]; ok {
				continue
			}
			records[name] = record
		}
		for name, mark := range cfg.Generations {
			if _, ok := owned[name]; ok {
				continue
			}
			if _, pruned := purgedNames[name]; pruned {
				// The derivation pruned this name's tombstone and advanced its
				// high-water triple in the same write: the file's older copy
				// must not overwrite the advance.
				continue
			}
			generations[name] = mark
		}
	}
	return records, generations
}

// validateHostRecords checks the machine records a hub.toml document carries:
// every record is complete, and where a name carries both the live record and
// the high-water triple they agree on the incarnation id and the presence
// epoch. Spec §6's reserved namespace refuses "a reserved value whose shape
// this build cannot decode" loudly before any rewrite; a disagreement is
// exactly such a shape — no writer of this build emits it — and reading it as
// either value would silently reinterpret one of them.
func validateHostRecords(records map[string]HostRecord, generations map[string]HostGeneration) error {
	for name, record := range records {
		if !record.complete() {
			if record.IncarnationID != "" && !validIncarnationID(record.IncarnationID) {
				return fmt.Errorf("host_records[%q] carries an incarnation id of %d bytes, over the %d-byte bound (or not valid UTF-8)",
					name, len(record.IncarnationID), hostops.MaxIncarnationIDBytes)
			}
			return fmt.Errorf("host_records[%q] carries an incomplete record (%q, %d)", name, record.IncarnationID, record.PresenceEpoch)
		}
		if err := validateHostProvisioning(record); err != nil {
			return fmt.Errorf("host_records[%q]: %w", name, err)
		}
	}
	for name, mark := range generations {
		if !mark.complete() {
			if mark.IncarnationID != "" && !validIncarnationID(mark.IncarnationID) {
				return fmt.Errorf("generations[%q] carries an incarnation id of %d bytes, over the %d-byte bound (or not valid UTF-8)",
					name, len(mark.IncarnationID), hostops.MaxIncarnationIDBytes)
			}
			return fmt.Errorf("generations[%q] carries an incomplete high-water triple (%d, %q, %d)",
				name, mark.Generation, mark.IncarnationID, mark.PresenceEpoch)
		}
	}
	for name, record := range records {
		mark, ok := generations[name]
		if !ok {
			continue
		}
		if mark.IncarnationID != record.IncarnationID {
			return fmt.Errorf("host %q carries two incarnation ids: host_records has %q, generations has %q",
				name, record.IncarnationID, mark.IncarnationID)
		}
		if mark.PresenceEpoch != record.PresenceEpoch {
			return fmt.Errorf("host %q carries two presence epochs: host_records has %d, generations has %d",
				name, record.PresenceEpoch, mark.PresenceEpoch)
		}
	}
	return nil
}

// validateHostProvisioning checks the bootstrap flags crash-fencing §6 puts on
// the machine record. The flag pair is written whole by the finalizing write
// (§6:137) and the attempt fence always precedes it (§6:133), so a version
// record without the installed flag, an installed flag without a version
// record, or an installed flag without its attempt fence is a shape no writer
// of this build emits — a reserved value whose shape cannot be decoded, refused
// loudly rather than silently reinterpreted on the next rewrite.
func validateHostProvisioning(record HostRecord) error {
	epoch := hostfence.Epoch{BootID: record.BootstrapEpochBoot, OpSeq: record.BootstrapEpochOpSeq}
	switch {
	case record.HelperVersion != 0 && !record.HelperInstalled:
		return fmt.Errorf("carries a helper_version record (%d) without helper_installed", record.HelperVersion)
	case record.HelperInstalled && record.HelperVersion == 0:
		return errors.New("carries helper_installed without a helper_version record")
	case record.HelperInstalled && !record.BootstrapAttempted:
		return errors.New("carries helper_installed without the bootstrap-attempt fence")
	case record.BootstrapAttempted && epoch.IsZero():
		return errors.New("carries the bootstrap-attempt fence without the attempt's fencing epoch")
	case !record.BootstrapAttempted && !epoch.IsZero():
		return errors.New("carries a bootstrap-attempt epoch without the attempt fence")
	case !record.BootstrapAttempted && record.BootstrapAttemptToken != "":
		return errors.New("carries a bootstrap-attempt token without the attempt fence")
	case !epoch.IsZero():
		if err := epoch.Validate(); err != nil {
			return fmt.Errorf("carries an invalid bootstrap-attempt epoch: %w", err)
		}
	}
	return nil
}

// hubTOMLReceiptTables derives the mutation-receipt tables a rewrite writes:
// the set the write carries — the store's own receipts, plus a committing
// mutation's own — with every file receipt for a name this write does not own
// preserved verbatim, exactly as hubTOMLRecordTables preserves records. The
// ownership rule is the record tables' one: a name in known or entries is the
// mutation's to change (a compensation or an un-commit drops the receipt its
// own commit staged), while a receipt for any other name is the file's data and
// rides through. known is always the store's non-nil snapshot on the mutation
// and rollback paths; a nil known is the exact-write sentinel (both other
// sentinels nil), which writes exactly the given set.
func hubTOMLReceiptTables(cfg Config, entries, known []hostreg.Host, receipts map[string]HostMutationReceipt, dropped map[string]struct{}) map[string]HostMutationReceipt {
	out := make(map[string]HostMutationReceipt, len(receipts)+len(cfg.MutationReceipts))
	maps.Copy(out, receipts)
	if known == nil {
		return out
	}
	owned := make(map[string]struct{}, len(known)+len(entries))
	for _, e := range known {
		owned[e.Name] = struct{}{}
	}
	for _, e := range entries {
		owned[e.Name] = struct{}{}
	}
	for key, receipt := range cfg.MutationReceipts {
		scope, ok := parseHostReceiptScopedKey(key)
		if !ok {
			// Unreachable: decodeHubTOMLForRewrite validates the section, so a
			// key this build cannot parse already refused the write.
			continue
		}
		if _, carried := owned[scope.Name]; carried {
			continue
		}
		if _, pruned := dropped[key]; pruned {
			// The derivation dropped this receipt (compaction or a purge);
			// preservation must not resurrect the file's older copy.
			continue
		}
		out[key] = receipt
	}
	return out
}

// ownedRecordNames is the record tables' shared ownership rule: a name in
// known or entries is the mutation's to change, while a record for any other
// name is the file's data and rides through a rewrite. known nil is the
// exact-write sentinel — preserve nothing.
func ownedRecordNames(entries, known []hostreg.Host) map[string]struct{} {
	if known == nil {
		return nil
	}
	owned := make(map[string]struct{}, len(known)+len(entries))
	for _, e := range known {
		owned[e.Name] = struct{}{}
	}
	for _, e := range entries {
		owned[e.Name] = struct{}{}
	}
	return owned
}

// hubTOMLTombstoneTables derives the tombstone tables a rewrite writes: the
// set the write carries (already pruned and evicted by the derivation), with
// every file tombstone for a name this write does not own preserved verbatim —
// the same ownership rule the record and receipt tables apply, so a tombstone
// the mutation does not own is not silently dropped by its rewrite. A nil known
// is the exact-write sentinel, which writes exactly the given set.
func hubTOMLTombstoneTables(cfg Config, entries, known []hostreg.Host, tombstones map[string]HostTombstone, dropped map[string]struct{}) map[string]HostTombstone {
	out := make(map[string]HostTombstone, len(tombstones)+len(cfg.Tombstones))
	maps.Copy(out, tombstones)
	owned := ownedRecordNames(entries, known)
	if owned == nil {
		return out
	}
	for name, tombstone := range cfg.Tombstones {
		if _, carried := owned[name]; carried {
			continue
		}
		if _, pruned := dropped[name]; pruned {
			// The derivation pruned this record (expiry, eviction, a live-name
			// purge): it must not ride back in through preservation.
			continue
		}
		out[name] = tombstone
	}
	return out
}

// hubTOMLPrunedReceiptTables derives the pruned-marker tables a rewrite
// writes: the carried set plus every file marker whose scope names a host this
// write does not own, preserved verbatim. The marker's owner is the name in its
// scoped key — the name whose receipt history the marker bounds.
func hubTOMLPrunedReceiptTables(cfg Config, entries, known []hostreg.Host, markers map[string]PrunedReceiptMarker, dropped map[string]struct{}) map[string]PrunedReceiptMarker {
	out := make(map[string]PrunedReceiptMarker, len(markers)+len(cfg.PrunedReceipts))
	maps.Copy(out, markers)
	owned := ownedRecordNames(entries, known)
	if owned == nil {
		return out
	}
	for key, marker := range cfg.PrunedReceipts {
		scope, ok := parseHostReceiptScopedKey(key)
		if !ok {
			// Unreachable: decodeHubTOMLForRewrite validates the section, so a
			// key this build cannot parse already refused the write.
			continue
		}
		if _, carried := owned[scope.Name]; carried {
			continue
		}
		if _, pruned := dropped[key]; pruned {
			// The derivation compacted this marker away; preservation must not
			// resurrect it.
			continue
		}
		out[key] = marker
	}
	return out
}

// hubTOMLStagedReceiptTables derives the staged-receipt marker tables a rewrite
// writes: the carried set plus every file marker for a host this write does not
// own, preserved verbatim — the same ownership rule the other record tables
// apply, so a marker a mutation does not own is not silently dropped by its
// rewrite (spec §5: "every marker write preserves other hosts' entries
// verbatim"). A nil known is the exact-write sentinel.
func hubTOMLStagedReceiptTables(cfg Config, entries, known []hostreg.Host, markers map[string]HostStagedReceipt, dropped map[string]struct{}) map[string]HostStagedReceipt {
	out := make(map[string]HostStagedReceipt, len(markers)+len(cfg.StagedReceipts))
	maps.Copy(out, markers)
	owned := ownedRecordNames(entries, known)
	if owned == nil {
		return out
	}
	for name, marker := range cfg.StagedReceipts {
		if _, carried := out[name]; carried {
			// The carried set is authoritative for the keys it names: a marker
			// this write moves (the flip writes) or drops must not be overwritten
			// by the file's older copy, which is also why the ownership test
			// below cannot be the only guard — a marker for a name outside the
			// live set (a tombstoned host's) is still this write's to move.
			continue
		}
		if _, carried := owned[name]; carried {
			continue
		}
		if _, pruned := dropped[name]; pruned {
			continue
		}
		out[name] = marker
	}
	return out
}

// hostBootstrapStore is hostfence.BootstrapStore over the manager's hub.toml
// record machinery: the read of one host's bootstrap record and the two atomic
// writes crash-fencing §6:133/:137 pins — the attempt fence in its own write
// before the attempt's first remote side effect, and the finalizing write that
// converges helperInstalled with the delivered version. Each write goes through
// the record machinery's one derive-then-install path, so the file and the
// store cannot disagree about the flags.
//
// BOUNDARY (S21): the first-contact caller that drives hostfence.Bootstrap —
// the attach/Ensure wiring that runs the flow on a host's first contact — is
// the deploy-pipeline surface's later slice. This slice lands the record
// machinery, the fence and the gate the flow needs; until that caller lands,
// bootstrapStore is consumed by the tests that pin both crash windows.
type hostBootstrapStore struct{ m *hubHostManager }

// bootstrapStore returns the bootstrap record seam the first-contact flow
// writes through.
func (m *hubHostManager) bootstrapStore() hostfence.BootstrapStore { return hostBootstrapStore{m: m} }

// Provisioning reads the host's current bootstrap record.
func (s hostBootstrapStore) Provisioning(host string) (hostfence.Provisioning, error) {
	s.m.cfg.mu.Lock()
	defer s.m.cfg.mu.Unlock()
	return s.m.cfg.store.provisioningFor(host), nil
}

// PersistAttemptFence writes §6:133's durable bootstrap-attempt fence in its
// own atomic hub.toml write, with the attempt's fencing epoch §6:139's recovery
// names the crashed attempt by.
func (s hostBootstrapStore) PersistAttemptFence(host string, identity hostfence.BootstrapIdentity, epoch hostfence.Epoch) (hostfence.Provisioning, bool, error) {
	if err := epoch.Validate(); err != nil {
		return hostfence.Provisioning{}, false, err
	}
	token, err := newBootstrapAttemptToken()
	if err != nil {
		return hostfence.Provisioning{}, false, err
	}
	// won is set under persistProvisioning's mutation lock, together with the
	// read the conditional write compares against: it is the explicit ownership
	// signal, never inferred from the epoch.
	won := false
	record, err := s.m.persistProvisioning(host, identity, func(p hostfence.Provisioning) (hostfence.Provisioning, error) {
		if p.AttemptFenced {
			// The conditional write: another attempt already owns the fence, so
			// this one is returned unchanged — its epoch is what §6:139's recovery
			// must name — and persistProvisioning writes nothing for it.
			return p, nil
		}
		won = true
		p.AttemptFenced = true
		p.AttemptEpoch = epoch
		p.AttemptToken = token
		return p, nil
	})
	if err != nil {
		return hostfence.Provisioning{}, false, err
	}
	return record, won, nil
}

// RevalidateAttemptFence reports whether token still stands for the attempt at
// epoch on host: the record still carries the fence, names that epoch, carries
// that token, and has not converged helperInstalled.
func (s hostBootstrapStore) RevalidateAttemptFence(host string, identity hostfence.BootstrapIdentity, epoch hostfence.Epoch, token string) (hostfence.Provisioning, bool, error) {
	s.m.cfg.mu.Lock()
	defer s.m.cfg.mu.Unlock()
	record, err := s.m.boundProvisioning(host, identity)
	if err != nil {
		return hostfence.Provisioning{}, false, err
	}
	ok := record.AttemptFenced && record.AttemptEpoch == epoch && record.AttemptToken == token && !record.HelperInstalled
	return record, ok, nil
}

// InvalidateAttemptFence retires token in the same atomic write discipline as
// the other record writes, preconditioned on the record still naming that
// attempt (fence set, same epoch, same token) and not having converged
// helperInstalled. A failed precondition writes nothing and returns the record
// as it stands, so a concurrent finalize or a moved fence is never clobbered.
func (s hostBootstrapStore) InvalidateAttemptFence(host string, identity hostfence.BootstrapIdentity, epoch hostfence.Epoch, token string) (hostfence.Provisioning, error) {
	return s.m.persistProvisioning(host, identity, func(p hostfence.Provisioning) (hostfence.Provisioning, error) {
		if !p.AttemptFenced || p.AttemptEpoch != epoch || p.AttemptToken != token || p.HelperInstalled {
			return p, nil
		}
		p.AttemptToken = ""
		return p, nil
	})
}

// newBootstrapAttemptToken mints the durable ownership token one fence write
// records: opaque, random, and never reused.
func newBootstrapAttemptToken() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("mint the bootstrap-attempt token: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

// FinalizeBootstrap converges helperInstalled with the delivered version in the
// finalizing atomic write (§6:137). It refuses a host that never carried the
// attempt fence: the fence-before-install invariant is what closes the unfenced
// exemption, and an installed-without-fence record is a shape no writer emits.
func (s hostBootstrapStore) FinalizeBootstrap(host string, identity hostfence.BootstrapIdentity, helperVersion uint64) (hostfence.Provisioning, error) {
	if helperVersion == 0 {
		return hostfence.Provisioning{}, errors.New("hostfence: finalize needs the delivered helper version")
	}
	return s.m.persistProvisioning(host, identity, func(p hostfence.Provisioning) (hostfence.Provisioning, error) {
		if !p.AttemptFenced {
			return hostfence.Provisioning{}, fmt.Errorf("host %q has no bootstrap-attempt fence, so helperInstalled is not converged", host)
		}
		p.HelperInstalled = true
		p.HelperVersion = helperVersion
		return p, nil
	})
}

// persistProvisioning applies mutate to one live host's bootstrap record and
// lands the result in one atomic hub.toml write under the mutation lock. A
// failed write installs nothing, so the store's record stays exactly as it was
// and the caller re-reads it; a name that is not a live entry refuses before
// any write (a bootstrap flag has no meaning for a removed name).
func (m *hubHostManager) persistProvisioning(host string, identity hostfence.BootstrapIdentity, mutate func(hostfence.Provisioning) (hostfence.Provisioning, error)) (hostfence.Provisioning, error) {
	m.cfg.mu.Lock()
	defer m.cfg.mu.Unlock()
	current, err := m.boundProvisioning(host, identity)
	if err != nil {
		return hostfence.Provisioning{}, err
	}
	entries := m.cfg.store.snapshot()
	next, err := mutate(current)
	if err != nil {
		return hostfence.Provisioning{}, err
	}
	if next == current {
		// A conditional write that found the record already in the requested
		// state (another attempt owns the fence) writes nothing: the read and the
		// comparison ran under the mutation lock, so the compare-and-set is
		// atomic and a lost race leaves hub.toml byte-identical.
		return next, nil
	}
	if err := m.persistHosts(entries, entries, hostPersistChange{
		provisioning: &pendingHostProvisioning{Name: host, Provisioning: next},
	}); err != nil {
		return hostfence.Provisioning{}, err
	}
	return next, nil
}

// boundProvisioning returns the host's bootstrap record after verifying the live
// entry still carries the identity the attempt was bound to. Every bootstrap
// write runs it, so a stale attempt that pauses across a remove and re-add can
// never fence, revalidate, invalidate, or finalize against the new incarnation.
// Callers hold the mutation lock.
func (m *hubHostManager) boundProvisioning(host string, identity hostfence.BootstrapIdentity) (hostfence.Provisioning, error) {
	live, ok := hostfence.BootstrapIdentity{}, false
	for _, entry := range m.cfg.store.snapshot() {
		if entry.Name != host {
			continue
		}
		live = hostfence.BootstrapIdentity{Generation: entry.Generation, IncarnationID: entry.IncarnationID, PresenceEpoch: entry.PresenceEpoch}
		ok = true
		break
	}
	if !ok {
		return hostfence.Provisioning{}, fmt.Errorf("host %q is not a live host, so no bootstrap record was written", host)
	}
	if identity.IsZero() || live != identity {
		// The typed stale-registration refusal: the deploy-pipeline's stale-entry
		// class, never a helper-gate arm (the helper is not the problem).
		return hostfence.Provisioning{}, &hostfence.StaleAttemptError{Host: host, Bound: identity, Live: live}
	}
	return m.cfg.store.provisioningFor(host), nil
}

// hubTOMLTeardownRemnantTables derives the teardown-remnant tables a rewrite
// writes. The remnant's owner is the host name in its record — the name the
// fence and the retention rules key by — so a remnant for a name this write
// does not own rides through verbatim, exactly as the other record tables'
// records do.
func hubTOMLTeardownRemnantTables(cfg Config, entries, known []hostreg.Host, remnants map[string]HostTeardownRemnant, dropped map[string]struct{}) map[string]HostTeardownRemnant {
	out := make(map[string]HostTeardownRemnant, len(remnants)+len(cfg.TeardownRemnants))
	maps.Copy(out, remnants)
	owned := ownedRecordNames(entries, known)
	if owned == nil {
		return out
	}
	for id, remnant := range cfg.TeardownRemnants {
		if _, carried := out[id]; carried {
			// The carried record wins: the retry's clearance, the re-add purge,
			// and the boot compaction all move records for names outside the
			// live set (a removed host's), so ownership alone cannot guard them.
			continue
		}
		if _, carried := owned[remnant.Host]; carried {
			continue
		}
		if _, pruned := dropped[id]; pruned {
			continue
		}
		out[id] = remnant
	}
	return out
}

// hubTOMLTeardownAttemptTables derives the attempt tables a rewrite writes. An
// attempt is owned through its remnant's host: a dropped remnant takes its
// attempts with it, which is why the ownership lookup goes through the carried
// remnant set rather than the host entry alone.
func hubTOMLTeardownAttemptTables(cfg Config, entries, known []hostreg.Host, attempts map[string]HostTeardownAttempt, remnants map[string]HostTeardownRemnant, dropped map[string]struct{}) map[string]HostTeardownAttempt {
	out := make(map[string]HostTeardownAttempt, len(attempts)+len(cfg.TeardownAttempts))
	maps.Copy(out, attempts)
	owned := ownedRecordNames(entries, known)
	if owned == nil {
		return out
	}
	for id, attempt := range cfg.TeardownAttempts {
		if _, carried := out[id]; carried {
			continue
		}
		if _, carried := remnants[attempt.RemnantID]; carried {
			// A remnant this write carries owns its attempts: the derivation
			// already decided which survive.
			continue
		}
		if remnant, ok := cfg.TeardownRemnants[attempt.RemnantID]; ok {
			if _, carried := owned[remnant.Host]; carried {
				// The write owns the remnant's host but no longer carries the
				// remnant (a purge): the attempt goes with it.
				continue
			}
		}
		if _, pruned := dropped[id]; pruned {
			continue
		}
		out[id] = attempt
	}
	return out
}
