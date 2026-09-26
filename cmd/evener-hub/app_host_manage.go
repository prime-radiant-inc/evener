package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/fsdurability"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
	"primeradiant.com/evener/internal/appserver"
)

// hostOriginHubTOML is the HostRow origin marker. The storage decision (registry
// spec 08 §6/§19) made hub.toml the machine-managed file every host lives in, so
// every row reports it; the wire field is retained with one value.
const hostOriginHubTOML = "hub.toml"

// hostTOMLBanner is the machine-managed banner every rewrite writes at the top
// of the selected hub.toml — the file saying, in itself, that the hub rewrites
// it and that operator comments and formatting do not survive (registry spec 08
// §6).
const hostTOMLBanner = "# This file is machine-managed by the evener hub.\n" +
	"# The hub rewrites it in place; comments and formatting are not preserved.\n"

// legacyHostSidecarFileName is the retired sidecar beside the selected
// hub.toml. It is read once by the boot migration — never written — and set
// aside under legacyHostSidecarAsideSuffix on success.
const legacyHostSidecarFileName = "hub.hosts.json"

// legacyHostSidecarAsideSuffix names the retired sidecar once its entries are
// folded into hub.toml: the file is renamed aside, never deleted, so its bytes
// survive for recovery while a later removal of a name it carried can never
// resurrect through it.
const legacyHostSidecarAsideSuffix = ".migrated"

// legacySidecarMigratedKey is the machine-managed key a successful migration
// records in hub.toml. It is what makes the retirement authoritative rather
// than only the rename's durability: on a filesystem whose directories cannot
// be synced (the tolerance hubTOMLSyncDir shares with the hub's other stores), a
// power loss can bring hub.hosts.json back after the UI has removed a host it
// carried — and a later boot that finds the marker in hub.toml knows the sidecar
// has already been folded in, so it ignores the stale file instead of
// re-merging names the UI removed. The marker rides the same atomic write as the
// merged host set, so any mutation that survives proves it survived too.
const legacySidecarMigratedKey = "legacy_sidecar_migrated"

// hostManagerConfig carries what the host-management handlers need. The
// registry is the controller's live one in production — the same
// *hostreg.Registry the SSH manager dials through and the attach handler
// validates against — so a host added at runtime is attachable without a
// restart. The stores are live: the hub owns them, mutations swap entries
// under mu, and reads never dial.
type hostManagerConfig struct {
	// hosts is the live host registry: the machine-managed hub.toml entries at
	// boot plus every entry added at runtime. Mutations hold mu; the SSH manager
	// and the attach handler consult the same instance in production.
	hosts *hostreg.Registry
	// store holds the durable host set: every live entry, in the order a
	// rewrite writes it (the boot set in the registry's name-sorted order,
	// then entries in add order; an edit replaces in place). The file's host
	// order is the hub's — the banner says formatting does not survive — so a
	// hand-authored out-of-order file is normalized by the first rewrite. It is
	// the model of the rewritten hub.toml — mutations derive their write from
	// it, so an entry a removal already committed can never be re-persisted by a
	// concurrent mutation whose write starts during the removal's teardown.
	store *hostStore
	// configPath is the selected hub.toml path — the file every mutation
	// rewrites in place. Empty (tests, embedders without a file) disables
	// persistence: the store stays memory-only and every method still works.
	configPath string
	// sources is the component-05 registry; a remote host's source is where
	// attachment state (Online) and the per-host client live.
	sources *appsource.Registry
	// remoteCache is the controller's remote-thread snapshot cache. Remove
	// prunes the removed host's rows from it — and drops its registration
	// generation, so a refresh in flight during the remove cannot republish
	// them — so its sessions stop rendering with the removal instead of
	// lingering live until the refresher's next tick. registerSource assigns
	// the name a fresh generation when a remove/re-add registers it again,
	// so a walk captured under the old registration cannot publish the old
	// host's rows under the new identity while the re-added host's own walks
	// publish normally. Nil (tests,
	// embedders without a cache): every tree read then walks the live
	// sources per request, and a removed host — no source — contributes no
	// rows on its own.
	remoteCache *hubcore.RemoteThreadCache
	// forgetLastGoodThreads drops the web server's retained last-known-good
	// rows for a source. Remove calls it in the finish phase beside
	// remoteCache.RemoveSource: the background walk stores the host's last
	// successful list under its name, and the removal must take that
	// retention with it too — left behind, the entry and its thread rows
	// outlive the host for the process lifetime, and churning distinct host
	// names grows the map without bound. Nil (tests,
	// embedders without a web server): nothing was ever retained, so there
	// is nothing to forget.
	forgetLastGoodThreads func(sourceID string)
	// manager owns every live SSH channel; removal goes through its atomic
	// RemoveHost so a concurrent attach cannot publish past deregistration, and
	// the row path resolves the entry's attached client through its
	// ChannelIfAttached so a row can pair the channel with the very
	// registration it renders (attachedClient). Nil in tests that only
	// exercise validation.
	manager *sshconn.Manager
	// client is the Ensure-backed dialing seam a new source's client func
	// uses (cfg.RemoteHostClient). Nil (tests, embedders) leaves the source
	// on the detached refusal remoteClientFor serves.
	client func(ctx context.Context, host string) (*appwire.Client, error)
	// online reports whether the controller's channel to host is currently
	// attached. Nil leaves every remote host to the source registry's own
	// Online (tests).
	online func(host string) bool
	// clientIfAttached is the non-dialing attached-only client lookup list
	// and status fall back to when no manager owns the channels (tests,
	// embedders): it answers by name alone, with no channel registration to
	// pair against the entry the row renders. Nil leaves rows without live
	// facts.
	clientIfAttached func(host string) (*appwire.Client, bool)
	// handshake returns the attach handshake facts for the channel behind
	// client, or false when no live channel backs it. Nil leaves rows
	// without handshake identity.
	handshake func(host string, client *appwire.Client) (appwire.InitializeResponse, bool)
	// facts returns the preflight facts for the connection behind client.
	// Nil leaves rows without preflight facts.
	facts func(ctx context.Context, host string, client *appwire.Client) (appsource.HostFacts, error)
	// state retains per-host attach state from the manager's lifecycle
	// events plus the last-known facts of the last attached render, so
	// offline and in-progress rows keep the metadata the wire contract
	// promises.
	state *hostAttachState
	// logf is the hub's logging path for store load problems; nil drops
	// the lines (tests that never load a broken file).
	logf func(format string, args ...any)
	// mu serializes add/remove/update read-modify-write cycles so concurrent calls
	// cannot lose updates or interleave a save with a registry mutation.
	mu sync.Mutex
	// mutating holds the names with a remove or update in flight: both mark the
	// name in their commit phase, after the durable change landed and the store
	// row moved with it, and clear it in their finish phase. Add needs no mark
	// because its whole mutation is atomic under mu, but it refuses a name that
	// remove or update already marked. The window a mutation releases the mutex
	// for — a teardown that blocks on the per-host gate a supervisor's
	// reconnect/ensure cycle can hold for minutes — must admit no second mutation
	// of the same name, so every mutation refuses a marked name until its own
	// finish clears the mark. Guarded by mu.
	mutating map[string]struct{}
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
	// loadErr records why the durable host set cannot be treated as fully
	// loaded: a legacy sidecar failed to parse or validate, or its one-time
	// migration failed. While it is set the in-memory snapshot is known
	// incomplete, so writes refuse — rewriting hub.toml would clobber the
	// entries that never made it into memory — and add/remove fail loudly
	// instead of silently losing them.
	loadErr error
}

// set installs entries as the store's contents; the constructor calls it once
// with the registry's boot set. The caller transfers ownership of the slice —
// the boot set is freshly built and held nowhere else — so set adopts it.
func (s *hostStore) set(entries []hostreg.Host) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = entries
}

// poison records err as the reason the durable host set is not fully loaded.
// The first reason wins; later ones only add log lines at the call site.
func (s *hostStore) poison(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr == nil {
		s.loadErr = err
	}
}

// poisoned reports why the durable host set cannot be trusted as fully loaded,
// or nil when every entry is loaded (or there is nothing to migrate).
func (s *hostStore) poisoned() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadErr
}

// legacyHostSidecarFile is the on-disk shape of the retired sidecar: entries in
// add order. It is a migration input only; nothing writes it any more.
type legacyHostSidecarFile struct {
	// Hosts is a pointer so the shape gate is one decode: absent and null both
	// leave it nil, while a present array — empty included — sets it.
	Hosts *[]legacyHostSidecarFileEntry `json:"hosts"`
}

// legacyHostSidecarFileEntry is one persisted sidecar entry: the seven
// HostConfig fields in snake_case (hub.toml's spelling for the same fields)
// plus the key path. The retired sidecar never crossed the wire, so it follows
// the repo's snake_case json default; the appwire package's camelCase HostRow
// is what clients see.
type legacyHostSidecarFileEntry struct {
	Name       string   `json:"name"`
	SSH        string   `json:"ssh"`
	User       string   `json:"user,omitempty"`
	EvenerPath string   `json:"evener_path,omitempty"`
	ConfigPath string   `json:"config_path,omitempty"`
	Addr       string   `json:"addr,omitempty"`
	Roots      []string `json:"roots,omitempty"`
	KeyPath    string   `json:"key_path,omitempty"`
}

// legacySidecarPathFor returns the retired sidecar's path beside the selected
// hub.toml: the one-time migration's input. An empty config path (tests,
// embedders without a file) disables persistence: the store stays memory-only
// and every method still works.
func legacySidecarPathFor(configPath string) string {
	if strings.TrimSpace(configPath) == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(configPath), legacyHostSidecarFileName)
}

// loadLegacyHostSidecar reads path into entries in file order. A missing file
// is no sidecar, not an error: a hub that never added a host has no file.
func loadLegacyHostSidecar(path string) ([]hostreg.Host, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read legacy host sidecar: %w", err)
	}
	return parseLegacyHostSidecar(data)
}

// parseLegacyHostSidecar decodes the retired sidecar's bytes in file order: the
// one-time migration's parse. A present document must
// carry a "hosts" array: an absent or null field is a load error, not an empty
// sidecar — a present empty array is the legitimate one.
func parseLegacyHostSidecar(data []byte) ([]hostreg.Host, error) {
	// The shape gate rides the typed decode: `hosts` must be present and
	// non-null, which is exactly "the pointer is non-nil" — absent and null
	// both leave it nil, while a present array (empty included) sets it. A
	// JSON-valid document without a usable array — {} or {"hosts":null} —
	// must be a load error, not an empty sidecar: loading it as "no entries"
	// would let the migration rewrite hub.toml from an empty snapshot,
	// silently discarding whatever the broken document carried. Loud, file
	// preserved, the corrupt-file contract.
	var file legacyHostSidecarFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parse legacy host sidecar: %w", err)
	}
	if file.Hosts == nil {
		return nil, errors.New(`parse legacy host sidecar: missing or null "hosts" array`)
	}
	entries := make([]hostreg.Host, 0, len(*file.Hosts))
	for _, h := range *file.Hosts {
		entries = append(entries, hostreg.Host{
			Name:       h.Name,
			SSH:        h.SSH,
			User:       h.User,
			EvenerPath: h.EvenerPath,
			ConfigPath: h.ConfigPath,
			Addr:       h.Addr,
			Roots:      h.Roots,
			KeyPath:    h.KeyPath,
		})
	}
	return entries, nil
}

// writeHubTOMLHosts rewrites path in place with the machine-managed banner at
// its top and entries as its [[hosts]] tables, preserving every other key the
// file already holds — the operator's addr, provider, and plugin settings are
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
func writeHubTOMLHosts(path string, entries []hostreg.Host) error {
	return writeHubTOMLHostsMarked(path, entries, false)
}

// writeHubTOMLHostsMarked is writeHubTOMLHosts plus the migration's marker:
// when migrated is true the same atomic write records
// legacySidecarMigratedKey, so the rewritten file itself says the retired
// sidecar has been folded in.
func writeHubTOMLHostsMarked(path string, entries []hostreg.Host, migrated bool) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	doc, err := readHubTOMLDocument(path)
	if err != nil {
		return err
	}
	doc["hosts"] = hubTOMLHostTables(entries)
	if migrated {
		doc[legacySidecarMigratedKey] = true
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
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("hub.toml mkdir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".hub.toml-*.tmp")
	if err != nil {
		return fmt.Errorf("hub.toml temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("hub.toml chmod: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("hub.toml write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("hub.toml sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("hub.toml close: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("hub.toml rename: %w", err)
	}
	// The rename is only durable once the directory entry that carries it is
	// synced too: a crash right after it could otherwise lose the committed
	// add or remove despite the synced temp file above. Some filesystems
	// cannot sync a directory at all; the tolerance inside the sync seam keeps
	// the save working there instead of turning a durability nicety into a
	// hard failure, the same way the hub's deletion and transcript-display
	// stores and the server's thread-clear journal treat this idiom.
	//
	// The rename above is the save's commit point: the target file already
	// holds the new entries, so a failure behind it cannot be returned as
	// though nothing was written. It is wrapped in hubTOMLPostRenameError,
	// and the callers compensate the live state (rollbackHubTOML) before
	// reporting the failure.
	if err := hubTOMLSyncDir(filepath.Dir(path)); err != nil {
		return &hubTOMLPostRenameError{err: err}
	}
	return nil
}

// readHubTOMLDocument loads path's current keys as a generic document, so a
// rewrite replaces only the `hosts` table and preserves every other key as
// data. A missing file is an empty document (the rewrite creates it); an
// unreadable or unparsable file refuses the write — the hub never clobbers a
// file it could not read.
func readHubTOMLDocument(path string) (map[string]any, error) {
	data, err := configReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]any{}, nil
		}
		return nil, fmt.Errorf("hub.toml rewrite read %s: %w", path, err)
	}
	var doc map[string]any
	if err := toml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("hub.toml rewrite parse %s: %w", path, err)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	return doc, nil
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

// hubTOMLSyncDir opens dir, syncs it, and closes it — the durability half
// of the atomic-rename idiom, so a crash right after a hub.toml rename cannot
// lose the committed add or remove. It is a swappable package variable so
// tests can force the post-rename failure path deterministically, the one
// point where a failed save has already replaced the target file; the default
// keeps the tolerance for filesystems that cannot sync a directory at all.
var hubTOMLSyncDir = func(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("hub.toml directory: %w", err)
	}
	if err := d.Sync(); err != nil && !hubTOMLSyncUnsupported(err) {
		_ = d.Close()
		return fmt.Errorf("hub.toml directory sync: %w", err)
	}
	if err := d.Close(); err != nil {
		return fmt.Errorf("hub.toml directory close: %w", err)
	}
	return nil
}

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

// hubTOMLSyncUnsupported reports whether a sync failed because the
// filesystem cannot sync a directory at all. It delegates to the hub's one
// canonical predicate (internal/fsdurability); hubcore's deletion store and
// the server's thread-clear journal keep matching package-private copies of
// the same tolerance in their own modules.
func hubTOMLSyncUnsupported(err error) bool {
	return fsdurability.SyncUnsupported(err)
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

// snapshot returns entries in add order; the slice is a copy. Callers hold
// hostManagerConfig.mu.
func (s *hostStore) snapshot() []hostreg.Host {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]hostreg.Host(nil), s.entries...)
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

// hostAttachRecord is one host's retained attach state: what the lifecycle
// events say about an in-progress or failed attach, plus the last-known facts
// of the last row that rendered attached. retiredThrough is the highest entry
// generation this name's record has been retired through.
type hostAttachRecord struct {
	midAttach bool
	lastErr   string
	known     appwire.HostRow // only the fact fields are read back
	// retiredThrough fences the retained state to the entry generations that are
	// still live: content is served only to a row whose entry generation is
	// above this mark, and a row at or below it writes nothing back. It is the
	// generation of the retired entry an edit's swap replaced (retire), kept
	// monotonically non-decreasing so a later retirement cannot lower it. Zero
	// means no identity has been retired for this name, which is why a fresh
	// name's rows are never fenced.
	retiredThrough uint64
}

// hostAttachState retains per-host attach records from the SSH manager's
// lifecycle events and from attached rows this surface renders, so offline
// and in-progress rows keep the metadata the wire contract promises (a host
// mid-attach renders midAttach; a host that failed renders lastAttachError;
// an offline row keeps its last-known facts).
//
// It is generation-scoped, not merely name-keyed: an update retires the
// identity a name's record describes, and the retirement is marked by that
// identity's entry generation (retire). Rows carry the generation of the entry
// they were built from, and only a row above the mark is served or may write
// back. That is what makes an edit's clear immune to a row that captured the
// pre-swap entry before the update but only reaches its state write after the
// swap's retirement: its generation is at or below the mark, so its stale facts
// are neither folded into the row it is building nor recorded for later rows.
// Records for removed hosts are dropped wholesale — remove, the clean-slate
// path — so a re-add starts clean, its mark included.
type hostAttachState struct {
	mu      sync.Mutex
	records map[string]*hostAttachRecord
}

func newHostAttachState() *hostAttachState {
	return &hostAttachState{records: map[string]*hostAttachRecord{}}
}

// observe records one SSH lifecycle event. It only writes the map: it runs
// from sshconn's OnEvent with the per-host lock held, so it must never call
// back into the manager.
func (s *hostAttachState) observe(ev sshconn.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.records[ev.Host]
	if rec == nil {
		rec = &hostAttachRecord{}
		s.records[ev.Host] = rec
	}
	switch ev.Kind {
	case sshconn.EventAttached:
		// A fresh attach supersedes the previous attempt's error and ends
		// the in-progress state.
		rec.midAttach = false
		rec.lastErr = ""
	case sshconn.EventFailed:
		rec.midAttach = false
		if ev.Err != nil {
			rec.lastErr = ev.Err.Error()
		}
	case sshconn.EventDetached:
		// A reconnecting detach does not end the in-progress attach: the
		// supervisor's link-drop path emits StateReconnecting immediately
		// before this event and keeps retrying through its backoff, so the
		// row keeps reporting the attach and the Connect control stays
		// disabled while the manager is already reconnecting, instead of
		// inviting a redundant manual attempt. Every other detach — a
		// teardown, a shutdown, a publish race — carries StateDisconnected
		// and ends the in-progress attach.
		if ev.State != sshconn.StateReconnecting {
			rec.midAttach = false
		}
	case sshconn.EventState:
		switch ev.State {
		case sshconn.StatePreflighting, sshconn.StateDeploying, sshconn.StateRestarting,
			sshconn.StateAttaching, sshconn.StateReconnecting:
			rec.midAttach = true
		default:
			rec.midAttach = false
		}
	}
}

// hostFactsValidity records which of an attached row's fact fields the live
// lookups actually refreshed: the handshake seam owns the server identity
// pair, the facts seam the hub/os/arch triple. The seams report one success
// flag per group, so validity is per lookup, not per field — a successful
// lookup's values are authoritative even when empty.
type hostFactsValidity struct {
	handshake bool
	facts     bool
}

// recordKnown keeps the facts of the last row that rendered attached, so the
// host's later offline rows still render them. Only the fields whose live
// lookup succeeded are written: a transient handshake or facts failure on an
// attached host leaves those fields empty in the row, and recording them would
// blank the previously retained facts — the offline rows that follow would
// lose the metadata the wire contract promises. gen is the entry generation the
// row was built from; a row at or below the name's retired mark describes an
// identity the record no longer belongs to, so it writes nothing — this is the
// write that a retirement fences out, and without it a late pre-swap row would
// recreate the retired identity's facts after the clear.
func (s *hostAttachState) recordKnown(row appwire.HostRow, validity hostFactsValidity, gen uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.records[row.Name]
	if rec != nil && gen <= rec.retiredThrough {
		return
	}
	if rec == nil {
		rec = &hostAttachRecord{}
		s.records[row.Name] = rec
	}
	if validity.handshake {
		rec.known.ServerName = row.ServerName
		rec.known.ServerVersion = row.ServerVersion
	}
	if validity.facts {
		rec.known.HubVersion = row.HubVersion
		rec.known.OS = row.OS
		rec.known.Arch = row.Arch
	}
}

// apply folds the retained record into row: the attach state always, the
// last-known facts only when the row is not attached (an attached row's facts
// come from the live channel). gen is the entry generation the row was built
// from; a row at or below the name's retired mark folds nothing, so a stale
// pre-swap row renders clean instead of the retired identity's state.
func (s *hostAttachState) apply(row *appwire.HostRow, gen uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.records[row.Name]
	if rec == nil || gen <= rec.retiredThrough {
		return
	}
	row.MidAttach = rec.midAttach
	row.LastAttachErr = rec.lastErr
	if !row.Attached {
		row.ServerName = rec.known.ServerName
		row.ServerVersion = rec.known.ServerVersion
		row.HubVersion = rec.known.HubVersion
		row.OS = rec.known.OS
		row.Arch = rec.known.Arch
	}
}

// retire drops the record's content for name and marks every row whose entry
// generation is <= gen as retired: such a row must neither be served the
// name's retained state nor write any back. gen is the generation of the entry
// an update retired, so a row that captured that entry — or any earlier one —
// is fenced, while a row built from the new identity's higher generation is
// served and records normally. The mark is monotonic (the max of the existing
// and the new generation) so a retirement can never lower it, and the record
// entry is kept (with its mark) so the mark survives even though the content is
// gone.
func (s *hostAttachState) retire(name string, gen uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.records[name]
	if rec == nil {
		rec = &hostAttachRecord{}
		s.records[name] = rec
	}
	if gen > rec.retiredThrough {
		rec.retiredThrough = gen
	}
	rec.midAttach = false
	rec.lastErr = ""
	rec.known = appwire.HostRow{}
}

// remove drops name's record; a removed or re-added host starts clean.
func (s *hostAttachState) remove(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.records, name)
}

// hubHostManager serves evener/host/add, evener/host/list,
// evener/host/status, evener/host/remove, and evener/host/update: the host
// registry surface. It owns no connections: list and status resolve through
// the attached-only seams, add validates hub.toml-authoritatively and wires the
// new host's source with the same seams startup uses, remove tears the host
// down through the manager's atomic RemoveHost, and update swaps its entry and
// retires its channel through UpdateHost. It never dials.
//
// It is controller-LOCAL: these methods act on the controller's own config and
// channels, so they MUST NOT be added to remoteHostAdminMethods (pinned by
// TestHostManageNotForwarded), and every handler refuses a request whose
// routing origin is non-empty (a peer hub calling over its attach bridge) —
// pinned by TestHostManageRefusesRemoteOrigin.
type hubHostManager struct {
	cfg *hostManagerConfig
}

// newHubHostManager builds the manager over the live registries. hosts is the
// controller's live registry — in production the one *hostreg.Registry the
// SSH manager dials through and the attach handler validates against, so
// entries loaded here and hosts added at runtime are attachable
// without a restart; a nil hosts falls back to an empty registry rather than
// a panic. configPath is the selected hub.toml path ("" disables
// persistence); logf is the hub's logging path for store load problems.
//
// The durable host set starts as the registry's boot entries — every host
// lives in the one machine-managed hub.toml — and a legacy sidecar, when one is
// present, is folded into hub.toml exactly once (migrateLegacyHostSidecar): its
// entries join the live set, hub.toml is rewritten with the merged set and the
// banner, and the retired file is set aside. A sidecar that fails to load or
// migrate, or a legacy entry that fails validation, is logged here and poisons
// writes: hub.toml keeps every entry it had until an operator fixes the file,
// instead of the next rewrite dropping the entries that never loaded.
func newHubHostManager(sources *appsource.Registry, manager *sshconn.Manager, cfg hubcore.WebConfig, configPath string, hosts *hostreg.Registry, logf func(format string, args ...any)) *hubHostManager {
	if hosts == nil {
		hosts, _ = hostreg.New(nil)
	}
	m := &hubHostManager{cfg: &hostManagerConfig{
		hosts:            hosts,
		store:            &hostStore{},
		configPath:       strings.TrimSpace(configPath),
		sources:          sources,
		remoteCache:      cfg.RemoteThreadCache,
		manager:          manager,
		client:           cfg.RemoteHostClient,
		online:           cfg.RemoteHostOnline,
		clientIfAttached: cfg.RemoteHostClientIfAttached,
		handshake:        cfg.RemoteHostHandshake,
		facts:            cfg.RemoteHostFacts,
		state:            newHostAttachState(),
		mutating:         map[string]struct{}{},
		logf:             logf,
	}}
	m.cfg.store.set(hosts.All())
	if err := m.migrateLegacyHostSidecar(); err != nil {
		// Loud, not fatal: the hub.toml hosts still serve. The store stays
		// poisoned so no later rewrite can land before the sidecar's entries
		// are folded in.
		m.logf("legacy host sidecar %s not migrated: %v", legacySidecarPathFor(m.cfg.configPath), err)
		m.cfg.store.poison(err)
	}
	return m
}

// migrateLegacyHostSidecar folds a retired hub.hosts.json into hub.toml exactly
// once, at boot. The order is the crash-safety story, and it is fixed:
//
//  1. hub.toml is rewritten (atomically, directory synced) with the merged set
//     and the machine-managed banner — the durable merged state exists before
//     the retired file can go away.
//  2. the sidecar is renamed aside — never deleted — and the directory synced,
//     so the retirement is durable before anything can act on the merged state.
//
// A name hub.toml already declares wins: the migration drops the sidecar's
// duplicate in the file's favor (the retired split made a live name in both
// files a hard startup error, so this precedence is the migration's own rule).
// The same atomic write records `legacy_sidecar_migrated` in hub.toml, and the
// marker — not the rename's durability alone — is what makes the retirement
// authoritative: a later boot that finds the sidecar again with the marker
// present ignores it as stale, so a name removed through the UI after a
// completed migration can never be resurrected by a rename a power loss undid,
// even on a filesystem that ignores directory syncs. The caller poisons writes
// on any other failure, including a failed set-aside rename: no mutation can
// land while an unmigrated sidecar remains. A sidecar that fails to parse or validate is not migrated at
// all — both files stay untouched and the caller poisons writes. A crash at any
// point re-runs the whole migration on the next boot and converges, because
// already-merged names collide with the live set and are skipped, and the
// set-aside rename either already happened or is retried.
func (m *hubHostManager) migrateLegacyHostSidecar() error {
	path := legacySidecarPathFor(m.cfg.configPath)
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// No sidecar: hub.toml is not rewritten at boot, so a hub that
			// never added a host keeps its file byte-identical.
			return nil
		}
		return fmt.Errorf("read legacy host sidecar: %w", err)
	}
	// A sidecar that reappears after a recorded migration is stale — the
	// marker and every mutation since are in the same file, so a survivor of
	// one survived both. Ignore it rather than re-merging names the UI may
	// have removed (the case a directory sync the filesystem ignores cannot
	// rule out).
	if doc, docErr := readHubTOMLDocument(m.cfg.configPath); docErr == nil && doc[legacySidecarMigratedKey] == true {
		m.logf("stale legacy host sidecar %s ignored: %s already records the migration", path, m.cfg.configPath)
		return nil
	}
	entries, err := parseLegacyHostSidecar(data)
	if err != nil {
		return err
	}
	for _, e := range entries {
		// Normalize before any use of the entry. The registry's Add would
		// normalize before storing, but the collision check and the source
		// registration read the entry as decoded: a padded sidecar name would
		// register a source under the padded spelling while the registry stores
		// the trimmed one.
		e = hostreg.Normalize(e)
		if err := validateHostEntry(e); err != nil {
			return fmt.Errorf("legacy host sidecar entry %q: %w", e.Name, err)
		}
	}
	// Validation is all-or-nothing: no entry merges until every entry is known
	// good, so a sidecar that fails validation is never half-applied (and the
	// caller poisons writes until the operator fixes it).
	for _, e := range entries {
		e = hostreg.Normalize(e)
		if _, ok := m.cfg.hosts.Get(e.Name); ok {
			// hub.toml wins: a colliding sidecar entry is a policy drop, not a
			// migration failure.
			continue
		}
		if err := m.cfg.hosts.Add(e); err != nil {
			return fmt.Errorf("legacy host sidecar entry %q: %w", e.Name, err)
		}
		m.cfg.store.add(e)
		m.registerSource(e)
	}
	if err := m.persistHostsMarked(m.cfg.store.snapshot(), true); err != nil {
		return err
	}
	aside := path + legacyHostSidecarAsideSuffix
	if _, err := os.Stat(aside); err == nil {
		return fmt.Errorf("cannot set legacy host sidecar %s aside: %s already exists", path, aside)
	}
	if err := os.Rename(path, aside); err != nil {
		return fmt.Errorf("set legacy host sidecar aside: %w", err)
	}
	// The rename is only durable once the directory entry carrying it is
	// synced: without this, a power loss could bring the retired sidecar back
	// and the next boot would re-merge names the UI had already removed. The
	// sync seam keeps its usual tolerance for filesystems that cannot sync a
	// directory at all.
	if err := hubTOMLSyncDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("set legacy host sidecar aside: %w", err)
	}
	return nil
}

// logf emits through the hub logging path when one is wired (tests may pass
// nil).
func (m *hubHostManager) logf(format string, args ...any) {
	if m.cfg.logf != nil {
		m.cfg.logf(format, args...)
	}
}

// observeEvent records one SSH lifecycle event into the host rows' retained
// attach state (main.go binds it to the sshconn manager's OnEvent). It only
// records: the event arrives with the per-host lock held, so it must never
// call back into the manager.
func (m *hubHostManager) observeEvent(ev sshconn.Event) {
	m.cfg.state.observe(ev)
}

// hostManageHandler wraps one host-management handler with the
// controller-local origin guard at the point of registration, so every method
// this surface wires — present and future — refuses a remote-originated
// request (a peer hub calling over its attach bridge) before it touches the
// registry; a future handler cannot forget it. The manager's own methods keep
// the same guard for direct calls (tests, embedders call the manager as an
// API), which TestHostManageRefusesRemoteOrigin pins.
func hostManageHandler[Req, Resp any](h func(context.Context, Req) (Resp, error)) func(context.Context, Req) (Resp, error) {
	return func(ctx context.Context, params Req) (Resp, error) {
		if err := guardControllerLocalHosts(ctx); err != nil {
			var zero Resp
			return zero, err
		}
		return h(ctx, params)
	}
}

// registerHostManageHandlers installs the add/list/status/remove/update
// host-management handlers. hosts is the one live registry the server constructor resolved —
// the same instance the attach handler validates against — and the manager
// and hub.toml path come from cfg: main.go threads the live sshconn.Manager,
// the live host registry, and the selected config path through WebConfig, so
// the surface is wired in production (a nil manager or config path there —
// tests, embedders — leaves the fallbacks: no channel teardown, no host
// persistence). navigation, when non-nil, is invalidated after successful
// add/remove/update commits so the manifest's sources converge without waiting
// for the next refresh tick. It returns the manager so tests can drive it
// directly.
func registerHostManageHandlers(server *appserver.Server, sources *appsource.Registry, cfg hubcore.WebConfig, hosts *hostreg.Registry, navigation *NavigationService, logf func(format string, args ...any)) *hubHostManager {
	m := newHubHostManager(sources, cfg.RemoteHostSSHManager, cfg, cfg.RemoteHostConfigPath, hosts, logf)
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerHostAdd, hostManageHandler(func(ctx context.Context, params appwire.HostAddParams) (appwire.HostRow, error) {
		row, err := m.Add(ctx, params)
		if err == nil && navigation != nil {
			navigation.Invalidate(navigationChangeHint{Sources: true})
		}
		return row, err
	}))
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerHostList, hostManageHandler(m.List))
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerHostStatus, hostManageHandler(m.Status))
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerHostRemove, hostManageHandler(func(ctx context.Context, params appwire.HostRemoveParams) (appwire.HostRemoveResponse, error) {
		resp, err := m.Remove(ctx, params)
		if err == nil && navigation != nil {
			navigation.Invalidate(navigationChangeHint{Sources: true})
		}
		return resp, err
	}))
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerHostUpdate, hostManageHandler(func(ctx context.Context, params appwire.HostUpdateParams) (appwire.HostUpdateResponse, error) {
		resp, err := m.Update(ctx, params)
		// An edit can change the host's roots, which is what a source's identity
		// addresses; both add and remove invalidate the manifest's sources on
		// commit, and an edit that moves a source must converge the same way
		// rather than wait for the next refresh tick.
		if err == nil && navigation != nil {
			navigation.Invalidate(navigationChangeHint{Sources: true})
		}
		return resp, err
	}))
	return m
}

// hostOnline reports whether host currently has a live channel. It reads the
// attached-only signal without spawning SSH: the dial seam belongs to
// evener/host/attach alone. It is the gate on the no-manager fallback only: a
// manager-backed row never consults it, because attachedClient confirms the
// live channel itself, so the source registry's fail-open default (Online()
// true with no signal wired) cannot mark a host attached that nothing dialed.
func (m *hubHostManager) hostOnline(host string) bool {
	if m.cfg.online != nil {
		return m.cfg.online(host)
	}
	// No signal wired (tests, embedders): fall back to the source registry's
	// own Online, which defaults true with no signal — the pre-06 default.
	// An unknown name is offline: the registry entry exists (the caller
	// resolved it), so a missing source means no channel was ever wired for
	// it. Every source this manager creates carries an explicit signal, so
	// this path cannot recurse.
	if m.cfg.sources == nil {
		return false
	}
	source, ok := m.cfg.sources.Source(host)
	if !ok {
		return false
	}
	online, ok := source.(appsource.OnlineSource)
	if !ok {
		return true
	}
	return online.Online()
}

// hostEntryRow builds the row fields every host row carries from the effective
// entry: the configured values and a clone of roots (never an alias — the row
// crosses the wire and a caller mutating its Roots must not be able to reach
// back into the registry entry). List, status, add, update, and remove rows all
// start from this, so the removal response carries exactly the entry it removed
// instead of a hand-picked subset that can silently drift from the wire shape.
// The origin marker is set here because it is one value: every host lives in
// the machine-managed hub.toml (registry spec 08 §6/§19).
func hostEntryRow(host hostreg.Host) appwire.HostRow {
	return appwire.HostRow{
		Name:       host.Name,
		Address:    host.SSH,
		User:       host.User,
		KeyPath:    host.KeyPath,
		EvenerPath: host.EvenerPath,
		ConfigPath: host.ConfigPath,
		Addr:       host.Addr,
		Roots:      slices.Clone(host.Roots),
		Origin:     hostOriginHubTOML,
	}
}

// hostRow renders one host's list row: the effective entry fields plus live
// state. Attached rows read the live channel's handshake and preflight facts
// through the attached-only lookups and record them as last-known — but only
// the channel paired with this row's entry, never one built from another
// generation of the name (attachedClient); a failed
// lookup keeps the previously retained fields instead of blanking them, so a
// transient probe failure cannot cost a later offline row its facts. Offline
// and in-progress rows render the retained attach state (midAttach,
// lastAttachError) and last-known facts from the record. It never dials:
// every seam here is attached-only. ctx is the caller's handler context — the
// facts read runs on it, so a caller that goes away cancels the read; List,
// Status, and Add build their rows without holding the mutation mutex, so a
// parked facts read holds up no commit either. The one
// mutex the row build itself takes is the brief mutation-mutex hold around
// the fenced retained-state fold at the end — after every network read has
// returned — so the parked read the fence exists for still holds up no
// commit.
func (m *hubHostManager) hostRow(ctx context.Context, host hostreg.Host) appwire.HostRow {
	row := hostEntryRow(host)
	// Attached is reported only when attachedClient confirms a live channel
	// built from the very entry this row renders — a channel under the same
	// name but another registration leaves the row offline rather than
	// borrowing its attached state. With a manager wired that confirmation is
	// the liveness signal itself: attachedClient resolves the channel once,
	// and reading the name again through hostOnline would resolve the same
	// channel a second time. The signal alone would not be sufficient anyway:
	// with no signal wired a hub.toml source fails open (Online() true), and
	// trusting that would render every configured host Attached with no facts
	// behind it — an online row the UI then refuses to Connect because it
	// looks already up. With no manager (tests, embedders) there is no
	// channel identity to confirm, so hostOnline stays the gate on the
	// name-only fallback seam, and no lookup wired leaves the row honestly
	// offline too: nothing can confirm a channel, so nothing may claim one.
	//
	// validity tracks which live lookups refreshed the row's fact fields, so
	// the retention below keeps the previously known values for the fields
	// whose lookup failed instead of blanking them with the failed read's
	// empties.
	var validity hostFactsValidity
	var (
		client   *appwire.Client
		attached bool
	)
	if m.cfg.manager != nil {
		client, attached = m.attachedClient(host)
	} else if m.hostOnline(host.Name) {
		client, attached = m.attachedClient(host)
	}
	if attached && client != nil {
		row.Attached = true
		if m.cfg.handshake != nil {
			if hs, ok := m.cfg.handshake(host.Name, client); ok {
				row.ServerName = hs.ServerInfo.Name
				row.ServerVersion = hs.ServerInfo.Version
				validity.handshake = true
			}
		}
		if m.cfg.facts != nil {
			if facts, err := m.cfg.facts(ctx, host.Name, client); err == nil {
				row.HubVersion = facts.HubVersion
				row.OS = facts.OS
				row.Arch = facts.Arch
				validity.facts = true
			}
			// A facts-read failure keeps the row attached: the dial
			// the attach already completed is authoritative
			// (app_host_attach.go's dial-authoritative rule), and a
			// failed facts read is not a detach. The row's empty fields
			// are the honest render of the failed read; what the row
			// retains for its later offline rows is decided by validity.
		}
	}
	// The retained-state fold and record are fenced on the entry generation
	// the row was built from: the row build runs outside the mutation mutex
	// so a parked facts read holds up no commit, and an unfenced recordKnown
	// would let a delayed row finish after a remove/re-add and recreate the
	// removed host's name-keyed attach record with facts the re-added host's
	// offline rows then rendered as their own. The fence and the state writes
	// share the mutation mutex, so
	// they serialize with both record drops (Remove's finish phase and Add's
	// clean re-add): a row that wins the race records before the removal's
	// drop deletes its record, and a row that loses sees the moved or missing
	// generation and records nothing. Either order leaves the re-added name's
	// record exactly as clean as its own commit left it.
	//
	// The fence also excludes a name whose retained state is in flight. A
	// mutation holds the name's mark from its commit phase to its finish
	// phase, and that span covers the window where an update's new entry is
	// already visible in the registry but its retirement has not run — the
	// swap and the retire hook are adjacent only inside the per-host gate,
	// with the mutation mutex released between them. A concurrent, gate-free
	// row that snapshots the new, higher-generation entry in that window still
	// passes hostEntryCurrent, and gen > retiredThrough still holds because
	// the retire has not run, so without this condition the row would fold the
	// retired identity's midAttach, lastAttachError, and last-known facts.
	// Suppressing both the fold and the record for the whole mark means no row
	// can fold a retired identity's state, and the rows render honestly blank
	// until the mutation's finish phase, after which the next row build folds
	// whatever the mutation left behind. Two consequences are deliberate:
	// during a removal window the host's own row also renders without its
	// retained state — the destination state of a removal anyway — and an
	// attached host's facts lookup that succeeds during the window is simply
	// re-recorded on the next row build.
	m.cfg.mu.Lock()
	if m.hostEntryCurrent(host) && !m.isMutating(host.Name) {
		m.cfg.state.apply(&row, host.Generation)
		if row.Attached {
			m.cfg.state.recordKnown(row, validity, host.Generation)
		}
	}
	m.cfg.mu.Unlock()
	return row
}

// hostEntryCurrent reports whether the live registry still holds host's name
// under the same registration host carries — the registration the row was
// built from, unchanged by any remove or remove/re-add: a re-add is a new
// insert (hostreg's registry-wide generation counter never reuses one), and
// a removed name has no live entry at all. It is hostreg's own
// same-registration predicate, so the row fence and the SSH manager's attach
// identity rechecks cannot drift apart. Callers hold the mutation mutex.
func (m *hubHostManager) hostEntryCurrent(host hostreg.Host) bool {
	return m.cfg.hosts.SameRegistration(host.Name, host)
}

// attachedClient resolves the live client one row may use for entry, pairing
// the channel with the entry the row renders. It reads the manager's
// ChannelIfAttached once and returns that channel's client only while the
// registration the channel was published for is the entry being rendered —
// content and generation both, the predicate hostreg.SameRegistration states
// and sshconn.Channel.MatchesRegistration applies to the channel. A host's live
// state is keyed by name, and the name outlives the registration it names, so
// a row that snapshotted its entry outside the mutation mutex (so a parked
// facts read holds up no commit) must not adopt a channel built from another
// one's attached state and facts; a mismatch renders the row offline. The facts
// and handshake seams it enables still resolve the channel by name, so a
// channel swapped inside that window leaves a row that reports attached with
// empty facts until the next build. With no manager wired (tests, embedders)
// there is no registration to compare, so the row falls back to the name-only
// clientIfAttached seam, whose callers own whatever pairing they built. The
// slice spec's §3.6 carries the rationale in full.
func (m *hubHostManager) attachedClient(entry hostreg.Host) (*appwire.Client, bool) {
	if m.cfg.manager != nil {
		ch, ok := m.cfg.manager.ChannelIfAttached(entry.Name)
		if !ok {
			return nil, false
		}
		if !ch.MatchesRegistration(entry) {
			return nil, false
		}
		return ch.Client(), true
	}
	if m.cfg.clientIfAttached != nil {
		return m.cfg.clientIfAttached(entry.Name)
	}
	return nil, false
}

// registerSource wires entry's appsource source exactly the way startup does
// (newHubSourceRegistry): the Ensure-backed dialing client when one is
// wired, the attached-only client/handshake/facts lookups, and a live online
// signal from the manager — so a host added at runtime is Connect-able and
// serves attached-only reads without a restart. Both paths run through the
// one appsource-level registrar (registerRemoteHubSource in app_rpc.go). With
// no dialing seam (tests, embedders) the client func refuses
// SessionUnavailable while detached, the pre-attach contract a hub.toml host
// already follows. A source that already exists is left alone.
func (m *hubHostManager) registerSource(entry hostreg.Host) {
	if m.cfg.sources == nil {
		return
	}
	if _, ok := m.cfg.sources.Source(entry.Name); ok {
		return
	}
	client := m.cfg.client
	if client == nil {
		client = remoteClientFor(entry.Name)
	}
	// The online seam passed here is the manager's raw signal (cfg.online),
	// never this manager's hostOnline — that reads the source, so wiring it
	// here would recurse. The shared registrar installs the same fail-open
	// default startup's sources get.
	registerRemoteHubSource(m.cfg.sources, m.cfg.remoteCache, entry, remoteHostSourceSeams{
		client:           client,
		clientIfAttached: m.cfg.clientIfAttached,
		handshake:        m.cfg.handshake,
		facts:            m.cfg.facts,
		online:           m.cfg.online,
	})
}

// markMutating records name as having a mutation in flight, in the commit phase
// that already made its durable change. Callers hold mu.
func (m *hubHostManager) markMutating(name string) {
	m.cfg.mutating[name] = struct{}{}
}

// unmarkMutating clears the mark in the finish phase, on the success and the
// failure exit alike: a successful mutation leaves the name mutable again, a
// failed one leaves it fully intact and retryable. Callers hold mu.
func (m *hubHostManager) unmarkMutating(name string) {
	delete(m.cfg.mutating, name)
}

// isMutating reports whether name has a mutation in flight — its durable commit
// landed and its live phase has not finished. Callers hold mu.
func (m *hubHostManager) isMutating(name string) bool {
	_, marked := m.cfg.mutating[name]
	return marked
}

// hostMutationConflict is the typed refusal for a name with a mutation already
// in flight: the conflict code tells the caller the name is transiently held and
// retryable, rather than mislabeling it a duplicate (Add) or file-declared (a
// second Remove). It commits nothing.
func hostMutationConflict(name string) error {
	return appwire.Conflict(fmt.Sprintf("host %q: a mutation is already in progress; retry once it finishes", name))
}

// hostEntryField maps a hostreg validation refusal to the input it blames, in
// the wire spelling the dialog's own inputs use (HostEntry's fields), so a
// message lands on the control the operator can fix. A refusal that blames the
// entry as a whole — a cycle, or a half-specified config_path/addr pair, which
// blames two inputs at once — returns "" and the caller raises it form-level.
func hostEntryField(err error) string {
	switch {
	case errors.Is(err, hostreg.ErrMissingSSH):
		return "address"
	case errors.Is(err, hostreg.ErrAmbiguousSSHUser):
		// The field that made the destination ambiguous: user is set while ssh
		// already carries one, and the message says so.
		return "user"
	case errors.Is(err, hostreg.ErrEmptyRoot):
		return "roots"
	case errors.Is(err, ErrHostAddr):
		return "addr"
	case errors.Is(err, hostreg.ErrInvalidName), errors.Is(err, hostreg.ErrReservedName):
		// Add only: the edit dialog has no name input, so this field is what the
		// add form places.
		return "name"
	default:
		// ErrHostCycle and anything else blame the entry as a whole.
		return ""
	}
}

// hostValidationRefusal turns a hostreg validation refusal into the wire
// refusal: the field-carrying shape when the refusal blames one input, a plain
// InvalidParams when it blames the entry as a whole.
func hostValidationRefusal(name string, err error) error {
	message := fmt.Sprintf("host %q: %v", name, err)
	if field := hostEntryField(err); field != "" {
		return appwire.InvalidHostField(field, message)
	}
	return appwire.InvalidParams(message)
}

// hostEntryToHost converts the wire's configured-host shape to the registry's
// equivalent shape. name is supplied separately because Add takes it from the
// entry while Update takes it from the immutable request target.
func hostEntryToHost(name string, entry appwire.HostEntry) hostreg.Host {
	return hostreg.Host{
		Name:       name,
		SSH:        entry.Address,
		User:       entry.User,
		KeyPath:    entry.KeyPath,
		EvenerPath: entry.EvenerPath,
		ConfigPath: entry.ConfigPath,
		Addr:       entry.Addr,
		Roots:      entry.Roots,
	}
}

// Add registers one host entry: name + SSH address + key path. It
// validates exactly like hub.toml loading (component-03 rules) and refuses a
// name the live set already holds — the duplicate refusal applies
// to live entries only: a removed name is gone, so re-add works — and refuses
// a name whose removal is still in flight, so a re-add cannot race the
// removal's finish.
//
// The commit is durable-first: hub.toml is rewritten before anything is
// exposed, so a write failure commits nothing (no registry entry, no store
// row, no source) and the caller can retry. The name's retained attach state
// resets at the top of the commit, before anything is exposed, so a
// concurrent attach that starts the moment the entry becomes visible cannot
// have its lifecycle state erased by the re-add's own cleanup. Only after
// the write lands does the live set gain the entry — registry, store row, and a fully wired
// source — at which point the host is attachable without a restart. A live
// insert that fails after the write rolls hub.toml back to the pre-add
// contents (rollbackHubTOML), so the durable state never keeps an add the API
// reported as failed; so does a write that fails behind its own rename, which
// leaves the entry durable while nothing is live — no reported-failed add
// can resurrect on the next start.
func (m *hubHostManager) Add(ctx context.Context, params appwire.HostAddParams) (appwire.HostRow, error) {
	if err := guardControllerLocalHosts(ctx); err != nil {
		return appwire.HostRow{}, err
	}
	// The wire's entry is the whole configured host, so the add path stores what
	// the dialog collected instead of the three fields slice 1 carried: the
	// entry is normalized and validated as one record, exactly as hub.toml
	// loading does.
	entry := hostreg.Normalize(hostEntryToHost(params.Entry.Name, params.Entry))
	// Validate without inserting: validateHostEntry runs the exact checks
	// hub.toml loading runs (name grammar, reserved name, ssh destination,
	// user/ssh agreement, non-empty roots, the config_path/addr pair, and the
	// addr host rules) over this one entry without touching live state, so
	// nothing is exposed before the durable save below.
	if err := validateHostEntry(entry); err != nil {
		return appwire.HostRow{}, hostValidationRefusal(entry.Name, err)
	}
	// The commit below is the read-modify-write cycle the mutation mutex
	// exists for; the mutex is released before the response row's facts read,
	// which runs on the network and must not hold up concurrent commits — the
	// same reason List and Status build rows lock-free.
	m.cfg.mu.Lock()
	// A removal of this name in flight must not admit a re-add: Remove's
	// teardown runs without the mutation mutex, and an add landing inside
	// that window would race the removal's finish — a re-registered source
	// or store row the finish then drops, or a live channel for a host
	// being removed. The refusal commits nothing, the hub.toml write included.
	if m.isMutating(entry.Name) {
		m.cfg.mu.Unlock()
		return appwire.HostRow{}, hostMutationConflict(entry.Name)
	}
	if _, ok := m.cfg.hosts.Get(entry.Name); ok {
		m.cfg.mu.Unlock()
		return appwire.HostRow{}, appwire.InvalidParams(fmt.Sprintf("host %q: %v", entry.Name, hostreg.ErrDuplicateHost))
	}
	// A re-added name starts with no stale record, and the reset has to run
	// HERE — before the save and the registry insert expose anything: the
	// lifecycle events a concurrent attach delivers land through
	// observeEvent, which takes no mutation mutex, so once the entry is
	// visible an attach can record its midAttach or lastAttachError at any
	// moment, and a drop that runs after the insert erases that record along
	// with the stale one. Before the insert no record
	// for the new generation can exist — every attach path requires the live
	// entry — so a reset placed here sweeps exactly the stale state the
	// re-add must not inherit, and nothing else.
	m.cfg.state.remove(entry.Name)
	// Durable commit first: hub.toml is the record of truth for every host,
	// so the entry is exposed only after the write landed. The
	// pre-save snapshot is the rollback copy: should the live insert below
	// fail, the file must not keep the entry (a failed add resurrecting on
	// the next start).
	prev := m.cfg.store.snapshot()
	if err := m.persistOrCompensate(append(prev, entry), prev); err != nil {
		// A failure the rename already committed is compensated back to the
		// pre-add contents (persistOrCompensate), so the refusal cannot stand
		// as a pre-commit one and the next start cannot resurrect an add this
		// call reports as failed.
		m.cfg.mu.Unlock()
		return appwire.HostRow{}, err
	}
	if err := m.addHostToRegistry(entry); err != nil {
		// The live insert refused — e.g. an SSH manager with no registry, the
		// one seam that can fail here — so the API reports failure and the
		// file rolls back to the pre-add contents: a retry starts from the
		// same durable state, instead of the next start silently completing
		// an add this call reported as failed. The rollback still runs under
		// the mutex: it is part of the commit, and a concurrent Add's own
		// save must not interleave with restoring the file.
		err = m.rollbackHubTOML(prev, err)
		m.cfg.mu.Unlock()
		return appwire.HostRow{}, err
	}
	// The response row's retained-state fold is fenced on the entry's
	// generation (hostRow), which the registry stamps at insert — the local
	// entry predates the stamp. Reread the stored entry so the row carries the
	// identity this call committed; a concurrent remove that already dropped
	// it leaves the local entry, whose stale generation then fails the fence
	// and renders the row without retained state — nothing a host that no
	// longer exists may claim anyway.
	if stored, ok := m.cfg.hosts.Get(entry.Name); ok {
		entry = stored
	}
	m.cfg.store.add(entry)
	m.registerSource(entry)
	m.cfg.mu.Unlock()
	// The commit is complete, so the row reads a fully added host — the entry
	// this call committed, whatever concurrent mutations do around it.
	return m.hostRow(ctx, entry), nil
}

// addHostToRegistry inserts entry into the live registry. With the SSH
// manager wired it goes through the manager's AddHost, which registers under
// the same per-host gate the attach paths and RemoveHost coordinate on, so a
// remove/re-add of a name can never interleave with an in-flight attach for
// it; without one (tests, embedders) the registry's own Add is the whole
// story, since nothing can be mid-attach through this hub.
func (m *hubHostManager) addHostToRegistry(entry hostreg.Host) error {
	if m.cfg.manager != nil {
		return m.cfg.manager.AddHost(entry)
	}
	return m.cfg.hosts.Add(entry)
}

// remoteClientFor returns the refusing client func for a host whose source
// has no dialing seam (an embedder or a test built without
// cfg.RemoteHostClient): the source serves attached-only reads until the
// first explicit Connect, exactly like a hub.toml host before its first
// attach.
func remoteClientFor(host string) func(ctx context.Context, _ string) (*appwire.Client, error) {
	return func(_ context.Context, _ string) (*appwire.Client, error) {
		return nil, appwire.SessionUnavailable(fmt.Sprintf("host %q is not attached", host))
	}
}

// dropHostDerivedState retires everything keyed to name once its entry is
// gone: the source registration, the name-keyed attach record, the remote-thread
// cache entry (its per-source generation included), then the web server's
// retained last-known-good list. The order is load-bearing: an in-flight walk
// must fail its cache-generation sweep or its source-ownership check, so it
// cannot re-store obsolete rows after the cache drop. The retained list must go
// too — left behind, the entry and its thread rows outlive the host for the
// process lifetime, and churning distinct host names grows the map without
// bound. Callers have already dropped (or committed the drop of) the store row,
// so nothing renders the name again, and they hold the mutation mutex.
func (m *hubHostManager) dropHostDerivedState(name string) {
	if m.cfg.sources != nil {
		m.cfg.sources.Remove(name)
	}
	m.cfg.state.remove(name)
	if m.cfg.remoteCache != nil {
		m.cfg.remoteCache.RemoveSource(name)
	}
	if m.cfg.forgetLastGoodThreads != nil {
		m.cfg.forgetLastGoodThreads(name)
	}
}

// persistOrCompensate writes entries durably and, when the write's rename
// already committed before its directory step failed, compensates the live
// state back with previous — the content the file must hold for the live set
// and the file to stay in step — before returning the failure. A plain
// pre-rename refusal wrote nothing and is returned unchanged.
func (m *hubHostManager) persistOrCompensate(entries, previous []hostreg.Host) error {
	if err := m.persistHosts(entries); err != nil {
		if hubTOMLRenameCommitted(err) {
			return m.rollbackHubTOML(previous, err)
		}
		return err
	}
	return nil
}

// persistHosts rewrites hub.toml durably, refusing while the store is known
// incomplete (a legacy sidecar that failed to load or migrate): rewriting the
// file then would clobber the entries that never made it into memory. Callers
// treat the refusal as fatal (add/remove report it; the entry stays un-exposed
// or the host stays intact), so the operator hears about the broken file
// instead of losing it. A failure the rename already committed
// (hubTOMLPostRenameError) is not a plain refusal — the file holds the new
// entries — so the callers compensate the live state before reporting it.
func (m *hubHostManager) persistHosts(entries []hostreg.Host) error {
	return m.persistHostsMarked(entries, false)
}

// persistHostsMarked is persistHosts with the migration's marker; only the
// migration passes true.
func (m *hubHostManager) persistHostsMarked(entries []hostreg.Host, migrated bool) error {
	if err := m.cfg.store.poisoned(); err != nil {
		return fmt.Errorf("hub.toml %s not rewritten: %w (fix the legacy host sidecar or remove its unloaded entries first)", m.cfg.configPath, err)
	}
	return writeHubTOMLHostsMarked(m.cfg.configPath, entries, migrated)
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
func (m *hubHostManager) rollbackHubTOML(previous []hostreg.Host, cause error) error {
	if err := m.persistHosts(previous); err != nil {
		if hubTOMLRenameCommitted(err) {
			// The rollback's own rename landed: the file holds previous, the
			// content the rollback exists to restore, and only its
			// durability step failed — the file and the live set agree. Say
			// that beside the cause rather than claiming a rollback failure
			// that did not happen.
			return fmt.Errorf("%w; hub.toml rollback landed but its directory step failed: %w", cause, err)
		}
		return fmt.Errorf("%w; hub.toml rollback failed: %w", cause, err)
	}
	return cause
}

// List returns every known host with truthful online state in name-sorted
// order (the registry's own; every row's origin field reads `hub.toml`).
// Attached rows report live channel facts and retain them
// as last-known; rows without a live channel render as offline with the
// retained attach state and last-known facts. It never dials.
//
// The mutation mutex covers only the snapshot — the registry rows — so a
// concurrent reader never observes the window between a registry insert and
// the store row and source registration that finish it: a half-committed host
// would list with no source.
// The row building that follows runs lock-free: hostRow's
// attached-only lookups and its facts read run on the network, and one slow
// or hung host must not block every concurrent Add and Remove commit or
// serialize other lists. The rows are the snapshot's point-in-time view: a
// host added after the snapshot is absent from that response, never
// half-committed in it. A host mid-removal still lists while its registry
// entry lasts — the removal mark stands in for the store row its commit
// already dropped — and every row carries the one origin marker, because every
// host lives in the machine-managed hub.toml.
func (m *hubHostManager) List(ctx context.Context, _ appwire.EmptyParams) (appwire.HostListResponse, error) {
	if err := guardControllerLocalHosts(ctx); err != nil {
		return appwire.HostListResponse{}, err
	}
	m.cfg.mu.Lock()
	hosts := m.cfg.hosts.All()
	m.cfg.mu.Unlock()
	// rows is non-nil even when no host is registered: make returns a usable
	// empty slice, and the wire type's non-nullable hosts array must never
	// marshal as JSON null.
	rows := make([]appwire.HostRow, 0, len(hosts))
	for _, host := range hosts {
		rows = append(rows, m.hostRow(ctx, host))
	}
	return appwire.HostListResponse{Hosts: rows}, nil
}

// Status returns one host's row: the same HostRow evener/host/list serves.
// Unknown names are InvalidParams. Never dials. It snapshots the host under
// the same mutation mutex List does, for the same
// fully-committed-row guarantee, and then builds the row lock-free for the
// same reason List does: the facts read must not hold up commits.
func (m *hubHostManager) Status(ctx context.Context, params appwire.HostStatusParams) (appwire.HostStatusResponse, error) {
	if err := guardControllerLocalHosts(ctx); err != nil {
		return appwire.HostStatusResponse{}, err
	}
	name := strings.TrimSpace(params.Name)
	m.cfg.mu.Lock()
	host, ok := m.cfg.hosts.Get(name)
	if !ok {
		m.cfg.mu.Unlock()
		return appwire.HostStatusResponse{}, appwire.InvalidParams(fmt.Sprintf("unknown host %q", name))
	}
	m.cfg.mu.Unlock()
	return appwire.HostStatusResponse{Host: m.hostRow(ctx, host)}, nil
}

// Remove deregisters one live host entry: its store row, its source, its
// registry entry, and its channel all go, and the name is gone until
// re-added. Every live host is removable — hub.toml is the machine-managed
// store the hub writes, so there is no file-declared class to protect
// (registry spec 08 §6/§19); unknown names are InvalidParams; a removal
// already in flight for the name is a Conflict.
//
// The removal runs in three phases. The commit phase holds the mutation
// mutex: validation, the durable-first store write (hub.toml loses the entry
// before any live state changes, so a write failure leaves the host fully
// intact and the caller can retry — a failure behind the write's own rename
// included, compensated back to the live contents before the refusal
// returns), the store row's drop in the same
// critical section (the store is what every later save derives its contents
// from, so a row kept past the save would let a concurrent Add or Remove
// re-persist an entry this removal already committed), and the mutation mark
// that fences the name. The mutex then releases for the teardown itself: the
// manager-owned RemoveHost blocks on the per-host gate a supervisor holds for
// a whole reconnect/ensure cycle and on the ssh child's exit, so holding the
// mutation mutex across it would freeze every concurrent host/list,
// host/status, and host/add behind one host's removal.
// The mark fences the window instead: Add, Update, and a second Remove refuse
// the name, so nothing re-exposes, edits, or double-tears a host whose removal
// is in flight. The finish phase retakes the mutex, clears the mark, and
// completes the bookkeeping. A teardown that fails rolls hub.toml forward
// again to keep the entry (rollbackHubTOML over the live snapshot), so the
// durable state never forgets a host the live set still holds, and the cleared
// mark leaves the name retryable. Nothing is resurrected: the entry, its source,
// its channel, and its retained attach state are all gone by the time a
// successful Remove returns — and so are its cached remote-thread rows, so
// its sessions stop rendering with the removal instead of lingering live
// until the refresher's next tick.
func (m *hubHostManager) Remove(ctx context.Context, params appwire.HostRemoveParams) (appwire.HostRemoveResponse, error) {
	if err := guardControllerLocalHosts(ctx); err != nil {
		return appwire.HostRemoveResponse{}, err
	}
	name := strings.TrimSpace(params.Name)
	// Commit phase: the removal's durable state and the in-memory store
	// change together, under the mutation mutex, as one read-modify-write
	// cycle.
	m.cfg.mu.Lock()
	if m.isMutating(name) {
		m.cfg.mu.Unlock()
		return appwire.HostRemoveResponse{}, hostMutationConflict(name)
	}
	host, ok := m.cfg.hosts.Get(name)
	if !ok {
		m.cfg.mu.Unlock()
		return appwire.HostRemoveResponse{}, appwire.InvalidParams(fmt.Sprintf("unknown host %q", name))
	}
	// Persist first: the durable store loses the entry before any live
	// state changes, so a save failure resurrects nothing — the host stays
	// fully intact and the caller can retry. A failure the rename already
	// committed leaves the file without the entry while the live set still
	// holds the host, so it is compensated back to the live contents before
	// the refusal is returned — the store row is still in place (it drops
	// only after a successful save), so the snapshot still carries the entry.
	if err := m.persistOrCompensate(m.cfg.store.without(host.Name), m.cfg.store.snapshot()); err != nil {
		m.cfg.mu.Unlock()
		return appwire.HostRemoveResponse{}, err
	}
	// The row drops with the save it belongs to, in the same critical
	// section: a concurrent Add or Remove committing in the window below
	// derives its save from the store, and a row still present here would
	// re-persist an entry this removal already committed — the next start
	// would resurrect the host this call is removing.
	m.cfg.store.remove(host.Name)
	// The mark fences the name for the window the mutex is about to release.
	m.markMutating(host.Name)
	m.cfg.mu.Unlock()

	// Teardown, mutex-free: with a manager wired, RemoveHost drops the
	// registry entry, stops the supervisor, and clears the channel under the
	// host lock in one step, so a concurrent Ensure cannot publish a fresh
	// channel after deregistration; without one (tests, embedders) the
	// registry's own Remove is the whole story.
	var teardownErr error
	if m.cfg.manager != nil {
		if err := m.cfg.manager.RemoveHost(host.Name); err != nil {
			teardownErr = fmt.Errorf("remove host %q: %w", host.Name, err)
		}
	} else if err := m.cfg.hosts.Remove(host.Name); err != nil {
		teardownErr = err
	}

	// Finish phase: the mutex comes back for the bookkeeping, and the mark
	// clears on both exits — a success leaves the name addable again, a
	// failure leaves it fully intact and retryable.
	m.cfg.mu.Lock()
	m.unmarkMutating(host.Name)
	if teardownErr != nil {
		// The teardown failed before dropping anything live (RemoveHost
		// refuses ahead of its registry step), so the removal un-commits: the
		// store row returns and the file regains it. The rollback saves the
		// live snapshot, not a pre-remove copy: concurrent Adds and Removes
		// may have committed in the window, and their entries must survive.
		m.cfg.store.add(host)
		err := m.rollbackHubTOML(m.cfg.store.snapshot(), teardownErr)
		m.cfg.mu.Unlock()
		return appwire.HostRemoveResponse{}, err
	}
	// The name's derived state — source, attach record, remote-thread cache
	// rows, and the retained last-known-good list — goes with the entry, in
	// the order dropHostDerivedState documents. The cache rows matter here
	// in particular: the refresher's next tick is up to 30s away, and
	// sourceOnline fail-opens for the now-unregistered source ID, so those
	// rows would keep rendering the removed host's sessions as live until
	// then; the host can serve no future refresh, so they go now — and the
	// host's registration generation drops with them, so a refresh walking
	// while the removal committed cannot republish them.
	m.dropHostDerivedState(host.Name)
	m.cfg.mu.Unlock()
	// The removal row carries the whole effective entry it removed — the same
	// fields a list or status row renders — not the name/address/key subset
	// slice 1 needed: the wire's HostRow now carries the configured entry, so a
	// removal response that dropped User, EvenerPath, ConfigPath, Addr, or Roots
	// would be a different shape than every other row for the same host.
	removedRow := hostEntryRow(host)
	removedRow.Removed = true
	return appwire.HostRemoveResponse{Host: removedRow}, nil
}

// Update applies one edit to a live host entry: the durable hub.toml entry,
// the store row, the live registry entry, and the host's channel. Name is
// immutable — it is the target this call addresses, never a value it changes,
// because it keys source IDs, cached rows, manager state, and the file's own
// entries. Every live host is editable — hub.toml is the machine-managed store
// the hub writes, so there is no file-declared class to protect (registry spec
// 08 §6/§19); unknown names are InvalidParams; a mutation already in flight for
// the name is a Conflict.
//
// The three phases mirror Remove's, which is what keeps the durable-first order
// and the compensation paths in one shape:
//
//   - Commit, under the mutation mutex: refuse an in-flight mutation on the
//     name, require a live entry, validate the entry BEFORE anything is
//     written — validateHostEntry is the same call the add flow runs, so a
//     refusal commits nothing and an entry the registry would reject never
//     reaches the file — persist durable-first with one atomic write that
//     replaces the entry in place, replace the store row in the same critical
//     section, set the mark, release the mutex.
//   - Live, mutex-free: with a manager wired, manager.UpdateHost replaces the
//     registry entry and retires the channel under the per-host gate as one
//     atomic step; without one, read the pre-swap entry, call the registry's own
//     Update, and retire the name's retained attach record by that entry's
//     generation — the generation-scoped clear the manager path's hook performs,
//     so a stale row racing the clear is still fenced. An error means nothing in
//     the registry or manager changed: the registry refuses ahead of its own
//     swap.
//   - Finish, under the mutex: clear the mark, compensate a failed live phase
//     by rolling hub.toml back to the live set as it stands now — not a
//     pre-commit copy, so a concurrent add or removal that committed in this
//     window survives — and, on success, compare the roots this call replaced
//     with the stored entry's. A roots edit drops the remote-thread cache entry,
//     retires the old source, clears its retained last-known-good list, then
//     registers the source afresh before building the response row from the
//     entry the registry now holds. Retiring both identities before the clear
//     fences any in-flight old-source walk out of re-storing obsolete rows.
//
// An edit never dials, deploys, or attaches: its live effects are exactly the
// registry replacement, the teardown, and the derived-state reconciliation.
func (m *hubHostManager) Update(ctx context.Context, params appwire.HostUpdateParams) (appwire.HostUpdateResponse, error) {
	if err := guardControllerLocalHosts(ctx); err != nil {
		return appwire.HostUpdateResponse{}, err
	}
	name := strings.TrimSpace(params.Name)
	// params.Entry.Name is deliberately not read: the update's target is
	// params.Name, and not reading the entry's own name is what makes a rename
	// unrepresentable rather than merely refused. Name is immutable — it keys
	// source IDs, cached rows, manager state, and the file's own entries — so the
	// request has nowhere to put a new one.
	entry := hostreg.Normalize(hostEntryToHost(name, params.Entry))
	// Commit phase: the durable state and the in-memory store change together,
	// under the mutation mutex, as one read-modify-write cycle. Its refusals are
	// ordered as the surface specifies: the in-flight mark first (a conflict — the
	// name is transiently held and the caller retries), then the target itself —
	// a live entry, so a gone or tombstone-only name is not found — and only then the
	// entry's shape. Resolving the target before validating the entry is what
	// keeps a generic field refusal from masking the more specific target refusal
	// an invalid entry aimed at a hub.toml name or an unknown name would
	// otherwise hide.
	m.cfg.mu.Lock()
	if m.isMutating(name) {
		m.cfg.mu.Unlock()
		return appwire.HostUpdateResponse{}, hostMutationConflict(name)
	}
	before, ok := m.cfg.hosts.Get(name)
	if !ok {
		m.cfg.mu.Unlock()
		return appwire.HostUpdateResponse{}, appwire.InvalidParams(fmt.Sprintf("unknown host %q", name))
	}
	// Validate before the write: a refusal here commits nothing, and an entry the
	// registry would reject never reaches the file. The registry re-runs the same
	// validation under its own lock; this check is what keeps the file clean, not
	// a substitute for it. It runs last of the commit-phase refusals so an invalid
	// entry still gets the target's own refusal above, and still before the
	// durable save so nothing is written when it refuses.
	if err := validateHostEntry(entry); err != nil {
		m.cfg.mu.Unlock()
		return appwire.HostUpdateResponse{}, hostValidationRefusal(name, err)
	}
	// Persist first: the durable store holds the edited entry before any live
	// state changes, so a save failure leaves the host fully intact and the
	// caller can retry. A failure the rename already committed leaves the file
	// holding the edit while the live set still holds the old entry, so it is
	// compensated back before the refusal returns.
	if err := m.persistOrCompensate(m.cfg.store.withReplaced(entry), m.cfg.store.snapshot()); err != nil {
		m.cfg.mu.Unlock()
		return appwire.HostUpdateResponse{}, err
	}
	// The store row moves with the save it belongs to, in the same critical
	// section: a concurrent Add or Remove committing in the window below derives
	// its save from the store, and a row still holding the old entry would
	// re-persist it, undoing the edit on the next start.
	m.cfg.store.replace(entry)
	m.markMutating(name)
	m.cfg.mu.Unlock()

	// Live phase, mutex-free: the manager's swap blocks on the per-host gate a
	// supervisor can hold for a whole reconnect/ensure cycle, so holding the
	// mutation mutex across it would freeze every concurrent host/list,
	// host/status, and host/add. The mark fences the window instead.
	var liveErr error
	if m.cfg.manager != nil {
		// The retiring identity's name-keyed attach record is retired from inside
		// the manager's own gate hold, in the same hold as the swap: UpdateHost
		// runs this hook after it has replaced the registry entry and torn down the
		// retired identity's channel, and still before it releases the gate,
		// handing it the entry the swap replaced. The retired identity's Detached
		// is emitted earlier in that same hold, and no lifecycle event for the new
		// identity can interleave between the swap and the retirement, because
		// every event is delivered synchronously with the gate held.
		//
		// The retirement is generation-scoped rather than a mere delete. A row that
		// captured the still-current old entry before the swap can be parked
		// outside this gate (the row build runs without the mutation mutex), pass
		// hostEntryCurrent before the swap, and only reach its state write after
		// this hook deleted the record — a late write that a bare remove would let
		// recreate the retired identity's facts. Marking the retired entry's
		// generation (retired.Generation) fences that row: its generation is at or
		// below the mark, so its apply folds nothing and its recordKnown writes
		// nothing, while a row built from the new identity's higher generation is
		// served and records normally. The hook must not call back into the
		// manager (the gate is non-reentrant) and must not take the mutation mutex
		// (another goroutine may hold it while parked on this gate) — it only
		// touches the record state's own lock.
		if err := m.cfg.manager.UpdateHost(entry, func(retired hostreg.Host) {
			m.cfg.state.retire(name, retired.Generation)
		}); err != nil {
			liveErr = fmt.Errorf("update host %q: %w", name, err)
		}
	} else {
		// No sshconn manager is wired (tests, embedders), so no lifecycle event can
		// exist for the new identity: retiring inline on the successful swap gives
		// the same guarantee the manager path's hook holds, and this also clears an
		// offline host's retained error and facts when an address edit has no
		// channel to tear down. The pre-swap entry's generation is read immediately
		// before the swap — the mark fences rows built from it, exactly as the
		// manager path fences rows built from the entry its swap replaced, so a
		// stale row still racing the clear cannot resurrect the retired identity's
		// state even with no manager.
		prior, _ := m.cfg.hosts.Get(name)
		if err := m.cfg.hosts.Update(entry); err != nil {
			liveErr = err
		} else {
			m.cfg.state.retire(name, prior.Generation)
		}
	}

	// Finish phase: the mutex comes back for the bookkeeping, and the mark clears
	// on both exits.
	m.cfg.mu.Lock()
	m.unmarkMutating(name)
	if liveErr != nil {
		// The live phase refused ahead of changing anything, so the edit
		// un-commits: the store row goes back to the live entry and the file
		// follows it. The rollback saves the live snapshot, not a pre-commit
		// copy: concurrent Adds and Removes may have committed in the window,
		// and their entries must survive.
		//
		// An absent live entry makes the un-commit a removal, exactly as the
		// finish phase's vanished arm below: a directly driven registry can drop
		// the name while this edit runs (the mutation mark only fences this
		// manager's own paths), and restoring the committed row would then write
		// the edit for a name that is not live — an edit a later Add duplicates
		// and the next hub.toml load rejects. The committed row is dropped before
		// the snapshot is taken, so the rollback writes the live set without it.
		if live, ok := m.cfg.hosts.Get(name); ok {
			m.cfg.store.replace(live)
		} else {
			m.cfg.store.remove(name)
			// The name's derived state goes with the dropped row, exactly as the
			// finish phase's vanished arm below retires it and in the order
			// dropHostDerivedState documents. The committed row is already gone,
			// so nothing renders this name again.
			m.dropHostDerivedState(name)
		}
		err := m.rollbackHubTOML(m.cfg.store.snapshot(), liveErr)
		m.cfg.mu.Unlock()
		return appwire.HostUpdateResponse{}, err
	}
	stored, ok := m.cfg.hosts.Get(name)
	if !ok {
		// A concurrent removal took the name while this edit's teardown ran —
		// impossible for the same name through this manager (the mark fences it),
		// but a directly driven registry could still have dropped it: the entry
		// is gone, so there is no row to render. Remove the committed store row
		// and save that live snapshot before refusing; otherwise a later Add would
		// append beside the stale edit and poison the next hub.toml load as a
		// duplicate name.
		refusal := appwire.InvalidParams(fmt.Sprintf("unknown host %q", name))
		m.cfg.store.remove(name)
		// The name's derived state goes with it, exactly as Remove's finish
		// phase retires it and in the order dropHostDerivedState documents. The
		// store row is already gone, so nothing renders this name again.
		m.dropHostDerivedState(name)
		err := m.rollbackHubTOML(m.cfg.store.snapshot(), refusal)
		m.cfg.mu.Unlock()
		return appwire.HostUpdateResponse{}, err
	}
	// The source's identity owns its derived rows, so a roots edit changes what
	// the source addresses and everything keyed to the old roots goes with it:
	// the remote-thread cache entry (its per-source generation included), the
	// source itself, and the web server's retained last-known-good list, after
	// which the source is registered afresh under the new roots. Both identities
	// must retire before retention is cleared: an in-flight old-source walk then
	// fails either its cache-generation fence or its source-instance ownership
	// check and cannot re-store obsolete rows after the clear. The cache drop also
	// stays before registerSource, so it cannot delete the generation the fresh
	// registration mints.
	//
	// A non-roots edit changes nothing the source's identity owns: it is the same
	// source, its cache generation still owns its rows, and the retained list is
	// still this host's — an edit of the SSH address or a path must not blank the
	// host's sessions in the tree.
	if !slices.Equal(before.Roots, stored.Roots) {
		if m.cfg.remoteCache != nil {
			m.cfg.remoteCache.RemoveSource(name)
		}
		if m.cfg.sources != nil {
			m.cfg.sources.Remove(name)
		}
		if m.cfg.forgetLastGoodThreads != nil {
			m.cfg.forgetLastGoodThreads(name)
		}
		m.registerSource(stored)
	}
	m.cfg.mu.Unlock()
	// The row's retained-state fold is fenced on the entry's generation, so the
	// reread above is what makes the returned row the identity this call
	// committed.
	return appwire.HostUpdateResponse{Host: m.hostRow(ctx, stored)}, nil
}
