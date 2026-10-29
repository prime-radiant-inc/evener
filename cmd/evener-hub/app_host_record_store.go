package hub

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
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
	// hosts reads the host entries alone, with their persisted identities
	// restored. ok is false under load's rule.
	hosts() ([]hostreg.Host, bool)
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

func (s hubTOMLRecordStore) hosts() ([]hostreg.Host, bool) {
	doc, ok := s.load()
	return doc.Hosts, ok
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
	return hostEntriesWithIdentity(cfg.Hosts, cfg.recordSet())
}

// hostEntriesWithIdentity maps configured [[hosts]] onto registry values and
// restores each host's persisted identity from records.
func hostEntriesWithIdentity(configured []HostConfig, records hostRecordSet) []hostreg.Host {
	entries := make([]hostreg.Host, 0, len(configured))
	for _, h := range configured {
		generation, incarnation, epoch := resolveHostIdentity(h.Name, records.HostRecords, records.Generations)
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
			// identity, when the records carry one.
			Generation:    generation,
			IncarnationID: incarnation,
			PresenceEpoch: epoch,
		})
	}
	return entries
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

// hostFileState is what the selected hub.toml holds.
type hostFileState int

const (
	// hostFileReadable: the document exists and decodes.
	hostFileReadable hostFileState = iota
	// hostFileAbsent: no path is selected, or the file is missing or empty.
	hostFileAbsent
	// hostFileUnreadable: the file exists but cannot be read or decoded.
	hostFileUnreadable
)

// readHubTOMLConfig decodes the selected hub.toml. Only a readable document
// carries hosts: callers leave an absent or unreadable file alone rather than
// act on a half-read one.
func readHubTOMLConfig(path string) (Config, hostFileState) {
	if strings.TrimSpace(path) == "" {
		return Config{}, hostFileAbsent
	}
	raw, err := configReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist), err == nil && len(raw) == 0:
		return Config{}, hostFileAbsent
	case err != nil:
		return Config{}, hostFileUnreadable
	}
	cfg, err := decodeConfig(path, string(raw))
	if err != nil {
		return Config{}, hostFileUnreadable
	}
	return cfg, hostFileReadable
}

// hostOpsRecordStore keeps the machine records in the operation store and only
// the [[hosts]] in hub.toml. A commit writes the store first and hub.toml
// second, so a crash between them leaves records for a host change hub.toml
// never received, which boot recovery rolls back, never a host change without
// its records.
type hostOpsRecordStore struct {
	// path is the selected hub.toml. Empty disables the file half: the store
	// keeps the records and no host set is written.
	path string
	ops  *hostops.Store
	logf func(format string, args ...any)
}

func (s hostOpsRecordStore) log(format string, args ...any) {
	if s.logf != nil {
		s.logf(format, args...)
	}
}

// records decodes the store's family into a validated record set.
func (s hostOpsRecordStore) records() (hostRecordSet, error) {
	family, _, _ := s.ops.HostRecords()
	set, err := decodeHostRecords(family)
	if err != nil {
		return hostRecordSet{}, err
	}
	set.TeardownRemnants = withRemnantIDs(set.TeardownRemnants)
	return set, nil
}

func (s hostOpsRecordStore) load() (hostRecordDocument, bool) {
	cfg, state := readHubTOMLConfig(s.path)
	if state != hostFileReadable {
		return hostRecordDocument{}, false
	}
	set, err := s.records()
	if err != nil {
		s.log("host records in the operation store are unreadable: %v", err)
		return hostRecordDocument{}, false
	}
	return hostRecordDocument{Hosts: hostEntriesWithIdentity(cfg.Hosts, set), Records: set}, true
}

func (s hostOpsRecordStore) hosts() ([]hostreg.Host, bool) {
	cfg, state := readHubTOMLConfig(s.path)
	if state != hostFileReadable {
		return nil, false
	}
	family, _, _ := s.ops.HostRecords()
	live, err := decodeRecordMap[HostRecord]("host", family.Hosts)
	if err != nil {
		s.log("host records in the operation store are unreadable: %v", err)
		return nil, false
	}
	generations, err := decodeRecordMap[HostGeneration]("generation", family.Generations)
	if err != nil {
		s.log("host records in the operation store are unreadable: %v", err)
		return nil, false
	}
	return hostEntriesWithIdentity(cfg.Hosts, hostRecordSet{HostRecords: live, Generations: generations}), true
}

func (s hostOpsRecordStore) absentOrEmpty() bool {
	_, state := readHubTOMLConfig(s.path)
	return state == hostFileAbsent && strings.TrimSpace(s.path) != ""
}

// errRecordsNotDurable reports a store write whose state is published but whose
// directory sync has not succeeded.
var errRecordsNotDurable = errors.New("the host records are published but not yet durable")

// commitRecords writes the machine records alone, leaving hub.toml as it is.
func (s hostOpsRecordStore) commitRecords(entries []hostreg.Host, records hostTOMLRecords) error {
	_, err := s.writeFamily(entries, records)
	return err
}

// writeFamily commits the record family and its boundary mirror in one store
// write and returns the family it replaced.
func (s hostOpsRecordStore) writeFamily(entries []hostreg.Host, records hostTOMLRecords) (hostops.HostRecords, error) {
	set := recordSetFor(entries, records)
	family, err := encodeHostRecords(set)
	if err != nil {
		return hostops.HostRecords{}, err
	}
	prior, _, _ := s.ops.HostRecords()
	if err := s.publishFamily(family, hostMirrorFor(set, s.ops.Boundaries())); err != nil {
		if errors.Is(err, errRecordsNotDurable) {
			return prior, err
		}
		return hostops.HostRecords{}, err
	}
	return prior, nil
}

// publishFamily commits a family and its mirror in one store write. A write
// that is published but not proven durable is reported as errRecordsNotDurable:
// the host file does not move on until it is.
func (s hostOpsRecordStore) publishFamily(family hostops.HostRecords, mirror hostops.HostMirror) error {
	if err := s.ops.CommitHostRecords(family, mirror); err != nil {
		if !hostops.RenameLanded(err) {
			return err
		}
		if syncErr := s.ops.SyncDir(); syncErr != nil {
			return fmt.Errorf("%w: %w", errRecordsNotDurable, syncErr)
		}
	}
	return nil
}

// takeBackFlip returns name's staged marker to its pre-flip phase. A commit's
// runtime swap starts only after its host change is in hub.toml, so a marker past
// its flip tells recovery the file holds the change; a compensation that is about
// to take the change out of the file takes the flip back first, and one that
// cannot leaves the file alone, still consistent with the marker.
func (s hostOpsRecordStore) takeBackFlip(name string) error {
	family, _, _ := s.ops.HostRecords()
	raw, staged := family.StagedReceipts[name]
	if !staged {
		return nil
	}
	markers, err := decodeRecordMap[HostStagedReceipt]("staged receipt", map[string]json.RawMessage{name: raw})
	if err != nil {
		return err
	}
	marker := markers[name]
	if !markerPastFlip(marker) {
		return nil
	}
	marker.SwapStarted = false
	marker.TeardownStarted = false
	marker.Phase = hostStagedPhaseStaged
	encoded, err := encodeRecordMap(map[string]HostStagedReceipt{name: marker})
	if err != nil {
		return err
	}
	family.StagedReceipts = maps.Clone(family.StagedReceipts)
	family.StagedReceipts[name] = encoded[name]
	set, err := decodeHostRecords(family)
	if err != nil {
		return err
	}
	return s.publishFamily(family, hostMirrorFor(set, s.ops.Boundaries()))
}

// hostFileFirstSeam, when set, is called between commitHostFileFirst's two
// writes. It is nil in production.
var hostFileFirstSeam func() error

// commitHostFileFirst is commit with the order reversed, for the compensation of
// a commit that already reached hub.toml (see hostPersistChange.hostFileFirst).
func (s hostOpsRecordStore) commitHostFileFirst(entries, known []hostreg.Host, records hostTOMLRecords, markerName string) error {
	if markerName != "" {
		if err := s.takeBackFlip(markerName); err != nil {
			return fmt.Errorf("take back the flip of %q before its host file: %w", markerName, err)
		}
	}
	if hostFileErr := writeHubTOMLHostsOnly(s.path, entries, known); hostFileErr != nil {
		// A restore that landed but is not proven durable leaves the staged
		// records in the store: dropping them first would strand a change a
		// crash can bring back into the file with nothing to roll it back. The
		// manager still settles (persistHosts), and the next successful write or
		// a boot replaces them.
		return hostFileErr
	}
	var err error
	if hostFileFirstSeam != nil {
		err = hostFileFirstSeam()
	}
	if err == nil {
		_, err = s.writeFamily(entries, records)
	}
	if err != nil {
		// The host change is already out of the file, which is the state that
		// matters: the manager follows it, and the store's staged records, which
		// a boot rolls back and the next successful write replaces, are left to
		// that. Reporting the compensation as failed would suggest the file
		// still holds the change it no longer does.
		s.log("the staged records were not dropped after the host file was restored: %v", err)
	}
	return nil
}

func (s hostOpsRecordStore) commit(entries, known []hostreg.Host, records hostTOMLRecords) error {
	prior, err := s.writeFamily(entries, records)
	if err != nil {
		if errors.Is(err, errRecordsNotDurable) {
			// Published but not durable: the host change must not reach hub.toml,
			// so the records staged for it are taken back like those of a refused
			// host-file write.
			s.restore(prior)
		}
		return err
	}
	if err := writeHubTOMLHostsOnly(s.path, entries, known); err != nil {
		if !hubTOMLRenameCommitted(err) {
			// The host set did not reach hub.toml, so the records staged for it
			// must not outlive the refusal: a later finalizer would commit a
			// mutation the caller was told failed. The compensation of a rename
			// that did land runs its own commit instead.
			s.restore(prior)
		}
		return err
	}
	return nil
}

// restore puts a prior family back after a commit whose host-set write failed.
// A failure here is logged: the unwritten host change leaves the staged marker
// for boot recovery to roll back.
func (s hostOpsRecordStore) restore(prior hostops.HostRecords) {
	mirror := hostops.HostMirror{}
	if set, err := decodeHostRecords(prior); err == nil {
		mirror = hostMirrorFor(set, s.ops.Boundaries())
	}
	if err := s.ops.CommitHostRecords(prior, mirror); err != nil && !hostops.RenameLanded(err) {
		s.log("host records not restored after a refused host-set write: %v", err)
	}
}

// recordSetFor is the record family one commit carries: the identity records
// of the live entries, the high-water marks of every other name, and the
// derived record maps.
func recordSetFor(entries []hostreg.Host, records hostTOMLRecords) hostRecordSet {
	hostRecords, generations := hostFileRecordSet(entries, records.highWater)
	return hostRecordSet{
		HostRecords:      hostRecords,
		Generations:      generations,
		MutationReceipts: records.receipts,
		Tombstones:       records.tombstones,
		PrunedReceipts:   records.prunedReceipts,
		StagedReceipts:   records.stagedReceipts,
		TeardownRemnants: records.remnants,
		TeardownAttempts: records.attempts,
	}
}

// hostMirrorFor projects a record set onto the boundary mirror the operation
// records are validated against: every complete generation record is a
// boundary, a boundary the set no longer carries is dropped, live names clear
// their removal markers and tombstoned names are dated from their removal once
// it is past its flip. existing is the mirror's current boundaries.
func hostMirrorFor(set hostRecordSet, existing map[string]hostops.Boundary) hostops.HostMirror {
	mirror := hostops.HostMirror{Boundaries: make(map[string]hostops.Boundary, len(set.Generations))}
	for name, mark := range set.Generations {
		if mark.complete() {
			mirror.Boundaries[name] = hostops.Boundary{
				Generation:    mark.Generation,
				IncarnationID: mark.IncarnationID,
				PresenceEpoch: mark.PresenceEpoch,
			}
		}
	}
	for name := range existing {
		if _, carried := mirror.Boundaries[name]; !carried {
			mirror.Remove = append(mirror.Remove, name)
		}
	}
	slices.Sort(mirror.Remove)
	for name := range set.HostRecords {
		mirror.Live = append(mirror.Live, name)
	}
	slices.Sort(mirror.Live)
	for name, tombstone := range set.Tombstones {
		if _, live := set.HostRecords[name]; live {
			continue
		}
		if marker, staged := set.StagedReceipts[name]; staged && !markerPastFlip(marker) {
			// The removal can still be rolled back, and the store's retention
			// must not compact the history of a host that stays configured.
			continue
		}
		removedAt, err := time.Parse(time.RFC3339, tombstone.RemovedAt)
		if err != nil {
			continue
		}
		if mirror.Removed == nil {
			mirror.Removed = map[string]hostops.RemovedHost{}
		}
		mirror.Removed[name] = hostops.RemovedHost{
			RemovedAt:     removedAt,
			Generation:    tombstone.Generation,
			IncarnationID: tombstone.IncarnationID,
		}
	}
	return mirror
}

// syncMirror rebuilds the boundary mirror from the family. The mirror and the
// family land in one write, so it can only trail after a fault between
// versions of the store; boot closes the gap.
func (s hostOpsRecordStore) syncMirror() error {
	set, err := s.records()
	if err != nil {
		return err
	}
	family, _, _ := s.ops.HostRecords()
	return s.ops.CommitHostRecords(family, hostMirrorFor(set, s.ops.Boundaries()))
}

// restoreUnlandedRemovalIdentities gives a host the identity it had when its
// staged removal never reached hub.toml: the removal's write dropped the host's
// live record, but hub.toml is byte for byte what the commit staged against, so
// the boot rolls the removal back and the host comes up as the incarnation the
// tombstone names instead of a new one. A hub.toml that changed since may have
// lost the host and had it added again by hand, which is a new incarnation.
func restoreUnlandedRemovalIdentities(configured []HostConfig, set hostRecordSet) hostRecordSet {
	for _, h := range configured {
		if record, ok := unlandedRemovalIdentity(set, h.Name, configured); ok {
			set.HostRecords[h.Name] = record
		}
	}
	return set
}

// unlandedRemovalIdentity returns the live record name had before a staged
// removal that never reached hub.toml dropped it.
func unlandedRemovalIdentity(set hostRecordSet, name string, configured []HostConfig) (HostRecord, bool) {
	marker, staged := set.StagedReceipts[name]
	tombstone, removed := set.Tombstones[name]
	if !staged || !removed || !stagedRemovalNeverLanded(marker, configured) {
		return HostRecord{}, false
	}
	return HostRecord{IncarnationID: tombstone.IncarnationID, PresenceEpoch: tombstone.PresenceEpoch}, true
}

// stagedRemovalNeverLanded reports whether marker stages a removal whose host
// change cannot have reached hub.toml: the entry configured holds for the host
// is the one the commit staged against, and the commit never got as far as its
// runtime swap (past it, a host in the file is one the operator recreated).
func stagedRemovalNeverLanded(marker HostStagedReceipt, configured []HostConfig) bool {
	scope, ok := parseHostReceiptScopedKey(marker.Key)
	if !ok || scope.Kind != hostMutationRemove || marker.HostFingerprint == "" || markerPastFlip(marker) {
		return false
	}
	return hostConfigFingerprint(configured, scope.Name) == marker.HostFingerprint
}

// hubTOMLHostFingerprint digests the entry hub.toml holds for name; empty when
// the file holds none. A file that cannot be read is an error, not an absence.
func hubTOMLHostFingerprint(path, name string) (string, error) {
	cfg, state := readHubTOMLConfig(path)
	if state == hostFileUnreadable {
		return "", fmt.Errorf("%s could not be read", path)
	}
	return hostConfigFingerprint(cfg.Hosts, name), nil
}

// hostConfigFingerprint digests the entry configured holds for name; empty when
// it holds none. Other hosts and settings do not move it.
func hostConfigFingerprint(configured []HostConfig, name string) string {
	for _, h := range configured {
		if h.Name != name {
			continue
		}
		encoded, err := json.Marshal(h)
		if err != nil {
			return ""
		}
		sum := sha256.Sum256(encoded)
		return hex.EncodeToString(sum[:])
	}
	return ""
}

// hostBootDocument is the host set and the records startup builds the host
// registry from. A store that holds the records is their home; otherwise the
// records are the ones hub.toml still carries. Records the store holds but this
// build cannot read are not guessed at: the hosts come up with identities
// minted at load, and the manager keeps host changes paused (bootStoreRecords).
func hostBootDocument(cfg Config, ops *hostops.Store) hostRecordDocument {
	if ops != nil {
		if _, _, imported := ops.HostRecords(); imported {
			set, err := hostOpsRecordStore{ops: ops}.records()
			if err != nil {
				return hostRecordDocument{Hosts: hostEntriesWithIdentity(cfg.Hosts, hostRecordSet{})}
			}
			set = restoreUnlandedRemovalIdentities(cfg.Hosts, set)
			return hostRecordDocument{Hosts: hostEntriesWithIdentity(cfg.Hosts, set), Records: set}
		}
	}
	return hostRecordDocument{Hosts: hostRegistryEntries(cfg), Records: cfg.recordSet()}
}
