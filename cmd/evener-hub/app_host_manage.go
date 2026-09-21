package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/fsdurability"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
	"primeradiant.com/evener/internal/appserver"
)

// hostOriginHubTOML and hostOriginSidecar are the HostRow origin markers: the
// entry's source after the hub.toml-then-sidecar merge. hub.toml is
// authoritative for its own names; the sidecar is the UI's only writable
// source.
const (
	hostOriginHubTOML = "hub.toml"
	hostOriginSidecar = "sidecar"
)

// hostSidecarFileName is the managed sidecar beside the selected hub.toml that
// carries UI-added host entries. hub.toml stays hand-authored (a TOML
// re-marshal would strip its comments), so the UI never rewrites it; the hub
// loads this file after hub.toml and merges the two sets.
const hostSidecarFileName = "hub.hosts.json"

// hostManagerConfig carries what the host-management handlers need. The
// registry is the controller's live one in production — the same
// *hostreg.Registry the SSH manager dials through and the attach handler
// validates against — so a host added at runtime is attachable without a
// restart. The stores are live: the hub owns them, mutations swap entries
// under mu, and reads never dial.
type hostManagerConfig struct {
	// hosts is the live host registry: hub.toml entries at boot plus sidecar
	// entries as they are added. Mutations hold mu; the SSH manager and the
	// attach handler consult the same instance in production.
	hosts *hostreg.Registry
	// sidecar holds the UI-added entries in add order. It is the durable side
	// of hosts for sidecar names; hub.toml names are never recorded here.
	sidecar *hostSidecarStore
	// sidecarPath is the sidecar file beside the selected hub.toml. Empty
	// (tests, embedders without a file) disables persistence: the store stays
	// memory-only and every method still works.
	sidecarPath string
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
	// RemoveHost so a concurrent attach cannot publish past deregistration.
	// Nil in tests that only exercise validation.
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
	// and status resolve through. Nil leaves rows without live facts.
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
	// logf is the hub's logging path for sidecar load problems; nil drops
	// the lines (tests that never load a broken file).
	logf func(format string, args ...any)
	// mu serializes add/remove read-modify-write cycles so concurrent calls
	// cannot lose updates or interleave a save with a registry mutation.
	mu sync.Mutex
	// removing holds the names whose removal is in flight: the durable sidecar
	// save already dropped the entry, and the sidecar row left the store with
	// it in the same commit phase (so no later save can re-persist it), while
	// the channel teardown runs without mu held. Add and a second Remove
	// refuse a marked name until the removal's finish phase clears the mark,
	// so the released window cannot admit a re-add that races the finish or a
	// second teardown of the same host. Guarded by mu.
	removing map[string]struct{}
}

// hostSidecarStore is the durable sidecar: UI-added entries in add order.
// The zero value is usable; all methods are safe for concurrent use. Callers
// that also mutate the registry hold hostManagerConfig.mu across both, so the
// two cannot drift apart under concurrency.
type hostSidecarStore struct {
	mu      sync.Mutex
	entries []hostreg.Host
	// loadErr records why the on-disk sidecar cannot be treated as fully
	// loaded: the file failed to parse, or an entry failed validation. While
	// it is set the in-memory snapshot is known incomplete, so saves refuse —
	// rewriting the file would clobber the entries that never made it into
	// memory — and add/remove fail loudly instead of silently losing them.
	loadErr error
}

// poison records err as the reason the on-disk sidecar is not fully loaded.
// The first reason wins; later ones only add log lines at the call site.
func (s *hostSidecarStore) poison(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr == nil {
		s.loadErr = err
	}
}

// poisoned reports why the sidecar's on-disk entries cannot be trusted as
// fully loaded, or nil when every entry is loaded (or there is no file).
func (s *hostSidecarStore) poisoned() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadErr
}

// hostSidecarFile is the on-disk shape of the sidecar: entries in add order.
type hostSidecarFile struct {
	Hosts []hostSidecarFileEntry `json:"hosts"`
}

// hostSidecarFileEntry is one persisted sidecar entry: the seven HostConfig
// fields in snake_case (hub.toml's spelling for the same fields) plus the key
// path. The sidecar never crosses the wire — the hub is its only writer and
// reader — so it follows the repo's snake_case json default; the appwire
// package's camelCase HostRow is what clients see.
type hostSidecarFileEntry struct {
	Name       string   `json:"name"`
	SSH        string   `json:"ssh"`
	User       string   `json:"user,omitempty"`
	EvenerPath string   `json:"evener_path,omitempty"`
	ConfigPath string   `json:"config_path,omitempty"`
	Addr       string   `json:"addr,omitempty"`
	Roots      []string `json:"roots,omitempty"`
	KeyPath    string   `json:"key_path,omitempty"`
}

// sidecarPathFor returns the sidecar path beside the selected hub.toml. An
// empty config path (tests, embedders without a file) disables persistence:
// the store stays memory-only and every method still works.
func sidecarPathFor(configPath string) string {
	if strings.TrimSpace(configPath) == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(configPath), hostSidecarFileName)
}

// loadHostSidecar reads path into entries in file order. A missing file is an
// empty sidecar, not an error: a hub that never added a host has no file. A
// present file must carry a "hosts" array: an absent or null field is a load
// error, not an empty sidecar — a present empty array is the legitimate one.
func loadHostSidecar(path string) ([]hostreg.Host, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read host sidecar: %w", err)
	}
	// The shape gate runs before the typed decode: `hosts` must be present and
	// non-null. A JSON-valid document without a usable array — {} or
	// {"hosts":null} — decodes to an empty Hosts below, and loading it as "no
	// entries" without poisoning the store would let the next add rewrite the
	// file from an empty snapshot, silently discarding whatever the broken
	// document carried. Missing or null is a load error
	// — loud, file preserved, the corrupt-file contract — while a present
	// array loads as before, an empty one included. A non-array `hosts`
	// passes the gate and still fails the typed decode below.
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(data, &shape); err != nil {
		return nil, fmt.Errorf("parse host sidecar: %w", err)
	}
	if hosts, ok := shape["hosts"]; !ok || string(hosts) == "null" {
		return nil, errors.New(`parse host sidecar: missing or null "hosts" array`)
	}
	var file hostSidecarFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parse host sidecar: %w", err)
	}
	entries := make([]hostreg.Host, 0, len(file.Hosts))
	for _, h := range file.Hosts {
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

// saveHostSidecar persists entries atomically: a 0600 temp file in the same
// directory, fsynced, renamed over the target, and the directory itself
// synced after the rename, so a crash lands the old file or the new one,
// never a half-write or a lost rename — without the directory sync, a
// successful add/remove could still vanish in a power failure despite the
// synced temp file. The rename is the save's commit point: a failure before
// it writes nothing, while a failure behind it
// (hostSidecarPostRenameError) means the file already holds the new entries
// and the caller owes the live state a compensation. 0600 keeps key paths
// from ever landing world-readable. An empty path (no config file) skips the
// write; the in-memory store stays authoritative for the process lifetime.
func saveHostSidecar(path string, entries []hostreg.Host) error {
	if path == "" {
		return nil
	}
	file := hostSidecarFile{Hosts: make([]hostSidecarFileEntry, 0, len(entries))}
	for _, e := range entries {
		file.Hosts = append(file.Hosts, hostSidecarFileEntry{
			Name:       e.Name,
			SSH:        e.SSH,
			User:       e.User,
			EvenerPath: e.EvenerPath,
			ConfigPath: e.ConfigPath,
			Addr:       e.Addr,
			Roots:      e.Roots,
			KeyPath:    e.KeyPath,
		})
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal host sidecar: %w", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("host sidecar mkdir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".hub.hosts.json-*.tmp")
	if err != nil {
		return fmt.Errorf("host sidecar temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("host sidecar chmod: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("host sidecar write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("host sidecar sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("host sidecar close: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("host sidecar rename: %w", err)
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
	// though nothing was written. It is wrapped in hostSidecarPostRenameError,
	// and the callers compensate the live state (rollbackSidecar) before
	// reporting the failure.
	if err := hostSidecarSyncDir(filepath.Dir(path)); err != nil {
		return &hostSidecarPostRenameError{err: err}
	}
	return nil
}

// hostSidecarSyncDir opens dir, syncs it, and closes it — the durability half
// of the atomic-rename idiom, so a crash right after a sidecar rename cannot
// lose the committed add or remove. It is a swappable package variable so
// tests can force the post-rename failure path deterministically, the one
// point where a failed save has already replaced the target file; the default
// keeps the tolerance for filesystems that cannot sync a directory at all.
var hostSidecarSyncDir = func(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("host sidecar directory: %w", err)
	}
	if err := d.Sync(); err != nil && !hostSidecarSyncUnsupported(err) {
		_ = d.Close()
		return fmt.Errorf("host sidecar directory sync: %w", err)
	}
	if err := d.Close(); err != nil {
		return fmt.Errorf("host sidecar directory close: %w", err)
	}
	return nil
}

// hostSidecarPostRenameError marks a save failure that followed the rename
// that replaced the sidecar file: the new entries are already the file's
// contents — only the directory sync that makes the rename durable failed. A
// save returning one is not a refusal that wrote nothing: the callers must
// bring the file and the live set back into step (rollbackSidecar) before
// returning the failure, so an add the API reports as failed cannot
// resurrect from the file on the next start, and a removal the API reports
// as failed cannot lose its host there.
type hostSidecarPostRenameError struct{ err error }

func (e *hostSidecarPostRenameError) Error() string { return e.err.Error() }
func (e *hostSidecarPostRenameError) Unwrap() error { return e.err }

// sidecarRenameCommitted reports whether err is a sidecar save failure the
// rename already committed: the file was replaced before the failure, so the
// caller owes the live state a compensation, not a plain refusal.
func sidecarRenameCommitted(err error) bool {
	var post *hostSidecarPostRenameError
	return errors.As(err, &post)
}

// hostSidecarSyncUnsupported reports whether a sync failed because the
// filesystem cannot sync a directory at all. It delegates to the hub's one
// canonical predicate (internal/fsdurability); hubcore's deletion store and
// the server's thread-clear journal keep matching package-private copies of
// the same tolerance in their own modules.
func hostSidecarSyncUnsupported(err error) bool {
	return fsdurability.SyncUnsupported(err)
}

// add inserts entry at the end. Callers hold hostManagerConfig.mu.
func (s *hostSidecarStore) add(entry hostreg.Host) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, entry)
}

// remove deletes name, reporting whether it was present. Removed stays
// removed: nothing is retained, so a later add of the same name starts clean.
// Callers hold hostManagerConfig.mu.
func (s *hostSidecarStore) remove(name string) bool {
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
func (s *hostSidecarStore) snapshot() []hostreg.Host {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]hostreg.Host(nil), s.entries...)
}

// without returns a copy of the entries minus name, in add order — the
// snapshot a removal persists before it mutates anything. Callers hold
// hostManagerConfig.mu.
func (s *hostSidecarStore) without(name string) []hostreg.Host {
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

// isSidecar reports whether name is a live sidecar entry.
func (s *hostSidecarStore) isSidecar(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.entries {
		if e.Name == name {
			return true
		}
	}
	return false
}

// hostAttachRecord is one host's retained attach state: what the lifecycle
// events say about an in-progress or failed attach, plus the last-known facts
// of the last row that rendered attached.
type hostAttachRecord struct {
	midAttach bool
	lastErr   string
	known     appwire.HostRow // only the fact fields are read back
}

// hostAttachState retains per-host attach records from the SSH manager's
// lifecycle events and from attached rows this surface renders, so offline
// and in-progress rows keep the metadata the wire contract promises (a host
// mid-attach renders midAttach; a host that failed renders lastAttachError;
// an offline row keeps its last-known facts). Records for removed hosts are
// dropped, so a re-add starts clean.
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
// lose the metadata the wire contract promises.
func (s *hostAttachState) recordKnown(row appwire.HostRow, validity hostFactsValidity) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.records[row.Name]
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
// come from the live channel).
func (s *hostAttachState) apply(row *appwire.HostRow) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.records[row.Name]
	if rec == nil {
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

// remove drops name's record; a removed or re-added host starts clean.
func (s *hostAttachState) remove(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.records, name)
}

// hubHostManager serves evener/host/add, evener/host/list,
// evener/host/status, and evener/host/remove: the slice-1 host registry
// surface. It owns no connections: list and status resolve through the
// attached-only seams, add validates hub.toml-authoritatively and wires the
// new host's source with the same seams startup uses, and remove tears the
// host down through the manager's atomic RemoveHost. It never dials.
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
// sidecar entries loaded here and hosts added at runtime are attachable
// without a restart; a nil hosts falls back to an empty registry rather than
// a panic. configPath is the selected hub.toml path ("" disables sidecar
// persistence); logf is the hub's logging path for sidecar load problems.
//
// The sidecar loads after the hub.toml registry builds, so sidecar entries
// join the live set before the first list serves. A sidecar name colliding
// with a live hub.toml entry is dropped — hub.toml is authoritative for its
// own names. A sidecar that fails to load, or an entry that fails validation,
// is logged here and poisons saves: the file keeps every entry it had until
// an operator fixes it, instead of the next save rewriting it without the
// entries that never loaded.
func newHubHostManager(sources *appsource.Registry, manager *sshconn.Manager, cfg hubcore.WebConfig, configPath string, hosts *hostreg.Registry, logf func(format string, args ...any)) *hubHostManager {
	if hosts == nil {
		hosts, _ = hostreg.New(nil)
	}
	m := &hubHostManager{cfg: &hostManagerConfig{
		hosts:            hosts,
		sidecar:          &hostSidecarStore{},
		sidecarPath:      sidecarPathFor(configPath),
		sources:          sources,
		remoteCache:      cfg.RemoteThreadCache,
		manager:          manager,
		client:           cfg.RemoteHostClient,
		online:           cfg.RemoteHostOnline,
		clientIfAttached: cfg.RemoteHostClientIfAttached,
		handshake:        cfg.RemoteHostHandshake,
		facts:            cfg.RemoteHostFacts,
		state:            newHostAttachState(),
		removing:         map[string]struct{}{},
		logf:             logf,
	}}
	entries, err := loadHostSidecar(m.cfg.sidecarPath)
	if err != nil {
		// Loud, not fatal: the hub.toml hosts still serve. The store stays
		// poisoned so no later save can clobber the file's unloaded entries.
		m.logf("host sidecar %s not loaded: %v", m.cfg.sidecarPath, err)
		m.cfg.sidecar.poison(err)
		return m
	}
	for _, e := range entries {
		// Normalize before any use of the entry. The registry's Add would
		// normalize before storing, but the hub.toml collision check, the
		// sidecar row, and the source registration all read the entry as
		// decoded: a padded sidecar name would register a source and a
		// sidecar row under the padded spelling while the registry stores
		// the trimmed one, so the row would list with a hub.toml origin and
		// removal would refuse it as hub.toml-declared.
		e = hostreg.Normalize(e)
		if _, ok := hosts.Get(e.Name); ok {
			// hub.toml is authoritative for its own names: a colliding sidecar
			// entry is a policy drop, not a load failure.
			continue
		}
		if err := hosts.Add(e); err != nil {
			m.logf("host sidecar entry %q not loaded: %v", e.Name, err)
			m.cfg.sidecar.poison(fmt.Errorf("entry %q: %w", e.Name, err))
			continue
		}
		m.cfg.sidecar.add(e)
		m.registerSource(e)
	}
	return m
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

// registerHostManageHandlers installs the four slice-1 host-management
// handlers. hosts is the one live registry the server constructor resolved —
// the same instance the attach handler validates against — and the manager
// and hub.toml path come from cfg: main.go threads the live sshconn.Manager,
// the live host registry, and the selected config path through WebConfig, so
// the surface is wired in production (a nil manager or config path there —
// tests, embedders — leaves the fallbacks: no channel teardown, no sidecar
// persistence). navigation, when non-nil, is invalidated after add/remove
// commits so the manifest's sources converge without waiting for the next
// refresh tick. It returns the manager so tests can drive it directly.
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
	return m
}

// hostOnline reports whether host currently has a live channel. It reads the
// attached-only signal without spawning SSH: the dial seam belongs to
// evener/host/attach alone. A true here is necessary but not sufficient for a
// row to render Attached — hostRow also requires the attached-only client
// lookup to confirm a live channel, so the source registry's fail-open
// default (Online() true with no signal wired) cannot mark a host attached
// that nothing dialed.
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

// hostRow renders one host's list row: the effective entry fields plus live
// state. Attached rows read the live channel's handshake and preflight facts
// through the attached-only lookups and record them as last-known; a failed
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
func (m *hubHostManager) hostRow(ctx context.Context, host hostreg.Host, origin string) appwire.HostRow {
	row := appwire.HostRow{
		Name:       host.Name,
		Address:    host.SSH,
		User:       host.User,
		KeyPath:    host.KeyPath,
		EvenerPath: host.EvenerPath,
		ConfigPath: host.ConfigPath,
		Addr:       host.Addr,
		Roots:      slices.Clone(host.Roots),
		Origin:     origin,
	}
	// Attached is reported only when the attached-only client lookup
	// confirms a live channel. The online
	// signal alone is not sufficient: with no signal wired a hub.toml source
	// fails open (Online() true), and trusting that would render every
	// configured host Attached with no facts behind it — an online row the
	// UI then refuses to Connect because it looks already up. No lookup
	// wired (tests, embedders) leaves the row honestly offline too: nothing
	// can confirm a channel, so nothing may claim one.
	//
	// validity tracks which live lookups refreshed the row's fact fields, so
	// the retention below keeps the previously known values for the fields
	// whose lookup failed instead of blanking them with the failed read's
	// empties.
	var validity hostFactsValidity
	if m.hostOnline(host.Name) && m.cfg.clientIfAttached != nil {
		client, ok := m.cfg.clientIfAttached(host.Name)
		if ok && client != nil {
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
	m.cfg.mu.Lock()
	if m.hostEntryCurrent(host) {
		m.cfg.state.apply(&row)
		if row.Attached {
			m.cfg.state.recordKnown(row, validity)
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

// markRemoving records name as mid-removal, in the commit phase that already
// saved the sidecar without the entry and dropped its store row. Callers
// hold mu.
func (m *hubHostManager) markRemoving(name string) {
	m.cfg.removing[name] = struct{}{}
}

// unmarkRemoving clears the removal mark in the finish phase, on the success
// and the failure exit alike: a successful removal leaves the name addable
// again, a failed one leaves it fully intact and retryable. Callers hold mu.
func (m *hubHostManager) unmarkRemoving(name string) {
	delete(m.cfg.removing, name)
}

// isRemoving reports whether name's removal is currently in flight — its
// durable commit landed and its teardown has not finished. Callers hold mu.
func (m *hubHostManager) isRemoving(name string) bool {
	_, marked := m.cfg.removing[name]
	return marked
}

// hostRemovingConflict is the typed refusal for a name whose removal is in
// flight: the conflict code tells the caller the name is transiently held and
// retryable, rather than mislabeling it a duplicate (Add) or file-declared
// (a second Remove). It commits nothing.
func hostRemovingConflict(name string) error {
	return appwire.Conflict(fmt.Sprintf("host %q: removal in progress; retry once the removal finishes", name))
}

// rowOrigin is the list-row origin for name: the sidecar origin while the name
// is a live sidecar entry — or its removal is in flight, which also started
// from a sidecar entry. The removal's commit drops the store row together
// with its durable save, but the registry entry the row still renders from
// belongs to the sidecar until the teardown takes it, so the mark keeps the
// origin truthful instead of relabeling a mid-removal host hub.toml-declared.
// Callers hold mu.
func (m *hubHostManager) rowOrigin(name string) string {
	if m.cfg.sidecar.isSidecar(name) || m.isRemoving(name) {
		return hostOriginSidecar
	}
	return hostOriginHubTOML
}

// hostEntryField maps a hostreg validation refusal to the input it blames, in
// the wire spelling the dialog's own inputs use (HostEntry's fields), so a
// message lands on the control the operator can fix. A refusal that blames the
// entry as a whole — a cycle — returns "" and the caller raises it form-level.
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

// Add registers one sidecar host entry: name + SSH address + key path. It
// validates exactly like hub.toml loading (component-03 rules) and refuses a
// name hub.toml or the live set already holds — the duplicate refusal applies
// to live entries only: a removed name is gone, so re-add works — and refuses
// a name whose removal is still in flight, so a re-add cannot race the
// removal's finish.
//
// The commit is durable-first: the sidecar file is written before anything is
// exposed, so a save failure commits nothing (no registry entry, no sidecar
// row, no source) and the caller can retry. The name's retained attach state
// resets at the top of the commit, before anything is exposed, so a
// concurrent attach that starts the moment the entry becomes visible cannot
// have its lifecycle state erased by the re-add's own cleanup. Only after
// the save lands does the live set gain the entry — registry, sidecar row, and a fully wired
// source — at which point the host is attachable without a restart. A live
// insert that fails after the save rolls the sidecar back to the pre-add
// contents (rollbackSidecar), so the durable state never keeps an add the API
// reported as failed; so does a save that fails behind its own rename, which
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
	entry := hostreg.Normalize(hostreg.Host{
		Name:       params.Entry.Name,
		SSH:        params.Entry.Address,
		User:       params.Entry.User,
		KeyPath:    params.Entry.KeyPath,
		EvenerPath: params.Entry.EvenerPath,
		ConfigPath: params.Entry.ConfigPath,
		Addr:       params.Entry.Addr,
		Roots:      params.Entry.Roots,
	})
	// Validate without inserting: hostreg's own entry validation runs the
	// exact add-time checks (name grammar, reserved name, ssh destination,
	// user/ssh agreement, non-empty roots) over this one entry without touching
	// live state, so nothing is exposed before the durable save below.
	if err := hostreg.ValidateEntry(entry); err != nil {
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
	// or sidecar row the finish then drops, or a live channel for a host
	// being removed. The refusal commits nothing, the sidecar write included.
	if m.isRemoving(entry.Name) {
		m.cfg.mu.Unlock()
		return appwire.HostRow{}, hostRemovingConflict(entry.Name)
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
	// Durable commit first: the sidecar file is the record of truth for
	// sidecar names, so the entry is exposed only after the save landed. The
	// pre-save snapshot is the rollback copy: should the live insert below
	// fail, the file must not keep the entry (a failed add resurrecting on
	// the next start).
	prev := m.cfg.sidecar.snapshot()
	if err := m.saveSidecar(append(prev, entry)); err != nil {
		if sidecarRenameCommitted(err) {
			// The rename already replaced the file — the entry is durable
			// while nothing is live — so the refusal must not stand as a
			// pre-commit one: restore the pre-add contents, or the next
			// start would resurrect an add this call reports as failed.
			err = m.rollbackSidecar(prev, err)
		}
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
		err = m.rollbackSidecar(prev, err)
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
	m.cfg.sidecar.add(entry)
	m.registerSource(entry)
	m.cfg.mu.Unlock()
	// The commit is complete, so the row reads a fully added host — the entry
	// this call committed, whatever concurrent mutations do around it.
	return m.hostRow(ctx, entry, hostOriginSidecar), nil
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

// saveSidecar persists entries durably, refusing while the on-disk sidecar is
// known unread or partially loaded: rewriting the file then would clobber the
// entries that never made it into memory. Callers treat the refusal as fatal
// (add/remove report it; the entry stays un-exposed or the host stays
// intact), so the operator hears about the broken file instead of losing it.
// A failure the rename already committed (hostSidecarPostRenameError) is not
// a plain refusal — the file holds the new entries — so the callers
// compensate the live state before reporting it.
func (m *hubHostManager) saveSidecar(entries []hostreg.Host) error {
	if err := m.cfg.sidecar.poisoned(); err != nil {
		return fmt.Errorf("host sidecar %s not rewritten: %w (fix or remove the unloaded entries in the file first)", m.cfg.sidecarPath, err)
	}
	return saveHostSidecar(m.cfg.sidecarPath, entries)
}

// rollbackSidecar re-persists previous after a post-save live mutation failed,
// keeping the durable sidecar in step with the live set: the API reported the
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
func (m *hubHostManager) rollbackSidecar(previous []hostreg.Host, cause error) error {
	if err := m.saveSidecar(previous); err != nil {
		if sidecarRenameCommitted(err) {
			// The rollback's own rename landed: the file holds previous, the
			// content the rollback exists to restore, and only its
			// durability step failed — the file and the live set agree. Say
			// that beside the cause rather than claiming a rollback failure
			// that did not happen.
			return fmt.Errorf("%w; host sidecar rollback landed but its directory step failed: %w", cause, err)
		}
		return fmt.Errorf("%w; host sidecar rollback failed: %w", cause, err)
	}
	return cause
}

// List returns every known host with truthful online state in name-sorted
// order (the registry's own; the origin field distinguishes hub.toml entries
// from sidecar ones). Attached rows report live channel facts and retain them
// as last-known; rows without a live channel render as offline with the
// retained attach state and last-known facts. It never dials.
//
// The mutation mutex covers only the snapshot — the registry rows and their
// sidecar origins — so a concurrent reader never observes the window between
// a registry insert and the sidecar row and source registration that finish
// it: a half-committed host would list with a hub.toml origin and no source.
// The row building that follows runs lock-free: hostRow's
// attached-only lookups and its facts read run on the network, and one slow
// or hung host must not block every concurrent Add and Remove commit or
// serialize other lists. The rows are the snapshot's point-in-time view: a
// host added after the snapshot is absent from that response, never
// half-committed in it. A host mid-removal lists under its sidecar origin
// while its registry entry lasts: the removal mark stands in for the store
// row its commit already dropped, so the row never renders as a hub.toml
// entry the operator cannot remove through the UI.
func (m *hubHostManager) List(ctx context.Context, _ appwire.EmptyParams) (appwire.HostListResponse, error) {
	if err := guardControllerLocalHosts(ctx); err != nil {
		return appwire.HostListResponse{}, err
	}
	type rowInput struct {
		host   hostreg.Host
		origin string
	}
	m.cfg.mu.Lock()
	hosts := m.cfg.hosts.All()
	inputs := make([]rowInput, 0, len(hosts))
	for _, host := range hosts {
		inputs = append(inputs, rowInput{host: host, origin: m.rowOrigin(host.Name)})
	}
	m.cfg.mu.Unlock()
	// rows is non-nil even when no host is registered: make returns a usable
	// empty slice, and the wire type's non-nullable hosts array must never
	// marshal as JSON null.
	rows := make([]appwire.HostRow, 0, len(inputs))
	for _, input := range inputs {
		rows = append(rows, m.hostRow(ctx, input.host, input.origin))
	}
	return appwire.HostListResponse{Hosts: rows}, nil
}

// Status returns one host's row: the same HostRow evener/host/list serves.
// Unknown names are InvalidParams. Never dials. It snapshots the host and its
// origin under the same mutation mutex List does, for the same
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
	origin := m.rowOrigin(host.Name)
	m.cfg.mu.Unlock()
	return appwire.HostStatusResponse{Host: m.hostRow(ctx, host, origin)}, nil
}

// Remove deregisters one sidecar host entry: its sidecar row, its source, its
// registry entry, and its channel all go, and the name is gone until
// re-added. hub.toml-declared names are refused (edit the file); unknown names
// are InvalidParams; a removal already in flight for the name is a Conflict.
//
// The removal runs in three phases. The commit phase holds the mutation
// mutex: validation, the durable-first sidecar save (the file loses the entry
// before any live state changes, so a save failure leaves the host fully
// intact and the caller can retry — a failure behind the save's own rename
// included, compensated back to the live contents before the refusal
// returns), the sidecar row's drop in the same
// critical section (the store is what every later save derives its contents
// from, so a row kept past the save would let a concurrent Add or Remove
// re-persist an entry this removal already committed), and the removing mark
// that fences the name. The mutex then releases for the teardown itself: the
// manager-owned RemoveHost blocks on the per-host gate a supervisor holds for
// a whole reconnect/ensure cycle and on the ssh child's exit, so holding the
// mutation mutex across it would freeze every concurrent host/list,
// host/status, and host/add behind one host's removal.
// The mark fences the window instead: Add and a second Remove refuse the
// name, so nothing re-exposes or double-tears a host whose removal is in
// flight. The finish phase retakes the mutex, clears the mark, and completes
// the bookkeeping. A teardown that fails rolls the sidecar forward again to
// keep the entry (rollbackSidecar over the live snapshot), so the durable
// state never forgets a host the live set still holds, and the cleared mark
// leaves the name retryable. Nothing is resurrected: the entry, its source,
// its channel, and its retained attach state are all gone by the time a
// successful Remove returns — and so are its cached remote-thread rows, so
// its sessions stop rendering with the removal instead of lingering live
// until the refresher's next tick.
func (m *hubHostManager) Remove(ctx context.Context, params appwire.HostRemoveParams) (appwire.HostRemoveResponse, error) {
	if err := guardControllerLocalHosts(ctx); err != nil {
		return appwire.HostRemoveResponse{}, err
	}
	name := strings.TrimSpace(params.Name)
	// Commit phase: the removal's durable state and the in-memory sidecar
	// change together, under the mutation mutex, as one read-modify-write
	// cycle.
	m.cfg.mu.Lock()
	if m.isRemoving(name) {
		m.cfg.mu.Unlock()
		return appwire.HostRemoveResponse{}, hostRemovingConflict(name)
	}
	host, ok := m.cfg.hosts.Get(name)
	if !ok {
		m.cfg.mu.Unlock()
		return appwire.HostRemoveResponse{}, appwire.InvalidParams(fmt.Sprintf("unknown host %q", name))
	}
	if !m.cfg.sidecar.isSidecar(host.Name) {
		m.cfg.mu.Unlock()
		return appwire.HostRemoveResponse{}, appwire.InvalidParams(fmt.Sprintf("host %q is declared in hub.toml; remove it by editing the file", host.Name))
	}
	// Persist first: the durable sidecar loses the entry before any live
	// state changes, so a save failure resurrects nothing — the host stays
	// fully intact and the caller can retry. A failure the rename already
	// committed leaves the file without the entry while the live set still
	// holds the host, so it is compensated back to the live contents before
	// the refusal is returned — the store row is still in place (it drops
	// only after a successful save), so the snapshot still carries the entry.
	if err := m.saveSidecar(m.cfg.sidecar.without(host.Name)); err != nil {
		if sidecarRenameCommitted(err) {
			err = m.rollbackSidecar(m.cfg.sidecar.snapshot(), err)
		}
		m.cfg.mu.Unlock()
		return appwire.HostRemoveResponse{}, err
	}
	// The row drops with the save it belongs to, in the same critical
	// section: a concurrent Add or Remove committing in the window below
	// derives its save from the store, and a row still present here would
	// re-persist an entry this removal already committed — the next start
	// would resurrect the host this call is removing.
	m.cfg.sidecar.remove(host.Name)
	// The mark fences the name for the window the mutex is about to release.
	m.markRemoving(host.Name)
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
	m.unmarkRemoving(host.Name)
	if teardownErr != nil {
		// The teardown failed before dropping anything live (RemoveHost
		// refuses ahead of its registry step), so the removal un-commits: the
		// sidecar row returns and the file regains it. The rollback saves the
		// live snapshot, not a pre-remove copy: concurrent Adds and Removes
		// may have committed in the window, and their entries must survive.
		m.cfg.sidecar.add(host)
		err := m.rollbackSidecar(m.cfg.sidecar.snapshot(), teardownErr)
		m.cfg.mu.Unlock()
		return appwire.HostRemoveResponse{}, err
	}
	if m.cfg.sources != nil {
		m.cfg.sources.Remove(host.Name)
	}
	m.cfg.state.remove(host.Name)
	// The remote-thread cache still holds the host's last-refreshed rows,
	// and the refresher's next tick is up to 30s away; sourceOnline
	// fail-opens for the now-unregistered source ID, so those rows would
	// keep rendering the removed host's sessions as live until the tick
	// rewrote the cache. The removal is committed
	// and the host can serve no future refresh, so the rows go with it — and
	// the host's registration generation drops with them, so a refresh that
	// was walking while the remove committed cannot republish the host's
	// rows when it finishes — an absent source mismatches every generation
	// a walk can hold. No configured cache
	// means every tree read walks the live sources, which no longer list
	// the host — nothing to prune.
	if m.cfg.remoteCache != nil {
		m.cfg.remoteCache.RemoveSource(host.Name)
	}
	// The web server's last-known-good retention goes with the removal too:
	// the background walk stored the host's last successful list under its
	// name, and the entry is unreachable once the source is gone from the
	// registry — but the map would keep it, thread rows included, for the
	// process lifetime, growing without bound as distinct host names churn.
	if m.cfg.forgetLastGoodThreads != nil {
		m.cfg.forgetLastGoodThreads(host.Name)
	}
	m.cfg.mu.Unlock()
	return appwire.HostRemoveResponse{Host: appwire.HostRow{
		Name:    host.Name,
		Address: host.SSH,
		KeyPath: host.KeyPath,
		Origin:  hostOriginSidecar,
		Removed: true,
	}}, nil
}
