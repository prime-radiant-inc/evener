package hub

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/BurntSushi/toml"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostops"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/execsupport/fsatomic"
)

// writeHubTOMLHostsRecords is the one hub.toml writer. It rewrites path in place
// with the machine-managed banner at its top and entries as its [[hosts]]
// tables, preserving every other key the file already holds — the operator's addr, provider, and plugin settings are
// data, and a rewrite must never drop them. The produced bytes are re-parsed
// through the loader's own decode before anything reaches disk, so a hub.toml
// this function writes is always one the hub can read back at boot; a file that
// cannot be read or parsed refuses the write instead of being clobbered.
//
// The write is atomic: a 0600 temp file in the same directory, fsynced,
// renamed over the target, and the directory itself synced after the rename, so
// a crash lands the old file or the new one, never a half-write or a lost
// rename — without the directory sync, a successful add/remove could still
// vanish in a power failure despite the synced temp file. The rename is the
// write's commit point: a failure before it writes nothing, while a failure
// behind it (hubTOMLPostRenameError) means the file already holds the new
// entries and the caller owes the live state a compensation. 0600 keeps key
// paths from ever landing world-readable; a hub.toml that predates the storage
// decision keeps the operator's mode until the hub's first rewrite. An empty
// path (no config file) skips the write; the in-memory store stays
// authoritative for the process lifetime.
//
// known is the set the rewrite is derived from — the store's pre-mutation
// snapshot — and it is what lets the writer tell a hand-added entry from an
// entry the mutation deliberately removed: only file entries whose names are
// absent from known are carried through as extras. A nil known disables
// preservation entirely (an exact write); a store snapshot is never nil, even
// when the store is empty — an empty snapshot must still preserve hand-added
// file entries, and nil would silently drop them.
//
// records carries every machine-managed record set this build writes: the
// per-host records and high-water marks, the mutation receipts, the
// removed-host tombstones, and the pruned markers. The manager's persist path
// builds the set with deriveHostTOMLRecords (which has already applied
// retention, capacity, and compaction) and installs the same set on success.
func writeHubTOMLHostsRecords(path string, entries, known []hostreg.Host, records hostTOMLRecords) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	doc, raw, err := readHubTOMLDocument(path)
	if err != nil {
		return err
	}
	// One decode of the file's current bytes serves both preservation rules —
	// the hand-added entries the write carries through and the records it
	// carries through with them — and a write that preserves neither (the exact
	// writes, both sentinels nil) skips it: the round-trip check below still
	// holds the bytes it writes to the loader's rules. A nil known is the
	// exact-write sentinel (no snapshot: preserve nothing); an empty known is a
	// real empty snapshot, and preservation still applies.
	var fileCfg Config
	if known != nil || records.highWater != nil {
		fileCfg, err = decodeHubTOMLForRewrite(path, raw)
		if err != nil {
			return err
		}
	}
	var extra []HostConfig
	if known != nil {
		extra = fileOnlyHostEntries(fileCfg, known, entries)
	}
	tables := append(hubTOMLHostTables(entries), extra...)
	if len(tables) == 0 {
		// Write no `hosts` key at all for an empty set: `hosts = []` cannot be
		// reopened as an array of tables, so a later operator hand-add of a
		// `[[hosts]]` table would make the file unreadable at the next boot.
		// The banner-only shape stays hand-editable.
		delete(doc, "hosts")
	} else {
		doc["hosts"] = tables
	}
	// The preservation rule skips the names whose records this very derivation
	// raised (the boot mirror pass's discarded generations): their [generations]
	// record and tombstone twin are the raise, and the file's older copy must
	// not ride back over either.
	preserved := records.droppedTombstones
	if len(records.raisedHighWater) > 0 {
		preserved = make(map[string]struct{}, len(records.droppedTombstones)+len(records.raisedHighWater))
		maps.Copy(preserved, records.droppedTombstones)
		maps.Copy(preserved, records.raisedHighWater)
	}
	hostRecords, generations := hubTOMLRecordTables(fileCfg, entries, known, records.highWater, preserved)
	if len(hostRecords) == 0 {
		delete(doc, "host_records")
	} else {
		doc["host_records"] = hostRecords
	}
	if len(generations) == 0 {
		delete(doc, "generations")
	} else {
		doc["generations"] = generations
	}
	receiptTables := hubTOMLReceiptTables(fileCfg, entries, known, records.receipts, records.droppedReceipts)
	if len(receiptTables) == 0 {
		delete(doc, "mutation_receipts")
	} else {
		doc["mutation_receipts"] = receiptTables
	}
	tombstoneTables := hubTOMLTombstoneTables(fileCfg, entries, known, records.tombstones, preserved)
	if len(tombstoneTables) == 0 {
		delete(doc, "tombstones")
	} else {
		doc["tombstones"] = tombstoneTables
	}
	prunedTables := hubTOMLPrunedReceiptTables(fileCfg, entries, known, records.prunedReceipts, records.droppedMarkers)
	if len(prunedTables) == 0 {
		delete(doc, "pruned_receipts")
	} else {
		doc["pruned_receipts"] = prunedTables
	}
	stagedTables := hubTOMLStagedReceiptTables(fileCfg, entries, known, records.stagedReceipts, records.droppedStaged)
	if len(stagedTables) == 0 {
		delete(doc, "staged_receipts")
	} else {
		doc["staged_receipts"] = stagedTables
	}
	storeSyncTables := hubTOMLStoreSyncTables(fileCfg, entries, known, records.storeSync, records.droppedStoreSync)
	if len(storeSyncTables) == 0 {
		delete(doc, "pending_store_sync")
	} else {
		doc["pending_store_sync"] = storeSyncTables
	}
	mirrorTables := hubTOMLMirrorCommitTables(fileCfg, entries, known, generations)
	if len(mirrorTables) == 0 {
		delete(doc, "mirror_commits")
	} else {
		doc["mirror_commits"] = mirrorTables
	}
	remnantTables := hubTOMLTeardownRemnantTables(fileCfg, entries, known, records.remnants, records.droppedRemnants)
	if len(remnantTables) == 0 {
		delete(doc, "teardown_remnants")
	} else {
		doc["teardown_remnants"] = remnantTables
	}
	attemptTables := hubTOMLTeardownAttemptTables(fileCfg, entries, known, records.attempts, records.remnants, records.droppedAttempts)
	if len(attemptTables) == 0 {
		delete(doc, "teardown_attempts")
	} else {
		doc["teardown_attempts"] = attemptTables
	}
	var buf bytes.Buffer
	buf.WriteString(hostTOMLBanner)
	if err := toml.NewEncoder(&buf).Encode(doc); err != nil {
		return fmt.Errorf("marshal hub.toml: %w", err)
	}
	data := buf.Bytes()
	// The rewrite is only as good as its round trip: run the bytes through the
	// loader's own decode before anything lands, so a file the hub would refuse
	// at boot can never be written by a mutation.
	if _, err := decodeConfig(path, string(data)); err != nil {
		return fmt.Errorf("hub.toml rewrite refused: %w", err)
	}
	// The replace's rename is the save's commit point: the target file already
	// holds the new entries once fsatomic reports ErrPublished, so that failure
	// cannot be returned as though nothing was written. It is wrapped in
	// hubTOMLPostRenameError, and the callers compensate the live state
	// (rollbackHubTOML) before reporting the failure.
	if err := hubTOMLReplaceWithBytes(path, data, hubTOMLSyncDir); err != nil {
		if errors.Is(err, fsatomic.ErrPublished) {
			return &hubTOMLPostRenameError{err: err}
		}
		return err
	}
	return nil
}

// readHubTOMLDocument loads path's current keys as a generic document, so a
// rewrite replaces only the `hosts` table and preserves every other key as
// data. A missing file is an empty document (the rewrite creates it); an
// unreadable or unparsable file refuses the write — the hub never clobbers a
// file it could not read.
func readHubTOMLDocument(path string) (map[string]any, []byte, error) {
	data, err := configReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]any{}, nil, nil
		}
		return nil, nil, fmt.Errorf("hub.toml rewrite read %s: %w", path, err)
	}
	var doc map[string]any
	if err := toml.Unmarshal(data, &doc); err != nil {
		return nil, nil, fmt.Errorf("hub.toml rewrite parse %s: %w", path, err)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	return doc, data, nil
}

// hubTOMLHostTables renders entries as the file's [[hosts]] tables, in the very
// HostConfig shape loadConfig decodes (its omitempty tags keep a rewrite from
// inventing a key the operator, or the dialog that added the host, never set).
// One schema, so the reader and the writer cannot drift apart.
func hubTOMLHostTables(entries []hostreg.Host) []HostConfig {
	tables := make([]HostConfig, 0, len(entries))
	for _, e := range entries {
		tables = append(tables, HostConfig{
			Name:       e.Name,
			SSH:        e.SSH,
			User:       e.User,
			EvenerPath: e.EvenerPath,
			ConfigPath: e.ConfigPath,
			Addr:       e.Addr,
			Roots:      append([]string(nil), e.Roots...),
			KeyPath:    e.KeyPath,
		})
	}
	return tables
}

// hubTOMLSyncDir is the directory sync every hub.toml write ends with, so a
// crash right after a rename cannot lose the committed add or remove. It is a
// swappable package variable so tests can force the post-rename failure path
// deterministically, the one point where a failed save has already replaced
// the target file.
var hubTOMLSyncDir = fsatomic.SyncDir

// hubTOMLStashSyncDir is the stash write's own directory-sync seam. It starts
// as the hub.toml seam's value at process start and is separate so a test that
// swaps hubTOMLSyncDir to drive hub.toml's post-rename failure path leaves the
// stash write (which runs before the staged write) unaffected.
var hubTOMLStashSyncDir = hubTOMLSyncDir

// hubTOMLPostRenameError marks a write failure that followed the rename
// that replaced hub.toml: the new entries are already the file's
// contents — only the directory sync that makes the rename durable failed. A
// write returning one is not a refusal that wrote nothing: the callers must
// bring the file and the live set back into step (rollbackHubTOML) before
// returning the failure, so an add the API reports as failed cannot
// resurrect from the file on the next start, and a removal the API reports
// as failed cannot lose its host there.
type hubTOMLPostRenameError struct{ err error }

func (e *hubTOMLPostRenameError) Error() string { return e.err.Error() }

func (e *hubTOMLPostRenameError) Unwrap() error { return e.err }

// hubTOMLRenameCommitted reports whether err is a hub.toml write failure the
// rename already committed: the file was replaced before the failure, so the
// caller owes the live state a compensation, not a plain refusal.
func hubTOMLRenameCommitted(err error) bool {
	var post *hubTOMLPostRenameError
	return errors.As(err, &post)
}

// persistHosts rewrites hub.toml durably. A failure the rename already
// committed (hubTOMLPostRenameError) is not a plain refusal — the file holds the
// new entries — so the callers compensate the live state before reporting it.
// known is the store's pre-mutation snapshot.
//
// The write is derive-then-install: the machine-record set is derived once
// (deriveHostTOMLRecords applies every retention, capacity, and compaction
// rule), that exact set is written, and only a successful write installs it
// into the store — so a failed write leaves both the durable file and the
// in-memory records untouched, and a compensation needs no staged-state
// bookkeeping. A capacity refusal (spec §15's `tombstone-capacity`) is mapped
// onto its wire envelope here, the one choke point every mutation path runs
// through.
func (m *hubHostManager) persistHosts(entries, known []hostreg.Host, change hostPersistChange) error {
	records, err := m.deriveHostTOMLRecords(entries, known, change)
	if err != nil {
		return tombstoneCapacityRefusal(err)
	}
	if err := writeHubTOMLHostsRecords(m.cfg.configPath, entries, known, records); err != nil {
		return err
	}
	m.cfg.store.installRecords(records)
	// The mirror rides behind the hub.toml commit: hub.toml is the authority
	// and the two files cannot be one write, so the mirror is written last and
	// a failure here leaves it behind the file rather than unwinding the
	// commit. See mirrorBoundaries.
	m.mirrorBoundaries(entries, known, records)
	return nil
}

// mirrorBoundaries records the triples the hub.toml write just committed in the
// operation store's per-host boundary records (registry spec 08 §7: "one record
// per host name — {generation, incarnationId, presenceEpoch} — written in the
// same atomic store writes that mirror the generation"). Every live entry's
// triple and every retained high-water record for a name with no live entry is
// carried, names whose entry holds no complete identity (a direct writer
// fixture) are skipped, and the whole set lands in one atomic store write
// together with the removal markers §4's removed-host ordering reads: every
// name carrying a removal tombstone is dated, every live name clears any
// marker it held.
//
// A mirror failure is logged, not returned: the hub.toml commit already landed
// and cannot be unwound, and the state it leaves — the file ahead of the
// mirror — is exactly what the deploy-pipeline spec §4 boot pass pushes
// forward. That pass, and the cursor validation that reads the mirror, belong
// to later slices; until they land the mirror trails the file and nothing
// reads it.
func (m *hubHostManager) mirrorBoundaries(entries, known []hostreg.Host, records hostTOMLRecords) {
	if m.cfg.ops == nil || strings.TrimSpace(m.cfg.configPath) == "" {
		return
	}
	// The mirrored triples are the same derivation the file's records come from
	// (hostFileRecordSet), so the mirror and the file cannot be projected by two
	// rules that drift apart.
	_, generations := hostFileRecordSet(entries, records.highWater)
	boundaries := make(map[string]hostops.Boundary, len(generations))
	for name, mark := range generations {
		boundaries[name] = hostops.Boundary{
			Generation:    mark.Generation,
			IncarnationID: mark.IncarnationID,
			PresenceEpoch: mark.PresenceEpoch,
		}
	}
	// The write owns the records of the names it carries and its pre-mutation
	// snapshot names — the same ownership rule the record tables use. A boundary
	// under an owned name this write carries no record for (a compensated add,
	// a name the live registry dropped while the write ran) is pruned in the
	// same atomic write, so the mirror never keeps a boundary for a host
	// hub.toml no longer records.
	owned := make(map[string]struct{}, len(known)+len(entries))
	for _, e := range known {
		owned[e.Name] = struct{}{}
	}
	for _, e := range entries {
		owned[e.Name] = struct{}{}
	}
	var remove []string
	for name := range owned {
		if _, carried := boundaries[name]; !carried {
			remove = append(remove, name)
		}
	}
	slices.Sort(remove)
	// The removal markers ride the same write: a tombstoned name is dated from
	// its own removed_at, a live name clears any marker, and a name the write
	// cannot classify (a pruned-tombstone high-water record) keeps whatever
	// marker it had — a removal the store cannot date is never invented, and
	// one it already knew is never forgotten (deploy pipeline 08b §4's
	// removed-host horizon).
	mirror := hostops.HostMirror{Boundaries: boundaries, Remove: remove}
	for _, entry := range entries {
		mirror.Live = append(mirror.Live, entry.Name)
	}
	for name, tombstone := range records.tombstones {
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
	if len(mirror.Boundaries) == 0 && len(mirror.Remove) == 0 && len(mirror.Live) == 0 && len(mirror.Removed) == 0 {
		return
	}
	if err := m.cfg.ops.MirrorHostState(mirror); err != nil {
		m.logf("host boundary records not mirrored: %v", err)
	}
}

// ---------------------------------------------------------------------------
// The cross-file commit intent and the generation-mirror markers
// (deploy-pipeline spec 08b §9, §4)
// ---------------------------------------------------------------------------

// HostStoreSyncIntent is hub.toml's `pending_store_sync` intent (§9): the
// exact store rows a commit's swap is deleting or invalidating, plus the
// hub.toml generation the intent belongs to. The commit's staged write carries
// it, the store's purge applies it after the swap succeeds, and a follow-up
// atomic write clears it.
type HostStoreSyncIntent struct {
	// Generation is the hub.toml generation the intent belongs to (the removed
	// entry's generation).
	Generation uint64 `toml:"generation"`
	// TokenValues names the exact store rows to delete, by token value. A
	// removal carries the host's outstanding row, when it has one.
	TokenValues []string `toml:"token_values"`
}

// HostMirrorCommit is hub.toml's generation-mirror commit marker (§4): the
// (hub.toml generation, store-mirror generation) pair every commit that
// advances a mirrored store-side generation writes into hub.toml's atomic
// write. Boot's bidirectional reconciliation reads it as the authorization
// evidence for a store mirror: a mirror newer than the file's mark with no
// matching marker was not authorized by a durable hub.toml write.
type HostMirrorCommit struct {
	HubTOMLGeneration uint64 `toml:"hub_toml_generation"`
	StoreGeneration   uint64 `toml:"store_generation"`
}

// validateMachineRecordName checks a name keying one of the machine-managed
// record sections: a bounded, valid-UTF-8, non-empty host name. No writer of
// these sections emits anything else.
func validateMachineRecordName(name string) error {
	if name == "" {
		return errors.New("a machine record is keyed by an empty host name")
	}
	if len(name) > hostops.MaxHostNameBytes {
		return fmt.Errorf("a machine record is keyed by a %d-byte host name, over the %d-byte bound", len(name), hostops.MaxHostNameBytes)
	}
	if !utf8.ValidString(name) {
		return errors.New("a machine record is keyed by a host name that is not valid UTF-8")
	}
	return nil
}

// validateHostStoreSync checks the cross-file intents a hub.toml document
// carries: a named key, a pinned generation, and at least one bounded,
// unique, non-empty row value.
func validateHostStoreSync(intents map[string]HostStoreSyncIntent) error {
	for name, intent := range intents {
		if err := validateMachineRecordName(name); err != nil {
			return err
		}
		if intent.Generation == 0 {
			return fmt.Errorf("pending_store_sync[%q] pins no hub.toml generation", name)
		}
		if len(intent.TokenValues) == 0 {
			return fmt.Errorf("pending_store_sync[%q] names no store rows", name)
		}
		seen := make(map[string]struct{}, len(intent.TokenValues))
		for _, value := range intent.TokenValues {
			if value == "" {
				return fmt.Errorf("pending_store_sync[%q] names an empty row value", name)
			}
			if len(value) > hostops.MaxStoreSyncValueBytes {
				return fmt.Errorf("pending_store_sync[%q] names a %d-byte row value, over the %d-byte bound",
					name, len(value), hostops.MaxStoreSyncValueBytes)
			}
			if !utf8.ValidString(value) {
				return fmt.Errorf("pending_store_sync[%q] names a row value that is not valid UTF-8", name)
			}
			if _, duplicate := seen[value]; duplicate {
				return fmt.Errorf("pending_store_sync[%q] names row %q twice", name, value)
			}
			seen[value] = struct{}{}
		}
	}
	return nil
}

// validateHostMirrorCommits checks the generation-mirror markers a hub.toml
// document carries: a named key and two non-zero generations.
func validateHostMirrorCommits(commits map[string]HostMirrorCommit) error {
	for name, commit := range commits {
		if err := validateMachineRecordName(name); err != nil {
			return err
		}
		if commit.HubTOMLGeneration == 0 || commit.StoreGeneration == 0 {
			return fmt.Errorf("mirror_commits[%q] carries a zero generation", name)
		}
	}
	return nil
}

// hubTOMLStoreSyncTables derives the pending-store-sync tables a rewrite
// writes: the intent set this write carries, plus every file intent for a name
// this write does not own, preserved verbatim — the same ownership rule the
// other record tables apply, so an intent a mutation does not own is not
// silently dropped by its rewrite. A nil known is the exact-write sentinel.
func hubTOMLStoreSyncTables(cfg Config, entries, known []hostreg.Host, intents map[string]HostStoreSyncIntent, dropped map[string]struct{}) map[string]HostStoreSyncIntent {
	out := make(map[string]HostStoreSyncIntent, len(intents)+len(cfg.PendingStoreSync))
	maps.Copy(out, intents)
	preserveUnowned(out, cfg.PendingStoreSync, ownedRecordNames(entries, known), keyIsHost, dropped)
	return out
}

// hubTOMLMirrorCommitTables derives the generation-mirror marker tables a
// rewrite writes. Every name this write owns and carries a generation for gets
// the (G, G) marker for that generation — the pair the mirror write that
// follows this one is about to land — while every file marker for a name the
// write does not own is preserved verbatim. A name with no generation record
// (a purged name) gets none: the marker goes with the records it authorizes.
func hubTOMLMirrorCommitTables(cfg Config, entries, known []hostreg.Host, generations map[string]HostGeneration) map[string]HostMirrorCommit {
	out := make(map[string]HostMirrorCommit, len(generations)+len(cfg.MirrorCommits))
	owned := ownedRecordNames(entries, known)
	for name, mark := range generations {
		if owned != nil {
			if _, ok := owned[name]; !ok {
				continue
			}
		}
		if !mark.complete() {
			continue
		}
		out[name] = HostMirrorCommit{HubTOMLGeneration: mark.Generation, StoreGeneration: mark.Generation}
	}
	if owned == nil {
		return out
	}
	for name, commit := range cfg.MirrorCommits {
		if _, carried := out[name]; carried {
			continue
		}
		if _, ok := owned[name]; ok {
			continue
		}
		out[name] = commit
	}
	return out
}

// hubTOMLStashPath names one commit's stash: a per-commit file beside hub.toml
// (registry spec 08 §6: "stashing a durable copy of the prior `hub.toml`
// bytes"), keyed by the commit's scoped receipt key. A shared name would let a
// later removal of another host overwrite the restore source an open
// compensation still names, so the next boot would restore the wrong bytes as
// that record's pre-mutation state. The compensation record carries the exact
// path; only a record naming it keeps it from the boot prune.
func hubTOMLStashPath(configPath, receiptKey string) string {
	sum := sha256.Sum256([]byte(receiptKey))
	return fmt.Sprintf("%s.stash.%x", configPath, sum[:8])
}

// stashKeyHexChars is the fixed width of the per-commit suffix
// hubTOMLStashPath emits (the first 8 bytes of a SHA-256 digest).
const stashKeyHexChars = 16

// isStashKeyHex reports whether suffix is a per-commit stash name's hex half.
func isStashKeyHex(suffix string) bool {
	if len(suffix) != stashKeyHexChars {
		return false
	}
	for _, digit := range suffix {
		if (digit < '0' || digit > '9') && (digit < 'a' || digit > 'f') {
			return false
		}
	}
	return true
}

// ownsStashPath reports whether stash names this hub's own stash family beside
// its hub.toml: a per-commit `<hub.toml>.stash.<16 hex>` name. Anything else is not this hub's to read,
// restore, or remove — a tampered or foreign record names a path, never bytes
// this hub wrote.
func (m *hubHostManager) ownsStashPath(stash string) bool {
	return stashPathOwnedBy(m.cfg.configPath, stash)
}

// stashPathOwnedBy is ownsStashPath's path-based twin, usable before a manager
// exists (the load wiring validates the records it opened).
func stashPathOwnedBy(configPath, stash string) bool {
	path := strings.TrimSpace(configPath)
	if path == "" || strings.TrimSpace(stash) == "" {
		return false
	}
	suffix, ok := strings.CutPrefix(stash, path+".stash.")
	return ok && isStashKeyHex(suffix)
}

// validateHostOpsStashReferences refuses a loaded compensation record whose
// stash reference is not this hub.toml family's own: the record's path becomes
// a restore source and an unconditional remove target, so a foreign path must
// refuse startup (the machine-managed-file posture: a record this build cannot
// account for is refused loudly, never served and never removed).
func validateHostOpsStashReferences(store *hostops.Store, configPath string) error {
	if store == nil {
		return nil
	}
	for host, record := range store.Compensations() {
		if stashPathOwnedBy(configPath, record.Stash) {
			continue
		}
		return fmt.Errorf("operation store compensation for %q names stash %q, which is not a stash path beside %s; refusing to serve rather than read or remove a foreign path",
			host, record.Stash, strings.TrimSpace(configPath))
	}
	return nil
}

// writeHubTOMLStash captures the selected hub.toml's current bytes into the
// stash before a removal's staged write replaces them, so the commit is
// compensable. It returns the stash reference the compensation record must
// carry. A hub with no config file has no cross-file commit to compensate and
// stashes nothing.
func (m *hubHostManager) writeHubTOMLStash(receiptKey string) (string, error) {
	path := strings.TrimSpace(m.cfg.configPath)
	if path == "" {
		return "", nil
	}
	prior, err := configReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		// No file yet: the hub is authoritative in memory and there is no
		// committed hub.toml bytes a restore would apply.
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("stash hub.toml %s: %w", path, err)
	}
	stash := hubTOMLStashPath(path, receiptKey)
	if err := hubTOMLReplaceWithBytes(stash, prior, hubTOMLStashSyncDir); err != nil {
		return "", fmt.Errorf("stash hub.toml %s: %w", path, err)
	}
	return stash, nil
}

// hubTOMLReplaceWithBytes atomically replaces path with data: a 0600 temp file
// in the same directory, fsynced, renamed over the target, then the directory
// synced — the same posture every hub.toml write keeps. sync is the caller's
// directory-sync seam: the stash keeps its own (hubTOMLStashSyncDir), so a
// test injecting a hub.toml sync failure drives the writes it names and not the
// stash beside them.
func hubTOMLReplaceWithBytes(path string, data []byte, sync func(string) error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	return fsatomic.WriteWithDirSync(path, data, 0o600, sync)
}

// restoreHubTOMLFromStash applies a compensation's stash: the prior hub.toml
// bytes go back atomically, so a crash cannot land a half-restored file. The
// stash is re-parsed through the loader before anything lands, exactly as
// every rewrite does.
func (m *hubHostManager) restoreHubTOMLFromStash(stash string) error {
	path := strings.TrimSpace(m.cfg.configPath)
	if path == "" {
		return nil
	}
	if strings.TrimSpace(stash) == "" {
		return errors.New("compensation record names no stash")
	}
	if !m.ownsStashPath(stash) {
		return fmt.Errorf("compensation record names stash %q, which is not a stash path beside %s", stash, path)
	}
	data, err := os.ReadFile(stash)
	if err != nil {
		return fmt.Errorf("read hub.toml stash %s: %w", stash, err)
	}
	if _, err := decodeConfig(path, string(data)); err != nil {
		return fmt.Errorf("hub.toml stash %s is not a readable document: %w", stash, err)
	}
	return hubTOMLReplaceWithBytes(path, data, hubTOMLSyncDir)
}

// pruneHubTOMLStash removes a stash once its commit reached an outcome: "The
// stash is deleted once the commit reaches either outcome" (registry spec
// 08 §6). A missing stash is already pruned.
func (m *hubHostManager) pruneHubTOMLStash(stash string) {
	if strings.TrimSpace(stash) == "" {
		return
	}
	if !m.ownsStashPath(stash) {
		// A record's path is not this hub's to remove: a tampered or foreign
		// record must never turn into an unconditional delete.
		m.logf("stash %q is not a stash path this hub owns; left untouched", stash)
		return
	}
	if err := os.Remove(stash); err != nil && !errors.Is(err, os.ErrNotExist) {
		m.logf("hub.toml stash %s not pruned: %v", stash, err)
	}
}

// rollbackHubTOML re-persists previous after a post-write live mutation failed,
// keeping hub.toml in step with the live set: the API reported the
// mutation as failed, so the file must not keep a copy the next start would
// resurrect (Add) or drop an entry the live set still holds (Remove).
// previous is the content the file must hold for the live set to stay in
// step: the pre-add contents for Add, and for Remove the live snapshot
// after the failed removal re-added its row — a stale pre-remove copy would
// clobber entries concurrently committed while the removal's teardown ran
// unlocked. This is the compensating half of the durable-first ordering: the
// save still leads, so a save failure still commits nothing live and the
// caller can retry, and a live mutation that fails after the save landed is
// rolled back rather than left diverging. A rollback save failure is
// surfaced alongside cause: the file is
// then known to diverge, and the caller must hear it rather than a
// clean-looking refusal. A rollback whose own rename landed while its
// directory step failed is the other case: the file already holds previous,
// so the state agrees and only the rollback's crash durability is uncertain
// — reported as landed beside the cause, never as a failed rollback.
func (m *hubHostManager) rollbackHubTOML(previous, known []hostreg.Host, cause error, change hostPersistChange) error {
	if err := m.persistHosts(previous, known, change); err != nil {
		if hubTOMLRenameCommitted(err) {
			// The rollback's own rename landed: the file holds previous, the
			// content the rollback exists to restore, and only its
			// durability step failed — the file and the live set agree. Say
			// that beside the cause rather than claiming a rollback failure
			// that did not happen.
			return fmt.Errorf("%w; %w: %w", cause, errHubTOMLRollbackUnsynced, err)
		}
		return fmt.Errorf("%w; %w: %w", cause, errHubTOMLRollbackFailed, err)
	}
	return cause
}

// errHubTOMLRollbackFailed marks the compensation return whose hub.toml
// restore did not converge: the rollback's own write failed before its rename,
// so the file may still hold the failed write. The deploy pipeline's cross-file
// compensation reads it to keep its store-local record and stash for a later
// boot rather than clearing a compensation beside a diverged file.
var errHubTOMLRollbackFailed = errors.New("hub.toml rollback failed")

// errHubTOMLRollbackUnsynced marks the compensation return whose hub.toml
// rollback rename landed while its directory step failed: the file holds the
// previous hosts, and only the rollback's crash durability is uncertain.
var errHubTOMLRollbackUnsynced = errors.New("hub.toml rollback landed but its directory step failed")
