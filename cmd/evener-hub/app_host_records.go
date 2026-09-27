package hub

import (
	"fmt"
	"unicode/utf8"

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
func hubTOMLRecordTables(cfg Config, entries, known []hostreg.Host, highWater map[string]HostGeneration) (map[string]HostRecord, map[string]HostGeneration) {
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
