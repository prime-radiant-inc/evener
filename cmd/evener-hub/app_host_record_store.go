package hub

import (
	"errors"
	"os"
	"strings"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// hostRecordSet is the machine-managed record family the hub keeps beside its
// [[hosts]]: the per-host identity records, the generation high-water marks,
// the mutation receipts, the removal tombstones, the pruned, staged and
// teardown-repair records, and the cross-file commit bookkeeping. Today the
// family is hub.toml's reserved sections; the maps carry the same keys either
// way.
type hostRecordSet struct {
	HostRecords      map[string]HostRecord
	Generations      map[string]HostGeneration
	MutationReceipts map[string]HostMutationReceipt
	Tombstones       map[string]HostTombstone
	PrunedReceipts   map[string]PrunedReceiptMarker
	StagedReceipts   map[string]HostStagedReceipt
	TeardownRemnants map[string]HostTeardownRemnant
	TeardownAttempts map[string]HostTeardownAttempt
	PendingStoreSync map[string]HostStoreSyncIntent
	MirrorCommits    map[string]HostMirrorCommit
}

// recordSet projects the reserved sections a decoded hub.toml carries.
func (c Config) recordSet() hostRecordSet {
	return hostRecordSet{
		HostRecords:      c.HostRecords,
		Generations:      c.Generations,
		MutationReceipts: c.MutationReceipts,
		Tombstones:       c.Tombstones,
		PrunedReceipts:   c.PrunedReceipts,
		StagedReceipts:   c.StagedReceipts,
		TeardownRemnants: c.TeardownRemnants,
		TeardownAttempts: c.TeardownAttempts,
		PendingStoreSync: c.PendingStoreSync,
		MirrorCommits:    c.MirrorCommits,
	}
}

// highWaterMarks maps the generation records onto the registry's high-water
// marks: the counters a boot seeds before anything mints.
func (s hostRecordSet) highWaterMarks() map[string]hostreg.HighWater {
	marks := make(map[string]hostreg.HighWater, len(s.Generations))
	for name, mark := range s.Generations {
		marks[name] = hostreg.HighWater{Generation: mark.Generation, PresenceEpoch: mark.PresenceEpoch}
	}
	return marks
}

// hostRecordDocument is what one load of the durable state yields: the host
// entries with each persisted identity restored onto them (registry spec 08
// §15: the boot load restores persisted generations before anything serves),
// and the machine records beside them.
type hostRecordDocument struct {
	Hosts   []hostreg.Host
	Records hostRecordSet
}

// hostRecordStore is the one durable home of the machine records and of the
// host set they travel with. The manager reads and writes records only
// through it.
type hostRecordStore interface {
	// load reads the durable state. ok is false when there is nothing to
	// read: no path is selected, the document is missing or empty, or it
	// cannot be read or decoded. Callers leave the durable state alone rather
	// than act on a half-read one.
	load() (hostRecordDocument, bool)
	// absentOrEmpty reports whether the durable document genuinely holds
	// nothing: it does not exist or has zero bytes. It differs from load's ok
	// because an unreadable document is not absent, and a recovery that may
	// only converge over real absence must tell them apart.
	absentOrEmpty() bool
	// commit makes entries and the derived records durable together. known is
	// the pre-mutation snapshot the write's ownership rule reads; a nil known
	// is the exact-write sentinel.
	commit(entries, known []hostreg.Host, records hostTOMLRecords) error
}

// hubTOMLRecordStore keeps the records in the selected hub.toml's reserved
// sections: one atomic file write carries the host set and every record.
type hubTOMLRecordStore struct {
	// path is the selected hub.toml. Empty disables persistence: the store
	// reads nothing and a commit writes nothing.
	path string
}

func (s hubTOMLRecordStore) load() (hostRecordDocument, bool) {
	if strings.TrimSpace(s.path) == "" {
		return hostRecordDocument{}, false
	}
	raw, err := configReadFile(s.path)
	if err != nil || len(raw) == 0 {
		return hostRecordDocument{}, false
	}
	cfg, err := decodeConfig(s.path, string(raw))
	if err != nil {
		return hostRecordDocument{}, false
	}
	return hostRecordDocument{Hosts: hostRegistryEntries(cfg), Records: cfg.recordSet()}, true
}

func (s hubTOMLRecordStore) absentOrEmpty() bool {
	path := strings.TrimSpace(s.path)
	if path == "" {
		return false
	}
	raw, err := configReadFile(path)
	if err != nil {
		return errors.Is(err, os.ErrNotExist)
	}
	return len(raw) == 0
}

func (s hubTOMLRecordStore) commit(entries, known []hostreg.Host, records hostTOMLRecords) error {
	return writeHubTOMLHostsRecords(s.path, entries, known, records)
}

// hostRegistryEntries maps the validated [[hosts]] entries onto the host
// registry's values. runMain hands the result to hostreg.New (the registry
// sshconn consumes) and to the web config's RemoteHosts (one source per host),
// so this mapping is the last place a configured field can be lost before
// either consumer sees it: every field belongs here, including the host's
// non-default locations (EvenerPath, ConfigPath, Addr) that keep the SSH
// manager attaching with the host's own hub.toml and probing its own listener.
//
// It is also where the file's machine records join the entry: the persisted
// (generation, incarnation id, presence epoch) triple is restored onto the
// entry, so the boot load keeps the identity the file recorded instead of
// minting a new one (registry spec 08 §15: "The boot load restores persisted
// generations before the store serves any request"). A file that carries no
// record leaves the zeros hostreg.New fills at load.
func hostRegistryEntries(cfg Config) []hostreg.Host {
	entries := make([]hostreg.Host, 0, len(cfg.Hosts))
	for _, h := range cfg.Hosts {
		generation, incarnation, epoch := resolveHostIdentity(h.Name, cfg.HostRecords, cfg.Generations)
		entries = append(entries, hostreg.Host{
			Name:       h.Name,
			SSH:        h.SSH,
			User:       h.User,
			EvenerPath: h.EvenerPath,
			ConfigPath: h.ConfigPath,
			Addr:       h.Addr,
			Roots:      h.Roots,
			KeyPath:    h.KeyPath,
			// Generation, IncarnationID and PresenceEpoch: the persisted
			// identity, when the file carries one.
			Generation:    generation,
			IncarnationID: incarnation,
			PresenceEpoch: epoch,
		})
	}
	return entries
}

// hostHighWaterMarks is the registry's seed for a decoded hub.toml.
func hostHighWaterMarks(cfg Config) map[string]hostreg.HighWater {
	return cfg.recordSet().highWaterMarks()
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
